package daemon_test

import (
	"strings"
	"testing"
	"time"
)

// Question screens in Claude Code 2.1.323's layout, with made-up text.
const (
	qTicks = `────────────────────────────────────────
←  ☐ Colors  ☐ Size  ✔ Submit  →

│ Which colors do you like?

❯ 1. [ ] Red
         A warm color.
  2. [ ] Blue
         A cool color.
  3. [ ] Type something
     Next
────────────────────────────────────────
  4. Chat about this

Enter to select · Tab/Arrow keys to navigate · Esc to cancel`

	qSingle = `────────────────────────────────────────
←  ☒ Colors  ☐ Size  ✔ Submit  →

│ Which size?

❯ 1. Small
  2. Large
  3. Type something.
────────────────────────────────────────
  4. Chat about this

Enter to select · Tab/Arrow keys to navigate · Esc to cancel`

	qReview = `────────────────────────────────────────
←  ☒ Colors  ☒ Size  ✔ Submit  →

Review your answers

   → Red
   → Small

Ready to submit your answers?

❯ 1. Submit answers
  2. Cancel`
)

func (e *env) alertWith(thread int64, sub string) (id int64, text string, buttons string) {
	for _, m := range e.botMessages(thread) {
		if strings.HasPrefix(m.Text, "❓") && strings.Contains(m.Text, sub) {
			var b []string
			if m.Markup != nil {
				for _, row := range m.Markup.InlineKeyboard {
					for _, x := range row {
						b = append(b, x.Text)
					}
				}
			}
			return m.ID, m.Text, strings.Join(b, "|")
		}
	}
	return 0, "", ""
}

// A question with answers to tick, then a second question, then the review
// page, all from Telegram: a box button ticks and keeps the alert, "Next ➡"
// moves the cursor to Next and presses Enter, and each next screen gets its
// own alert. An answered alert shrinks to its question and the answer.
func TestQuestionsWithTicksFromTelegram(t *testing.T) {
	e, _, thread := piTopic(t, "pi-ask")
	e.setScreen(qTicks)
	e.setStatus("blocked", "permission") // Claude Code reports a question so
	q := e.waitText(thread, "Which colors do you like?")
	if !strings.HasPrefix(q.Text, "❓ <b>Question</b>") {
		t.Errorf("alert %q", q.Text)
	}

	// Tick Red: the key is 1, and the alert stays with a ticked box.
	e.pressButton(q, buttonData(q, "☐ 1 Red"))
	e.waitFor("the digit", func() bool { return len(e.keys()) == 1 })
	e.setScreen(strings.Replace(qTicks, "1. [ ] Red", "1. [✔] Red", 1))
	e.waitFor("the ticked box on the alert", func() bool {
		_, _, b := e.alertWith(thread, "Which colors")
		return strings.Contains(b, "☑ 1 Red") && strings.Contains(b, "Next ➡")
	})

	// Next: the cursor moves down to Next, one key at a time, then Enter.
	ticked := strings.Replace(qTicks, "1. [ ] Red", "1. [✔] Red", 1)
	e.pressButton(q, buttonData(q, "Next"))
	moves := []string{
		strings.Replace(strings.Replace(ticked, "❯ 1.", "  1.", 1), "  2. [ ]", "❯ 2. [ ]", 1),
		strings.Replace(strings.Replace(ticked, "❯ 1.", "  1.", 1), "  3. [ ]", "❯ 3. [ ]", 1),
		strings.Replace(strings.Replace(ticked, "❯ 1.", "  1.", 1), "     Next", "❯    Next", 1),
	}
	for i, s := range moves {
		e.waitFor("a down key", func() bool { return len(e.keys()) == 2+i })
		if got := e.keys()[1+i]; got != "down" {
			t.Fatalf("key %d = %q, want down", 2+i, got)
		}
		e.setScreen(s)
	}
	e.waitFor("Enter on Next", func() bool { return len(e.keys()) == 5 })
	if got := e.keys()[4]; got != "enter" {
		t.Fatalf("keys %v, want Enter last", e.keys())
	}
	e.setScreen(qSingle)
	e.waitFor("the first alert shrunk", func() bool {
		_, text, b := e.alertWith(thread, "Which colors")
		return b == "" && strings.Contains(text, "➡ Red") && !strings.Contains(text, "A warm color")
	})

	// The second question gets its own alert; its answer goes on to review.
	e.waitFor("the second question", func() bool { id, _, _ := e.alertWith(thread, "Which size?"); return id != 0 })
	q2 := e.waitText(thread, "Which size?")
	e.pressButton(q2, buttonData(q2, "1 Small"))
	e.waitFor("the digit", func() bool { return len(e.keys()) == 6 })
	e.setScreen(qReview)
	e.waitFor("the review page", func() bool { id, _, _ := e.alertWith(thread, "Ready to submit"); return id != 0 })
	r := e.waitText(thread, "Ready to submit")
	e.pressButton(r, buttonData(r, "1 Submit answers"))
	e.waitFor("the submit key", func() bool { return len(e.keys()) == 7 })
	if got := strings.Join(e.keys(), " "); got != "1 down down down enter 1 1" {
		t.Errorf("keys %q", got)
	}
}

