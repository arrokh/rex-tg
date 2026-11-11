// Command crashharness runs the rex-tg daemon for the crash tests. The test
// starts it, kills it with SIGKILL at a chosen moment, and starts it again
// with the same state folder.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"rex-tg/internal/config"
	"rex-tg/internal/daemon"
	"rex-tg/internal/rex"
	"rex-tg/internal/telegram"
)

func main() {
	var (
		state = flag.String("state", "", "state folder")
		home  = flag.String("home", "", "fake HOME")
		api   = flag.String("api", "", "base URL of the fake Telegram")
		token = flag.String("token", "", "bot token")
		bin   = flag.String("rex", "", "path of the (fake) rex CLI")
	)
	flag.Parse()
	if *state == "" || *home == "" || *api == "" || *token == "" || *bin == "" {
		fmt.Fprintln(os.Stderr, "crashharness: -state, -home, -api, -token, and -rex are required")
		os.Exit(2)
	}
	d := func(v time.Duration) config.Duration { return config.Duration{Duration: v} }
	cfg := config.Default(*home)
	cfg.State.Dir = *state
	cfg.Rex.CLI = *bin
	cfg.Rex.ScanInterval = d(200 * time.Millisecond)
	cfg.Display.ProgressDelay = d(150 * time.Millisecond)
	cfg.Display.ProgressEditInterval = d(100 * time.Millisecond)
	cfg.InputSettings.JoinWindow = d(50 * time.Millisecond)
	cfg.InputSettings.JoinWindowLong = d(100 * time.Millisecond)
	cfg.InputSettings.BusyAfterTyping = d(400 * time.Millisecond)
	cfg.Telegram.RatePerSecond = 200
	cfg.Telegram.RateBurst = 200
	for _, in := range []*config.RuntimeInput{&cfg.Agents.Pi, &cfg.Agents.Claude, &cfg.Agents.Codex} {
		in.PasteEnterDelay = d(20 * time.Millisecond)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := daemon.Run(ctx, daemon.Options{
		Config:    cfg,
		Token:     *token,
		Home:      *home,
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Rex:       rex.New(*bin),
		Telegram:  telegram.New(*token, telegram.WithBaseURL(*api)),
		ProcStart: func(int) (time.Time, error) { return time.Now().Add(-time.Hour), nil },
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "crashharness:", err)
		os.Exit(1)
	}
}
