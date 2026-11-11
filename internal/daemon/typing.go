package daemon

import (
	"context"
	"fmt"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/format"
	"rex-tg/internal/inbound"
	"rex-tg/internal/registry"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

const defaultQueueExpiry = 30 * time.Minute

// delivery is the message that is being typed into a pane right now.
type delivery struct {
	in      store.Inbound
	started time.Time
	echoKey string // set when the transcript recorded the prompt before Enter was confirmed
}

type prepKind int

const (
	prepWait prepKind = iota
	prepRefuse
	prepGo
)

// prepResult is the outcome of the checks before typing.
type prepResult struct {
	kind   prepKind
	reason string
	pane   store.Pane
	rt     agents.Runtime
}

// baseFacts gathers the facts about a topic that need no call to Rex.
func (d *daemon) baseFacts(ctx context.Context, th store.Thread) (inbound.Facts, store.Pane, error) {
	f := inbound.Facts{FromOwner: true, OwnerChat: true, TopicLive: th.Status == store.ThreadLive, Paused: d.paused.Load()}
	pane, ok, err := d.st.PaneByConversation(ctx, th.CurrentConversationID)
	if err != nil {
		return f, pane, err
	}
	f.PaneExists = ok && pane.GoneAt.IsZero()
	als, err := d.st.OpenAlerts(ctx, th.ID)
	if err != nil {
		return f, pane, err
	}
	f.PromptOpen = len(als) > 0
	return f, pane, nil
}

// facts adds the check that the agent is still in front, from Rex.
func (d *daemon) facts(ctx context.Context, th store.Thread) (inbound.Facts, store.Pane, error) {
	f, pane, err := d.baseFacts(ctx, th)
	if err != nil || !f.PaneExists {
		return f, pane, err
	}
	proc, err := d.rx.Process(ctx, pane.Session, pane.Block)
	if err != nil {
		return f, pane, err
	}
	f.AgentInFront = registry.AgentInFront(proc, pane.PID, pane.ProcessName)
	return f, pane, nil
}

func (d *daemon) queueExpiry() time.Duration {
	if x := d.cfg.InputSettings.QueueExpiry.Duration; x > 0 {
		return x
	}
	return defaultQueueExpiry
}

// queueText stores text that the user wants typed (media, voice, /force) as a
// queued message. The row id is minus the update id, so a repeat is ignored.
func (d *daemon) queueText(threadID, orig, tgMsg int64, text, kind string, act inbound.Action) {
	th, err := d.st.Thread(d.ctx, threadID)
	if err != nil || th.Status == store.ThreadClosed {
		return
	}
	id := -orig
	inserted, err := d.st.InsertInbound(d.ctx, store.Inbound{
		UpdateID: id, ChatID: th.ChatID, MessageThreadID: th.MessageThreadID, TGMessageID: tgMsg,
		FromID: d.ownerUser, Kind: kind, Body: text, BodyHash: inbound.Hash(text), ThreadID: th.ID,
		State: store.InboundQueued, CreatedAt: d.now(),
	})
	if err != nil {
		d.log.Warn("queue text", "err", err)
		return
	}
	if !inserted {
		return
	}
	if act != inbound.ActText {
		d.actions[id] = act
	}
	d.dirty[th.ID] = true
	delete(d.nextPrep, th.ID)
}

// joined handles a group from the joiner: the messages become one prompt.
func (d *daemon) joined(g inbound.Joined) {
	if len(g.UpdateIDs) == 0 {
		return
	}
	first := g.UpdateIDs[0]
	tgFirst := d.joining[first]
	for _, id := range g.UpdateIDs {
		delete(d.joining, id)
	}
	if len(g.UpdateIDs) > 1 {
		// The first update carries the joined text; the others are done.
		for _, id := range g.UpdateIDs {
			if err := d.st.SetInboundState(d.ctx, id, store.InboundDone, "joined", d.now()); err != nil {
				d.log.Warn("joined: state", "err", err)
			}
		}
		d.queueText(g.ThreadID, first, tgFirst, g.Text, "text", inbound.ActText)
	}
	d.dirty[g.ThreadID] = true
	delete(d.nextPrep, g.ThreadID)
}

// pump starts the delivery of the oldest queued message of each topic that
// has one and is not busy (flow 4).
func (d *daemon) pump(now time.Time) {
	for tid := range d.dirty {
		if d.busy[tid] || now.Before(d.nextPrep[tid]) {
			continue
		}
		th, err := d.st.Thread(d.ctx, tid)
		if err != nil || th.Status == store.ThreadClosed {
			delete(d.dirty, tid)
			continue
		}
		qs, err := d.st.QueuedInbound(d.ctx, tid)
		if err != nil {
			continue
		}
		var pick *store.Inbound
		for i := range qs {
			if _, wait := d.joining[qs[i].UpdateID]; wait {
				continue
			}
			if _, forced := d.actions[qs[i].UpdateID]; forced {
				pick = &qs[i]
				break
			}
			if pick == nil {
				pick = &qs[i]
			}
		}
		if pick == nil {
			if len(d.joining) == 0 {
				delete(d.dirty, tid)
			}
			continue
		}
		in := *pick
		act, forced := d.actions[in.UpdateID]
		if !forced {
			act = inbound.ActText
		}
		if act == inbound.ActText && now.Sub(in.CreatedAt) > d.queueExpiry() {
			d.expire(th, in)
			continue
		}
		d.busy[tid] = true
		d.nextPrep[tid] = now.Add(prepInterval)
		d.spawn(func() {
			res := d.prepare(th, in, act)
			d.emit(evPrepared{thread: tid, in: in, res: res})
		})
	}
}

// prepare runs the readiness and safety checks for one queued message. It
// runs in a worker: it calls Rex.
func (d *daemon) prepare(th store.Thread, in store.Inbound, act inbound.Action) prepResult {
	ctx := d.ctx
	wait := func(reason string) prepResult { return prepResult{kind: prepWait, reason: reason} }
	refuse := func(reason string) prepResult { return prepResult{kind: prepRefuse, reason: reason} }

	f, pane, err := d.baseFacts(ctx, th)
	if err != nil {
		return wait("the store is busy")
	}
	if f.Paused || !f.TopicLive || !f.PaneExists {
		_, reason := inbound.Check(act, f)
		return refuse(reason)
	}
	if act == inbound.ActText {
		ready, why, err := d.reg.Ready(ctx, pane.ID, d.now())
		if err != nil {
			return wait("the registry is busy")
		}
		if !ready {
			return wait(why)
		}
	}
	proc, err := d.rx.Process(ctx, pane.Session, pane.Block)
	if err != nil {
		d.log.Warn("prepare: process", "err", err)
		return wait("Rex is not reachable")
	}
	f.AgentInFront = registry.AgentInFront(proc, pane.PID, pane.ProcessName)
	if ok, reason := inbound.Check(act, f); !ok {
		return refuse(reason)
	}
	rt := agents.Runtime("")
	if conv, err := d.st.Conversation(ctx, th.CurrentConversationID); err == nil {
		rt = conv.Runtime
	}
	return prepResult{kind: prepGo, pane: pane, rt: rt}
}

func (d *daemon) stillQueued(threadID, updateID int64) bool {
	qs, err := d.st.QueuedInbound(d.ctx, threadID)
	if err != nil {
		return false
	}
	for _, q := range qs {
		if q.UpdateID == updateID {
			return true
		}
	}
	return false
}

func (d *daemon) onPrepared(e evPrepared) {
	tid := e.thread
	th, err := d.st.Thread(d.ctx, tid)
	if err != nil {
		delete(d.busy, tid)
		return
	}
	if e.res.kind != prepGo {
		delete(d.busy, tid)
	}
	if !d.stillQueued(tid, e.in.UpdateID) {
		delete(d.busy, tid)
		return
	}
	switch e.res.kind {
	case prepWait:
		if !d.waitNoticed[e.in.UpdateID] {
			d.waitNoticed[e.in.UpdateID] = true
			d.say(th.ChatID, th.MessageThreadID, "⏳ Waiting until the agent is idle.", waitMarkup(e.in.UpdateID))
		}
	case prepRefuse:
		d.refuse(th, e.in, e.res.reason)
	case prepGo:
		d.startTyping(th, e.in, e.res)
	}
}

func waitMarkup(updateID int64) *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{
		{Text: "⚡ Send now", CallbackData: fmt.Sprintf("s:%d", updateID)},
		{Text: "✖ Cancel", CallbackData: fmt.Sprintf("c:%d", updateID)},
	}}}
}

