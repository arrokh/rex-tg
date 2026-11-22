package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	echoKey string             // set when the transcript recorded the prompt before Enter was confirmed
	cancel  context.CancelFunc // stops in-flight work when its topic closes
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
	notice string // what the message waits for, for the waiting notice (prepWait)
	pane   store.Pane
	rt     agents.Runtime
	conv   int64 // the session the checks were made for
	// guard: the agent works, so a prompt may open at any moment. The
	// screen is read for one right before the paste and before Enter.
	guard bool
}

// Kinds of queued rows that wait until the agent is idle even when
// input.while_busy is "steer": an agent's setting command (/model), and the
// note of a prompt answer, which belongs after the answer's turn.
const (
	kindControl = "control"
	kindNote    = "note"
)

// steerable tells whether a queued row may join a running turn: the user's
// own text, a file, or a voice note, but not an agent command (//compact,
// a control) or a shell command (!ls), which want an idle agent.
func steerable(in store.Inbound) bool {
	switch in.Kind {
	case "text", "file", "voice":
	default:
		return false
	}
	b := strings.TrimSpace(in.Body)
	return !strings.HasPrefix(b, "/") && !strings.HasPrefix(b, "!")
}

// What the waiting notice says a message waits for.
const (
	waitIdle   = "until the agent is idle."
	waitPrompt = "until the prompt is answered."
	waitTake   = "for the agent to take your last message."
	waitReady  = "until the agent is ready."
)

// waitNotice is what a message that waits for reason waits for. A message
// that would not steer waits until the agent is idle.
func waitNotice(reason string, canSteer bool) string {
	if !canSteer {
		return waitIdle
	}
	switch reason {
	case registry.ReasonBlocked, reasonPromptShown:
		return waitPrompt
	case registry.ReasonJustTyped:
		return waitTake
	}
	return waitReady
}

// waitNoticeFor is the text of the topic's one waiting notice: what the
// oldest message waits for, and how many messages wait.
func waitNoticeFor(what string, n int) string {
	if what == "" {
		what = waitIdle
	}
	if n > 1 {
		return fmt.Sprintf("⏳ %d messages wait %s", n, what)
	}
	return "⏳ Waiting " + what
}

// reasonPromptShown: an alert is open while the agent's state still says
// working.
const reasonPromptShown = "the agent shows a prompt"

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
func (d *daemon) queueText(threadID, orig, tgMsg int64, text, kind string, act inbound.Action) bool {
	th, err := d.st.Thread(d.ctx, threadID)
	if err != nil || th.Status == store.ThreadClosed {
		return false
	}
	id := -orig
	inserted, err := d.st.InsertInbound(d.ctx, d.textRow(th, orig, tgMsg, text, kind))
	if err != nil {
		d.log.Warn("queue text", "err", err)
		return false
	}
	if !inserted {
		return true
	}
	if act != inbound.ActText {
		d.actions[id] = act
	}
	d.dirty[th.ID] = true
	delete(d.nextPrep, th.ID)
	return true
}

// textRow is the queued row of text that rex-tg types for the user. Its id
// is minus the update id, so a repeat is ignored.
func (d *daemon) textRow(th store.Thread, orig, tgMsg int64, text, kind string) store.Inbound {
	return store.Inbound{
		UpdateID: -orig, ChatID: th.ChatID, MessageThreadID: th.MessageThreadID, TGMessageID: tgMsg,
		FromID: d.ownerUser, Kind: kind, Body: text, BodyHash: inbound.Hash(text), ThreadID: th.ID,
		State: store.InboundQueued, CreatedAt: d.now(),
	}
}

