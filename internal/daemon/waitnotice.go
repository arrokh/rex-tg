package daemon

import (
	"context"
	"fmt"

	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// The waiting notice: "⏳ Waiting until the agent is idle." with ⚡ Send now
// and ✖ Cancel, posted once for a message that waits in the queue. It is
// only true while the message waits, so it goes away when the message leaves
// the queue: it was typed (👀, then 👍 on the message), refused or expired
// (👎 and a reply that says why), or typing failed (a notice says so).
// ✖ Cancel turns it into "✖ Cancelled" instead.
//
// Its chat and message number are kept in the store (kv), so a restart in
// between does not leave it behind.

const waitNoticeText = "⏳ Waiting until the agent is idle."

func waitNoticeKey(updateID int64) string { return fmt.Sprintf("wait_notice:%d", updateID) }

// postWaitNotice posts the waiting notice of a queued message (worker) and
// reports its message number to the core.
func (d *daemon) postWaitNotice(th store.Thread, updateID int64, text string) {
	if text == "" {
		text = waitNoticeText
	}
	chat, thread, tid := th.ChatID, th.MessageThreadID, th.ID
	d.spawn(func() {
		m, err := d.sendNow(chat, thread, text, waitMarkup(updateID))
		if err == nil && m.MessageID != 0 {
			d.markBelow(tid)
			d.emit(evWaitNotice{updateID: updateID, chat: chat, msg: m.MessageID})
		}
	})
}

// reasonCancelled is the reason of a message that ✖ Cancel took out of the
// queue.
const reasonCancelled = "cancelled"

// onWaitNotice keeps the number of a posted waiting notice (core). When the
// message left the queue while the notice was on its way, the notice goes at
// once, unless ✖ Cancel was tapped on it: that turns it into "✖ Cancelled".
func (d *daemon) onWaitNotice(e evWaitNotice) {
	in, ok, err := d.st.InboundByID(d.ctx, e.updateID)
	if err != nil {
		d.log.Warn("waiting notice: read message", "err", err)
		return
	}
	if ok && in.State == store.InboundDone && in.Reason == reasonCancelled {
		return
	}
	if err := d.st.Set(d.ctx, waitNoticeKey(e.updateID), fmt.Sprintf("%d:%d", e.chat, e.msg)); err != nil {
		d.log.Warn("waiting notice: save", "err", err)
		return
	}
	// Typing may still put the message back in the queue, so a message that
	// is being typed keeps its notice until it reaches a final state.
	if !ok || (in.State != store.InboundQueued && in.State != store.InboundTyping) {
		d.dropWaitNotice(e.updateID)
	}
}

// keepWaitNotice forgets the waiting notice of a message without deleting
// it (core).
func (d *daemon) keepWaitNotice(updateID int64) {
	if err := d.st.Delete(d.ctx, waitNoticeKey(updateID)); err != nil {
		d.log.Warn("waiting notice: forget", "err", err)
	}
}

// dropWaitNotice deletes the waiting notice of a message that left the queue
// (core). A message that never got one is left alone. When Telegram does not
// delete it (a bot can delete only messages younger than 48 hours), its
// buttons go at least.
func (d *daemon) dropWaitNotice(updateID int64) {
	v, ok, err := d.st.Get(d.ctx, waitNoticeKey(updateID))
	if err != nil || !ok {
		return
	}
	d.keepWaitNotice(updateID)
	var chat, msg int64
	if _, err := fmt.Sscanf(v, "%d:%d", &chat, &msg); err != nil || msg == 0 {
		return
	}
	d.spawn(func() {
		err := d.snd.Call(sender.PriorityEdit, func(ctx context.Context, tg *telegram.Client) error {
			return tg.DeleteMessage(ctx, chat, msg)
		})
		switch telegram.Classify(err) {
		case telegram.ClassNone, telegram.ClassDeleteTargetGone, telegram.ClassThreadNotFound:
			return
		}
		if d.ctx.Err() == nil {
			d.log.Debug("waiting notice: delete", "err", err)
			d.removeButtons(chat, msg)
		}
	})
}
