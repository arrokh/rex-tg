package daemon

import (
	"fmt"
	"slices"
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
	if !r.ok || !r.review || r.question != "Ready to submit your answers?" || len(r.answers) != 2 ||
		r.answers[0].answer != "Blue" || r.answers[1].answer != "Small" {
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
	if got := labels(v.alertOptions()); got != "1 Yes=1 | 2 No=2 | Esc=escape" {
		t.Errorf("Claude permission choices = %s", got)
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

func TestUnknownBlockedPromptsAreVisibleWithoutGuessedActions(t *testing.T) {
	d := &daemon{cfg: config.Default("/h"), now: time.Now}
	lines := screenLines("A future confirmation prompt\nPress a key to continue")
	for _, rt := range []agents.Runtime{agents.Pi, agents.Claude, agents.Codex} {
		c := d.alertContent(lines, "", rt)
		if c.kind != store.AlertQuestion || len(c.opts) != 0 {
			t.Errorf("%s: kind %q, options %+v; want a no-action alert", rt, c.kind, c.opts)
		}
		if !strings.Contains(c.text, "No keys will be sent") || !strings.Contains(c.text, "Press a key to continue") {
			t.Errorf("%s: alert does not explain the safe fallback: %q", rt, c.text)
		}
		if alertMarkup(1, c.opts) != nil {
			t.Errorf("%s: an unrecognized prompt has a keyboard", rt)
		}
	}
}

func TestUnknownTypedPermissionHasNoGuessedActions(t *testing.T) {
	cfg := config.Default("/h")
	// Older generated configs may still contain these former default answers.
	cfg.Agents.Claude.Answers = []config.Answer{{Label: "1 Yes", Keys: []string{"1"}}}
	cfg.Agents.Codex.Answers = []config.Answer{{Label: "Yes", Keys: []string{"y"}}}
	d := &daemon{cfg: cfg, now: time.Now}
	lines := []string{"A future permission screen", "Delete the selected directory?", "  1. Continue", "  2. Cancel"}
	for _, rt := range []agents.Runtime{agents.Pi, agents.Claude, agents.Codex} {
		c := d.alertContent(lines, store.AlertPermission, rt)
		if c.kind != store.AlertPermission || len(c.opts) != 0 || alertMarkup(1, c.opts) != nil {
			t.Errorf("%s: kind %q, options %+v; want a no-action permission alert", rt, c.kind, c.opts)
		}
		if !strings.Contains(c.text, "No keys will be sent") || !strings.Contains(c.text, "Delete the selected directory") {
			t.Errorf("%s: the unknown action is not explained: %q", rt, c.text)
		}
	}
}

func TestConfiguredPermissionFallbackIsExplicitAndPermissionOnly(t *testing.T) {
	cfg := config.Default("/h")
	cfg.Agents.Claude.Answers = []config.Answer{{Label: "Known legacy choice", Keys: []string{"1"}}}
	lines := []string{"A future permission screen", "Delete the selected directory?", "  1. Continue", "  2. Cancel"}
	d := &daemon{cfg: cfg, now: time.Now}
	withoutOptIn := d.alertContent(lines, store.AlertPermission, agents.Claude)
	if len(withoutOptIn.opts) != 0 {
		t.Fatalf("answers without the opt-in = %+v", withoutOptIn.opts)
	}
	cfg.Agents.Claude.AllowUnknownPermissionAnswers = true
	d.cfg = cfg
	permission := d.alertContent(lines, store.AlertPermission, agents.Claude)
	if len(permission.opts) != 1 || permission.opts[0].Label != "Known legacy choice" ||
		!strings.Contains(permission.text, "configured fallback choices") {
		t.Fatalf("explicit fallback = %+v", permission)
	}
	for _, kind := range []string{"", store.AlertQuestion} {
		question := d.alertContent(lines, kind, agents.Claude)
		if len(question.opts) != 0 || alertMarkup(1, question.opts) != nil {
			t.Errorf("kind %q got configured permission choices: %+v", kind, question.opts)
		}
	}
}

func TestUnknownQuestionDoesNotUsePermissionFallbackAnswers(t *testing.T) {
	cfg := config.Default("/h")
	cfg.Agents.Claude.AllowUnknownPermissionAnswers = true
	cfg.Agents.Claude.Answers = []config.Answer{{Label: "Known fallback", Keys: []string{"1"}}}
	d := &daemon{cfg: cfg, now: time.Now}
	c := d.alertContent([]string{"Question needs input", "Type a response"}, store.AlertQuestion, agents.Claude)
	if c.kind != store.AlertQuestion || len(c.opts) != 0 {
		t.Fatalf("kind %q, options %+v; want a no-action question alert", c.kind, c.opts)
	}
	for _, opt := range c.opts {
		if strings.Contains(strings.ToLower(opt.Label), "always") || strings.Contains(strings.ToLower(opt.Label), "yes") {
			t.Errorf("permission fallback leaked into a question: %+v", opt)
		}
	}
}

func TestOversizedUntypedQuestionKeepsItsChoices(t *testing.T) {
	d := &daemon{cfg: config.Default("/h"), now: time.Now}
	var b strings.Builder
	b.WriteString("────────────────────\nPick each operation?\n\n❯ 1. [ ] Option 1\n")
	b.WriteString(strings.Repeat("         A detailed explanation of this choice. ", 30) + "\n")
	for i := 2; i <= 8; i++ {
		fmt.Fprintf(&b, "  %d. [ ] Option %d\n", i, i)
		b.WriteString(strings.Repeat("         A detailed explanation of this choice. ", 30) + "\n")
	}
	b.WriteString("     Next\n────────────────────\nEsc to cancel\n")
	lines := screenLines(b.String())
	v := parsePromptView(lines, false)
	c := d.alertContent(lines, "", agents.Claude)
	if c.kind != store.AlertQuestion || len(c.opts) < 8 {
		t.Fatalf("view ok %v, options %d, question %q; kind %q, options %d; oversized untyped question lost its choices", v.ok, len(v.options), v.question, c.kind, len(c.opts))
	}
	if !strings.Contains(c.text, "❓ Claude Code:") || !strings.Contains(c.text, "Pick each operation?") || strings.Contains(c.text, "Permission needed") {
		t.Errorf("fallback mislabels the question: %q", c.text)
	}
	if got := labels(c.opts); !strings.Contains(got, "Next ➡="+nextKey) {
		t.Errorf("oversized question lost its Next action: %s", got)
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
	if h := alertHead(v, "Claude Code", store.AlertPermission); h != "❓ Claude Code: Review your answers" {
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
	if got := labels(v.alertOptions()); got != "1 Table=1 | 2 List=2 | 💬 Chat about this=rex-tg:chat | Esc=escape" {
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

// The preview box is cut by screen column: a wide answer label (an emoji)
// before it does not shift the cut.
func TestCutPreviewByScreenColumn(t *testing.T) {
	lines := []string{
		"❯ 1. Ship ✅ now        ┌────────┐",
		"  2. Later              │ │ │ 1. x │",
		"                        └────────┘",
		"after",
	}
	got := cutPreview(lines)
	want := []string{"❯ 1. Ship ✅ now", "  2. Later", "", "after"}
	if !slices.Equal(got, want) {
		t.Errorf("cutPreview = %q, want %q", got, want)
	}
}

// The review page of long questions (Claude Code 2.1.323, seen live, made-up
// text): its title is pushed above the lines read, and each question wraps.
// It is known by its answers and reads as a list of questions and answers.
const longReviewScreen = `pages are public to anyone with the
   link, and a bot can't delete them. How should long
   answers open?
   → Private viewer (Recommended)
 ● Which address should the viewer use? Only you can open
   it, and every request is checked.
   → Tunnel (Recommended)

Ready to submit your answers?

❯ 1. Submit answers
  2. Cancel`

func TestReviewPageOfLongQuestions(t *testing.T) {
	v := parsePromptView(screenLines(longReviewScreen), false)
	if !v.ok || !v.review || !v.isQuestion() {
		t.Fatalf("not seen as the review page: %+v", v)
	}
	if len(v.answers) != 2 || v.answers[0].answer != "Private viewer (Recommended)" || v.answers[1].answer != "Tunnel (Recommended)" ||
		!strings.HasPrefix(v.answers[0].question, "… pages are public") || !strings.HasPrefix(v.answers[1].question, "Which address should the viewer use? Only you can open it,") {
		t.Fatalf("answers = %+v", v.answers)
	}
	h := alertHTML(v, "Claude Code", store.AlertPermission, nil)
	for _, want := range []string{"❓ <b>Review your answers</b> · Claude Code", "→ <b>Tunnel (Recommended)</b>", "<b>Ready to submit your answers?</b>", "Tap Submit answers"} {
		if !strings.Contains(h, want) {
			t.Errorf("alert lacks %q:\n%s", want, h)
		}
	}
	if strings.Contains(h, "Permission needed") || strings.Contains(h, "answer No") {
		t.Errorf("reads as a permission prompt:\n%s", h)
	}
	if got := labels(v.alertOptions()); got != "1 Submit answers=1 | 2 Cancel=2 | Esc=escape" {
		t.Errorf("buttons %s", got)
	}
	if hd := alertHead(v, "Claude Code", store.AlertPermission); hd != "❓ Claude Code: Review your answers" {
		t.Errorf("head = %q", hd)
	}
}

// The preview box changes with the cursor, so the screen's fingerprint
// leaves it out: the question stays the same question.
func TestAlertHashIgnoresThePreview(t *testing.T) {
	other := strings.Replace(previewScreen, "| Name | Qty |", "- Name: Qty   ", 1)
	if other == previewScreen {
		t.Fatal("the test screen did not change")
	}
	if alertHash(screenLines(other)) != alertHash(screenLines(previewScreen)) {
		t.Error("a new preview counts as a new question")
	}
	if alertHash(screenLines(strings.Replace(previewScreen, "Which layout", "Which colour", 1))) == alertHash(screenLines(previewScreen)) {
		t.Error("a new question counts as the same")
	}
}

// pi's extension dialogs (pi-coding-agent's selector), with made-up text.
const (
	piSelectScreen = `⠋ Working...
────────────────────────────────────────

 Pick a plan (20s)

 → Small
   Medium
   Large

 ↑↓ navigate  enter select  escape cancel

────────────────────────────────────────
~/work/demo (main)`

	piConfirmScreen = `────────────────────────────────────────

 Clear the plan?
 Plan: ship the thing (3 of 5 done)

 → Yes
   No

 ↑↓ navigate  enter select  escape cancel

────────────────────────────────────────
~/work/demo (main)`

	piScrollScreen = `────────────────────────────────────────

 Pick one

   Item 2
   Item 3
 → Item 4
   Item 5
   (4/12)

 ↑↓ navigate  enter select  escape cancel

────────────────────────────────────────`
)

func TestPromptViewPiSelect(t *testing.T) {
	v := parsePromptView(screenLines(piSelectScreen), true)
	if !v.ok || !v.dialog || !v.isQuestion() || v.multi() {
		t.Fatalf("view %+v", v)
	}
	if v.question != "Pick a plan" || len(v.body) != 0 {
		t.Errorf("question %q body %v (the countdown is no part of it)", v.question, v.body)
	}
	var labels []string
	for _, o := range v.alertOptions() {
		labels = append(labels, o.Label)
	}
	if got := strings.Join(labels, "|"); got != "1 Small|2 Medium|3 Large|Esc" {
		t.Errorf("buttons %q", got)
	}
	if k := v.options[1].key; k != pickPrefix+"Medium" {
		t.Errorf("key %q", k)
	}
	rows, cur := promptRows(screenLines(piSelectScreen))
	if len(rows) != 3 || cur != 0 || rows[2] != pickPrefix+"Large" {
		t.Errorf("rows %v cursor %d", rows, cur)
	}
}

func TestPromptViewPiConfirm(t *testing.T) {
	v := parsePromptView(screenLines(piConfirmScreen), true)
	if !v.ok || v.question != "Clear the plan?" || len(v.body) != 1 || v.body[0] != "Plan: ship the thing (3 of 5 done)" {
		t.Fatalf("view %+v", v)
	}
	if len(v.options) != 2 || v.options[0].label != "Yes" || v.options[1].label != "No" {
		t.Errorf("options %+v", v.options)
	}
	html := alertHTML(v, "pi", "permission", nil)
	if !strings.HasPrefix(html, "❓ <b>Question</b> · pi") || !strings.Contains(html, "Tap an answer") {
		t.Errorf("alert %q", html)
	}
	moved := strings.Replace(strings.Replace(piConfirmScreen, " → Yes", "   Yes", 1), "   No", " → No", 1)
	if _, cur := promptRows(screenLines(moved)); cur != 1 {
		t.Errorf("cursor %d, want 1", cur)
	}
}

func TestInferAlertKindNeedsRecognizableChoices(t *testing.T) {
	cases := []struct {
		name string
		view promptView
		want string
	}{
		{
			name: "explicit shell confirmation",
			view: promptView{question: "Do you want to proceed?", options: []viewOption{{label: "Yes"}, {label: "No"}}},
			want: store.AlertPermission,
		},
		{
			name: "pi extension dialog",
			view: promptView{question: "Pick one", dialog: true, options: []viewOption{{label: "Yes"}, {label: "No"}}},
			want: store.AlertQuestion,
		},
		{
			name: "unknown yes/no question",
			view: promptView{question: "Proceed or cancel?", options: []viewOption{{label: "Yes"}, {label: "No"}}},
		},
		{
			name: "unclassified choices",
			view: promptView{question: "Choose a mode", options: []viewOption{{label: "Fast"}, {label: "Safe"}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inferAlertKind(tc.view); got != tc.want {
				t.Errorf("kind = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUnknownChangedFollowUpScreenStaysEligibleForNoActionAlert(t *testing.T) {
	previous := alertHash(screenLines(piConfirmScreen))
	unknown := screenLines("A future follow-up prompt\nPress a key to continue")
	if parsePromptView(unknown, true).ok {
		t.Fatal("fixture unexpectedly parses as a prompt")
	}
	if !followUpScreenChanged(unknown, previous) {
		t.Fatal("changed unknown follow-up was suppressed")
	}
	if followUpScreenChanged(screenLines(piConfirmScreen), previous) {
		t.Fatal("the screen just answered was treated as a new follow-up")
	}
}

// The hash holds the dialog, not the cursor, the spinner above it, the
// footer under it, or the countdown.
func TestAlertHashPiDialog(t *testing.T) {
	base := alertHash(screenLines(piSelectScreen))
	for name, s := range map[string]string{
		"cursor":  strings.Replace(strings.Replace(piSelectScreen, " → Small", "   Small", 1), "   Medium", " → Medium", 1),
		"spinner": strings.Replace(piSelectScreen, "⠋ Working...", "⠙ Working... 3s", 1),
		"footer":  strings.Replace(piSelectScreen, "~/work/demo (main)", "~/work/demo (main)  12% ctx", 1),
		"timeout": strings.Replace(piSelectScreen, "(20s)", "(7s)", 1),
	} {
		if alertHash(screenLines(s)) != base {
			t.Errorf("%s changed the hash", name)
		}
	}
	if alertHash(screenLines(strings.Replace(piSelectScreen, "Large", "Huge", 1))) == base {
		t.Error("another answer did not change the hash")
	}
	if alertHash(screenLines(strings.Replace(piSelectScreen, "Pick a plan", "Pick a size", 1))) == base {
		t.Error("another question did not change the hash")
	}
	// A list that scrolls: the window moves with the cursor.
	scrolled := strings.Replace(strings.Replace(piScrollScreen, "   Item 2\n", "   Item 1\n", 1), "(4/12)", "(5/12)", 1)
	if alertHash(screenLines(piScrollScreen)) != alertHash(screenLines(scrolled)) {
		t.Error("the window of a long list changed the hash")
	}
	if v := parsePromptView(screenLines(piScrollScreen), true); !v.ok || len(v.options) != 4 {
		t.Errorf("scrolling list %+v", v)
	}
}

// Screens that only look like a dialog are none: no cursor row, two of
// them, a repeated answer, a wrong indent, or no rule above.
func TestPiDialogNeedsItsShape(t *testing.T) {
	for name, s := range map[string]string{
		"no cursor":   strings.Replace(piConfirmScreen, " → Yes", "   Yes", 1),
		"two cursors": strings.Replace(piConfirmScreen, "   No", " → No", 1),
		"same answer": strings.Replace(piConfirmScreen, "   No", "   Yes", 1),
		"indent":      strings.Replace(piConfirmScreen, "   No", "  No", 1),
		"no rule":     strings.Replace(piConfirmScreen, "────────────────────────────────────────\n\n Clear", "text\n\n Clear", 1),
	} {
		if d := parsePiDialog(screenLines(s)); d.ok {
			t.Errorf("%s: %+v", name, d)
		}
	}
	if d := parsePiDialog(screenLines(multiScreen)); d.ok {
		t.Errorf("a Claude Code question is no pi dialog: %+v", d)
	}
}

// Extensions add lines to pi's footer (usage, accounts, versions), so the
// hints of a dialog can sit well above the bottom of the screen (seen live:
// eight lines under them).
func TestPromptViewPiDialogAboveATallFooter(t *testing.T) {
	screen := piConfirmScreen + "\n" + strings.Join([]string{
		"~/work/demo • [demo] tidy up",
		"↑1k ↓2k R3k $0.001 (sub) 1.0%/200k (auto)          model-x • high",
		"acct-a 5h [██████████] 100% left",
		"acct-b 5h [████████░░] 80% left",
		"mail: someone@example.com (global)",
		"tool 1.2.3",
	}, "\n")
	v := parsePromptView(screenLines(screen), true)
	if !v.ok || !v.dialog || v.question != "Clear the plan?" || len(v.options) != 2 {
		t.Fatalf("view %+v", v)
	}
}