// joined handles a group from the joiner: the messages become one prompt.
func (d *daemon) joined(g inbound.Joined) {
	if len(g.UpdateIDs) == 0 {
		return
	}
	first := g.UpdateIDs[0]
	tgFirst := d.joining[first]
	var others []int64 // Telegram messages after the first, which get the same reactions
	for i, id := range g.UpdateIDs {
		if m := d.joining[id]; i > 0 && m != 0 {
			others = append(others, m)
		}
		delete(d.joining, id)
	}
	if len(g.UpdateIDs) > 1 {
		// The first update carries the joined text; the others are done. One
		// transaction stores the joined row and ends the parts, so a crash
		// cannot leave both to be typed. On failure the parts stay queued and
		// are typed one by one.
		th, err := d.st.Thread(d.ctx, g.ThreadID)
		if err != nil || th.Status == store.ThreadClosed {
			d.dirty[g.ThreadID] = true
			return
		}
		if _, err := d.st.JoinInbound(d.ctx, d.textRow(th, first, tgFirst, g.Text, "text"), g.UpdateIDs); err != nil {
			d.log.Warn("join messages", "err", err)
			d.dirty[g.ThreadID] = true
			return
		}
		d.extra[-first] = others
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
		if in.Kind == kindControl {
			if _, bound := d.ctlPending[in.UpdateID]; !bound {
				d.refuse(th, in, "the original session for this setting change could not be verified")
				continue
			}
		}
		if d.controlStale(th, in.UpdateID) {
			d.refuse(th, in, "the session in this pane changed after the command was sent (/clear, a resume, or another agent); send it again")
			continue
		}
		d.busy[tid] = true
		d.nextPrep[tid] = now.Add(d.timing.Prep)
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
	var canSteer bool // set once the agent is known
	wait := func(reason string) prepResult {
		return prepResult{kind: prepWait, reason: reason, notice: waitNotice(reason, canSteer)}
	}
	refuse := func(reason string) prepResult { return prepResult{kind: prepRefuse, reason: reason} }

	f, pane, err := d.baseFacts(ctx, th)
	if err != nil {
		return wait("the store is busy")
	}
	if f.Paused || !f.TopicLive || !f.PaneExists {
		_, reason := inbound.Check(act, f)
		return refuse(reason)
	}
	rt := agents.Runtime("")
	if conv, err := d.st.Conversation(ctx, th.CurrentConversationID); err == nil {
		rt = conv.Runtime
	}
	canSteer = steerable(in) && d.steers(rt)
	steer := false
	if act == inbound.ActText || act == inbound.ActSendNow && !steerable(in) {
		ready, why, err := d.reg.Ready(ctx, pane.ID, d.now())
		if err != nil {
			return wait("the registry is busy")
		}
		// While the agent works, a message steers the run when that is on:
		// the same typing as ⚡ Send now. A prompt, a message just typed
		// (not taken yet), or an unknown state still waits.
		if !ready && !(why == registry.ReasonWorking && canSteer) {
			return wait(why)
		}
		if !ready && f.PromptOpen {
			// A prompt may be on its way while the state still says
			// working: wait for it to end, as a message sent then would.
			return wait(reasonPromptShown)
		}
		steer = !ready
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
	return prepResult{kind: prepGo, pane: pane, rt: rt, conv: th.CurrentConversationID,
		guard: steer || act == inbound.ActSendNow}
}

// steers reports whether a message sent while an agent of runtime rt works
// is typed at once (input.while_busy = "steer"). The agent must take Enter
// while it works in a known way: steer or queue (agents.<rt>.while_working).
func (d *daemon) steers(rt agents.Runtime) bool {
	if d.cfg.InputSettings.WhileBusy != "steer" || rt == "" {
		return false
	}
	switch d.cfg.Input(rt).WhileWorking {
	case "steer", "queue":
		return true
	}
	return false
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
		// The flag is kept in memory and in the row's reason, so a restart
		// does not post the waiting notice again.
		// A short wait gets no notice: a message that the agent takes
		// within seconds (a busy window, a prompt answered) would only
		// flash one.
		// One notice for the topic: the oldest waiting message has it, and
		// it counts the messages behind it.
		n := d.waitingCount(tid)
		text := waitNoticeFor(e.res.notice, n)
		if d.waitNoticed[e.in.UpdateID] || e.in.Reason == reasonWaitNoticed {
			d.updateWaitNotice(tid, e.in.UpdateID, text, n)
		}
		if !d.waitNoticed[e.in.UpdateID] && e.in.Reason != reasonWaitNoticed && d.now().Sub(e.in.CreatedAt) >= d.timing.WaitNotice {
			d.waitNoticed[e.in.UpdateID] = true
			d.waitTexts[e.in.UpdateID] = text
			sendable := d.sendNowEligibleCount(tid)
			d.waitSendables[e.in.UpdateID] = sendable
			d.postWaitNotice(th, e.in.UpdateID, text, n, sendable)
			if err := d.st.SetInboundState(d.ctx, e.in.UpdateID, store.InboundQueued, reasonWaitNoticed, d.now()); err != nil {
				d.log.Warn("waiting: state", "err", err)
			}
		}
	case prepRefuse:
		d.refuse(th, e.in, e.res.reason)
	case prepGo:
		// The checks ran in a worker. Make sure nothing changed since, right
		// before typing (design section 9.5).
		switch {
		case th.Status == store.ThreadClosed:
			delete(d.busy, tid)
			d.refuse(th, e.in, inbound.ReasonTopicNotLive)
		case d.paused.Load(), th.Status != store.ThreadLive, th.CurrentConversationID != e.res.conv:
			delete(d.busy, tid) // the row stays queued; the next round checks again
		default:
			d.startTyping(th, e.in, e.res)
		}
	}
}

// waitMarkup gives Send-now access only to the eligible FIFO prefix. A
// command, pause, or open prompt must not be presented as sendable.
func waitMarkup(updateID int64, total, sendable int) *telegram.InlineKeyboardMarkup {
	var row []telegram.InlineKeyboardButton
	if sendable > 0 {
		send := "⚡ Send now"
		if sendable > 1 {
			if sendable == total {
				send = "⚡ Send all now"
			} else {
				send = fmt.Sprintf("⚡ Send %d now", sendable)
			}
		}
		row = append(row, telegram.InlineKeyboardButton{Text: send, CallbackData: fmt.Sprintf("s:%d:%d", updateID, sendable)})
	}
	cancel := "✖ Cancel"
	if total > 1 {
		cancel = "✖ Cancel all"
	}
	row = append(row, telegram.InlineKeyboardButton{Text: cancel, CallbackData: fmt.Sprintf("c:%d", updateID)})
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{row}}
}

