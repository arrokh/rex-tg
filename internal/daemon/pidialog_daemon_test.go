package daemon_test

import (
	"strings"
	"testing"
	"time"
)

// pi's extension dialog (a select, or a confirm: a select of Yes and No),
// with made-up text. The spinner line above it changes all the time, and the
// footer under it is pi's own.
func piDialog(spinner, cursor string) string {
	row := func(label string) string {
		if label == cursor {
			return " → " + label
		}
		return "   " + label
	}
	return "⠋ " + spinner + " Working...\n" +
		"────────────────────────────────────────\n\n" +
		" Clear the plan?\n Plan: ship the thing (3 of 5 done)\n\n" +
		row("Yes") + "\n" + row("No") + "\n\n" +
		" ↑↓ navigate  enter select  escape cancel\n\n" +
		"────────────────────────────────────────\n" +
		"~/work/demo (main)\n"
}

// /approve is for permission prompts, not Pi's general Yes/No question dialog.
func TestApproveDoesNotAnswerPiQuestionDialog(t *testing.T) {
	e, _, thread := piTopic(t, "pi-dialog-approve")
	e.setScreen(piDialog("a", "Yes"))
	e.setStatus("blocked", "permission")
	alert := e.waitText(thread, "Clear the plan?")
	e.say(thread, "/approve")
	e.waitText(thread, "no clear one-time Yes or Allow answer")
	e.never("/approve answering a question", 300*time.Millisecond, func() bool { return len(e.keys()) > 0 })
	if buttonData(alert, "1 Yes") == "" || !e.hasText(thread, "Clear the plan?") {
		t.Fatalf("question alert was not left open: %+v", alert)
	}
}

// An extension dialog of pi shows in Telegram as a question with a button per
// answer. The alert stays open while the dialog is on screen, even when the
// rest of the screen changes. A tap moves the cursor and presses Enter. The
// alert closes when the dialog goes.
func TestPiExtensionDialogFromTelegram(t *testing.T) {
	e, _, thread := piTopic(t, "pi-dialog")
	e.setScreen(piDialog("a", "Yes"))
	e.setStatus("blocked", "permission") // pi reports a confirm so
	q := e.waitText(thread, "Clear the plan?")
	if !strings.HasPrefix(q.Text, "❓ <b>Question</b>") {
		t.Errorf("alert %q", q.Text)
	}
	if buttonData(q, "1 Yes") == "" || buttonData(q, "2 No") == "" || buttonData(q, "Esc") == "" {
		t.Fatalf("buttons: %+v", q.Markup)
	}
	e.waitAlertSaved(thread)

	// Not taken for answered while the dialog shows, though the spinner above
	// it moves.
	for _, s := range []string{"b", "c", "d", "e"} {
		e.setScreen(piDialog(s, "Yes"))
		time.Sleep(120 * time.Millisecond)
	}
	e.never("the alert closing", 300*time.Millisecond, func() bool { return e.hasText(thread, "Answered on the Mac") })

	// No: one key down, read after it, then Enter.
	e.pressButton(q, buttonData(q, "2 No"))
	e.waitFor("a down key", func() bool { return len(e.keys()) == 1 })
	if got := e.keys()[0]; got != "down" {
		t.Fatalf("pressed %q, want down", got)
	}
	e.setScreen(piDialog("f", "No"))
	e.waitFor("Enter", func() bool { return len(e.keys()) == 2 })
	if got := e.keys()[1]; got != "enter" {
		t.Fatalf("keys %v, want Enter last", e.keys())
	}
	e.waitFor("the alert shrunk to the answer", func() bool { return e.hasText(thread, "✅ No") })

	// A second dialog that goes away on the Mac closes its alert.
	e.setScreen(piDialog("g", "Yes"))
	e.setStatus("blocked", "question")
	e.waitFor("the second alert", func() bool { return len(alertMessages(e, thread)) == 2 })
	e.setScreen("⠋ Working...\n")
	e.setStatus("working", "")
	e.waitFor("Answered on the Mac", func() bool { return e.hasText(thread, "✔ Answered on the Mac") })
}
