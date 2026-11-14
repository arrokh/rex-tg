package daemon

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rex-tg/internal/config"
	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
	"rex-tg/internal/telegram/tgtest"
)

// A long Telegram wait is kept in the store: a restart asks Telegram nothing
// until it ends. When it ends, the main chat gets one note.
func TestHoldOutlivesARestartAndEndsWithANote(t *testing.T) {
	const chat = 4242
	srv := tgtest.New(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var mu sync.Mutex
	now := time.Date(2026, 11, 14, 17, 23, 0, 0, time.Local)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }

	// run starts a daemon with what tick and the hold need, as after a start.
	run := func() *daemon {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		tg := srv.Client(telegram.WithClock(clock))
		snd := sender.New(st, tg, sender.Options{Now: clock})
		go func() { _ = snd.Run(ctx) }()
		d := &daemon{st: st, ctx: ctx, tg: tg, snd: snd, cfg: config.Default(t.TempDir()), now: clock, ownerChat: chat,
			log: slog.New(slog.NewTextHandler(io.Discard, nil)), timing: Timing{}.withDefaults()}
		tg.OnHold(d.noteHold)
		d.restoreHold(ctx)
		return d
	}
	notes := func() int {
		n := 0
		for _, m := range srv.Messages(chat) {
			if strings.Contains(m.Text, "Telegram held rex-tg's messages from 17:23 to 18:58") {
				n++
			}
		}
		return n
	}

	d := run()
	srv.FailNext("sendMessage", 1, 429, "Too Many Requests: retry after 5700", 5700)
	if _, err := d.tg.SendMessage(d.ctx, telegram.SendMessageParams{ChatID: chat, Text: "answer"}); telegram.Classify(err) != telegram.ClassRateLimited {
		t.Fatalf("send: %v", err)
	}
	if v, ok, _ := st.Get(d.ctx, holdKey); !ok || !strings.Contains(v, "sendMessage") {
		t.Fatalf("hold not kept: %q %v", v, ok)
	}

	// A restart during the wait: the new client refuses at once.
	d = run()
	_, err = d.tg.SendMessage(d.ctx, telegram.SendMessageParams{ChatID: chat, Text: "again"})
	if telegram.Classify(err) != telegram.ClassRateLimited || len(srv.Calls("sendMessage")) != 1 {
		t.Fatalf("after the restart: %v, %d requests", err, len(srv.Calls("sendMessage")))
	}
	d.checkHoldEnd()
	if notes() != 0 {
		t.Fatal("a note before the wait ended")
	}

	// The wait ends: one note, and the hold is forgotten.
	mu.Lock()
	now = now.Add(95 * time.Minute)
	mu.Unlock()
	d.checkHoldEnd()
	deadline := time.Now().Add(5 * time.Second)
	for notes() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	d.checkHoldEnd()
	if n := notes(); n != 1 {
		t.Fatalf("notes = %d, want 1", n)
	}
	if _, ok, _ := st.Get(d.ctx, holdKey); ok {
		t.Fatal("the hold is still kept after its end")
	}
}