// waitingCount is the number of messages that wait in a topic's queue, not
// counting the parts that are being joined (core).
func (d *daemon) waitingCount(threadID int64) int {
	qs, err := d.st.QueuedInbound(d.ctx, threadID)
	if err != nil {
		return 1
	}
	n := 0
	for _, q := range qs {
		if _, joining := d.joining[q.UpdateID]; !joining {
			n++
		}
	}
	return max(n, 1)
}

// forget drops what the core keeps for a message that reached a final state,
// and its waiting notice.
func (d *daemon) forget(updateID int64) {
	d.dropWaitNotice(updateID)
	delete(d.actions, updateID)
	delete(d.waitNoticed, updateID)
	delete(d.waitTexts, updateID)
	delete(d.waitSendables, updateID)
	delete(d.vimNoticed, updateID)
}

// refuse ends a queued message with 👎 and a reply that says why.
func (d *daemon) refuse(th store.Thread, in store.Inbound, reason string) {
	if err := d.st.SetInboundState(d.ctx, in.UpdateID, store.InboundRefused, reason, d.now()); err != nil {
		d.log.Warn("refuse: state", "err", err)
	}
	d.controlTyped(in.UpdateID, false)
	d.forget(in.UpdateID)
	d.react(in, "👎")
	delete(d.extra, in.UpdateID) // 👎 is its last reaction
	d.replyTopic(th, fmt.Sprintf("refused:%d", in.UpdateID), "⛔ Not sent: "+format.Escape(reason)+".")
}

func (d *daemon) expire(th store.Thread, in store.Inbound) {
	if err := d.st.SetInboundState(d.ctx, in.UpdateID, store.InboundExpired, "waited too long", d.now()); err != nil {
		d.log.Warn("expire: state", "err", err)
	}
	d.controlTyped(in.UpdateID, false)
	d.forget(in.UpdateID)
	d.react(in, "👎")
	delete(d.extra, in.UpdateID) // 👎 is its last reaction
	d.replyTopic(th, fmt.Sprintf("expired:%d", in.UpdateID),
		fmt.Sprintf("⌛ Not sent: it waited more than %s for the agent.", format.Escape(d.queueExpiry().String())))
}

func (d *daemon) react(in store.Inbound, emoji string) {
	if in.TGMessageID != 0 {
		d.snd.React(in.ChatID, in.TGMessageID, emoji)
	}
	for _, m := range d.extra[in.UpdateID] { // the other messages of a joined group
		d.snd.React(in.ChatID, m, emoji)
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
	ctx, cancel := context.WithCancel(d.ctx)
	d.delivering[th.ID] = &delivery{in: in, started: d.now(), cancel: cancel}
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
	pane, text, piVim, claudeVim, guard := res.pane, in.Body, res.rt == agents.Pi, res.rt == agents.Claude, res.guard
	d.spawn(func() {
		defer cancel()
		err := d.typeInto(ctx, th.ID, pane, text, delay, submit, piVim, claudeVim, guard)
		d.emit(evTyped{thread: th.ID, in: in, err: err})
	})
}

// Outcomes of typeInto that are not plain failures.
var (
	errPausedBefore = errors.New("typing is paused")
	errGoneBefore   = errors.New("the agent is not in front")
	errTargetBefore = errors.New("the topic or agent session changed")
	errRetryBefore  = errors.New("the pane could not be checked")
	errPromptBefore = errors.New("a prompt is on the screen")
	errStoppedAfter = errors.New("the text was pasted but Enter was not pressed")
	errPaste        = errors.New("the paste failed")
	errSubmit       = errors.New("pressing the submit key failed")
)

// safeNow re-checks, right before a key goes out, that typing is not paused
// and that the prepared agent process is still in front of the pane.
func (d *daemon) safeNow(ctx context.Context, pane store.Pane) (paused, inFront bool, err error) {
	if d.paused.Load() {
		return true, false, nil
	}
	proc, err := d.rx.Process(ctx, pane.Session, pane.Block)
	if err != nil {
		return false, false, err
	}
	return false, registry.AgentInFront(proc, pane.PID, pane.ProcessName), nil
}

func (d *daemon) safeKey(ctx context.Context, pane store.Pane) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	paused, front, err := d.safeNow(ctx, pane)
	switch {
	case err != nil:
		return errRetryBefore
	case paused:
		return errPausedBefore
	case !front:
		return errGoneBefore
	}
	return nil
}

