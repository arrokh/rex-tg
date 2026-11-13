package daemon

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"rex-tg/internal/format"
	"rex-tg/internal/store"
	"rex-tg/internal/termimg"
)

// An alert shows the prompt the way a person reads it, not as a screen dump
// (design section 8.6): who asks, what it wants to do, the warnings, and the
// question; the answers are the buttons. When it is answered, the alert
// shrinks to one line with the question and the answer, so old prompts do
// not fill the topic.
//
// The layouts come from live screens of Claude Code 2.1.323 and the binaries
// of Claude Code and Codex 0.167.1:
//
//	Claude Code permission       Claude Code question (AskUserQuestion)
//	 Bash command                 ←  ☐ Leftovers  ☐ Drafts  ✔ Submit  →
//	   rm -f build                │ Which ones should I fix?
//	 ⚠ … block this request in    ❯ 1. [ ] 👍 on /model
//	 Do you want to proceed?               Control commands get a 👍 …
//	 ❯ 1. Yes                       2. [ ] Prune old keys
//	   2. No                        3. [ ] Type something
//	 Esc to cancel · Tab to amend      Next
//	                              ──────────────
//	                                4. Chat about this
//	                              Enter to select · Tab/Arrow keys to …
//
// Several answers to tick: a digit ticks or unticks one and moves the
// cursor there; "Next" (or "Submit") under them goes on, by Enter with the
// cursor on it. Several questions: a tab bar on top; each answered question
// moves to the next tab, and the last tab is "Review your answers" with
// "1. Submit answers" and "2. Cancel".

// viewOption is one answer of a prompt.
type viewOption struct {
	num   string // "1"
	label string // "Yes", without the number and the box
	desc  string // the line under it, if any
	key   string // the key that picks it: the digit, or the printed key
	box   int    // 0 no box, 1 "[ ]", 2 "[✔]"
	input bool   // "Type something": a text field, not an answer
}

// promptView is a prompt as an alert shows it.
type promptView struct {
	tabs     string   // the tab bar of several questions: "☒ Leftovers · ☐ Drafts · ✔ Submit"
	title    string   // the first line, such as "Bash command"
	body     []string // the lines between the title and the question
	notes    []string // warnings, such as "⚠ …"
	question string   // "Do you want to proceed?", or the question asked
	options  []viewOption
	next     bool // a "Next" or "Submit" row under answers to tick
	review   bool // the "Review your answers" page of several questions
	ok       bool // the screen holds a prompt this view understands
}

// isQuestion tells a question (AskUserQuestion) from a permission prompt.
func (v promptView) isQuestion() bool {
	if v.tabs != "" || v.next || v.review {
		return true
	}
	for _, o := range v.options {
		if o.box != 0 || o.input {
			return true
		}
	}
	return false
}

// multi tells whether the answers are ticked, several at a time.
func (v promptView) multi() bool {
	for _, o := range v.options {
		if o.box != 0 {
			return true
		}
	}
	return false
}

var (
	boxRe      = regexp.MustCompile(`^\[( |✔|✓|x|X)\]\s*`)
	ruleRe     = regexp.MustCompile(`[─━═]{8,}`)
	agentOutRe = regexp.MustCompile(`^(⏺|•|⎿|✻|✽|✶|✢|·\s)`)
)

