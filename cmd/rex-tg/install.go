package main

import (
	"context"
	"errors"
	"fmt"

	"rex-tg/internal/install"
)

func validOnly(env Env, only string) bool {
	switch only {
	case "", "pi", "claude", "codex":
		return true
	}
	fmt.Fprintf(env.Stderr, "rex-tg: --only must be pi, claude, or codex, not %q\n", only)
	return false
}

func printChange(env Env, name string, c install.Change, err error, verb string) bool {
	switch {
	case err != nil:
		fmt.Fprintf(env.Stdout, "✗ %-8s %v\n", name, err)
		return false
	case c.Changed:
		line := fmt.Sprintf("✓ %-8s %s %s", name, verb, c.Path)
		if c.Note != "" {
			line += " (" + c.Note + ")"
		}
		if c.Backup != "" {
			line += " [backup " + c.Backup + "]"
		}
		fmt.Fprintln(env.Stdout, line)
	default:
		line := fmt.Sprintf("- %-8s unchanged %s", name, c.Path)
		if c.Note != "" {
			line += " (" + c.Note + ")"
		}
		fmt.Fprintln(env.Stdout, line)
	}
	return true
}

func installCmd(ctx context.Context, args []string, env Env) int {
	fs := newFlags("install", env)
	only := fs.String("only", "", "limit hook changes to pi, claude, or codex")
	noLoad := fs.Bool("no-load", false, "write the launchd agent but do not load it")
	if code, stop := parseFlags(fs, args); stop {
		return code
	}
	if !validOnly(env, *only) {
		return 2
	}
	p, err := env.paths()
	if err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: %v\n", err)
		return 1
	}
	ok := true
	if *only == "" || *only == "claude" {
		c, err := install.InstallClaude(p)
		ok = printChange(env, "claude", c, err, "added hooks to") && ok
	}
	if *only == "" || *only == "codex" {
		c, err := install.InstallCodex(p)
		ok = printChange(env, "codex", c, err, "added hooks to") && ok
	}
	if *only == "" || *only == "pi" {
		c, err := install.InstallPi(p)
		ok = printChange(env, "pi", c, err, "wrote") && ok
	}
	c, err := install.InstallLaunchd(p, !*noLoad)
	ok = printChange(env, "launchd", c, err, "wrote") && ok
	if *only == "" || *only == "codex" {
		fmt.Fprintln(env.Stdout, "Next: start Codex once and trust the new hook entries.")
	}
	if !ok {
		return 1
	}
	return 0
}

func uninstallCmd(ctx context.Context, args []string, env Env) int {
	fs := newFlags("uninstall", env)
	only := fs.String("only", "", "limit changes to pi, claude, or codex")
	force := fs.Bool("force", false, "remove Codex hooks even when other entries lose trust")
	keep := fs.Bool("keep-launchd", false, "keep the launchd agent")
	if code, stop := parseFlags(fs, args); stop {
		return code
	}
	if !validOnly(env, *only) {
		return 2
	}
	p, err := env.paths()
	if err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: %v\n", err)
		return 1
	}
	ok := true
	if *only == "" || *only == "claude" {
		c, err := install.UninstallClaude(p)
		ok = printChange(env, "claude", c, err, "removed hooks from") && ok
	}
	if *only == "" || *only == "codex" {
		c, err := install.UninstallCodex(p, *force)
		if errors.Is(err, install.ErrWouldShiftTrust) {
			fmt.Fprintf(env.Stdout, "✗ codex    NOT removed: %v\n", err)
			fmt.Fprintln(env.Stdout, "           Codex trusts hooks by position, so other entries would lose trust.")
			fmt.Fprintln(env.Stdout, "           Run `rex-tg uninstall --only codex --force` to remove anyway, then trust the shifted entries in Codex.")
			ok = false
		} else {
			ok = printChange(env, "codex", c, err, "removed hooks from") && ok
		}
	}
	if *only == "" || *only == "pi" {
		c, err := install.UninstallPi(p)
		ok = printChange(env, "pi", c, err, "removed") && ok
	}
	if *only == "" && !*keep {
		c, err := install.UninstallLaunchd(p, true)
		ok = printChange(env, "launchd", c, err, "removed") && ok
	}
	if !ok {
		return 1
	}
	return 0
}
