package daemon_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/config"
	"rex-tg/internal/daemon"
	"rex-tg/internal/telegram/tgtest"
)

func TestAgentsBoardFollowsTheStates(t *testing.T) {
	e, _, _ := piTopic(t, "pi-board") // opening the topic posted a list already
	before := e.countText(0, "Active agents")
	e.say(0, "/agents")
	e.waitFor("the new list", func() bool { return e.countText(0, "Active agents") == before+1 })
	var board tgtest.Message
	for _, m := range e.botMessages(0) {
		if strings.Contains(m.Text, "Active agents") {
			board = m // the newest list is the live one
		}
	}
	if !strings.Contains(board.Text, "updates by itself") {
		t.Errorf("board = %q, want the live note", board.Text)
	}
	text := func() string { m, _ := e.message(board.ID); return m.Text }

	e.setStatus("working", "")
	e.waitFor("the board shows working", func() bool { return strings.Contains(text(), "working") })

	e.setScreen(permScreen)
	e.setStatus("blocked", "permission")
	e.waitFor("the board shows waiting", func() bool { return strings.Contains(text(), "waiting for you") })
	if m, _ := e.message(board.ID); m.Markup == nil || buttonData(m, "Refresh") == "" {
		t.Errorf("the board lost its buttons: %+v", m.Markup)
	}
	if n := e.countText(0, "Active agents"); n != before+1 {
		t.Errorf("%d agent lists, want the board edited in place", n)
	}
}

// reminders lists the reminder messages of a topic, deleted ones too.
func reminders(e *env, thread int64) []tgtest.Message {
	var out []tgtest.Message
	for _, m := range e.tg.Messages(chatID) {
		if m.FromBot && m.ThreadID == thread && strings.Contains(m.Text, "Still waiting for your answer") {
			out = append(out, m)
		}
	}
	return out
}

func TestUnansweredPromptGetsReminders(t *testing.T) {
	e, _, thread := piTopicCfg(t, "pi-remind", func(e *env) {
		e.cfg.Alerts.RemindAfter = config.Duration{Duration: 300 * time.Millisecond}
		e.cfg.Alerts.Reminders = 2
	})
	e.setScreen(permScreen)
	e.setStatus("blocked", "permission")
	alert := e.waitText(thread, "Permission needed")

	e.waitFor("a first reminder", func() bool { return len(reminders(e, thread)) == 1 })
	r1 := reminders(e, thread)[0]
	if r1.ReplyTo != alert.ID || r1.Silent {
		t.Errorf("reminder = %+v, want a reply to the alert %d, with sound", r1, alert.ID)
	}
	e.waitFor("a second reminder in place of the first", func() bool {
		rs := reminders(e, thread)
		return len(rs) == 2 && rs[0].Deleted && !rs[1].Deleted
	})
	e.never("a third reminder", 900*time.Millisecond, func() bool { return len(reminders(e, thread)) > 2 })

	// The prompt ends: its reminder goes.
	e.setStatus("working", "")
	e.waitFor("the reminder deleted", func() bool {
		rs := reminders(e, thread)
		return len(rs) == 2 && rs[1].Deleted
	})
}

func TestDaemonStopsForAnUpdateOfItsBinary(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	bin := filepath.Join(t.TempDir(), "rex-tg")
	if err := os.WriteFile(bin, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	opt := e.options()
	opt.SelfPath = bin
	e.startWith(opt)
	e.mu.Lock()
	done := e.done
	e.mu.Unlock()
	select {
	case err := <-done:
		t.Fatalf("Run returned %v before an update", err)
	case <-time.After(300 * time.Millisecond):
	}

	// Replace the binary the way an installer does: a new file in its place.
	next := bin + ".new"
	if err := os.WriteFile(next, []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, bin); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, daemon.ErrUpdated) {
			t.Fatalf("Run returned %v, want ErrUpdated", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after the update")
	}
	e.mu.Lock()
	e.cancel, e.done = nil, nil // it stopped by itself
	e.mu.Unlock()
}

// A reminder whose send got no answer may have arrived: rex-tg never
// guesses its number to delete it. Its "✓ Dismiss" button deletes the
// message that holds it, and nothing else.
func TestLostReminderIsDismissedByItsButton(t *testing.T) {
	e, _, thread := piTopicCfg(t, "pi-remind-lost", func(e *env) {
		e.cfg.Alerts.RemindAfter = config.Duration{Duration: 300 * time.Millisecond}
		e.cfg.Alerts.Reminders = 1
	})
	e.setScreen(permScreen)
	e.setStatus("blocked", "permission")
	e.waitText(thread, "Permission needed")
	e.waitFor("the progress message", func() bool { return e.hasText(thread, "Waiting for your answer") })
	e.tg.LoseAnswerNext("sendMessage", 1) // the reminder arrives; its answer is lost
	e.waitFor("the reminder", func() bool { return len(reminders(e, thread)) == 1 })
	r := reminders(e, thread)[0]
	if buttonData(r, "Dismiss") == "" {
		t.Fatalf("the reminder has no Dismiss button: %+v", r.Markup)
	}
	e.setStatus("working", "")
	// The prompt ends: the reminder's number is unknown, so it stays.
	e.never("the lost reminder deleted on a guess", 500*time.Millisecond, func() bool { return reminders(e, thread)[0].Deleted })
	e.pressButton(r, buttonData(r, "Dismiss"))
	e.waitFor("the reminder dismissed", func() bool { return reminders(e, thread)[0].Deleted })
}
