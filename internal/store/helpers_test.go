package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"rex-tg/internal/agents"
)

var (
	ctx = context.Background()
	t0  = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state", "rex-tg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.now = func() time.Time { return t0 }
	return s
}

// must returns v, or panics (which fails the test) when err is set.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func conv(t *testing.T, s *Store, id string) Conversation {
	t.Helper()
	return must(s.UpsertConversation(ctx, Conversation{
		Runtime: agents.Claude, NativeID: id, Transcript: "/tmp/" + id + ".jsonl", FirstSeenAt: t0,
	}))
}

// topic links a new conversation to topic number n (message_thread_id n).
func topic(t *testing.T, s *Store, n int64) (Thread, Conversation) {
	t.Helper()
	c := conv(t, s, "s"+time.Duration(n).String())
	th := must(s.LinkTopic(ctx, 100, n, c.ID, "topic", t0))
	return th, c
}

func item(th int64, key string, parts ...string) OutboxItem {
	it := OutboxItem{ThreadID: th, Key: key, Kind: "answer"}
	for _, p := range parts {
		it.Parts = append(it.Parts, PartContent{Content: p, Format: FormatHTML})
	}
	return it
}
