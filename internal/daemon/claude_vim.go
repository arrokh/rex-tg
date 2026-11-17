package daemon

import (
	"context"
	"regexp"
	"strings"

	"rex-tg/internal/store"
)

// Claude Code's /vim mode shows "-- <mode> --" in the footer line below the
// prompt box, in every mode except NORMAL (read from the Claude Code 2.1.322
// bundle). Enter does not submit in VISUAL mode, and a paste goes in in every
// mode. Escape in VISUAL is kept by the vim layer and leaves VISUAL. Escape in
// NORMAL also reaches Claude Code (two in a row can open the rewind dialog),
// so rex-tg presses it only while the screen shows VISUAL, and only once.
var claudeVisualIndicator = regexp.MustCompile(`(?:^|\s)-- VISUAL(?: LINE)? --(?:\s|$)`)

// claudeStuckLabel is the stuck mark label for a Claude pane that stays in
// VISUAL after an Escape.
const claudeStuckLabel = "claude:VISUAL"

// claudeVisual reports whether the screen shows Claude Code's VISUAL or
// VISUAL LINE indicator. Only the lines below the last prompt-box bottom
// border (a line that starts with ╰) count, so agent output never does.
func claudeVisual(screen string) bool {
	_, visual := claudeFooter(screen)
	return visual
}

// claudeFooter reports whether the screen shows Claude Code's prompt box
// (box) and the VISUAL indicator below it (visual).
func claudeFooter(screen string) (box, visual bool) {
	lines := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	at := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "╰") {
			at = i
			break
		}
	}
	if at < 0 {
		return false, false
	}
	for _, l := range lines[at+1:] {
		if claudeVisualIndicator.MatchString(l) {
			return true, true
		}
	}
	return true, false
}

// settleClaudeVim leaves VISUAL mode in a Claude Code pane before a paste. It
// presses one Escape when the screen shows VISUAL, then reads the screen until
// the indicator is gone. When it stays, no more keys go out while the screen
// is unchanged: it returns errVimStuck and the message waits. It reports
// whether it pressed a key. A pane without the indicator is left alone, and a
// screen that cannot be read counts as no indicator.
func (d *daemon) settleClaudeVim(ctx context.Context, pane store.Pane) (pressed bool, err error) {
	return d.settleClaudeVimChecked(ctx, pane, func() error { return d.safeKey(ctx, pane) })
}

func (d *daemon) settleClaudeVimChecked(ctx context.Context, pane store.Pane, check func() error) (pressed bool, err error) {
	screen, err := d.rx.Capture(ctx, pane.Session, pane.Block)
	if err != nil || !claudeVisual(screen) {
		if v, ok := d.vimStuck.Load(pane.ID); ok && v.(vimStuckMark).label == claudeStuckLabel {
			d.vimStuck.Delete(pane.ID) // the indicator is gone
		}
		return false, nil
	}
	if v, ok := d.vimStuck.Load(pane.ID); ok {
		if m := v.(vimStuckMark); m.label == claudeStuckLabel && m.pid == pane.PID {
			return false, errVimStuck
		}
		d.vimStuck.Delete(pane.ID)
	}
	if err := check(); err != nil {
		return false, err
	}
	if err := d.rx.SendKeys(ctx, pane.Session, pane.Block, "escape"); err != nil {
		return false, errRetryBefore
	}
	for range vimPolls {
		if !sleepCtx(ctx, d.timing.VimPoll) {
			return true, ctx.Err()
		}
		screen, err := d.rx.Capture(ctx, pane.Session, pane.Block)
		if err != nil {
			return true, errRetryBefore
		}
		// VISUAL is left when the prompt box shows without the indicator. A
		// screen caught halfway through a redraw, without the box, does not
		// count.
		if box, visual := claudeFooter(screen); box && !visual {
			return true, nil
		}
	}
	d.vimStuck.Store(pane.ID, vimStuckMark{claudeStuckLabel, pane.PID})
	return true, errVimStuck
}
