package daemon

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/config"
	"rex-tg/internal/format"
	"rex-tg/internal/store"
)

// Screens in the layouts of Claude Code 2.1.323 (seen live) and Codex 0.167.1,
// with made-up text.
const (
	multiScreen = `⏺ Some answer of the agent above the question.
────────────────────────────────────────
←  ☐ Colors  ☐ Size  ✔ Submit  →

│ Which colors do you like?

❯ 1. [ ] Red
         A warm color.
  2. [✔] Blue
         A cool color.
  3. [ ] Type something
     Next
────────────────────────────────────────
  4. Chat about this

Enter to select · Tab/Arrow keys to navigate · Esc to cancel`

	singleScreen = `────────────────────────────────────────
←  ☒ Colors  ☐ Size  ✔ Submit  →

│ Which size?

❯ 1. Small
  2. Large
  3. Type something.
────────────────────────────────────────
  4. Chat about this

Enter to select · Tab/Arrow keys to navigate · Esc to cancel`

	reviewScreen = `────────────────────────────────────────
←  ☒ Colors  ☒ Size  ✔ Submit  →

Review your answers

   → Blue
   → Small

Ready to submit your answers?

❯ 1. Submit answers
  2. Cancel`

	claudePermScreen = `⏺ Bash(rm -rf build)
────────────────────────────────────────
 Bash command

   rm -rf build
   Remove the build folder

 ⚠ Claude Code will automatically block this request in 2:00, to avoid holding up an unattended session

 Do you want to proceed?
 ❯ 1. Yes
   2. No

 Esc to cancel · Tab to amend`

	codexScreen = `• Running the tests
Would you like to run the following command?

  $ go test ./...

› 1. Yes, proceed (y)
  2. Yes, and don't ask again for commands that start with ` + "`go test`" + ` (p)
  3. No, and tell Codex what to do differently (esc)

  Press enter to confirm or esc to cancel`
)

func screenLines(s string) []string { return lastLines(s, alertLines) }

func labels(opts []store.AlertOption) string {
	var out []string
	for _, o := range opts {
		out = append(out, o.Label+"="+o.Keys[0])
	}
	return strings.Join(out, " | ")
}

func TestPromptViewMultiSelect(t *testing.T) {
	v := parsePromptView(screenLines(multiScreen), true)
	if !v.ok || !v.isQuestion() || !v.multi() || !v.next {
		t.Fatalf("view %+v", v)
	}
	if v.tabs != "☐ Colors ☐ Size ✔ Submit" || v.question != "Which colors do you like?" {
		t.Errorf("tabs %q, question %q", v.tabs, v.question)
	}
	if got := labels(v.alertOptions()); got != "☐ 1 Red=1 | ☑ 2 Blue=2 | 4 Chat about this=4 | Next ➡="+nextKey+" | Esc=escape" {
		t.Errorf("buttons %s", got)
	}
	h := alertHTML(v, "Claude Code", store.AlertPermission, nil)
	for _, want := range []string{"❓ <b>Question</b> · Claude Code", "<b>Which colors do you like?</b>", "1. Red — <i>A warm color.</i>", "☑ 2. Blue", "Tick any, then tap Next"} {
		if !strings.Contains(h, want) {
			t.Errorf("alert lacks %q:\n%s", want, h)
		}
	}
	for _, not := range []string{"Esc to cancel", "Some answer of the agent", "─"} {
		if strings.Contains(h, not) {
			t.Errorf("alert has %q:\n%s", not, h)
		}
	}
}

func TestPromptViewSingleAndReview(t *testing.T) {
	v := parsePromptView(screenLines(singleScreen), true)
	if !v.ok || !v.isQuestion() || v.multi() || v.question != "Which size?" {
		t.Fatalf("single: %+v", v)
	}
	if got := labels(v.alertOptions()); got != "1 Small=1 | 2 Large=2 | 4 Chat about this=4 | Esc=escape" {
		t.Errorf("single buttons %s", got)
	}
	r := parsePromptView(screenLines(reviewScreen), true)
	if !r.ok || r.title != "Review your answers" || r.question != "Ready to submit your answers?" {
		t.Fatalf("review: %+v", r)
	}
	if got := labels(r.alertOptions()); got != "1 Submit answers=1 | 2 Cancel=2 | Esc=escape" {
		t.Errorf("review buttons %s", got)
	}
}

