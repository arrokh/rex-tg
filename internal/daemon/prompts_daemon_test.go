package daemon_test

import (
	"context"
	"strings"
	"testing"
	"time"
)

// limitScreen is a Claude Code safety check that blocks itself after a
// time limit, as on screen.
func limitScreen(limit string) string {
	return "Bash command\n  rm -f /tmp/x\n" +
		"⚠ Claude Code will automatically block this request in " + limit + ", to avoid holding up an unattended session\n" +
		"Do you want to proceed?\n❯ 1. Yes\n  2. No\nEsc to cancel · Tab to amend\n"
}

// A prompt with a time limit says by when to answer, reminds once before
// the time runs out, and says when the agent blocked it. A prompt answered
// on the Mac in time gets no such notice.
func TestPromptWithATimeLimit(t *testing.T) {
	e, _, thread := piTopic(t, "pi-limit")
	e.setScreen(limitScreen("0:02"))
	e.setStatus("blocked", "permission")
	alert := e.waitText(thread, "Permission needed")
	deadline := time.Now().Add(2 * time.Second)
	if !strings.Contains(alert.Text, "Answer within 0:02") {
		t.Errorf("alert %q does not say by when to answer", alert.Text)
	}
	e.waitText(thread, "to answer the prompt above, then")
	e.waitFor("the time limit to pass", func() bool { return time.Now().After(deadline.Add(200 * time.Millisecond)) })
	e.setStatus("idle", "")
	e.waitText(thread, "⌛ <b>Blocked</b>")
	e.waitFor("the reminder to go", func() bool { return !e.hasText(thread, "to answer the prompt above, then") })

	// Answered on the Mac long before its time: no notice.
	e.setScreen(limitScreen("about 30 seconds"))
	e.setStatus("blocked", "permission")
	e.waitFor("a second alert", func() bool { return len(alertMessages(e, thread)) == 2 })
	e.setStatus("idle", "")
	e.never("a second blocked notice", 400*time.Millisecond, func() bool {
		n := 0
		for _, m := range e.botMessages(thread) {
			if strings.Contains(m.Text, "Blocked") {
				n++
			}
		}
		return n > 1
	})
}

// A text reply to an alert is a note: rex-tg refuses with Esc, and types the
// note once the agent waits for it.
func TestReplyToAnAlertIsANote(t *testing.T) {
	e, _, thread := piTopic(t, "pi-note")
	e.setScreen(permScreen)
	e.setStatus("blocked", "permission")
	alert := e.waitText(thread, "Permission needed")
	e.waitAlertSaved(thread)
	e.tg.PushReply(chatID, thread, ownerID, alert.ID, "", "use trash instead of rm")
	e.waitFor("the refusal", func() bool { return len(e.keys()) == 1 })
	if got := e.keys()[0]; got != "escape" {
		t.Fatalf("pressed %q, want escape (No)", got)
	}
	e.waitText(thread, "Your note goes to the agent next")
	e.never("typing while the prompt shows", 300*time.Millisecond, func() bool { return len(e.pastes()) > 0 })
	if !strings.Contains(alert.Text, "Reply with text to answer No") {
		t.Errorf("the alert does not say what a reply does: %q", alert.Text)
	}
	e.setStatus("idle", "")
	e.waitFor("the note", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; got != "use trash instead of rm" {
		t.Errorf("typed %q", got)
	}
}

// A reply that says yes presses nothing: a reply answers No, so "yes, go
// ahead" must never deny the request.
func TestYesReplyToAnAlertPressesNothing(t *testing.T) {
	e, _, thread := piTopic(t, "pi-yes")
	e.setScreen(permScreen)
	e.setStatus("blocked", "permission")
	alert := e.waitText(thread, "Permission needed")
	e.waitAlertSaved(thread)
	e.tg.PushReply(chatID, thread, ownerID, alert.ID, "", "yes, go ahead")
	e.waitText(thread, "Nothing pressed")
	if len(e.keys()) != 0 || len(e.pastes()) != 0 {
		t.Fatalf("keys %v, pastes %v: want nothing", e.keys(), e.pastes())
	}
}

// A tap on a prompt whose countdown moved since the alert still presses the
// key: the countdown is not part of the screen check.
func TestTapOnACountdownPrompt(t *testing.T) {
	e, _, thread := piTopic(t, "pi-tap")
	e.setScreen(limitScreen("1:30"))
	e.setStatus("blocked", "permission")
	alert := e.waitText(thread, "Permission needed")
	e.setScreen(limitScreen("1:12"))
	e.pressButton(alert, buttonData(alert, "1 Yes"))
	e.waitFor("the key", func() bool { return len(e.keys()) == 1 })
	if got := e.keys()[0]; got != "1" {
		t.Fatalf("pressed %q, want 1", got)
	}
	for _, s := range e.callbackTexts() {
		if strings.Contains(s, "gone") {
			t.Fatalf("the tap was refused: %q", s)
		}
	}
}

// waitAlertSaved waits until the open alert of a topic knows its Telegram
// message: a person replies seconds after the alert shows, a test at once.
func (e *env) waitAlertSaved(thread int64) {
	e.t.Helper()
	st := e.peek()
	th, ok, err := st.ThreadByTopic(context.Background(), chatID, thread)
	if err != nil || !ok {
		e.t.Fatalf("thread: %v %v", ok, err)
	}
	e.waitFor("the alert's message saved", func() bool {
		als, _ := st.OpenAlerts(context.Background(), th.ID)
		return len(als) > 0 && als[len(als)-1].TGMessageID != 0
	})
}
