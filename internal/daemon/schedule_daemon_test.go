package daemon_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/store"
)

func schedKeys(e *env) []string {
	keys, _ := e.peek().KeysWithPrefix(context.Background(), "sched:")
	return keys
}

func TestAtSendsAfterItIsDue(t *testing.T) {
	e, _, thread := piTopic(t, "pi-at")
	e.say(thread, "/at 2s hello later")
	note := e.waitText(thread, "Scheduled for")
	if !strings.Contains(note.Text, "hello later") {
		t.Errorf("confirmation %q", note.Text)
	}
	if len(e.pastes()) != 0 {
		t.Fatalf("typed before it was due: %v", e.pastes())
	}
	e.waitFor("the paste", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; got != "hello later" {
		t.Errorf("pasted %q", got)
	}
	m := e.waitText(thread, "Sending the scheduled message")
	if !m.Silent {
		t.Error("the note made a sound")
	}
	e.never("a second paste", 2500*time.Millisecond, func() bool { return len(e.pastes()) > 1 })
	if k := schedKeys(e); len(k) != 0 {
		t.Errorf("schedule left: %v", k)
	}
}

func TestAtListsAndCancels(t *testing.T) {
	e, _, thread := piTopic(t, "pi-at-list")
	e.say(thread, "/at")
	e.waitText(thread, "No scheduled messages")
	e.say(thread, "/at 2h second")
	e.say(thread, "/at 30m first")
	e.waitFor("two schedules", func() bool { return len(schedKeys(e)) == 2 })
	e.say(thread, "/at")
	list := e.waitText(thread, "Scheduled messages")
	i1, i2 := strings.Index(list.Text, "first"), strings.Index(list.Text, "second")
	if i1 < 0 || i2 < i1 || !strings.Contains(list.Text, "1. ") {
		t.Errorf("list %q", list.Text)
	}
	e.say(thread, "/at cancel 1")
	e.waitText(thread, "Cancelled: first")
	e.say(thread, "/at cancel 5")
	e.waitText(thread, "Usage: /at cancel")
	if k := schedKeys(e); len(k) != 1 {
		t.Errorf("keys %v, want one", k)
	}
	e.never("a paste", 300*time.Millisecond, func() bool { return len(e.pastes()) > 0 })
}

// A schedule that came due while the daemon was down is sent when it is less
// than an hour late, and reported as missed otherwise.
func TestAtAfterARestart(t *testing.T) {
	e, _, thread := piTopic(t, "pi-at-restart")
	e.say(thread, "/at 1h late one")
	e.say(thread, "/at 2h missed one")
	e.waitFor("two schedules", func() bool { return len(schedKeys(e)) == 2 })
	e.stop()
	e.withStore(func(st *store.Store) {
		ctx := context.Background()
		keys, _ := st.KeysWithPrefix(ctx, "sched:")
		for _, k := range keys {
			v, _, _ := st.Get(ctx, k)
			var s map[string]any
			if err := json.Unmarshal([]byte(v), &s); err != nil {
				t.Fatal(err)
			}
			ago := 10 * time.Minute
			if s["text"] == "missed one" {
				ago = 3 * time.Hour
			}
			s["at"] = time.Now().Add(-ago).Unix()
			b, _ := json.Marshal(s)
			if err := st.Set(ctx, k, string(b)); err != nil {
				t.Fatal(err)
			}
		}
	})
	e.start()
	e.waitFor("the late one typed", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; got != "late one" {
		t.Errorf("pasted %q", got)
	}
	e.waitText(thread, "late, rex-tg was not running")
	e.waitText(thread, "Missed: missed one")
	e.never("another paste", 2500*time.Millisecond, func() bool { return len(e.pastes()) > 1 })
	if k := schedKeys(e); len(k) != 0 {
		t.Errorf("schedule left: %v", k)
	}
}