// footerLine tells a line of key hints, which the buttons replace.
func footerLine(l string) bool {
	l = strings.ToLower(l)
	for _, w := range []string{"esc to cancel", "enter to select", "to navigate", "tab to amend", "tab amend", "to add notes",
		"-- insert --", "ctrl+g", "press enter to confirm", "enter to confirm", "esc to go back"} {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// stripCursor removes a cursor in front of a line's text.
func stripCursor(l string) (string, bool) {
	t := strings.TrimLeft(l, " ")
	r, size := utf8.DecodeRuneInString(t)
	if isCursorRune(r) && strings.HasPrefix(t[size:], " ") {
		return strings.Repeat(" ", indent(l)+1) + t[size:], true
	}
	return l, false
}

// cutPreview removes the preview box that Claude Code draws to the right of
// a question's answers when they have previews: from the row of its top
// corner (┌, after two spaces) to the row of its bottom corner (└ in the
// same column), each row is cut at that column. Lines above and below, such
// as a long question, stay whole.
func cutPreview(lines []string) []string {
	top, col := -1, -1
	for i, l := range lines {
		w := 0
		var prev, prev2 rune
		for k, r := range l {
			if r == '┌' && prev == ' ' && prev2 == ' ' && strings.TrimSpace(l[:k]) != "" {
				top, col = i, w
				break
			}
			prev2, prev = prev, r
			w += runeWidth(r)
		}
		if top >= 0 {
			break
		}
	}
	if top < 0 {
		return lines
	}
	out := append([]string(nil), lines...)
	for i := top; i < len(out); i++ {
		head, rest := cutAtColumn(out[i], col)
		out[i] = strings.TrimRight(head, " ")
		if r, _ := utf8.DecodeRuneInString(rest); r == '└' {
			break
		}
	}
	return out
}

// cutAtColumn splits a line at a screen column: a wide character (CJK, most
// emoji) takes two columns, as in the terminal.
func cutAtColumn(l string, col int) (head, rest string) {
	w := 0
	for k, r := range l {
		if w >= col {
			return l[:k], l[k:]
		}
		w += runeWidth(r)
	}
	return l, ""
}

// runeWidth is the number of terminal columns a character takes.
func runeWidth(r rune) int { return termimg.RuneWidth(r) }

// parsePromptView reads a prompt from the bottom lines of a screen. It finds
// the last list of numbered answers, takes the prompt's lines above it (up to
// a rule, the agent's output, or maxQuestionLines), and the descriptions,
// the text field, and the "Next" row below.
func parsePromptView(lines []string, shortcuts bool) promptView {
	var v promptView
	lines = cutPreview(lines)
	clean := make([]string, len(lines))
	for i, l := range lines {
		clean[i] = unbox(l)
	}
	// The last list numbered from 1.
	first := -1
	for i, l := range clean {
		if m := optionRe.FindStringSubmatch(l); m != nil && m[1] == "1" {
			first = i
		}
	}
	if first < 0 {
		return v
	}
	// Answers, their descriptions, and the rows under them. A prompt waits
	// for a key: one of its rows has the cursor, and nothing but key hints
	// comes after it. A numbered list in the agent's output has neither.
	optIndent, hasCursor, after := -1, false, false
	for i := first; i < len(clean); i++ {
		l, cur := stripCursor(clean[i])
		t := strings.TrimSpace(l)
		if cur && (optionRe.MatchString(clean[i]) || t == "Next" || t == "Submit") {
			hasCursor = true
		}
		switch {
		case t == "" || isBorder(l) || ruleRe.MatchString(t) || footerLine(t):
			continue
		case t == "Chat about this":
			// Without a number (a question with previews): the arrows reach
			// it, no digit does, so it gets no button.
			continue
		case optionRe.MatchString(clean[i]):
			m := optionRe.FindStringSubmatch(clean[i])
			text := strings.TrimSpace(m[2])
			o := viewOption{num: m[1], key: m[1]}
			if bm := boxRe.FindStringSubmatch(text); bm != nil {
				o.box = 1
				if bm[1] != " " {
					o.box = 2
				}
				text = strings.TrimSpace(text[len(bm[0]):])
			}
			if k := shortcutKey(text); shortcuts && k != "" {
				o.key = k
			}
			o.label = text
			lt := strings.TrimSuffix(strings.ToLower(text), ".")
			o.input = lt == "type something" || lt == "other"
			v.options = append(v.options, o)
			optIndent = indent(l)
		case t == "Next" || t == "Submit":
			v.next = true
		case len(v.options) > 0 && indent(l) > optIndent:
			// A description under the answer before it.
			o := &v.options[len(v.options)-1]
			if o.desc == "" {
				o.desc = t
			}
		default:
			after = true // other text below the list: not a prompt that waits
		}
	}
	// The prompt's lines above the answers.
	start := first
	for i := first - 1; i >= 0 && first-i <= maxQuestionLines; i-- {
		t := strings.TrimSpace(clean[i])
		if ruleRe.MatchString(t) || (t != "" && isBorder(clean[i])) || agentOutRe.MatchString(t) || strings.HasPrefix(t, "❯ ") {
			break
		}
		start = i
	}
	var block []string
	var para []int // the paragraph of each block line: blank lines split them
	n := 0
	for _, l := range clean[start:first] {
		t := strings.TrimSpace(l)
		switch {
		case t == "" || footerLine(t):
			n++
		case strings.Contains(t, "←") && strings.Contains(t, "→") && strings.ContainsAny(t, "☐☒✔"),
			// One question: its tab alone, as the first line above it (a
			// checklist line further down is no tab).
			len(block) == 0 && v.tabs == "" && (strings.HasPrefix(t, "☐ ") || strings.HasPrefix(t, "☒ ")):
			f := strings.Fields(strings.Trim(t, "←→ "))
			v.tabs = strings.Join(f, " ")
			n++
		default:
			if strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(t, "│")), "Review your answers") {
				v.review = true // also when its tab bar is not on the screen
			}
			block = append(block, l)
			para = append(para, n)
		}
	}
	plain := func(l string) string { return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "│")) }
	question := -1
	for i := len(block) - 1; i >= 0; i-- {
		if t := plain(block[i]); strings.HasSuffix(t, "?") && !deadlineRe.MatchString(t) {
			question = i
			break
		}
	}
	// A question of the agent's own (Claude Code's AskUserQuestion) may not
	// end with "?", and a long one wraps over lines: take its last
	// paragraph, all of it. A permission prompt keeps its one line.
	qFrom := question
	if v.isQuestion() && len(block) > 0 {
		if question < 0 {
			question = len(block) - 1
			for question > 0 && deadlineRe.MatchString(plain(block[question])) {
				question--
			}
		}
		qFrom = question
		for qFrom > 0 && para[qFrom-1] == para[question] && !deadlineRe.MatchString(plain(block[qFrom-1])) {
			qFrom--
		}
	}
	var qParts []string
	for i, l := range block {
		t := strings.TrimSpace(l)
		bar := strings.HasPrefix(t, "│")
		t = strings.TrimSpace(strings.TrimPrefix(t, "│"))
		switch {
		case i >= qFrom && i <= question && question >= 0:
			qParts = append(qParts, t)
			v.question = strings.Join(qParts, " ")
		case deadlineRe.MatchString(t):
			// The alert says it in its own words (deadlineLine).
		case strings.HasPrefix(t, "⚠") || (bar && v.tabs == ""):
			v.notes = append(v.notes, t)
		case v.title == "" && i < max(question, 1):
			v.title = t
		default:
			v.body = append(v.body, t)
		}
	}
	if v.title == v.question {
		v.title = ""
	}
	if v.isQuestion() {
		for i := range v.options {
			v.options[i].key = v.options[i].num // a question takes its digits
		}
	}
	v.ok = len(v.options) > 0 && (v.question != "" || v.title != "") && hasCursor && !after
	return v
}

