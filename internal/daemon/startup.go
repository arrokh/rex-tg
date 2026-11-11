package daemon

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"rex-tg/internal/agents"
	"rex-tg/internal/registry"
	"rex-tg/internal/rex"
)

// Startup questions (design section 8.7). An agent that /new started may ask
// something on its screen before its session starts, such as "trust this
// folder?" of Claude Code and Codex. Until then the agent reports no session,
// so it has no topic where you could answer. rex-tg shows the question in
// Telegram with a button for each answer.
//
// Both agents draw the answers as a list with a cursor (❯ or ›) and take Up,
// Down, and Enter (read from Claude Code 2.1.322 and Codex 0.167.1). Claude
// Code's trust question shows no numbers, and its cursor starts on "No,
// exit". So a button moves the cursor with the arrow keys, one key at a
// time, and reads the screen after each key, as the vim steps do. It presses
// Enter only when the cursor is on the chosen answer, so Enter picks what the
// screen shows. Before each key, it checks again that typing is not paused
// and that the agent is in front.

// startupLines is how many lines at the bottom of the screen are searched.
const startupLines = 30

// maxQuestionLines is how many lines above the answers are shown.
const maxQuestionLines = 12

// startupMenu is a list of answers with a cursor on a pane's screen.
type startupMenu struct {
	question []string // the lines above the answers
	labels   []string // the answers, top to bottom
	cursor   int      // the answer with the cursor
}

// id tells questions apart by their text and answers, not by the cursor.
func (m startupMenu) id() string {
	return screenHash(append(append([]string{"q"}, m.question...), append([]string{"a"}, m.labels...)...))
}

// cursorRunes are the cursors that the agents draw in front of the chosen
// answer.
func isCursorRune(r rune) bool {
	return r == '❯' || r == '›' || r == '>' || r == '▶' || r == '▸'
}

// unbox removes a box's left and right border from a line, and the spaces at
// its end.
func unbox(l string) string {
	l = strings.TrimRight(l, " \t\r")
	if t := strings.TrimLeft(l, " "); t != "" {
		if r, size := utf8.DecodeRuneInString(t); r == '│' || r == '┃' || r == '║' {
			l = t[size:]
		}
	}
	for _, b := range []string{"│", "┃", "║"} {
		if strings.HasSuffix(l, b) {
			l = strings.TrimSuffix(l, b)
			break
		}
	}
	return strings.TrimRight(l, " ")
}

// indent is the number of spaces in front of a line's text.
func indent(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }

// isBorder reports a line that is only a box's top or bottom edge.
func isBorder(l string) bool {
	return strings.Trim(l, " ╭╮╰╯─━═┌┐└┘┏┓┗┛") == ""
}

// parseStartupMenu finds a list of answers with a cursor in the bottom lines
// of a screen. The cursor line starts with a cursor and a space; the other
// answers are the lines next to it whose text starts in the same column.
// Deeper lines between them (descriptions) are skipped. There must be one
// cursor line and at least two answers, so an input box with its prompt
// sign is not a list.
func parseStartupMenu(screen string) (startupMenu, bool) {
	raw := lastLines(screen, startupLines)
	lines := make([]string, len(raw))
	for i, l := range raw {
		lines[i] = unbox(l)
	}
	cur, label := -1, ""
	want := 0 // the number of characters in front of each answer's text
	for i, l := range lines {
		t := strings.TrimLeft(l, " ")
		r, size := utf8.DecodeRuneInString(t)
		rest := t[size:]
		if !isCursorRune(r) || !strings.HasPrefix(rest, " ") || strings.TrimSpace(rest) == "" {
			continue
		}
		if cur >= 0 {
			return startupMenu{}, false // two cursors: not one list
		}
		cur, label = i, strings.TrimSpace(rest)
		// The cursor stands where the other answers have a space, so count
		// characters, not bytes: ❯ is one character but three bytes.
		want = utf8.RuneCountInString(l) - utf8.RuneCountInString(strings.TrimLeft(rest, " "))
	}
	if cur < 0 {
		return startupMenu{}, false
	}
	isAnswer := func(l string) bool { return strings.TrimSpace(l) != "" && indent(l) == want }
	isDeeper := func(l string) bool { return strings.TrimSpace(l) != "" && indent(l) > want }

	var above []string
	first := cur
	for i := cur - 1; i >= 0; i-- {
		if isAnswer(lines[i]) {
			above = append([]string{strings.TrimSpace(lines[i])}, above...)
			first = i
		} else if !isDeeper(lines[i]) {
			break
		}
	}
	labels := append(above, label)
	for i := cur + 1; i < len(lines); i++ {
		if isAnswer(lines[i]) {
			labels = append(labels, strings.TrimSpace(lines[i]))
		} else if !isDeeper(lines[i]) {
			break
		}
	}
	if len(labels) < 2 || len(labels) > 9 {
		return startupMenu{}, false
	}
	var question []string
	for i := first - 1; i >= 0 && len(question) < maxQuestionLines; i-- {
		if !isBorder(lines[i]) {
			question = append([]string{strings.TrimRight(lines[i], " ")}, question...)
		}
	}
	for len(question) > 0 && strings.TrimSpace(question[0]) == "" {
		question = question[1:]
	}
	for len(question) > 0 && strings.TrimSpace(question[len(question)-1]) == "" {
		question = question[:len(question)-1]
	}
	return startupMenu{question: question, labels: labels, cursor: len(above)}, true
}

