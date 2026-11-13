package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"rex-tg/internal/agents"
	"rex-tg/internal/agents/claude"
	"rex-tg/internal/agents/codex"
	"rex-tg/internal/agents/pi"
	"rex-tg/internal/daemon"
	"rex-tg/internal/install"
	"rex-tg/internal/keychain"
	"rex-tg/internal/rex"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

type doctorOut struct {
	env  Env
	fail int
}

func (d *doctorOut) ok(format string, a ...any)   { fmt.Fprintf(d.env.Stdout, "✓ "+format+"\n", a...) }
func (d *doctorOut) warn(format string, a ...any) { fmt.Fprintf(d.env.Stdout, "! "+format+"\n", a...) }
func (d *doctorOut) bad(format string, a ...any) {
	d.fail++
	fmt.Fprintf(d.env.Stdout, "✗ "+format+"\n", a...)
}

func doctorCmd(ctx context.Context, args []string, env Env) int {
	fs := newFlags("doctor", env)
	cfgPath := fs.String("config", "", "config file")
	if code, stop := parseFlags(fs, args); stop {
		return code
	}
	d := &doctorOut{env: env}
	cfg, err := env.loadConfig(*cfgPath)
	if err != nil {
		d.bad("config: %v", err)
		return 1
	}
	d.ok("config loaded")

	// Rex CLI.
	if _, err := exec.LookPath(cfg.Rex.CLI); err != nil {
		d.bad("Rex CLI not found at %s", cfg.Rex.CLI)
	} else if w, err := rex.New(cfg.Rex.CLI).Whoami(ctx); err != nil {
		d.bad("Rex CLI at %s: whoami failed: %v", cfg.Rex.CLI, err)
	} else {
		d.ok("Rex CLI works (%s, %d sessions)", w.RexVersion, w.SessionCount)
	}

	// Token, getMe, topics.
	var tg *telegram.Client
	token, err := env.keychain().Get(ctx, tokenService, tokenAccount)
	switch {
	case errors.Is(err, keychain.ErrNotFound):
		d.bad("bot token not in the Keychain (run `rex-tg login`)")
	case err != nil:
		d.bad("bot token: %v", err)
	default:
		d.ok("bot token is in the Keychain")
		me, err := env.telegram(cfg, token).GetMe(ctx)
		switch {
		case err != nil:
			d.bad("Telegram getMe failed: %v", err)
		case !me.HasTopicsEnabled:
			d.bad("@%s: Threaded Mode is off (BotFather > your bot > Bot Settings > Threaded Mode)", me.Username)
		default:
			d.ok("@%s answers, Threaded Mode is on", me.Username)
			tg = env.telegram(cfg, token)
		}
	}

	// Database and pairing.
	if _, err := os.Stat(cfg.DBPath()); err != nil {
		d.bad("database %s does not exist yet", cfg.DBPath())
	} else if st, err := store.OpenReadOnly(cfg.DBPath()); err != nil {
		d.bad("database does not open: %v", err)
	} else {
		d.ok("database opens (%s)", cfg.DBPath())
		if v, ok, err := st.Get(ctx, "owner_user_id"); err == nil && ok && v != "" {
			d.ok("owner is paired")
		} else {
			d.bad("no owner paired (run `rex-tg pair`)")
		}
		if v, ok, err := st.Get(ctx, "owner_chat_id"); err == nil && ok && tg != nil {
			if chat, err := strconv.ParseInt(v, 10, 64); err == nil && chat != 0 {
				menuLine(ctx, d, tg, chat)
			}
		}
		unknownTypesLines(ctx, d, st)
		_ = st.Close()
	}

	// Installed pieces.
	if p, err := env.paths(); err != nil {
		d.bad("%v", err)
	} else if rep, err := install.Inspect(p); err != nil {
		d.bad("inspect install: %v", err)
	} else {
		d.ok("this binary: %s", p.Binary)
		// A second copy (an old `make install` next to Homebrew) makes a bare
		// `rex-tg install` point the hooks at whichever comes first on PATH.
		if old := filepath.Join(env.Home, ".local", "bin", "rex-tg"); old != p.Binary && install.FileExists(old) && !sameBinary(old, p.Binary) {
			d.warn("another copy is at %s; replace it with a link to %s, so the hooks, launchd, and your shell use one binary", old, p.Binary)
		}
		hookLine(d, "Claude hooks", rep.Claude)
		hookLine(d, "Codex hooks", rep.Codex)
		fileLine(d, "pi extension", rep.Pi)
		fileLine(d, "launchd plist", rep.Launchd)
		target := "gui/" + env.UID + "/" + p.Label
		if _, err := env.Runner(ctx, "launchctl", "print", target); err != nil {
			d.bad("launchd agent %s is not loaded", p.Label)
		} else {
			d.ok("launchd agent %s is loaded", p.Label)
		}
	}

	// Folders.
	for _, dir := range []string{cfg.State.Dir, cfg.SpoolDir()} {
		st, err := os.Stat(dir)
		switch {
		case err != nil:
			d.warn("folder %s does not exist yet", dir)
		case st.Mode().Perm() != 0o700:
			d.bad("folder %s has mode %04o, want 0700", dir, st.Mode().Perm())
		default:
			d.ok("folder %s has mode 0700", dir)
		}
	}

	// Lock.
	switch held, err := lockHeld(cfg.LockPath()); {
	case err != nil:
		d.warn("daemon lock: %v", err)
	case held:
		d.ok("a daemon is running (holds the lock)")
	default:
		d.warn("no daemon is running")
	}

	if d.fail > 0 {
		return 1
	}
	return 0
}