// refuse ends a queued message with 👎 and a reply that says why.
func (d *daemon) refuse(th store.Thread, in store.Inbound, reason string) {
	if err := d.st.SetInboundState(d.ctx, in.UpdateID, store.InboundRefused, reason, d.now()); err != nil {
		d.log.Warn("refuse: state", "err", err)
	}
	delete(d.actions, in.UpdateID)
	d.react(in, "👎")
	d.replyTopic(th, fmt.Sprintf("refused:%d", in.UpdateID), "⛔ Not sent: "+format.Escape(reason)+".")
}

func (d *daemon) expire(th store.Thread, in store.Inbound) {
	if err := d.st.SetInboundState(d.ctx, in.UpdateID, store.InboundExpired, "waited too long", d.now()); err != nil {
		d.log.Warn("expire: state", "err", err)
	}
	d.react(in, "👎")
	d.replyTopic(th, fmt.Sprintf("expired:%d", in.UpdateID),
		fmt.Sprintf("⌛ Not sent: it waited more than %s for the agent.", format.Escape(d.queueExpiry().String())))
}

func (d *daemon) react(in store.Inbound, emoji string) {
	if in.TGMessageID != 0 {
		d.snd.React(in.ChatID, in.TGMessageID, emoji)
	}
}

// replyTopic answers in a topic through the outbox. The key comes from stable
// inputs (an update id and a tag), so a restart never repeats a reply.
func (d *daemon) replyTopic(th store.Thread, tag, html string) {
	d.notice(th.ID, "reply:"+tag, html)
}

