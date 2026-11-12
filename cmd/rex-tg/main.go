// Command rex-tg connects coding agents in Rex panes to Telegram.
package main

import (
	"context"
	"os"
	"path/filepath"

	"rex-tg/internal/install"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(context.Background(), os.Args[1:], realEnv()))
}

func realEnv() Env {
	home, _ := os.UserHomeDir()
	return Env{
		Stdin:    os.Stdin,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		Getenv:   os.Getenv,
		Home:     home,
		Security: "/usr/bin/security",
		Executable: func() (string, error) {
			exe, err := os.Executable()
			if err != nil {
				return "", err
			}
			if r, err := filepath.EvalSymlinks(exe); err == nil {
				exe = r
			}
			exe, err = filepath.Abs(exe)
			if err != nil {
				return "", err
			}
			return install.StablePath(exe, install.FileExists), nil
		},
		StdinIsTerminal: func() bool {
			st, err := os.Stdin.Stat()
			return err != nil || st.Mode()&os.ModeCharDevice != 0
		},
	}
}