// deliveryCheck binds an in-flight delivery to the topic, conversation and
// pane checked by prepare. A /clear or resume can replace the conversation
// without replacing the foreground process. Re-read the binding after the
// process RPC, which may have been in flight when the topic changed.
func (d *daemon) deliveryCheck(ctx context.Context, threadID int64, pane store.Pane) error {
	if ctx.Err() != nil {
		return errTargetBefore
	}
	paused, front, err := d.safeNow(ctx, pane)
	if ctx.Err() != nil {
		return errTargetBefore
	}
	switch {
	case err != nil:
		return errRetryBefore
	case paused:
		return errPausedBefore
	case !front:
		return errGoneBefore
	}
	th, err := d.st.Thread(ctx, threadID)
	if err != nil {
		return errRetryBefore
	}
	current, err := d.st.Pane(ctx, pane.ID)
	if err != nil {
		return errRetryBefore
	}
	if th.Status != store.ThreadLive || th.CurrentConversationID != pane.ConversationID ||
		current.ConversationID != pane.ConversationID || !current.GoneAt.IsZero() ||
		current.Session != pane.Session || current.Block != pane.Block || current.PID != pane.PID || current.ProcessName != pane.ProcessName {
		return errTargetBefore
	}
	return nil
}

// promptShown reads the screen and tells whether it shows a prompt that
// waits for a key (a numbered list with a cursor at the bottom).
func (d *daemon) promptShown(ctx context.Context, pane store.Pane) (bool, error) {
	screen, err := d.rx.Capture(ctx, pane.Session, pane.Block)
	if err != nil {
		return false, err
	}
	return screenHasPrompt(lastLines(screen, alertLines), nil), nil
}

// typeInto pastes the text, waits, and presses the submit key. The safety
// checks run again before the paste and before the submit key. With guard
// (the agent works), the screen must also show no prompt at both points: a
// paste would go into the prompt, and Enter would pick its answer. Errors
// never carry Rex's output, which could hold the typed text.
func (d *daemon) typeInto(ctx context.Context, threadID int64, pane store.Pane, text string, delay time.Duration, submit string, piVim, claudeVim, guard bool) error {
	check := func() error { return d.deliveryCheck(ctx, threadID, pane) }
	if err := check(); err != nil {
		return err
	}
	// pi-vim drops a paste unless it is in INSERT mode. A pending vim command
	// or an EX line is cancelled on the way there. Nothing is pasted when
	// this fails, so the next try reads the screen again.
	if piVim {
		if _, err := d.settlePiVimChecked(ctx, pane, vimInsert, check); err != nil {
			return err
		}
	}
	// Claude Code takes a paste in every vim mode, but Enter does not submit in
	// VISUAL mode. Leave it with one Escape, and only when the footer shows it.
	if claudeVim {
		if _, err := d.settleClaudeVimChecked(ctx, pane, check); err != nil {
			return err
		}
	}
	if guard {
		switch shown, err := d.promptShown(ctx, pane); {
		case err != nil:
			return errRetryBefore
		case shown:
			return errPromptBefore
		}
	}
	if piVim || claudeVim || guard {
		if err := check(); err != nil {
			return err
		}
	}
	if err := d.rx.Paste(ctx, pane.Session, pane.Block, text); err != nil {
		return errPaste
	}
	if delay > 0 && !sleepCtx(ctx, delay) {
		return ctx.Err()
	}
	// Enter must not reach a shell, and /pause must stop it too.
	if err := check(); err != nil {
		return errStoppedAfter
	}
	if guard {
		if shown, err := d.promptShown(ctx, pane); err != nil || shown {
			return errStoppedAfter // a prompt came up after the paste
		}
		if err := check(); err != nil {
			return errStoppedAfter
		}
	}
	if err := d.rx.SendKeys(ctx, pane.Session, pane.Block, submit); err != nil {
		return errSubmit
	}
	if err := d.reg.MarkTyped(ctx, pane.ID, d.now()); err != nil {
		d.log.Warn("mark typed", "err", err)
	}
	return nil
}