func TestPromptViewPermissions(t *testing.T) {
	v := parsePromptView(screenLines(claudePermScreen), true)
	if !v.ok || v.isQuestion() || v.title != "Bash command" || v.question != "Do you want to proceed?" {
		t.Fatalf("claude: %+v", v)
	}
	h := alertHTML(v, "Claude Code", store.AlertPermission, nil)
	for _, want := range []string{"🔐 <b>Permission needed</b> · Claude Code", "<b>Bash command</b>", "<pre>rm -rf build\nRemove the build folder</pre>", "<b>Do you want to proceed?</b>", "Reply with text to answer No"} {
		if !strings.Contains(h, want) {
			t.Errorf("alert lacks %q:\n%s", want, h)
		}
	}
	if strings.Contains(h, "Tab to amend") || strings.Contains(h, "automatically block") {
		t.Errorf("alert has a hint or the raw countdown:\n%s", h)
	}
	if got := alertHead(v, "Claude Code", store.AlertPermission); got != "🔐 Claude Code: Bash command · rm -rf build" {
		t.Errorf("head %q", got)
	}

	c := parsePromptView(screenLines(codexScreen), true)
	if !c.ok || c.question != "Would you like to run the following command?" {
		t.Fatalf("codex: %+v", c)
	}
	if got := labels(c.alertOptions()); !strings.Contains(got, "=y |") || !strings.Contains(got, "=p |") || !strings.HasSuffix(got, "=escape") {
		t.Errorf("codex buttons %s", got)
	}
	if h := alertHTML(c, "Codex", store.AlertPermission, nil); !strings.Contains(h, "<pre>$ go test ./...</pre>") || strings.Contains(h, "Running the tests") {
		t.Errorf("codex alert:\n%s", h)
	}
}

// The cursor, the countdown, and the key hints change while a prompt waits;
// none of them makes it another prompt.
func TestAlertHashIgnoresCursorAndHints(t *testing.T) {
	a := screenLines(multiScreen)
	moved := screenLines(strings.Replace(strings.Replace(multiScreen, "❯ 1.", "  1.", 1), "  2. [✔]", "❯ 2. [✔]", 1))
	insert := screenLines(strings.Replace(multiScreen, "Enter to select · Tab/Arrow keys to navigate · Esc to cancel", "-- INSERT -- · Enter to select · ctrl+g to edit in VS Code", 1))
	if alertHash(a) != alertHash(moved) || alertHash(a) != alertHash(insert) {
		t.Error("the cursor or the hints changed the hash")
	}
	ticked := screenLines(strings.Replace(multiScreen, "1. [ ] Red", "1. [✔] Red", 1))
	if alertHash(a) == alertHash(ticked) {
		t.Error("a ticked box did not change the hash")
	}
}

func TestPromptRows(t *testing.T) {
	rows, cur := promptRows(screenLines(multiScreen))
	if strings.Join(rows, ",") != "1,2,3,next,4" || cur != 0 {
		t.Errorf("rows %v cursor %d", rows, cur)
	}
	onNext := strings.Replace(strings.Replace(multiScreen, "❯ 1.", "  1.", 1), "     Next", "❯    Next", 1)
	if _, cur := promptRows(screenLines(onNext)); cur != 3 {
		t.Errorf("cursor on Next = %d, want 3", cur)
	}
}

// A numbered list in the agent's output is no prompt: it has no cursor, or
// other text follows it.
func TestPromptViewNeedsAWaitingList(t *testing.T) {
	noCursor := "⏺ Here is the plan:\n\nWhich step first?\n  1. Write the test\n  2. Fix the bug\n"
	if v := parsePromptView(screenLines(noCursor), true); v.ok {
		t.Errorf("a list without a cursor is a prompt: %+v", v)
	}
	textAfter := "Which step first?\n❯ 1. Write the test\n  2. Fix the bug\n\n⏺ Running the tests now\n"
	if v := parsePromptView(screenLines(textAfter), true); v.ok {
		t.Errorf("a list with the agent's output below it is a prompt: %+v", v)
	}
}

// An alert too long for one Telegram message falls back to the screen's
// bottom lines, which preBlock keeps short.
func TestAlertContentTooLongFallsBack(t *testing.T) {
	d := &daemon{cfg: config.Default("/h"), now: time.Now}
	var b strings.Builder
	b.WriteString("────────────────────\n│ Pick one?\n")
	for i := 1; i <= 9; i++ {
		fmt.Fprintf(&b, "  %d. Answer %d\n         %s\n", i, i, strings.Repeat("a long description ", 30))
	}
	screen := strings.Replace(b.String(), "  1. Answer 1", "❯ 1. Answer 1", 1)
	c := d.alertContent(screenLines(screen), store.AlertPermission, agents.Claude)
	if format.VisibleLen(c.text) > alertMaxText+200 || !strings.Contains(c.text, "<pre>") {
		t.Errorf("alert of %d characters, fallback %v", format.VisibleLen(c.text), strings.Contains(c.text, "<pre>"))
	}
}

