package daemon

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"rex-tg/internal/store"
	"rex-tg/internal/telegram/tgtest"
)

// TestPollMarksOnlyAStoredBatch: the time of a getUpdates call counts for
// the sender's check (PolledSince) only once its whole batch is stored, so
// a user's message in it is a known number before a part is judged.
func TestPollMarksOnlyAStoredBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := tgtest.New(t)
	d := &daemon{
		ctx: ctx, st: st, tg: srv.Client(), events: make(chan any, 4), now: time.Now,
		timing: Timing{LongPoll: time.Second}.withDefaults(),
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)), polled: &atomic.Int64{},
	}
	srv.PushMessage(1, 0, 2, "a message sent before the note")
	go d.pollLoop(ctx)

	batch := func() evUpdates {
		t.Helper()
		select {
		case ev := <-d.events:
			return ev.(evUpdates)
		case <-time.After(5 * time.Second):
			t.Fatal("no batch")
		}
		return evUpdates{}
	}
	// A batch that is not stored does not count.
	e := batch()
	if d.polled.Load() != 0 {
		t.Fatal("the poll counted before its batch was stored")
	}
	*e.next = 0
	close(e.done)
	time.Sleep(300 * time.Millisecond) // the loop waits, then fetches the batch again
	if d.polled.Load() != 0 {
		t.Fatal("the poll counted although nothing was stored")
	}
	// The same batch, stored this time: now it counts.
	e = batch()
	if d.polled.Load() != 0 {
		t.Fatal("the poll counted before its batch was stored")
	}
	*e.next = e.ups[len(e.ups)-1].UpdateID + 1
	close(e.done)
	deadline := time.Now().Add(2 * time.Second)
	for d.polled.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stored batch did not count")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
