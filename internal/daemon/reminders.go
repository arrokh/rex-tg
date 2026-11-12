package daemon

import (
	"context"
	"fmt"
	"time"

	"rex-tg/internal/format"
	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// Reminders of open alerts (design section 8.6). A permission prompt or a
// question stops the agent until you answer. When an alert is still open
// after [alerts] remind_after, rex-tg posts a reminder in its topic, as a
// reply to the alert, so a tap on it jumps there. It makes a sound, unless
// the topic is muted. Each later reminder comes remind_after after the one
// before (also after a pause of the daemon, so they never come in a burst),
// up to [alerts] reminders times, and takes the place of the one before. The
// reminder goes away when the alert closes, also when it closed while the
// daemon was stopped.
//
// At most once: a reminder's number is stored before it is sent (kv
// "remind:<alert>"), so a send whose outcome is unknown is never repeated,
// also not after a restart. The next reminder is a new message, due by time.

const remindPrefix = "remind:"

func remindKey(alert int64) string { return fmt.Sprintf("%s%d", remindPrefix, alert) }

// reminder is the state of one alert's reminders (core).
type reminder struct {
	n         int       // reminders sent (or on their way)
	chat, msg int64     // the newest reminder message, 0 when none is known
	last      time.Time // when the newest reminder was claimed
	prev      time.Time // last before that claim, to give a claim back
	busy      bool      // a send or a delete is on its way
}

// evReminder reports a posted reminder.
type evReminder struct {
	alert     int64
	chat, msg int64
	notSent   bool // the send surely never left: the claim is given back
}

// reminderOf returns the reminder state of an alert, loading it after a
// restart (core).
func (d *daemon) reminderOf(alert int64) *reminder {
	if r := d.reminders[alert]; r != nil {
		return r
	}
	r := &reminder{}
	if v, ok, err := d.st.Get(d.ctx, remindKey(alert)); err == nil && ok {
		var last int64
		if _, err := fmt.Sscanf(v, "%d:%d:%d:%d", &r.n, &r.chat, &r.msg, &last); err != nil {
			r.n, r.chat, r.msg, last = 0, 0, 0, 0
		}
		if last > 0 {
			r.last = time.Unix(last, 0)
		}
	}
	d.reminders[alert] = r
	return r
}

func (d *daemon) saveReminder(alert int64, r *reminder) {
	if err := d.st.Set(d.ctx, remindKey(alert), fmt.Sprintf("%d:%d:%d:%d", r.n, r.chat, r.msg, r.last.Unix())); err != nil {
		d.log.Warn("reminder: save", "err", err)
	}
}

// tickReminders posts the reminders that are due and drops the reminders of
// closed alerts (core). It looks every Timing.RemindCheck.
func (d *daemon) tickReminders(now time.Time) {
	if now.Sub(d.lastRemind) < d.timing.RemindCheck {
		return
	}
	d.lastRemind = now
	after, most := d.cfg.Alerts.RemindAfter.Duration, d.cfg.Alerts.Reminders
	als, err := d.st.AllOpenAlerts(d.ctx)
	if err != nil {
		d.log.Warn("reminders: open alerts", "err", err)
		return
	}
	open := map[int64]bool{}
	for _, a := range als {
		open[a.ID] = true
	}
	if !d.remindSwept {
		// Reminders of alerts that closed while the daemon was stopped.
		if keys, err := d.st.KeysWithPrefix(d.ctx, remindPrefix); err == nil {
			d.remindSwept = true // else the next look tries again
			for _, k := range keys {
				var id int64
				if _, err := fmt.Sscanf(k, remindPrefix+"%d", &id); err == nil && !open[id] {
					d.reminderOf(id) // loaded, so the loop below drops it
				}
			}
		}
	}
	for _, a := range als {
		if after <= 0 || most <= 0 || a.TGMessageID == 0 {
			continue
		}
		r := d.reminderOf(a.ID)
		since := a.CreatedAt
		if r.n > 0 && r.last.After(since) {
			since = r.last
		}
		if r.busy || r.n >= most || now.Sub(since) < after {
			continue
		}
		th, err := d.st.Thread(d.ctx, a.ThreadID)
		if err != nil || th.Status != store.ThreadLive {
			continue
		}
		oldChat, oldMsg := r.chat, r.msg
		r.n++
		r.busy, r.prev, r.last = true, r.last, now
		r.chat, r.msg = 0, 0    // the old one is deleted below
		d.saveReminder(a.ID, r) // before the send: never twice
		d.sendReminder(th, a, r.n, now, oldChat, oldMsg)
	}
	for id, r := range d.reminders {
		if !open[id] && !r.busy {
			d.dropReminder(id, r)
		}
	}
}

// sendReminder posts reminder n of an alert in a worker, after it deletes
// the reminder before it (oldChat, oldMsg).
func (d *daemon) sendReminder(th store.Thread, a store.Alert, n int, now time.Time, oldChat, oldMsg int64) {
	waited := now.Sub(a.CreatedAt).Round(time.Minute)
	text := fmt.Sprintf("⏰ <b>Still waiting for your answer</b> · %s\nThe agent cannot go on until you answer the prompt above.",
		format.Escape(waitedText(waited)))
	alert, chat, topic, reply, silent := a.ID, th.ChatID, th.MessageThreadID, a.TGMessageID, th.Muted
	d.spawn(func() {
		if oldMsg != 0 {
			d.deleteMessage(oldChat, oldMsg)
		}
		var msg telegram.Message
		err := d.snd.Call(sender.PriorityAlert, func(ctx context.Context, tg *telegram.Client) error {
			m, err := tg.SendMessage(ctx, telegram.SendMessageParams{
				ChatID: chat, MessageThreadID: topic, Text: text, ParseMode: "HTML",
				DisableNotification: silent, ReplyToMessageID: reply,
			})
			msg = m
			return err
		})
		if err != nil && d.ctx.Err() == nil {
			d.log.Warn("reminder: send", "alert", alert, "n", n, "err", err)
		}
		d.emit(evReminder{alert: alert, chat: chat, msg: msg.MessageID, notSent: telegram.Classify(err) == telegram.ClassNotSent})
	})
}

func waitedText(d time.Duration) string {
	if d < time.Minute { // only with a short remind_after
		return fmt.Sprintf("%d s", int(d/time.Second))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}
	return fmt.Sprintf("%dh %02dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
}

// onReminder keeps the number of a posted reminder (core). When its alert
// closed while it was on its way, it goes at once.
func (d *daemon) onReminder(e evReminder) {
	r := d.reminders[e.alert]
	if r == nil {
		return
	}
	r.busy = false
	r.chat, r.msg = e.chat, e.msg
	if e.msg == 0 {
		r.chat = 0
	}
	if e.notSent && r.n > 0 {
		r.n, r.last = r.n-1, r.prev // it never left this Mac: try it again later
	}
	d.saveReminder(e.alert, r)
}

// dropReminder deletes the newest reminder of a closed alert and forgets it
// (core).
func (d *daemon) dropReminder(alert int64, r *reminder) {
	delete(d.reminders, alert)
	if err := d.st.Delete(d.ctx, remindKey(alert)); err != nil {
		d.log.Warn("reminder: forget", "err", err)
	}
	if r.msg != 0 {
		chat, msg := r.chat, r.msg
		d.spawn(func() { d.deleteMessage(chat, msg) })
	}
}

// deleteMessage deletes a bot message (worker). A message that is gone
// already is fine.
func (d *daemon) deleteMessage(chat, msg int64) {
	err := d.snd.Call(sender.PriorityEdit, func(ctx context.Context, tg *telegram.Client) error {
		return tg.DeleteMessage(ctx, chat, msg)
	})
	switch telegram.Classify(err) {
	case telegram.ClassNone, telegram.ClassDeleteTargetGone, telegram.ClassThreadNotFound:
		return
	}
	if d.ctx.Err() == nil {
		d.log.Debug("delete message", "chat", chat, "message", msg, "err", err)
		d.removeButtons(chat, msg)
	}
}
