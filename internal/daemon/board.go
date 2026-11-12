package daemon

import (
	"context"
	"fmt"
	"time"

	"rex-tg/internal/sender"
	"rex-tg/internal/telegram"
)

// The live /agents board (design section 8.7). The newest /agents message
// shows every agent with its state, the ones that wait for you first, and
// rex-tg edits it when an agent comes, goes, or changes its state. So one
// message tells at a glance which agent works, waits, or is idle; pin it to
// keep it at hand. Edits make no sound, are spaced by Timing.BoardEdit, and
// happen only when the text changes. The board's chat and message number are
// kept in the store (kv "agents_board"), so it stays live after a restart.

const boardKey = "agents_board"

// board is the live /agents message (core).
type board struct {
	chat, msg int64
	shown     string    // the HTML that Telegram has
	dirty     bool      // the agents may have changed
	busy      bool      // a redraw is on its way
	last      time.Time // the last redraw
}

// evBoard reports a new board (a posted or refreshed /agents message), or the
// end of a redraw.
type evBoard struct {
	chat, msg int64
	html      string
	redraw    bool // the end of a redraw, not a new board
	gone      bool // the message was deleted
}

// macThreads lists the topics whose session is in use on the Mac (core), for
// a list drawn in a worker.
func (d *daemon) macThreads() map[int64]bool {
	out := map[int64]bool{}
	for id, t := range d.trackers {
		if t.OnMac() {
			out[id] = true
		}
	}
	return out
}

// boardOf returns the live board, loading it after a restart (core).
func (d *daemon) boardOf() *board {
	if d.board != nil || d.boardLoaded {
		return d.board
	}
	d.boardLoaded = true
	v, ok, err := d.st.Get(d.ctx, boardKey)
	if err != nil || !ok {
		return nil
	}
	b := &board{dirty: true} // redraw once: the agents may have changed while it was down
	if _, err := fmt.Sscanf(v, "%d:%d", &b.chat, &b.msg); err != nil || b.msg == 0 {
		return nil
	}
	d.board = b
	return b
}

// boardDirty asks for a redraw of the board (core).
func (d *daemon) boardDirty() {
	if b := d.boardOf(); b != nil {
		b.dirty = true
	}
}

// onBoard takes a new board or the end of a redraw (core).
func (d *daemon) onBoard(e evBoard) {
	b := d.boardOf()
	if e.redraw {
		if b == nil || b.chat != e.chat || b.msg != e.msg {
			return // a newer /agents took over meanwhile
		}
		b.busy = false
		if e.gone {
			d.board = nil
			if err := d.st.Delete(d.ctx, boardKey); err != nil {
				d.log.Warn("agents board: forget", "err", err)
			}
			return
		}
		b.shown = e.html
		return
	}
	// A change may have come between the drawing and now, while this was not
	// the board yet: draw it once more (no edit when nothing changed).
	d.board = &board{chat: e.chat, msg: e.msg, shown: e.html, last: d.now(), dirty: true}
	if err := d.st.Set(d.ctx, boardKey, fmt.Sprintf("%d:%d", e.chat, e.msg)); err != nil {
		d.log.Warn("agents board: save", "err", err)
	}
}

// tickBoard redraws the board when it may have changed (core). It also
// redraws it every Timing.BoardRefresh, so the times in it stay right.
func (d *daemon) tickBoard(now time.Time) {
	b := d.boardOf()
	if b == nil || b.busy {
		return
	}
	if now.Sub(b.last) >= d.timing.BoardRefresh {
		b.dirty = true
	}
	if !b.dirty || now.Sub(b.last) < d.timing.BoardEdit {
		return
	}
	b.dirty, b.busy, b.last = false, true, now
	chat, msg, shown := b.chat, b.msg, b.shown
	mac := d.macThreads()
	d.spawn(func() {
		res := evBoard{chat: chat, msg: msg, html: shown, redraw: true}
		defer func() { d.emit(res) }()
		views, err := d.reg.Agents(d.ctx) // the registry's state; no Rex scan
		if err != nil {
			return
		}
		text, markup := d.renderAgents(views, mac, d.now())
		if text == shown {
			return
		}
		err = d.snd.Call(sender.PriorityEdit, func(ctx context.Context, tg *telegram.Client) error {
			_, err := tg.EditMessageText(ctx, telegram.EditMessageTextParams{
				ChatID: chat, MessageID: msg, Text: text, ParseMode: "HTML", ReplyMarkup: markup,
			})
			if telegram.Classify(err) == telegram.ClassNotModified {
				return nil
			}
			return err
		})
		switch {
		case err == nil:
			res.html = text
		case telegram.Classify(err) == telegram.ClassEditTargetGone:
			res.gone = true
		case d.ctx.Err() == nil:
			d.log.Debug("agents board: edit", "err", err)
		}
	})
}