// A reply to a question is the user's own answer: rex-tg opens the
// question's text field with its digit, checks that the cursor is there,
// pastes the text, and presses Enter. It never refuses the question.
func TestReplyToAQuestionIsTheAnswer(t *testing.T) {
	e, _, thread := piTopic(t, "pi-ask-text")
	e.setScreen(qSingle)
	e.setStatus("blocked", "permission")
	q := e.waitText(thread, "Which size?")
	e.waitAlertSaved(thread)
	e.tg.PushReply(chatID, thread, ownerID, q.ID, "", "medium, please")
	e.tg.PushReply(chatID, thread, ownerID, q.ID, "", "medium, please") // a second reply, at once
	e.waitFor("the field's digit", func() bool { return len(e.keys()) == 1 })
	if got := e.keys()[0]; got != "3" {
		t.Fatalf("pressed %q, want 3 (Type something)", got)
	}
	e.setScreen(strings.Replace(strings.Replace(qSingle, "❯ 1.", "  1.", 1), "  3. Type something.", "❯ 3. Type something.", 1))
	e.waitFor("the pasted answer", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; got != "medium, please" {
		t.Errorf("pasted %q", got)
	}
	e.waitFor("Enter", func() bool { return len(e.keys()) == 2 && e.keys()[1] == "enter" })
	e.waitFor("the alert shrunk to the answer", func() bool {
		_, text, b := e.alertWith(thread, "Which size?")
		return b == "" && strings.Contains(text, "✍ medium, please")
	})
	e.never("the answer typed twice", 400*time.Millisecond, func() bool { return len(e.pastes()) > 1 })
}

// A question with previews (made-up text, Claude Code's layout): its "Chat
// about this" has no number, so its button walks the cursor down to it, one
// key at a time with the screen read after each, and presses Enter there.
const qPreview = `────────────────────────────────────────────────────────────────────────────────────────────
 ☐ Layout

│ Which layout should the report use?

❯ 1. Table                        ┌──────────────────────────────────────────────────┐
  2. List                         │ | Name | Qty |                                   │
                                  │ 1. Table of contents                             │
                                  └──────────────────────────────────────────────────┘

                                  Notes: press n to add notes

────────────────────────────────────────────────────────────────────────────────────────────
  Chat about this

Enter to select · ↑/↓ to navigate · n to add notes · Esc to cancel`

func TestChatAboutThisWithPreviewsFromTelegram(t *testing.T) {
	e, _, thread := piTopic(t, "pi-ask-preview")
	e.setScreen(qPreview)
	e.setStatus("blocked", "permission")
	q := e.waitText(thread, "Which layout should the report use?")
	data := buttonData(q, "Chat about this")
	if data == "" {
		t.Fatalf("no Chat about this button: %+v", q.Markup)
	}
	e.pressButton(q, data)
	// The box shows the preview of the answer under the cursor, so it
	// changes with every key, as in Claude Code.
	listPreview := strings.Replace(strings.Replace(qPreview, "| Name | Qty |", "- Name: Qty   ", 1), "1. Table of contents", "2. A list of names  ", 1)
	moves := []string{
		strings.Replace(strings.Replace(listPreview, "❯ 1. Table", "  1. Table", 1), "  2. List", "❯ 2. List", 1),
		strings.Replace(strings.Replace(listPreview, "❯ 1. Table", "  1. Table", 1), "  Chat about this", "❯ Chat about this", 1),
	}
	for i, s := range moves {
		e.waitFor("a down key", func() bool { return len(e.keys()) == 1+i })
		if got := e.keys()[i]; got != "down" {
			t.Fatalf("key %d = %q, want down (keys %v)", 1+i, got, e.keys())
		}
		e.setScreen(s)
	}
	e.waitFor("Enter on Chat about this", func() bool { return len(e.keys()) == 3 })
	if got := e.keys()[2]; got != "enter" {
		t.Fatalf("keys %v, want Enter last", e.keys())
	}
}
