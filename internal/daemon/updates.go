package daemon

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"rex-tg/internal/format"
	"rex-tg/internal/inbound"
	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

const (
	textOnly      = "Only text is supported for now."
	notLinkedText = "This topic is not linked to an agent. Send /agents to list the agents and open one."
)

// onUpdates stores and routes a batch of updates (flow 3). The poll offset
// moves only after this returns, so a stored update is never lost.
func (d *daemon) onUpdates(e evUpdates) {
	next := int64(0)
	for _, u := range e.ups {
		d.storeErr = nil
		d.onUpdate(u)
		if d.storeErr != nil {
			// Not stored: leave the offset at this update so Telegram sends it again.
			d.log.Error("update not stored; it will be fetched again", "update", u.UpdateID, "err", d.storeErr)
			d.storeErr = nil
			break
		}
		if u.UpdateID >= next {
			next = u.UpdateID + 1
		}
	}
	if next > 0 {
		d.kvSetInt("tg_offset", next)
	}
	*e.next = next
	close(e.done)
}

func (d *daemon) onUpdate(u telegram.Update) {
	switch {
	case u.CallbackQuery != nil:
		d.onCallback(u)
	case u.EditedMessage != nil:
		d.onEdited(u)
	case u.Message != nil:
		d.onMessage(u)
	}
}

// fromOwner is the sender check (design section 9.5, check 1).
func (d *daemon) fromOwner(from *telegram.User, chatID int64) bool {
	return d.ownerUser != 0 && from != nil && from.ID == d.ownerUser && chatID == d.ownerChat
}

// record stores an update before it is handled. inserted is false when the
// update was stored before (a repeat after a restart); err is a store error.
func (d *daemon) record(u telegram.Update, m *telegram.Message, kind, body string, threadID int64, state string) (inserted bool, err error) {
	in := store.Inbound{
		UpdateID: u.UpdateID, ChatID: m.Chat.ID, MessageThreadID: m.MessageThreadID, TGMessageID: m.MessageID,
		FromID: m.From.ID, Kind: kind, Body: body, ThreadID: threadID, State: state, CreatedAt: d.now(),
	}
	if kind == "text" {
		in.BodyHash = inbound.Hash(body)
	}
	ok, err := d.st.InsertInbound(d.ctx, in)
	if err != nil {
		return false, fmt.Errorf("store update %d: %w", u.UpdateID, err)
	}
	return ok, nil
}

// recorded is record for handlers: a store error is kept in d.storeErr for
// onUpdates, and counts as "do not handle".
func (d *daemon) recorded(u telegram.Update, m *telegram.Message, kind, body string, threadID int64, state string) bool {
	ok, err := d.record(u, m, kind, body, threadID, state)
	if err != nil {
		d.storeErr = err
		return false
	}
	return ok
}

func (d *daemon) onMessage(u telegram.Update) {
	m := u.Message
	if m.From == nil {
		return
	}
	if d.ownerUser == 0 {
		d.tryPair(m)
		return
	}
	if !d.fromOwner(m.From, m.Chat.ID) {
		d.log.Info("update dropped: not the owner", "update", u.UpdateID, "from", m.From.ID)
		return
	}
	if m.IsTopicMessage && m.MessageThreadID != 0 {
		d.topicMessage(u, m)
		return
	}
	d.mainMessage(u, m)
}

func (d *daemon) mainMessage(u telegram.Update, m *telegram.Message) {
	if m.Text == "" {
		if d.recorded(u, m, "other", "", 0, store.InboundDone) {
			d.say(m.Chat.ID, 0, textOnly, nil)
		}
		return
	}
	r := inbound.RouteText(m.Text, false, d.botUser)
	if r.Kind != inbound.RouteCommand {
		return
	}
	if !d.recorded(u, m, "command", m.Text, 0, store.InboundDone) {
		return
	}
	d.mainCommand(r, m.Chat.ID, 0, false)
}

func (d *daemon) topicMessage(u telegram.Update, m *telegram.Message) {
	th, ok, err := d.st.ThreadByTopic(d.ctx, m.Chat.ID, m.MessageThreadID)
	if err != nil || !ok || th.Status == store.ThreadClosed {
		// With Threaded Mode, a private chat has no main chat: Telegram puts
		// every message in a topic and creates one for a new conversation.
		// A topic that is not linked to an agent therefore works as the
		// main chat, and the answer stays in that topic.
		if m.Text != "" {
			if r := inbound.RouteText(m.Text, false, d.botUser); r.Kind == inbound.RouteCommand {
				if d.recorded(u, m, "command", m.Text, 0, store.InboundDone) {
					d.mainCommand(r, m.Chat.ID, m.MessageThreadID, false)
				}
				return
			}
		}
		if d.recorded(u, m, "other", "", 0, store.InboundDone) {
			d.say(m.Chat.ID, m.MessageThreadID, notLinkedText, nil)
		}
		return
	}
	d.lastThread = th.ID
	if m.Text == "" {
		d.media(u, m, th)
		return
	}
	r := inbound.RouteText(m.Text, true, d.botUser)
	switch r.Kind {
	case inbound.RouteCommand:
		if d.recorded(u, m, "command", m.Text, th.ID, store.InboundDone) {
			d.topicCommand(u, m, th, r)
		}
	case inbound.RouteAgentText:
		if !d.recorded(u, m, "text", r.Text, th.ID, store.InboundQueued) {
			return
		}
		d.joining[u.UpdateID] = m.MessageID
		d.joiner.Add(th.ID, u.UpdateID, r.Text, d.now())
		d.dirty[th.ID] = true
	}
}

