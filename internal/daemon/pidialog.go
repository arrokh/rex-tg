package daemon

import (
	"regexp"
	"strconv"
	"strings"
)

// pi shows the dialog of an extension (ctx.ui.select, and ctx.ui.confirm,
// which is a select of Yes and No) in the place of its editor, from
// pi-coding-agent's extension-selector component:
//
//	────────────────────────────────────────   a rule
//	                                           a blank line
//	 Clear the plan?                           the title, bold; confirm adds its
//	 Plan: ship the thing (3 of 5 done)        message in the next lines
//	                                           a blank line
//	 → Yes                                     the cursor row: "→ " in column 1
//	   No                                      the others: three columns in
//	 (3/12)                                    only when the list scrolls
//	                                           a blank line
//	 ↑↓ navigate  enter select  escape cancel  the key hints
//	                                           a blank line
//	────────────────────────────────────────   a rule, then pi's footer
//
// The answers have no numbers, so the digits of a Claude Code prompt do not
// reach them: a button moves the cursor to the row and presses Enter
// (pressRow). pi shows ten rows at most, and the window follows the cursor.
// With a timeout, the title ends in " (12s)".

// pickPrefix starts the pseudo key of an answer of a pi dialog; the rest is
// the answer's text, which is also its row id (promptRows).
const pickPrefix = "rex-tg:pick:"

var (
	piHintRe   = regexp.MustCompile(`(?i)^\s*↑↓ navigate\b.*\bselect\b.*\bcancel\b\s*$`)
	piScrollRe = regexp.MustCompile(`^\s*\(\d+/(\d+)\)\s*$`)
	piTimeRe   = regexp.MustCompile(`\s*\(\d+s\)$`)
)

// piHintLines is how far from the bottom of the screen the hints are looked
// for: pi's footer is under the dialog, and extensions make it taller.
const piHintLines = 20

// piDialog is a dialog of pi as it stands on the screen.
type piDialog struct {
	title  []string // the title and the message, the countdown cut off
	labels []string // the answers that show, top to bottom
	cursor int
	total  string // the length of a list that scrolls, else ""
	ok     bool
}

// parsePiDialog reads a dialog of pi from the bottom lines of a screen.
func parsePiDialog(lines []string) piDialog {
	hint := -1
	for i := len(lines) - 1; i >= 0 && len(lines)-i <= piHintLines; i-- {
		if piHintRe.MatchString(lines[i]) {
			hint = i
			break
		}
	}
	if hint < 0 {
		return piDialog{}
	}
	var d piDialog
	i := hint - 1
	for i >= 0 && strings.TrimSpace(lines[i]) == "" {
		i--
	}
	if i >= 0 {
		if m := piScrollRe.FindStringSubmatch(lines[i]); m != nil {
			d.total = m[1]
			i--
		}
	}
	// The answers: lines next to each other, one with the cursor.
	var rows []string
	curIndent := -1
	for ; i >= 0 && strings.TrimSpace(lines[i]) != ""; i-- {
		rows = append(rows, strings.TrimRight(lines[i], " \t\r"))
	}
	if len(rows) == 0 {
		return piDialog{}
	}
	for k, j := 0, len(rows)-1; k < j; k, j = k+1, j-1 {
		rows[k], rows[j] = rows[j], rows[k]
	}
	d.cursor = -1
	for k, r := range rows {
		if t := strings.TrimLeft(r, " "); strings.HasPrefix(t, "→ ") {
			if d.cursor >= 0 {
				return piDialog{}
			}
			d.cursor, curIndent = k, indent(r)
		}
	}
	if d.cursor < 0 {
		return piDialog{}
	}
	seen := map[string]bool{}
	for k, r := range rows {
		t := strings.TrimSpace(r)
		if k == d.cursor {
			t = strings.TrimSpace(strings.TrimPrefix(t, "→"))
		} else if indent(r) != curIndent+2 {
			return piDialog{}
		}
		if t == "" || seen[t] {
			return piDialog{} // an answer is told by its text
		}
		seen[t] = true
		d.labels = append(d.labels, t)
	}
	// The title, up to the rule above it.
	ruled := false
	var title []string
	for ; i >= 0 && len(title) <= maxQuestionLines; i-- {
		t := strings.TrimSpace(lines[i])
		if ruleRe.MatchString(t) && isBorder(t) {
			ruled = true
			break
		}
		if t != "" {
			title = append(title, piTimeRe.ReplaceAllString(t, ""))
		}
	}
	if !ruled {
		return piDialog{} // no rule above: not pi's dialog, or too tall to tell
	}
	if len(title) == 0 {
		return piDialog{}
	}
	for k := len(title) - 1; k >= 0; k-- {
		d.title = append(d.title, title[k])
	}
	d.ok = true
	return d
}

// view is the dialog as a prompt view: the first line asks, the next ones
// say more, and each answer is a row to walk to.
func (d piDialog) view() promptView {
	v := promptView{question: d.title[0], body: d.title[1:], dialog: true, ok: true}
	for i, l := range d.labels {
		v.options = append(v.options, viewOption{num: strconv.Itoa(i + 1), label: l, key: pickPrefix + l})
	}
	return v
}

// hashLines are the lines that make the dialog the same one: its title, and
// its answers (only their number when the list scrolls, since the window
// moves with the cursor). The spinner above and the footer under it change
// by themselves, and the cursor moves.
func (d piDialog) hashLines() []string {
	out := append([]string{"pi dialog"}, d.title...)
	if d.total != "" {
		return append(out, "of "+d.total)
	}
	return append(out, d.labels...)
}

// rows are the ids of the answers (pickPrefix and the text), top to bottom.
func (d piDialog) rows() []string {
	out := make([]string, len(d.labels))
	for i, l := range d.labels {
		out[i] = pickPrefix + l
	}
	return out
}