// Errors of answerStartup.
var (
	errStartupGone  = errors.New("the question is no longer on the screen")
	errStartupStuck = errors.New("the agent did not react to a key")
)

// readStartupMenu reads the pane's screen and the list on it.
func (d *daemon) readStartupMenu(ctx context.Context, st rex.Started) (startupMenu, bool, error) {
	screen, err := d.rx.Capture(ctx, st.SessionID, st.BlockID)
	if err != nil {
		return startupMenu{}, false, err
	}
	m, ok := parseStartupMenu(screen)
	return m, ok, nil
}

// startupSafe is the check before each key: typing is not paused, and the
// pane's foreground process is the agent that /new started.
func (d *daemon) startupSafe(ctx context.Context, st rex.Started, rt agents.Runtime) error {
	if d.paused.Load() {
		return errPausedBefore
	}
	p, err := d.rx.Process(ctx, st.SessionID, st.BlockID)
	if err != nil {
		return errRetryBefore
	}
	if got, ok := registry.Detect(d.rules, p.Foreground); !ok || got != rt {
		return errGoneBefore
	}
	return nil
}

// answerStartup picks answer target of the question id on the pane's screen:
// it moves the cursor there with Up or Down, one key at a time, then presses
// Enter, and waits until the question is gone. It returns errStartupGone when
// the question is not (or no longer) on the screen, errStartupStuck when a
// key shows no effect, errPausedBefore or errGoneBefore from the check
// before a key, and errRetryBefore when a call fails.
func (d *daemon) answerStartup(ctx context.Context, st rex.Started, rt agents.Runtime, id string, target int) error {
	m, ok, err := d.readStartupMenu(ctx, st)
	switch {
	case err != nil:
		return errRetryBefore
	case !ok || m.id() != id || target < 0 || target >= len(m.labels):
		return errStartupGone
	}
	for range len(m.labels) {
		if m.cursor == target {
			break
		}
		key := "down"
		if target < m.cursor {
			key = "up"
		}
		if err := d.startupSafe(ctx, st, rt); err != nil {
			return err
		}
		if err := d.rx.SendKeys(ctx, st.SessionID, st.BlockID, key); err != nil {
			return errRetryBefore
		}
		next, err := d.waitStartupChange(ctx, st, m)
		if err != nil {
			return err
		}
		m = next
	}
	if m.cursor != target {
		return errStartupStuck
	}
	if err := d.startupSafe(ctx, st, rt); err != nil {
		return err
	}
	if err := d.rx.SendKeys(ctx, st.SessionID, st.BlockID, "enter"); err != nil {
		return errRetryBefore
	}
	for range vimPolls {
		if !sleepCtx(ctx, d.timing.VimPoll) {
			return ctx.Err()
		}
		now, ok, err := d.readStartupMenu(ctx, st)
		if err == nil && (!ok || now.id() != id) {
			return nil
		}
	}
	return errStartupStuck
}

// waitStartupChange reads the screen until the cursor of question was
// moves. It returns errStartupGone when another screen shows, and
// errStartupStuck when the cursor stays. A screen without a list does not
// count at once: it may be a redraw caught halfway.
func (d *daemon) waitStartupChange(ctx context.Context, st rex.Started, was startupMenu) (startupMenu, error) {
	missing := false
	for range vimPolls {
		if !sleepCtx(ctx, d.timing.VimPoll) {
			return was, ctx.Err()
		}
		m, ok, err := d.readStartupMenu(ctx, st)
		switch {
		case err != nil:
			return was, errRetryBefore
		case !ok:
			missing = true
			continue
		case m.id() != was.id():
			return was, errStartupGone
		case m.cursor != was.cursor:
			return m, nil
		}
		missing = false
	}
	if missing {
		return was, errStartupGone
	}
	return was, errStartupStuck
}