// alertOptions are the buttons of a view: one per answer (a box to tick
// shows ☐ or ☑), "Next ➡" when answers are ticked, and Esc. A text field
// gets no button: a reply with text does that (alertNote).
func (v promptView) alertOptions() []store.AlertOption {
	var out []store.AlertOption
	for _, o := range v.options {
		if o.input {
			continue
		}
		label := o.num + " " + o.label
		switch o.box {
		case 1:
			label = "☐ " + label
		case 2:
			label = "☑ " + label
		}
		out = append(out, store.AlertOption{Label: format.Truncate(label, alertMaxBtn), Keys: []string{o.key}})
	}
	if v.next && v.multi() {
		out = append(out, store.AlertOption{Label: "Next ➡", Keys: []string{nextKey}})
	}
	return withEsc(out)
}

// nextKey is the pseudo key of "Next ➡": move the cursor to the Next row and
// press Enter (pressNext).
const nextKey = "rex-tg:next"

// toggleOption tells an answer that ticks a box: the alert stays open.
func toggleOption(o store.AlertOption) bool {
	return strings.HasPrefix(o.Label, "☐ ") || strings.HasPrefix(o.Label, "☑ ")
}

// maxBodyLines and maxBodyRunes cut a long command or diff in an alert.
const (
	maxBodyLines = 12
	maxBodyRunes = 1200
)