// startTyping sets the row to typing and pastes the text and Enter in a
// worker (flow 4).
func (d *daemon) startTyping(th store.Thread, in store.Inbound, res prepResult) {
	if err := d.st.SetInboundState(d.ctx, in.UpdateID, store.InboundTyping, "", d.now()); err != nil {
		d.log.Warn("typing: state", "err", err)
		delete(d.busy, th.ID)
		return
	}
	delete(d.actions, in.UpdateID)
	d.delivering[th.ID] = &delivery{in: in, started: d.now()}
	prof := d.cfg.Input(res.rt)
	delay := prof.PasteEnterDelay.Duration
	submit := "enter"
	if a, ok := d.adapters[res.rt]; ok {
		ip := a.Input()
		if delay <= 0 {
			delay = ip.PasteEnterDelay
		}
		if ip.SubmitKey != "" {
			submit = ip.SubmitKey
		}
	}
	pane, text := res.pane, in.Body
	d.spawn(func() {
		err := d.typeInto(pane, text, delay, submit)
		d.emit(evTyped{thread: th.ID, in: in, err: err})
	})
}

// typeInto pastes the text, waits, and presses the submit key.
func (d *daemon) typeInto(pane store.Pane, text string, delay time.Duration, submit string) error {
	ctx := d.ctx
	if err := d.rx.Paste(ctx, pane.Session, pane.Block, text); err != nil {
		return fmt.Errorf("paste: %w", err)
	}
	if delay > 0 && !sleepCtx(ctx, delay) {
		return ctx.Err()
	}
	if err := d.rx.SendKeys(ctx, pane.Session, pane.Block, submit); err != nil {
		return fmt.Errorf("submit: %w", err)
	}
	if err := d.reg.MarkTyped(ctx, pane.ID, d.now()); err != nil {
		d.log.Warn("mark typed", "err", err)
	}
	return nil
}

func (d *daemon) onTyped(e evTyped) {
	dl := d.delivering[e.thread]
	delete(d.delivering, e.thread)
	delete(d.busy, e.thread)
	d.dirty[e.thread] = true
	now := d.now()
	if e.err != nil {
		d.log.Warn("typing failed", "thread", e.thread, "err", e.err)
		if err := d.st.SetInboundState(d.ctx, e.in.UpdateID, store.InboundUnknown, "typing failed", now); err != nil {
			d.log.Warn("typing: state", "err", err)
		}
		d.react(e.in, "👎")
		d.notice(e.thread, fmt.Sprintf("typing-unknown:%d", e.in.UpdateID), typingUnknownNotice)
		return
	}
	if err := d.st.SetInboundState(d.ctx, e.in.UpdateID, store.InboundDelivered, "", now); err != nil {
		d.log.Warn("typing: state", "err", err)
	}
	if dl != nil && dl.echoKey != "" {
		if err := d.st.AckInbound(d.ctx, e.in.UpdateID, dl.echoKey); err != nil {
			d.log.Warn("ack inbound", "err", err)
		}
		d.react(e.in, "👍")
		return
	}
	d.react(e.in, "👀")
}
