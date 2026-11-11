package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"gopkg.in/natefinch/lumberjack.v2"

	"rex-tg/internal/daemon"
	"rex-tg/internal/keychain"
)

func daemonCmd(ctx context.Context, args []string, env Env) int {
	fs := newFlags("daemon", env)
	cfgPath := fs.String("config", "", "config file")
	logStderr := fs.Bool("log-stderr", false, "also log to stderr")
	if code, stop := parseFlags(fs, args); stop {
		return code
	}
	cfg, err := env.loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: config: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(cfg.LogDir(), 0o700); err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: %v\n", err)
		return 1
	}
	rotator := &lumberjack.Logger{
		Filename:   filepath.Join(cfg.LogDir(), "rex-tg.log"),
		MaxSize:    10,
		MaxBackups: 3,
	}
	defer rotator.Close()
	var w io.Writer = rotator
	if *logStderr {
		w = io.MultiWriter(rotator, env.Stderr)
	}
	logger := slog.New(slog.NewJSONHandler(w, nil))

	token, err := env.keychain().Get(ctx, tokenService, tokenAccount)
	if errors.Is(err, keychain.ErrNotFound) {
		fmt.Fprintln(env.Stderr, "rex-tg: no bot token in the Keychain. Run `rex-tg login`.")
		logger.Error("no bot token in the Keychain")
		return 1
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: read the bot token: %v\n", err)
		logger.Error("read the bot token", "err", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	err = env.RunDaemon(ctx, daemon.Options{
		Config: cfg,
		Token:  token,
		Home:   env.Home,
		Logger: logger,
		Now:    env.Now,
	})
	switch {
	case err == nil || errors.Is(err, context.Canceled):
		logger.Info("daemon stopped")
		return 0
	case errors.Is(err, daemon.ErrAlreadyRunning):
		fmt.Fprintln(env.Stderr, "rex-tg: another daemon is already running")
		logger.Error("daemon not started", "err", err)
		return 3
	default:
		fmt.Fprintf(env.Stderr, "rex-tg: daemon: %v\n", err)
		logger.Error("daemon failed", "err", err)
		return 1
	}
}