func (d *daemon) onEdited(u telegram.Update) {
	m := u.EditedMessage
	if m.From == nil || !d.fromOwner(m.From, m.Chat.ID) {
		return
	}
	if !d.recorded(u, m, "other", "", 0, store.InboundDone) {
		return
	}
	thread := int64(0)
	if m.IsTopicMessage {
		thread = m.MessageThreadID
	}
	d.say(m.Chat.ID, thread, "Edits are not sent to the agent.", nil)
}

// onCallback handles a button press. Every press is answered.
func (d *daemon) onCallback(u telegram.Update) {
	cb := u.CallbackQuery
	if cb.Message == nil || !d.fromOwner(&cb.From, cb.Message.Chat.ID) {
		d.answerCallback(cb.ID, "", false)
		return
	}
	ok, err := d.st.InsertInbound(d.ctx, store.Inbound{
		UpdateID: u.UpdateID, ChatID: cb.Message.Chat.ID, MessageThreadID: cb.Message.MessageThreadID,
		TGMessageID: cb.Message.MessageID, FromID: cb.From.ID, Kind: "callback", Body: cb.Data,
		State: store.InboundDone, CreatedAt: d.now(),
	})
	if err != nil {
		d.storeErr = fmt.Errorf("store callback %d: %w", u.UpdateID, err)
		d.answerCallback(cb.ID, "", false)
		return
	}
	if !ok {
		return // answered before the restart
	}
	parts := strings.Split(cb.Data, ":")
	arg := func(i int) (int64, bool) {
		if i >= len(parts) {
			return 0, false
		}
		n, err := strconv.ParseInt(parts[i], 10, 64)
		return n, err == nil
	}
	switch parts[0] {
	case "o":
		if id, ok := arg(1); ok {
			var replyThread int64 // answer where the button was pressed
			if cb.Message != nil {
				replyThread = cb.Message.MessageThreadID
			}
			d.openTopic(cb.ID, id, replyThread)
			return
		}
	case "s":
		if id, ok := arg(1); ok {
			d.sendNowButton(cb, id)
			return
		}
	case "c":
		if id, ok := arg(1); ok {
			d.cancelButton(cb, id)
			return
		}
	case "a":
		id, ok1 := arg(1)
		idx, ok2 := arg(2)
		if ok1 && ok2 {
			d.spawn(func() {
				d.answerAlert(id, int(idx), func(text string, isErr bool) { d.answerCallback(cb.ID, text, isErr) })
			})
			return
		}
	case "ag":
		chat, msg := cb.Message.Chat.ID, cb.Message.MessageID
		d.answerCallback(cb.ID, "", false)
		d.spawn(func() { d.refreshAgents(chat, msg) })
		return
	}
	d.answerCallback(cb.ID, "Unknown button.", false)
}

func (d *daemon) threadOfMessage(m *telegram.Message) (store.Thread, bool) {
	th, ok, err := d.st.ThreadByTopic(d.ctx, m.Chat.ID, m.MessageThreadID)
	return th, err == nil && ok && th.Status != store.ThreadClosed
}

// sendNowButton is `⚡ Send now`: it types the message without waiting.
func (d *daemon) sendNowButton(cb *telegram.CallbackQuery, id int64) {
	th, ok := d.threadOfMessage(cb.Message)
	if !ok || !d.stillQueued(th.ID, id) {
		d.answerCallback(cb.ID, "That message was already handled.", false)
		return
	}
	d.actions[id] = inbound.ActSendNow
	d.dirty[th.ID] = true
	delete(d.nextPrep, th.ID)
	d.answerCallback(cb.ID, "Sending…", false)
	d.removeButtons(cb.Message.Chat.ID, cb.Message.MessageID)
}

// cancelButton is `✖ Cancel`: the waiting message is not sent.
func (d *daemon) cancelButton(cb *telegram.CallbackQuery, id int64) {
	th, ok := d.threadOfMessage(cb.Message)
	if !ok || !d.stillQueued(th.ID, id) {
		d.answerCallback(cb.ID, "That message was already handled.", false)
		return
	}
	if err := d.st.SetInboundState(d.ctx, id, store.InboundDone, "cancelled", d.now()); err != nil {
		d.log.Warn("cancel: state", "err", err)
	}
	delete(d.actions, id)
	d.answerCallback(cb.ID, "Cancelled", false)
	chat, msg := cb.Message.Chat.ID, cb.Message.MessageID
	d.spawn(func() {
		_ = d.snd.Call(sender.PriorityEdit, func(ctx context.Context, tg *telegram.Client) error {
			_, err := tg.EditMessageText(ctx, telegram.EditMessageTextParams{ChatID: chat, MessageID: msg, Text: "✖ Cancelled"})
			return err
		})
	})
}

func escapef(format_ string, args ...any) string {
	return format.Escape(fmt.Sprintf(format_, args...))
}