func (d *daemon) onTyped(e evTyped) {
	defer d.maybeCloseStoppedTopic(e.thread)
	dl := d.delivering[e.thread]
	if dl != nil && dl.cancel != nil {
		dl.cancel()
	}
	delete(d.delivering, e.thread)
	delete(d.busy, e.thread)
	d.dirty[e.thread] = true
	now := d.now()
	switch {
	case errors.Is(e.err, errPausedBefore), errors.Is(e.err, errRetryBefore), errors.Is(e.err, errPromptBefore):
		if th, err := d.st.Thread(d.ctx, e.thread); err == nil && th.Status == store.ThreadClosed {
			d.refuse(th, e.in, inbound.ReasonTopicNotLive)
			return
		}
		// Nothing was typed. The message waits and is checked again; after
		// a prompt on the screen, not before a second has passed.
		if err := d.st.SetInboundState(d.ctx, e.in.UpdateID, store.InboundQueued, e.in.Reason, now); err != nil {
			d.log.Warn("typing: state", "err", err)
		}
		if errors.Is(e.err, errPromptBefore) {
			d.nextPrep[e.thread] = now.Add(max(time.Second, d.timing.Prep))
		}
		return
	case errors.Is(e.err, errTargetBefore):
		if th, err := d.st.Thread(d.ctx, e.thread); err == nil {
			d.refuse(th, e.in, errTargetBefore.Error()+"; send it again")
		}
		return
	case errors.Is(e.err, errGoneBefore):
		if th, err := d.st.Thread(d.ctx, e.thread); err == nil {
			d.refuse(th, e.in, inbound.ReasonAgentNotFront)
		}
		return
	case errors.Is(e.err, errVimStuck):
		// Nothing was pasted. The message waits; the next try comes after a
		// pause, and presses no key while the editor shows the same label.
		if err := d.st.SetInboundState(d.ctx, e.in.UpdateID, store.InboundQueued, e.in.Reason, now); err != nil {
			d.log.Warn("typing: state", "err", err)
		}
		d.nextPrep[e.thread] = now.Add(d.timing.VimRetry)
		if !d.vimNoticed[e.in.UpdateID] {
			d.vimNoticed[e.in.UpdateID] = true
			d.notice(e.thread, fmt.Sprintf("vim-stuck:%d", e.in.UpdateID), vimStuckNote)
		}
		return
	}
	if e.err != nil {
		d.controlTyped(e.in.UpdateID, false)
		d.log.Warn("typing failed", "thread", e.thread, "reason", e.err.Error()) // no raw Rex output: it may hold the text
		if err := d.st.SetInboundState(d.ctx, e.in.UpdateID, store.InboundUnknown, "typing failed", now); err != nil {
			d.log.Warn("typing: state", "err", err)
		}
		d.forget(e.in.UpdateID)
		d.react(e.in, "👎")
		delete(d.extra, e.in.UpdateID) // 👎 is its last reaction
		d.notice(e.thread, fmt.Sprintf("typing-unknown:%d", e.in.UpdateID), typingUnknownNotice)
		return
	}
	d.forget(e.in.UpdateID)
	if err := d.st.SetInboundState(d.ctx, e.in.UpdateID, store.InboundDelivered, "", now); err != nil {
		d.log.Warn("typing: state", "err", err)
	}
	control := d.controlTyped(e.in.UpdateID, true)
	d.react(e.in, "👀")
	if dl != nil && dl.echoKey != "" {
		// The agent recorded the prompt before the typing call returned.
		// The reactions still go out in order: 👀, then 👍.
		if err := d.st.AckInbound(d.ctx, e.in.UpdateID, dl.echoKey); err != nil {
			d.log.Warn("ack inbound", "err", err)
		}
		d.react(e.in, "👍")
		return
	}
	if control || inbound.SlashCommand(e.in.Body) {
		// An agent's slash command (/model, /restart, /compact) usually
		// writes no prompt, so no echo would give it 👍: typed is done. Any
		// kind of row counts: a file's caption comes first in its text. The
		// row stays delivered, so an agent that does echo it is still known
		// to come from Telegram, not the Mac.
		d.react(e.in, "👍")
		delete(d.extra, e.in.UpdateID) // 👍 is its last reaction
	}
}

// reasonWaitNoticed marks a queued row whose waiting notice was posted.
const reasonWaitNoticed = "wait-noticed"