// A long question wraps over lines on the screen, and one of the agent's
// own questions may not end with "?": the alert and the line that stays
// after the answer keep the whole question.
func TestQuestionTextWrapsAndNeedsNoQuestionMark(t *testing.T) {
	wrapped := `────────────────────────────────────────
←  ☐ Release  ☐ Follow-ups  ✔ Submit  →

│ Live test (answer in Telegram): ship this batch —
│ review fixes and steering — as v0.1.12?

❯ 1. PR, merge, release
  2. PR only
  3. Type something.
────────────────────────────────────────
  4. Chat about this

Enter to select · Tab/Arrow keys to navigate · Esc to cancel`
	v := parsePromptView(screenLines(wrapped), false)
	want := "Live test (answer in Telegram): ship this batch — review fixes and steering — as v0.1.12?"
	if !v.ok || v.question != want {
		t.Fatalf("question = %q (ok %v), want %q", v.question, v.ok, want)
	}
	if h := alertHead(v, "Claude Code", store.AlertPermission); !strings.Contains(h, "ship this batch") {
		t.Errorf("head = %q", h)
	}

	statement := `────────────────────────────────────────
←  ☒ Release  ☐ Follow-ups  ✔ Submit  →

│ Tick every follow-up you want,
│ then tap Next.

❯ 1. [ ] Poll-order test
  2. [ ] Steer notice check
  3. [ ] Type something
     Next
────────────────────────────────────────
  4. Chat about this

Enter to select · Tab/Arrow keys to navigate · Esc to cancel`
	v = parsePromptView(screenLines(statement), false)
	if !v.ok || v.question != "Tick every follow-up you want, then tap Next." {
		t.Fatalf("question = %q (ok %v)", v.question, v.ok)
	}
	if h := alertHead(v, "Claude Code", store.AlertPermission); !strings.HasSuffix(h, ": Tick every follow-up you want, then tap Next.") {
		t.Errorf("head = %q", h)
	}
}

// The review page of several questions is a question too, also when its tab
// bar is off the screen: its alert and the line that stays say so.
func TestReviewPageIsAQuestion(t *testing.T) {
	screen := `Review your answers

 ● Ship this batch as v0.1.12?
   → PR, merge, release

Ready to submit your answers?

❯ 1. Submit answers
  2. Cancel`
	v := parsePromptView(screenLines(screen), false)
	if !v.ok || !v.isQuestion() || v.question != "Ready to submit your answers?" {
		t.Fatalf("view = %+v", v)
	}
	if h := alertHead(v, "Claude Code", store.AlertPermission); h != "❓ Claude Code: Ready to submit your answers?" {
		t.Errorf("head = %q", h)
	}
}

// A question whose answers have previews (Claude Code 2.1.323, seen live,
// made-up text): the preview box stands right of the answers, "Chat about
// this" has no number, and a notes hint sits under the box.
const previewScreen = `────────────────────────────────────────────────────────────────────────────────────────────
 ☐ Layout

│ Which layout should the report use? The table one is wider, the list one
│ reads better on a phone.

❯ 1. Table                        ┌──────────────────────────────────────────────────┐
  2. List                         │ | Name | Qty |                                   │
                                  │ | apple | 3 |                                    │
                                  │ 1. Table of contents                             │
                                  └──────────────────────────────────────────────────┘

                                  Notes: press n to add notes

────────────────────────────────────────────────────────────────────────────────────────────
  Chat about this

Enter to select · ↑/↓ to navigate · n to add notes · Esc to cancel`

func TestQuestionWithPreviews(t *testing.T) {
	v := parsePromptView(screenLines(previewScreen), false)
	if !v.ok || !v.isQuestion() {
		t.Fatalf("not seen as a question: %+v", v)
	}
	if want := "Which layout should the report use? The table one is wider, the list one reads better on a phone."; v.question != want {
		t.Errorf("question = %q", v.question)
	}
	if got := labels(v.alertOptions()); got != "1 Table=1 | 2 List=2 | Esc=escape" {
		t.Errorf("buttons %s", got)
	}
	if h := alertHead(v, "Claude Code", store.AlertPermission); !strings.HasPrefix(h, "❓ Claude Code: Which layout") {
		t.Errorf("head = %q", h)
	}
	// The preview's own numbered line is not an answer.
	for _, o := range v.options {
		if strings.Contains(o.label, "contents") {
			t.Errorf("a preview line became an answer: %+v", o)
		}
	}
}
