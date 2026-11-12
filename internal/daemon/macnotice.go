package daemon

import (
	"context"
	"encoding/json"
	"fmt"

	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
	"rex-tg/internal/turn"
)

// The Mac hold (design section 8.4a). While the user works in the pane on
// the Mac, a topic gets one notice ("💻 In use on the Mac") in place of the
// prompts, progress, and answers of those turns. The tracker decides what is
// held (turn.MacNotice, turn.MacBack, turn.HeldAnswer); this file posts and
// edits the notice, and keeps the newest held answer for its
// "📄 Last answer" button.
//
// The notice is posted once, silently, and then only edited, so it never
// buzzes again. A post whose outcome is unknown is not repeated (at most
// once): the hold then goes on without a notice. The notice's chat and
// message number are kept in the store (kv "mac_notice:<thread>"), and the
// held answer too ("mac_answer:<thread>"), so a restart keeps both.

// macAnswerData is the callback data of the "📄 Last answer" button.
const macAnswerData = "ma"

func macNoticeKey(thread int64) string { return fmt.Sprintf("mac_notice:%d", thread) }
func macAnswerKey(thread int64) string { return fmt.Sprintf("mac_answer:%d", thread) }

// macNote is the notice of one hold (core).
type macNote struct {
	chat, msg int64  // msg is 0 until the post returned
	want      string // the HTML to show
	shown     string // the HTML that Telegram has
	button    bool   // Telegram shows the button
	busy      bool   // a post or an edit is on its way
	failed    bool   // the post failed: no notice for this hold
	closing   bool   // the hold ended: finish what is on its way, then stop
}

// evMacSent reports the end of a post or an edit of a notice.
type evMacSent struct {
	thread int64
	note   *macNote
	msg    int64 // the posted message, for a post
	html   string
	button bool
	err    error
}

// showMacNotice shows the notice of a topic's hold in the state that html
// renders (core): a post for a new hold, else an edit.
func (d *daemon) showMacNotice(th store.Thread, html string) {
	n := d.storedMacNote(th)
	if n == nil {
		n = &macNote{chat: th.ChatID}
		d.macNotes[th.ID] = n
	}
	n.want = html
	d.pushMacNotice(th, n)
}

// storedMacNote returns the notice of a topic's hold: the one in memory, or
// after a restart the one in the store (core). Nil when there is none.
func (d *daemon) storedMacNote(th store.Thread) *macNote {
	if n := d.macNotes[th.ID]; n != nil {
		return n
	}
	v, ok, err := d.st.Get(d.ctx, macNoticeKey(th.ID))
	if err != nil || !ok {
		return nil
	}
	n := &macNote{}
	if _, err := fmt.Sscanf(v, "%d:%d", &n.chat, &n.msg); err != nil || n.msg == 0 {
		return nil
	}
	// The hold began before a restart. Its text is not known, so the next
	// state edits it in full.
	d.macNotes[th.ID] = n
	return n
}

// pushMacNotice sends the wanted state of a notice when it is not on its way
// already (core). Only one request per notice runs at a time, so edits
// cannot arrive in the wrong order.
func (d *daemon) pushMacNotice(th store.Thread, n *macNote) {
	button := !n.closing && d.hasHeldAnswer(th.ID)
	if n.busy || n.failed || (n.closing && n.msg == 0) || (n.want == n.shown && button == n.button) {
		return // a closing notice is only edited, never posted
	}
	n.busy = true
	html, chat, msg, topic := n.want, n.chat, n.msg, th.MessageThreadID
	markup := macMarkup(button)
	thread := th.ID
	d.spawn(func() {
		ev := evMacSent{thread: thread, note: n, msg: msg, html: html, button: button}
		ev.err = d.snd.Call(sender.PriorityEdit, func(ctx context.Context, tg *telegram.Client) error {
			if msg == 0 {
				m, err := tg.SendMessage(ctx, telegram.SendMessageParams{
					ChatID: chat, MessageThreadID: topic, Text: html, ParseMode: "HTML",
					DisableNotification: true, ReplyMarkup: markup,
				})
				ev.msg = m.MessageID
				return err
			}
			_, err := tg.EditMessageText(ctx, telegram.EditMessageTextParams{
				ChatID: chat, MessageID: msg, Text: html, ParseMode: "HTML", ReplyMarkup: markup,
			})
			if telegram.Classify(err) == telegram.ClassNotModified {
				return nil
			}
			return err
		})
		d.emit(ev)
	})
}

// onMacSent takes the result of a post or an edit (core), and sends a newer
// state that came meanwhile.
func (d *daemon) onMacSent(e evMacSent) {
	n := e.note
	n.busy = false
	live := d.macNotes[e.thread] == n
	if !live && !n.closing {
		return
	}
	switch {
	case e.err == nil:
		if n.msg == 0 {
			n.msg = e.msg
			if live {
				if err := d.st.Set(d.ctx, macNoticeKey(e.thread), fmt.Sprintf("%d:%d", n.chat, n.msg)); err != nil {
					d.log.Warn("mac notice: save", "err", err)
				}
			}
		}
		n.shown, n.button = e.html, e.button
	case n.msg == 0:
		// Never post it again on a guess: it may have arrived.
		n.failed = true
		if d.ctx.Err() == nil {
			d.log.Warn("mac notice: post", "thread", e.thread, "err", e.err)
		}
		return
	case telegram.Classify(e.err) == telegram.ClassEditTargetGone:
		// The user deleted the notice. The next state posts a new one.
		n.msg, n.shown, n.button = 0, "", false
		if !live {
			return
		}
		if err := d.st.Delete(d.ctx, macNoticeKey(e.thread)); err != nil {
			d.log.Warn("mac notice: forget", "err", err)
		}
	default:
		if d.ctx.Err() == nil {
			d.log.Debug("mac notice: edit", "thread", e.thread, "err", e.err)
		}
		return // an edit is safe to try again with the next state
	}
	th, err := d.st.Thread(d.ctx, e.thread)
	if err != nil || th.Status == store.ThreadClosed {
		return
	}
	d.pushMacNotice(th, n)
}

