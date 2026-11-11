package daemon

import (
	"regexp"
	"strings"
	"time"
	"unicode"
)

// vimMode is the mode that the pi-vim prompt editor (a pi package) shows on
// screen. pi-vim drops a paste while in NORMAL mode, so rex-tg reads the mode
// before it types into a pi pane (found live: a message was typed into a
// pi-vim prompt in NORMAL mode and never arrived).
//
// Claude Code's /vim mode needs none of this: its prompt takes a paste in
// every vim mode, and it shows no label in NORMAL mode (read from the Claude
// Code 2.1.322 bundle). Codex has no vim mode.
type vimMode int

const (
	vimNone   vimMode = iota // no vim label: an ordinary editor
	vimInsert                // INSERT: text goes in as typed
	vimNormal                // plain NORMAL: "i" enters INSERT
	vimVisual                // VISUAL or V-LINE: Escape returns to NORMAL without reaching the agent
	vimBusy                  // NORMAL with a pending command, EX, or unknown: do not guess
)

// piVimLabel matches the mode that pi-vim draws at the right end of the input
// box's top border, for example "──── NORMAL", "──── INSERT", or
// "──── NORMAL 3d_".
var piVimLabel = regexp.MustCompile(`^\s*─[─\s]*─\s+(NORMAL|INSERT|VISUAL|V-LINE|EX)\b(.*)$`)

// vimLines is how many lines at the bottom of the screen are searched: the
// prompt editor and its status lines sit there.
const vimLines = 20

// vimModeSwitchWait is the pause after the keys that enter INSERT mode, so the
// editor has switched before the paste arrives.
const vimModeSwitchWait = 150 * time.Millisecond

// reasonVimBusy is the refusal reason when the editor is mid-command.
const reasonVimBusy = "the prompt editor is in the middle of a vim command; finish or cancel it on the Mac, then send again"

// piVimMode reports the pi-vim mode from a pi pane's screen.
func piVimMode(screen string) vimMode {
	lines := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	if len(lines) > vimLines {
		lines = lines[len(lines)-vimLines:]
	}
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimRight(lines[i], " ┃│")
		if m := piVimLabel.FindStringSubmatch(l); m != nil {
			return vimModeFrom(m[1], strings.TrimSpace(m[2]))
		}
	}
	return vimNone
}

func vimModeFrom(label, rest string) vimMode {
	switch label {
	case "INSERT":
		return vimInsert
	case "NORMAL":
		if rest == "" {
			return vimNormal
		}
		return vimBusy
	case "VISUAL", "V-LINE":
		return vimVisual
	}
	return vimBusy
}

// enterInsertKeys returns the keys that put the editor in INSERT mode before
// rex-tg pastes, and false when the editor is in a state where pressing keys
// would be a guess (a pending vim command or EX mode).
func enterInsertKeys(m vimMode) ([]string, bool) {
	switch m {
	case vimNone, vimInsert:
		return nil, true
	case vimNormal:
		return []string{"i"}, true
	case vimVisual:
		return []string{"escape", "i"}, true
	}
	return nil, false
}

// noAutoLink stops Telegram from turning names such as "resume.md" into web
// links: it puts a zero-width space after a dot that sits between a letter or
// digit and a letter. It is for display text only.
func noAutoLink(s string) string {
	r := []rune(s)
	var b strings.Builder
	for i, c := range r {
		b.WriteRune(c)
		if c == '.' && i > 0 && i+1 < len(r) &&
			(unicode.IsLetter(r[i-1]) || unicode.IsDigit(r[i-1])) && unicode.IsLetter(r[i+1]) {
			b.WriteRune('\u200b')
		}
	}
	return b.String()
}

// interruptKeys adds one Escape in front of an interrupt that starts with
// Escape when pi-vim would take that first Escape to leave INSERT or VISUAL
// mode. Without the label, nothing is added: pi opens its session tree on a
// double Escape by default (its doubleEscapeAction setting).
func interruptKeys(m vimMode, keys []string) []string {
	if (m != vimInsert && m != vimVisual) || len(keys) == 0 || keys[0] != "escape" {
		return keys
	}
	return append([]string{"escape"}, keys...)
}