// alertHTML is the text of an alert: who asks and what, then the question.
// The screen's text is redacted.
func alertHTML(v promptView, agent, kind string, dl *alertDeadline) string {
	r := func(s string) string { return format.Escape(format.Redact(s)) }
	var b strings.Builder
	if v.isQuestion() || kind == store.AlertQuestion {
		fmt.Fprintf(&b, "❓ <b>Question</b> · %s", format.Escape(agent))
	} else {
		fmt.Fprintf(&b, "🔐 <b>Permission needed</b> · %s", format.Escape(agent))
	}
	if v.tabs != "" {
		b.WriteString("\n<i>" + r(v.tabs) + "</i>")
	}
	if v.title != "" {
		b.WriteString("\n<b>" + r(v.title) + "</b>")
	}
	if len(v.body) > 0 {
		body := v.body
		if len(body) > maxBodyLines {
			body = append(append([]string{}, body[:maxBodyLines]...), "…")
		}
		// Redact first, so a cut cannot split a secret past the patterns.
		text := format.Redact(strings.Join(body, "\n"))
		if rs := []rune(text); len(rs) > maxBodyRunes {
			text = string(rs[:maxBodyRunes]) + "…"
		}
		if v.isQuestion() {
			b.WriteString("\n" + format.Escape(text))
		} else {
			b.WriteString("\n<pre>" + format.Escape(text) + "</pre>")
		}
	}
	for _, n := range v.notes {
		b.WriteString("\n" + r(n))
	}
	if dl != nil {
		b.WriteString("\n" + deadlineLine(*dl))
	}
	if v.question != "" {
		b.WriteString("\n\n<b>" + r(v.question) + "</b>")
	}
	// Answers with a description are listed, since a button holds only a
	// few words.
	described := false
	for _, o := range v.options {
		if o.desc != "" {
			described = true
		}
	}
	if described {
		for _, o := range v.options {
			if o.input {
				continue
			}
			line := o.num + ". " + o.label
			if o.box == 2 {
				line = "☑ " + line
			}
			if o.desc != "" {
				line += " — <i>" + r(o.desc) + "</i>"
				line = strings.Replace(line, o.num+". "+o.label, o.num+". "+r(o.label), 1)
			} else {
				line = r(line)
			}
			b.WriteString("\n" + line)
		}
	}
	switch {
	case v.multi():
		b.WriteString("\n<i>Tick any, then tap Next. Reply with text to cancel and send your own answer.</i>")
	case v.isQuestion() || kind == store.AlertQuestion:
		b.WriteString("\n<i>Reply with text to cancel and send your own answer.</i>")
	default:
		b.WriteString("\n<i>Reply with text to answer No and send it as a note.</i>")
	}
	return b.String()
}

// alertHead is the one line that stays of an answered alert: who asked and
// what.
func alertHead(v promptView, agent, kind string) string {
	icon := "🔐"
	if v.isQuestion() || kind == store.AlertQuestion {
		icon = "❓"
	}
	what := v.title
	if len(v.body) > 0 {
		if what != "" {
			what += " · "
		}
		what += v.body[0]
	}
	if what == "" || v.isQuestion() {
		what = v.question
	}
	return icon + " " + format.Escape(agent) + ": " + format.Escape(format.Truncate(format.Redact(what), 120))
}

// promptRows are the rows a cursor moves over: the answers and the Next row,
// top to bottom, and the one with the cursor (-1: none).
func promptRows(lines []string) (rows []string, cursor int) {
	cursor = -1
	// Only the prompt's list: from its last row numbered 1.
	start := 0
	for i, l := range lines {
		if m := optionRe.FindStringSubmatch(unbox(l)); m != nil && m[1] == "1" {
			start = i
		}
	}
	for _, l := range lines[start:] {
		l = unbox(l)
		plain, cur := stripCursor(l)
		t := strings.TrimSpace(plain)
		switch {
		case optionRe.MatchString(l):
			m := optionRe.FindStringSubmatch(l)
			rows = append(rows, m[1])
		case t == "Next" || t == "Submit":
			rows = append(rows, "next")
		default:
			continue
		}
		if cur {
			cursor = len(rows) - 1
		}
	}
	return rows, cursor
}

// alertHash is the hash of a prompt on the screen that the cursor and the
// countdown do not change, so a cursor moved on the Mac or by "Next" does not
// make the alert stale.
func alertHash(lines []string) string {
	var out []string
	for _, l := range lines {
		l, _ = stripCursor(unbox(l))
		if footerLine(strings.TrimSpace(l)) {
			continue // the hints change with the row under the cursor
		}
		out = append(out, l)
	}
	return screenHash(out)
}