// endMacNotice ends the hold of a topic (core). Its notice keeps its text:
// a state that is on its way still arrives, and then the "📄 Last answer"
// button goes, with the held answer. The next hold posts a new notice at the
// bottom of the topic, where the user sees it.
func (d *daemon) endMacNotice(th store.Thread) {
	n := d.storedMacNote(th)
	d.forgetMac(th.ID)
	switch {
	case n == nil:
	case n.want == "" && !n.busy:
		// Loaded from the store after a restart: its text is not known, so
		// only its button goes.
		d.removeButtons(n.chat, n.msg)
	default:
		n.closing = true
		d.pushMacNotice(th, n)
	}
}

// forgetMac drops the hold state of a topic: the notice and the held answer
// (core). A closed topic calls it too.
func (d *daemon) forgetMac(thread int64) {
	delete(d.macNotes, thread)
	for _, k := range []string{macNoticeKey(thread), macAnswerKey(thread)} {
		if err := d.st.Delete(d.ctx, k); err != nil {
			d.log.Warn("mac notice: forget", "key", k, "err", err)
		}
	}
}

// sweepMacNotices runs from the tick until it worked once (core). With
// MacNotice off, a hold from before the start is gone (the trackers drop it),
// so its notice loses its "📄 Last answer" button and the stored state goes.
// Else that button would post an old answer, and turning MacNotice on again
// would edit the old notice far up the topic.
func (d *daemon) sweepMacNotices() {
	if d.macSwept {
		return
	}
	if d.cfg.Display.MacNotice {
		d.macSwept = true
		return
	}
	notices, err := d.st.KeysWithPrefix(d.ctx, "mac_notice:")
	if err != nil {
		return // the next tick tries again
	}
	answers, err := d.st.KeysWithPrefix(d.ctx, "mac_answer:")
	if err != nil {
		return
	}
	d.macSwept = true
	for _, k := range notices {
		var chat, msg int64
		if v, ok, err := d.st.Get(d.ctx, k); err == nil && ok {
			if _, err := fmt.Sscanf(v, "%d:%d", &chat, &msg); err == nil {
				d.removeButtons(chat, msg)
			}
		}
	}
	for _, k := range append(notices, answers...) {
		if err := d.st.Delete(d.ctx, k); err != nil {
			d.log.Warn("mac notice: forget", "key", k, "err", err)
		}
	}
}

// heldAnswer is the newest answer of a held turn, as stored.
type heldAnswer struct {
	Key      string `json:"key"`
	Markdown string `json:"markdown"`
}

// holdAnswer keeps the newest answer of a held turn (core).
func (d *daemon) holdAnswer(thread int64, a turn.HeldAnswer) {
	b, err := json.Marshal(heldAnswer{Key: a.Key, Markdown: a.Markdown})
	if err == nil {
		err = d.st.Set(d.ctx, macAnswerKey(thread), string(b))
	}
	if err != nil {
		d.log.Warn("mac notice: keep the answer", "err", err)
	}
}

func (d *daemon) heldAnswer(thread int64) (heldAnswer, bool) {
	v, ok, err := d.st.Get(d.ctx, macAnswerKey(thread))
	if err != nil || !ok {
		return heldAnswer{}, false
	}
	var a heldAnswer
	if json.Unmarshal([]byte(v), &a) != nil || a.Key == "" {
		return heldAnswer{}, false
	}
	return a, true
}

func (d *daemon) hasHeldAnswer(thread int64) bool {
	_, ok := d.heldAnswer(thread)
	return ok
}

func macMarkup(button bool) *telegram.InlineKeyboardMarkup {
	if !button {
		return nil
	}
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{
		{{Text: "📄 Last answer", CallbackData: macAnswerData}},
	}}
}

// macAnswerButton is "📄 Last answer": it posts the newest held answer, like
// any answer. Its outbox key comes from the answer, so a second tap does not
// post it again.
func (d *daemon) macAnswerButton(cb *telegram.CallbackQuery) {
	th, ok := d.threadOfMessage(cb.Message)
	if !ok {
		d.answerCallback(cb.ID, "This topic is closed.", false)
		return
	}
	a, ok := d.heldAnswer(th.ID)
	if !ok {
		d.answerCallback(cb.ID, "No answer from the Mac yet.", false)
		return
	}
	posted := false
	for _, it := range d.work(th, []turn.Action{turn.Post{Key: "held:" + a.Key, Kind: "answer", Markdown: a.Markdown}}) {
		if _, created, err := d.st.Enqueue(d.ctx, it); err != nil {
			d.log.Warn("enqueue", "thread", th.ID, "kind", it.Kind, "err", err)
		} else if created {
			posted = true
		}
	}
	if !posted {
		d.answerCallback(cb.ID, "That answer is posted already.", false)
		return
	}
	d.snd.Wake()
	d.answerCallback(cb.ID, "", false)
}
