package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"rex-tg/internal/telegram"
)

// A long wait from Telegram (telegram.HoldAfter) holds the whole chat: the
// client refuses every request that posts to it until the wait ends. The
// hold is kept in the store, so a restart does not ask Telegram during the
// wait. When it ends, the main chat gets one note that says what happened.
// The held messages go out at the same time, some of them before the note.

const holdKey = "telegram_hold"

// restoreHold puts back a hold that an earlier run kept.
func (d *daemon) restoreHold(ctx context.Context) {
	v, ok, err := d.st.Get(ctx, holdKey)
	if err != nil || !ok {
		return
	}
	var h telegram.Hold
	if err := json.Unmarshal([]byte(v), &h); err != nil {
		d.log.Warn("drop a hold that cannot be read", "err", err)
		_ = d.st.Delete(ctx, holdKey)
		return
	}
	d.tg.SetHold(h)
	d.lastHold.Store(&h)
	if h.Until.After(d.now()) {
		d.log.Warn("Telegram asked rex-tg to wait; messages wait until then", "until", h.Until.Format(time.RFC3339), "method", h.Method)
	}
}

// noteHold keeps a new or longer hold (the client's OnHold). Two calls may
// come out of order: a shorter hold never replaces a longer one.
func (d *daemon) noteHold(h telegram.Hold) {
	d.holdMu.Lock()
	defer d.holdMu.Unlock()
	if cur := d.lastHold.Load(); cur != nil && !h.Until.After(cur.Until) {
		return
	}
	if b, err := json.Marshal(h); err == nil {
		if err := d.st.Set(context.Background(), holdKey, string(b)); err != nil {
			d.log.Warn("save the hold", "err", err)
		}
	}
	d.lastHold.Store(&h)
}

// checkHoldEnd posts one note to the main chat once a hold has ended
// (tick). The hold is forgotten before the note is sent: a note that fails is
// not sent again.
func (d *daemon) checkHoldEnd() {
	h := d.lastHold.Load()
	if h == nil {
		return
	}
	if _, on := d.tg.CurrentHold(); on {
		return
	}
	// Under holdMu, so a new hold that comes meanwhile is not deleted.
	d.holdMu.Lock()
	if !d.lastHold.CompareAndSwap(h, nil) {
		d.holdMu.Unlock()
		return
	}
	if err := d.st.Delete(d.ctx, holdKey); err != nil {
		d.log.Warn("forget the hold", "err", err)
	}
	d.holdMu.Unlock()
	if d.ownerChat == 0 {
		return
	}
	d.log.Info("Telegram's wait ended; sending the held messages", "since", h.Since.Format(time.RFC3339), "until", h.Until.Format(time.RFC3339))
	d.say(d.ownerChat, 0, holdNote(*h), nil)
}

// holdNote says what the user saw: no replies for a while.
func holdNote(h telegram.Hold) string {
	layout := "15:04"
	if h.Since.Local().YearDay() != h.Until.Local().YearDay() {
		layout = "Jan 2 15:04"
	}
	return fmt.Sprintf("⚠️ Telegram held rex-tg's messages from %s to %s, because rex-tg sent too many (429 Too Many Requests). "+
		"Nothing was lost: the held messages are sent now.", h.Since.Local().Format(layout), h.Until.Local().Format(layout))
}
