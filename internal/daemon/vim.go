package daemon

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"rex-tg/internal/store"
)

// vimMode is the mode that the pi-vim prompt editor (a pi package) shows on
// screen. pi-vim drops a paste unless it is in INSERT mode, so rex-tg brings
// it there before it types into a pi pane (found live: a message was typed
// into a pi-vim prompt in NORMAL mode and never arrived). Without pi-vim
// there is no label, and typing works as before.
//
// Claude Code's /vim mode takes a paste in every vim mode (read from the
// Claude Code 2.1.322 bundle), but Enter does not submit in VISUAL mode: see
// claude_vim.go. Codex has no vim mode.
type vimMode int

const (
	vimNone   vimMode = iota // no pi-vim label: an ordinary editor
	vimInsert                // INSERT: a paste goes in
	vimNormal                // plain NORMAL: an Escape here reaches pi and stops the agent
	vimVisual                // VISUAL or V-LINE, with or without a pending count
	vimBusy                  // a pending command (for example NORMAL 3d_) or an EX line
)

// vimState is the pi-vim label on a pane's screen.
type vimState struct {
	mode  vimMode
	label string // for example "NORMAL 3d_"; empty without pi-vim
}

// piVimLabel matches the mode label that pi-vim draws at the right end of the
// input box's bottom border, for example "──── NORMAL", "──── INSERT", or
// "──── NORMAL 3d_" (getModeLabel in pi-vim's index.ts).
var piVimLabel = regexp.MustCompile(`^\s*─[─\s]*─\s+(NORMAL|INSERT|VISUAL|V-LINE|EX)\b(.*)$`)

// vimLines is how many lines at the bottom of the screen are searched: the
// prompt editor and its status lines sit there.
const vimLines = 20

// pi-vim steps. Each key is one step, and the screen is read after it, every
// Timing.VimPoll (so a key's effect may take about 600 ms to show). After
// errVimStuck the next try comes after Timing.VimRetry.
const (
	vimMaxKeys = 6  // keys in one try
	vimPolls   = 15 // screen reads while waiting for a key's effect
)

// vimStuckNote is said once per message when pi-vim does not react.
const vimStuckNote = "⏳ The vim editor did not react to a key, so your message waits. It is typed when the editor reacts again."

// errVimStuck means pi-vim did not show the effect of a key in time. No more
// keys go to that pane while its label stays the same, so keys cannot pile
// up in a pane that does not read them. The message waits and is tried again.
var errVimStuck = errors.New("pi-vim did not react to a key")

// piVimState reads the pi-vim label from a pi pane's screen.
func piVimState(screen string) vimState {
	lines := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	if len(lines) > vimLines {
		lines = lines[len(lines)-vimLines:]
	}
	for i := len(lines) - 1; i >= 0; i-- {
		m := piVimLabel.FindStringSubmatch(strings.TrimRight(lines[i], " ┃│"))
		if m == nil {
			continue
		}
		rest := strings.TrimSpace(m[2])
		return vimState{mode: vimModeFrom(m[1], rest), label: strings.TrimSpace(m[1] + " " + rest)}
	}
	return vimState{}
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

// nextVimKey returns the one key that moves pi-vim a step toward want, or ""
// when it is there or absent. want is vimInsert before a paste and vimNormal
// before an interrupt. pi-vim keeps an Escape for itself in INSERT and VISUAL
// mode, on an EX line, and with a pending command; each such Escape goes one
// step back toward plain NORMAL (handleEscape in pi-vim's index.ts). In plain
// NORMAL mode an Escape reaches pi and stops the agent, so it is never chosen
// there.
func nextVimKey(m, want vimMode) string {
	switch m {
	case vimNone:
		return ""
	case vimNormal:
		if want == vimInsert {
			return "i"
		}
		return ""
	case vimInsert:
		if want == vimInsert {
			return ""
		}
	}
	return "escape"
}

// vimStuckMark is what d.vimStuck holds for a pane: the label that showed no
// reaction to a key, and the pi process it was seen with.
type vimStuckMark struct {
	label string
	pid   int
}

// settlePiVim brings pi-vim to want, one key at a time, and reads the screen
// after each key, so no key is pressed on a guess. Before each key it checks
// again that typing is not paused and that the agent is in front, because
// this can take a few seconds. It reports whether it pressed a key.
//
// It returns nil when pi-vim shows want, when the pane has no pi-vim label,
// or when the screen cannot be read at the start (then typing goes on as
// without pi-vim). It returns errPausedBefore or errGoneBefore from the
// check, errVimStuck when a key shows no effect, and errRetryBefore or a
// context error when a call fails on the way.
func (d *daemon) settlePiVim(ctx context.Context, pane store.Pane, want vimMode) (pressed bool, err error) {
	screen, err := d.rx.Capture(ctx, pane.Session, pane.Block)
	if err != nil {
		return false, nil
	}
	st := piVimState(screen)
	for range vimMaxKeys {
		if v, ok := d.vimStuck.Load(pane.ID); ok {
			if m := v.(vimStuckMark); m.label != st.label || m.pid != pane.PID {
				d.vimStuck.Delete(pane.ID) // the screen or the process changed since
			}
		}
		key := nextVimKey(st.mode, want)
		if key == "" {
			return pressed, nil
		}
		if _, ok := d.vimStuck.Load(pane.ID); ok {
			return pressed, errVimStuck
		}
		switch paused, front, err := d.safeNow(pane); {
		case err != nil:
			return pressed, errRetryBefore
		case paused:
			return pressed, errPausedBefore
		case !front:
			return pressed, errGoneBefore
		}
		if err := d.rx.SendKeys(ctx, pane.Session, pane.Block, key); err != nil {
			return pressed, errRetryBefore
		}
		pressed = true
		next, err := d.waitVimChange(ctx, pane, st)
		switch {
		case ctx.Err() != nil:
			return pressed, ctx.Err()
		case err != nil:
			return pressed, errRetryBefore
		case next == st:
			d.vimStuck.Store(pane.ID, vimStuckMark{st.label, pane.PID})
			return pressed, errVimStuck
		}
		st = next
	}
	if nextVimKey(st.mode, want) == "" {
		return pressed, nil
	}
	return pressed, errVimStuck
}

// waitVimChange reads the screen until it shows a pi-vim label other than
// was. A screen without a label does not count: every pi-vim mode has one,
// so a missing label is a redraw caught halfway or something covering the
// editor. It returns was when no other label showed in time.
func (d *daemon) waitVimChange(ctx context.Context, pane store.Pane, was vimState) (vimState, error) {
	for range vimPolls {
		if !sleepCtx(ctx, d.timing.VimPoll) {
			return was, ctx.Err()
		}
		screen, err := d.rx.Capture(ctx, pane.Session, pane.Block)
		if err != nil {
			return was, err
		}
		if st := piVimState(screen); st.mode != vimNone && st != was {
			return st, nil
		}
	}
	return was, nil
}