func hookLine(d *doctorOut, name string, h install.HookReport) {
	switch {
	case h.Err != "":
		d.bad("%s: %s", name, h.Err)
	case h.Installed():
		d.ok("%s installed (%s)", name, h.Path)
	default:
		d.warn("%s not fully installed, or they name another rex-tg binary (%s); run `rex-tg install` with this binary", name, h.Path)
	}
}

func fileLine(d *doctorOut, name string, f install.FileReport) {
	switch {
	case f.Err != "":
		d.bad("%s: %s", name, f.Err)
	case f.State == install.StateCurrent:
		d.ok("%s is current (%s)", name, f.Path)
	default:
		d.warn("%s is %s: missing, or it names another rex-tg binary (%s); run `rex-tg install` with this binary", name, f.State, f.Path)
	}
}

// sameBinary tells whether two paths lead to one file, for example a link in
// ~/.local/bin to the Homebrew binary.
func sameBinary(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}

// lockHeld tries a non-blocking flock and releases it at once.
func lockHeld(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil
		}
		return false, err
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, nil
}

// unknownTypesLines reports unknown transcript line types that the daemon
// recorded in KV key unknown_line_types.
func unknownTypesLines(ctx context.Context, d *doctorOut, st *store.Store) {
	v, ok, err := st.Get(ctx, "unknown_line_types")
	var m map[string]map[string]int
	if err == nil && ok && v != "" {
		_ = json.Unmarshal([]byte(v), &m)
	}
	// Types that this binary's adapters know are no format change any more
	// (counted by an older version).
	adapters := map[string]agents.Adapter{"pi": pi.New(), "claude": claude.New(), "codex": codex.New()}
	for rt, types := range m {
		if a, ok := adapters[rt]; ok {
			for typ := range types {
				if agents.KnowsType(a, typ) {
					delete(types, typ)
				}
			}
		}
	}
	found := false
	for _, rt := range []string{"pi", "claude", "codex"} {
		types := m[rt]
		if len(types) == 0 {
			continue
		}
		found = true
		names := make([]string, 0, len(types))
		for n := range types {
			names = append(names, n)
		}
		sort.Slice(names, func(i, j int) bool {
			if types[names[i]] != types[names[j]] {
				return types[names[i]] > types[names[j]]
			}
			return names[i] < names[j]
		})
		parts := make([]string, 0, 5)
		for i, n := range names {
			if i == 4 {
				parts = append(parts, "…")
				break
			}
			parts = append(parts, fmt.Sprintf("%s ×%d", n, types[n]))
		}
		d.warn("%s: %d unknown line types (%s)", rt, len(types), strings.Join(parts, ", "))
	}
	if !found {
		d.ok("transcript formats: no unknown line types")
	}
}

// menuLine reports the command menu that the owner sees. Telegram shows the
// first list that is set: the chat's own, then all private chats, then the
// default. Lists for one language are not checked.
func menuLine(ctx context.Context, d *doctorOut, tg *telegram.Client, owner int64) {
	want := daemon.BotCommands()
	scopes := []struct {
		name  string
		scope *telegram.BotCommandScope
	}{
		{"your chat", &telegram.BotCommandScope{Type: "chat", ChatID: owner}},
		{"all private chats", &telegram.BotCommandScope{Type: "all_private_chats"}},
		{"the default scope", nil},
	}
	for _, sc := range scopes {
		got, err := tg.GetMyCommands(ctx, sc.scope)
		if err != nil {
			d.warn("command menu: getMyCommands failed: %v", err)
			return
		}
		if len(got) == 0 {
			continue
		}
		if sameCommands(got, want) {
			d.ok("command menu has the %d rex-tg commands (list for %s)", len(got), sc.name)
		} else {
			names := make([]string, 0, len(got))
			for _, c := range got {
				names = append(names, "/"+c.Command)
			}
			d.warn("command menu shows %d other commands (list for %s: %s); restart the daemon to replace it",
				len(got), sc.name, strings.Join(names, " "))
		}
		return
	}
	d.warn("command menu is empty; restart the daemon to set it")
}

func sameCommands(a, b []telegram.BotCommand) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Command != b[i].Command {
			return false
		}
	}
	return true
}
