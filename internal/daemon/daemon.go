// Package daemon wires rex-tg together: the Rex gateway, the registry, the
// transcript tailers, the turn trackers, the Telegram router, the typing
// queue, the alerts, and the sender. See docs/implementation.md ("Wave 3 →
// internal/daemon") and docs/design.md.
//
// One core goroutine owns all state changes (see core.go). Slow work (Rex
// calls, Telegram sends, downloads, speech to text) runs in worker goroutines
// that report back to the core loop as events.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"syscall"
	"time"

	"rex-tg/internal/config"
	"rex-tg/internal/rex"
	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// ErrAlreadyRunning means another daemon holds the instance lock.
var ErrAlreadyRunning = errors.New("daemon: another rex-tg daemon is already running")

// Options configures Run. Nil test seams use the real implementations.
type Options struct {
	Config    config.Config
	Token     string
	Home      string
	Logger    *slog.Logger
	Rex       *rex.Client
	Telegram  *telegram.Client
	Now       func() time.Time
	ProcStart func(pid int) (time.Time, error)
	// Timing shortens the daemon's fixed waits in tests. Zero keeps the
	// production values.
	Timing Timing
}

// Run runs the daemon until ctx ends. It returns nil when ctx ended, and an
// error when the daemon had to stop (another poller, a blocked bot, a failed
// start).
func Run(ctx context.Context, opt Options) error {
	home := opt.Home
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("daemon: home folder: %w", err)
		}
		home = h
	}
	cfg := opt.Config
	if cfg.State.Dir == "" {
		cfg.State.Dir = config.Default(home).State.Dir
	}
	log := opt.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	now := opt.Now
	if now == nil {
		now = time.Now
	}

	if err := os.MkdirAll(cfg.State.Dir, 0o700); err != nil {
		return fmt.Errorf("daemon: create state folder: %w", err)
	}
	// MkdirAll leaves the mode of an existing folder alone; older installs made them 0755.
	for _, dir := range []string{cfg.State.Dir, cfg.LogDir(), cfg.SpoolDir(), cfg.UploadDir()} {
		if err := os.Chmod(dir, 0o700); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Warn("restrict folder mode", "dir", dir, "err", err)
		}
	}
	lock, err := acquireLock(cfg.LockPath())
	if err != nil {
		return err
	}
	defer releaseLock(lock)

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("daemon: open store: %w", err)
	}
	defer st.Close()

	tg := opt.Telegram
	if tg == nil {
		tg = telegram.New(opt.Token, telegram.WithBaseURL(cfg.Telegram.APIBase))
	}
	rx := opt.Rex
	if rx == nil {
		rx = rex.New(cfg.Rex.CLI)
	}
	snd := sender.New(st, tg, sender.Options{
		RatePerSecond: cfg.Telegram.RatePerSecond,
		Burst:         cfg.Telegram.RateBurst,
		Now:           opt.Now,
		Logger:        log,
	})
	d := newDaemon(cfg, home, log, now, st, tg, rx, snd, opt.ProcStart, opt.Timing.withDefaults())
	return d.run(ctx)
}

// acquireLock takes the exclusive instance lock. The returned file keeps it
// until it is closed.
func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("daemon: open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("daemon: lock %s: %w", path, err)
	}
	_ = f.Truncate(0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	return f, nil
}

// releaseLock drops the instance lock, then closes its file. The unlock comes
// first: a child process forked a moment ago (for a rex call) holds a copy of
// the file until it runs exec, and a plain close would leave the lock with
// that copy.
func releaseLock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}
