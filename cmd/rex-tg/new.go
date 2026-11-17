package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"rex-tg/internal/config"
	"rex-tg/internal/daemon"
	"rex-tg/internal/rex"
	"rex-tg/internal/store"
)

func newCmd(ctx context.Context, args []string, env Env) int {
	cfgPath, name, agentArgs, help, err := parseNewArgs(args)
	if help {
		printNewUsage(env.Stdout)
		return 0
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: %v\n", err)
		printNewUsage(env.Stderr)
		return 2
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		printNewUsage(env.Stderr)
		return 2
	}

	cfg, err := env.loadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: config: %v\n", err)
		return 1
	}
	command, err := newCommand(name, cfg, env.Home)
	if err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: %v\n", err)
		return 2
	}
	command = append(command, agentArgs...)
	dir, err := env.Getwd()
	if err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: get current folder: %v\n", err)
		return 1
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: resolve current folder: %v\n", err)
		return 1
	}
	dir = filepath.Clean(dir)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		if err == nil {
			err = errors.New("not a folder")
		}
		fmt.Fprintf(env.Stderr, "rex-tg: current folder %q: %v\n", dir, err)
		return 1
	}

	if err := os.MkdirAll(cfg.State.Dir, 0o700); err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: create state folder: %v\n", err)
		return 1
	}
	if err := os.Chmod(cfg.State.Dir, 0o700); err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: secure state folder: %v\n", err)
		return 1
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: open state: %v\n", err)
		return 1
	}
	defer st.Close()

	label := strings.TrimSpace(cfg.Rex.NewSession)
	tab := rex.Tab{Label: daemon.NewAgentTabLabel(dir, name), CWD: dir, Command: command}
	client := rex.New(cfg.Rex.CLI)
	var started rex.Started
	var reused, newSession, startAttempted bool
	var warning string
	err = daemon.WithNewSessionLock(ctx, cfg.State.Dir, func() error {
		sessions, err := client.Sessions(ctx)
		if err != nil {
			return err
		}
		var session *rex.Session
		for i := range sessions {
			if strings.EqualFold(strings.TrimSpace(sessions[i].Label), label) {
				session = &sessions[i]
				break
			}
		}
		if session == nil {
			id, ok, err := st.Get(ctx, daemon.RexNewSessionKey)
			if err != nil {
				return fmt.Errorf("read the Rex session ID: %w", err)
			}
			if ok && id != "" {
				for i := range sessions {
					if sessions[i].ID == id {
						session = &sessions[i]
						break
					}
				}
			}
		}
		if session != nil {
			if err := st.Set(ctx, daemon.RexNewSessionKey, session.ID); err != nil {
				warning = "could not save the Rex session ID; check `rex ls` before trying again"
			}
			reused = true
			startAttempted = true
			started, err = client.NewWindow(ctx, session.ID, tab)
			return err
		}

		startAttempted = true
		started, err = client.NewSession(ctx, label, tab)
		if err != nil {
			return err
		}
		newSession = true
		if err := st.Set(ctx, daemon.RexNewSessionKey, started.SessionID); err != nil {
			warning = "could not save the Rex session ID; it was started, so check `rex ls` before trying again"
		}
		return nil
	})
	if err != nil {
		if startAttempted {
			fmt.Fprintf(env.Stderr, "rex-tg: Rex did not confirm the new tab; it may exist already. Check the Rex session before trying again: %v\n", err)
		} else {
			fmt.Fprintf(env.Stderr, "rex-tg: could not start %s: %v\n", name, err)
		}
		return 1
	}
	if newSession {
		fmt.Fprintf(env.Stdout, "Started %s in %s; tab %q in Rex session %q.\n", name, dir, tab.Label, label)
	} else if reused {
		fmt.Fprintf(env.Stdout, "Added %s in %s; tab %q in Rex session %q.\n", name, dir, tab.Label, label)
	}
	if warning != "" {
		fmt.Fprintf(env.Stderr, "rex-tg: warning: %s\n", warning)
	}
	return 0
}

// parseNewArgs consumes rex-tg options before the agent name. Every later
// argument belongs to the agent, including flags such as --resume.
func parseNewArgs(args []string) (configPath, name string, agentArgs []string, help bool, err error) {
	for i := 0; i < len(args); {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			return configPath, "", nil, true, nil
		case a == "--config":
			if i+1 >= len(args) || args[i+1] == "" {
				return "", "", nil, false, errors.New("--config needs a file")
			}
			configPath = args[i+1]
			i += 2
		case strings.HasPrefix(a, "--config="):
			configPath = strings.TrimPrefix(a, "--config=")
			if configPath == "" {
				return "", "", nil, false, errors.New("--config needs a file")
			}
			i++
		case a == "--":
			i++
			if i >= len(args) {
				return "", "", nil, false, nil
			}
			return configPath, args[i], append([]string(nil), args[i+1:]...), false, nil
		case strings.HasPrefix(a, "-"):
			return "", "", nil, false, fmt.Errorf("unknown option %q; put agent options after the agent name", a)
		default:
			return configPath, a, append([]string(nil), args[i+1:]...), false, nil
		}
	}
	return configPath, "", nil, false, nil
}

func printNewUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: rex-tg new [--config F] <pi|cux|claude|codex> [agent args...]")
	fmt.Fprintln(w, "Arguments after the agent name are passed to the agent unchanged.")
}

func newCommand(name string, cfg config.Config, home string) ([]string, error) {
	var command []string
	switch name {
	case "pi":
		command = cfg.Agents.Pi.Launch
	case "claude":
		command = cfg.Agents.Claude.Launch
	case "codex":
		command = cfg.Agents.Codex.Launch
	case "cux":
		if !daemon.HasProgram(home, "cux") {
			return nil, errors.New("cux was not found on PATH or in ~/.local/bin")
		}
		command = []string{"cux"}
	default:
		return nil, fmt.Errorf("unknown agent %q; choose pi, cux, claude, or codex", name)
	}
	if len(command) == 0 {
		return nil, fmt.Errorf("no launch command is configured for %s", name)
	}
	return append([]string(nil), command...), nil
}
