package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"rex-tg/internal/config"
	"rex-tg/internal/daemon"
	"rex-tg/internal/install"
	"rex-tg/internal/keychain"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

const (
	tokenService = "rex-tg"
	tokenAccount = "bot-token"
)

// Env holds everything the commands take from the outside world, so tests can
// replace it. Nil fields get real defaults.
type Env struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	Home           string
	// ConfigPath overrides the default <Home>/.config/rex-tg/config.toml.
	ConfigPath string
	Now        func() time.Time
	// Security is the path of the security tool.
	Security string
	// Executable returns the absolute, symlink-free path of this binary. A Homebrew Cellar path becomes the stable opt path.
	Executable      func() (string, error)
	StdinIsTerminal func() bool
	// TelegramBase overrides the Bot API base URL from the config.
	TelegramBase string
	// Runner runs launchctl.
	Runner install.Runner
	// UID is the user ID for gui/<uid>.
	UID string
	// Interactive runs a command attached to the terminal.
	Interactive func(ctx context.Context, name string, args ...string) error
	// RunDaemon is daemon.Run.
	RunDaemon func(ctx context.Context, opt daemon.Options) error
	// PairingCode is daemon.NewPairingCode.
	PairingCode func(ctx context.Context, st *store.Store, now time.Time) (string, error)
}

func (e Env) withDefaults() Env {
	if e.Stdin == nil {
		e.Stdin = os.Stdin
	}
	if e.Stdout == nil {
		e.Stdout = io.Discard
	}
	if e.Stderr == nil {
		e.Stderr = io.Discard
	}
	if e.Getenv == nil {
		e.Getenv = os.Getenv
	}
	if e.Now == nil {
		e.Now = time.Now
	}
	if e.Security == "" {
		e.Security = keychain.DefaultBin
	}
	if e.Executable == nil {
		e.Executable = func() (string, error) { return "", errors.New("unknown executable path") }
	}
	if e.StdinIsTerminal == nil {
		e.StdinIsTerminal = func() bool { return false }
	}
	if e.Runner == nil {
		e.Runner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	if e.UID == "" {
		e.UID = strconv.Itoa(os.Getuid())
	}
	if e.Interactive == nil {
		e.Interactive = func(ctx context.Context, name string, args ...string) error {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			return cmd.Run()
		}
	}
	if e.RunDaemon == nil {
		e.RunDaemon = daemon.Run
	}
	if e.PairingCode == nil {
		e.PairingCode = daemon.NewPairingCode
	}
	return e
}

func (e Env) defaultConfigPath() string {
	if e.ConfigPath != "" {
		return e.ConfigPath
	}
	return filepath.Join(e.Home, ".config", "rex-tg", "config.toml")
}

func (e Env) keychain() keychain.Client { return keychain.Client{Bin: e.Security} }

func (e Env) loadConfig(path string) (config.Config, error) {
	if path == "" {
		path = e.defaultConfigPath()
	}
	cfg, err := config.Load(path, e.Home)
	if err != nil {
		return cfg, err
	}
	if e.TelegramBase != "" {
		cfg.Telegram.APIBase = e.TelegramBase
	}
	return cfg, nil
}

func (e Env) telegram(cfg config.Config, token string) *telegram.Client {
	return telegram.New(token, telegram.WithBaseURL(cfg.Telegram.APIBase), telegram.WithProxy(cfg.TelegramProxy()), telegram.WithFallback(cfg.Telegram.Fallback))
}

// paths returns the install locations, with the live folder of the config
// that the daemon reads: the pi extension must write where the daemon looks.
// A config that does not load is an error.
func (e Env) paths() (install.Paths, error) {
	p, err := e.basePaths()
	if err != nil {
		return p, err
	}
	cfg, err := e.loadConfig("")
	if err != nil {
		return install.Paths{}, err
	}
	p.LiveDir = cfg.LiveDir() // where the daemon looks for pi's live text
	return p, nil
}

// basePaths is paths without the config, for uninstall, which must work
// even when the config is broken.
func (e Env) basePaths() (install.Paths, error) {
	bin, err := e.Executable()
	if err != nil {
		return install.Paths{}, fmt.Errorf("find the rex-tg binary: %w", err)
	}
	p := install.DefaultPaths(e.Home, bin)
	p.Runner = e.Runner
	p.UID = e.UID
	p.Now = e.Now
	return p, nil
}

const usageText = `rex-tg mirrors coding agents in Rex panes into Telegram topics.

Usage: rex-tg <command> [flags]

Commands:
  login [--token-stdin]   store the bot token in the Keychain, check it, and pair
  pair                    print a new pairing code for /start
  install [--only pi|claude|codex] [--no-load]
                          add the hooks, the pi extension, and the launchd agent
  uninstall [--only pi|claude|codex] [--force] [--keep-launchd]
                          remove what install added
  daemon [--config F] [--log-stderr]
                          run the daemon (launchd runs this)
  hook <pi|claude|codex>  called by agent hooks; prints nothing
  status                  show topics, queued messages, and open alerts
  doctor                  check the setup
  version                 print the version
  help                    show this text

The config file is ~/.config/rex-tg/config.toml.
`

func run(ctx context.Context, args []string, env Env) int {
	env = env.withDefaults()
	if len(args) == 0 {
		fmt.Fprint(env.Stderr, usageText)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "hook":
		return hookCmd(ctx, rest, env)
	case "login":
		return loginCmd(ctx, rest, env)
	case "pair":
		return pairCmd(ctx, rest, env)
	case "install":
		return installCmd(ctx, rest, env)
	case "uninstall":
		return uninstallCmd(ctx, rest, env)
	case "daemon":
		return daemonCmd(ctx, rest, env)
	case "status":
		return statusCmd(ctx, rest, env)
	case "doctor":
		return doctorCmd(ctx, rest, env)
	case "version", "--version", "-v":
		fmt.Fprintf(env.Stdout, "rex-tg %s\n", version)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(env.Stdout, usageText)
		return 0
	}
	fmt.Fprintf(env.Stderr, "rex-tg: unknown command %q\n\n%s", cmd, usageText)
	return 2
}

// newFlags makes a flag set that reports to stderr.
func newFlags(name string, env Env) *flag.FlagSet {
	fs := flag.NewFlagSet("rex-tg "+name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	return fs
}

// parseFlags returns an exit code and whether the command should stop.
func parseFlags(fs *flag.FlagSet, args []string) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, true
		}
		return 2, true
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(fs.Output(), "rex-tg: unexpected argument %q\n", fs.Arg(0))
		return 2, true
	}
	return 0, false
}
