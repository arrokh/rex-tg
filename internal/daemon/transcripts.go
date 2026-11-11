package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/format"
	"rex-tg/internal/inbound"
	"rex-tg/internal/registry"
	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/tail"
	"rex-tg/internal/turn"
)

const (
	partLimit    = 4000
	maxTextParts = 4
	maxRichRunes = 32768
)

// tailer follows the transcript of one session that belongs to a topic.
type tailer struct {
	conv   int64
	thread int64
	gen    int
	f      *tail.Follower
	cancel context.CancelFunc
	stopAt time.Time // set while the tailer waits to stop; zero while it runs
	// replayUntil is the newest event time from before a re-read from byte 0
	// began. All batches of that re-read are checked against it, not against
	// the cursor, which the re-read's own new lines move forward.
	replayUntil time.Time
	replaying   bool
}

// pendingTail is a tailer that waits for the scan of its transcript.
type pendingTail struct {
	thread int64
	token  int
	stopAt time.Time // set when the session stopped while the scan ran
}

// tailScan is the result of scanning a transcript to its end in a worker.
type tailScan struct {
	conv   store.Conversation
	thread int64
	token  int
	ok     bool
	pos    tail.Position
	newest time.Time // the last event time, so a later replay of the file is recognized
	finals []agents.Event
}

// startTailer starts (or restarts) the tailer of a session at the place its
// TailStart names (design section 6). A start at the end of the file scans the
// file in a worker, then posts the backfill and starts the follower in the
// core loop (onTailScan).
func (d *daemon) startTailer(conv store.Conversation, threadID int64, start registry.TailStart) error {
	d.stopTailer(conv.ID, false)
	switch {
	case start.FromStart:
		d.follow(conv, threadID, tail.Position{})
	case start.AtEnd:
		n := 0
		if start.Backfill {
			n = d.cfg.Display.Backfill
		}
		d.scanThenFollow(conv, threadID, n)
	default:
		cur, ok, err := d.st.Cursor(d.ctx, conv.Transcript)
		switch {
		case err != nil:
			return fmt.Errorf("daemon: read cursor: %w", err)
		case ok:
			d.follow(conv, threadID, tail.Position{Offset: cur.Offset, DevIno: cur.DevIno})
		default:
			d.scanThenFollow(conv, threadID, 0)
		}
	}
	return nil
}

// follow starts the follower of a transcript at pos.
func (d *daemon) follow(conv store.Conversation, threadID int64, pos tail.Position) {
	d.gen++
	fctx, cancel := context.WithCancel(d.ctx)
	f := tail.Follow(fctx, conv.Transcript, pos, tail.Options{Poll: d.timing.TailPoll})
	tl := &tailer{conv: conv.ID, thread: threadID, gen: d.gen, f: f, cancel: cancel}
	d.tailers[conv.ID] = tl
	gen := tl.gen
	d.spawn(func() {
		for b := range f.Batches() {
			d.emit(evBatch{conv: conv.ID, gen: gen, b: b})
		}
		// A missing file is not an error (the follower waits for it), so an
		// error here is a failed read. Start again from the stored cursor
		// after a pause, or the topic would stop showing the session.
		if err := f.Err(); err != nil {
			d.log.Warn("transcript follower stopped; starting it again", "conversation", conv.ID, "err", err)
			if sleepCtx(d.ctx, d.timing.TailRestart) {
				d.emit(evTailEnded{conv: conv.ID, gen: gen})
			}
		}
	})
}

func (d *daemon) scanThenFollow(conv store.Conversation, threadID int64, n int) {
	d.gen++
	token := d.gen
	d.pending[conv.ID] = pendingTail{thread: threadID, token: token}
	d.spawn(func() {
		res := d.scanEnd(conv, n)
		res.thread, res.token = threadID, token
		d.emit(evTailScan(res))
	})
}

// scanEnd reads the whole transcript once: its end position, the newest event
// time, and the last n final answers. It runs in a worker.
func (d *daemon) scanEnd(conv store.Conversation, n int) tailScan {
	res := tailScan{conv: conv}
	a, ok := d.adapters[conv.Runtime]
	if !ok {
		return res
	}
	end, err := tail.Scan(conv.Transcript, func(l tail.Line) error {
		evs, err := a.ParseLine(l.Data, agents.LineContext{SessionID: conv.NativeID, Offset: l.Offset, CWD: conv.CWD})
		if err != nil {
			return nil
		}
		for _, e := range evs {
			if e.At.After(res.newest) {
				res.newest = e.At
			}
			if n > 0 && e.Kind == agents.AssistantText && e.Phase == agents.Final {
				res.finals = append(res.finals, e)
				if len(res.finals) > n {
					res.finals = res.finals[1:]
				}
			}
		}
		return nil
	})
	if err != nil {
		d.log.Debug("scan transcript", "conversation", conv.ID, "err", err)
		return res
	}
	res.ok, res.pos = true, end
	return res
}

// onTailScan finishes a start at the end of the file: the cursor, the
// backfill, and the follower. A missing file leaves the position at 0.
func (d *daemon) onTailScan(e evTailScan) {
	p, ok := d.pending[e.conv.ID]
	if !ok || p.token != e.token {
		return // stopped or restarted meanwhile
	}
	delete(d.pending, e.conv.ID)
	if e.ok {
		cur, _, _ := d.st.Cursor(d.ctx, e.conv.Transcript)
		cur.Transcript, cur.DevIno, cur.Offset, cur.UpdatedAt = e.conv.Transcript, e.pos.DevIno, e.pos.Offset, d.now()
		if e.newest.After(cur.LastEventAt) {
			cur.LastEventAt = e.newest
		}
		if err := d.st.SaveCursor(d.ctx, cur); err != nil {
			d.log.Warn("save cursor", "err", err)
		}
		for _, f := range e.finals {
			d.enqueue(store.OutboxItem{
				ThreadID: e.thread, Key: "backfill:" + f.Key, Kind: "backfill", Silent: true,
				Parts: d.markdownParts(f.Text, "backfill", false),
			})
		}
	}
	d.follow(e.conv, e.thread, e.pos)
	if !p.stopAt.IsZero() { // the session stopped while the scan ran
		d.tailers[e.conv.ID].stopAt = p.stopAt
	}
}

// restartTailer starts a stopped tailer again from the stored cursor (after a
// failed commit). Without a cursor the follower had started at byte 0.
func (d *daemon) restartTailer(convID int64) {
	if _, ok := d.tailers[convID]; ok {
		return
	}
	if _, ok := d.pending[convID]; ok {
		return
	}
	th, ok := d.threadOf(convID)
	if !ok {
		return
	}
	conv, err := d.st.Conversation(d.ctx, convID)
	if err != nil {
		return
	}
	pos := tail.Position{}
	if cur, ok, err := d.st.Cursor(d.ctx, conv.Transcript); err == nil && ok {
		pos = tail.Position{Offset: cur.Offset, DevIno: cur.DevIno}
	}
	d.follow(conv, th.ID, pos)
}

// stopTailer stops the tailer of a session now, or after a grace period so
// the last lines written before the agent exited still arrive.
func (d *daemon) stopTailer(conv int64, graceful bool) {
	if p, ok := d.pending[conv]; ok {
		if !graceful {
			delete(d.pending, conv)
		} else if p.stopAt.IsZero() {
			// The scan still runs: its follower stops after the grace period.
			p.stopAt = d.now().Add(d.timing.TailerGrace)
			d.pending[conv] = p
		}
	}
	tl, ok := d.tailers[conv]
	if !ok {
		return
	}
	if graceful {
		if tl.stopAt.IsZero() {
			tl.stopAt = d.now().Add(d.timing.TailerGrace)
		}
		return
	}
	delete(d.tailers, conv)
	tl.cancel()
	tl.f.Close()
}

func (d *daemon) stopAllTailers() {
	for conv := range d.tailers {
		d.stopTailer(conv, false)
	}
}

// tracker returns the turn tracker of a topic. After a restart it is rebuilt
// from the snapshot in the key-value table.
func (d *daemon) tracker(th store.Thread) *turn.Tracker {
	if t, ok := d.trackers[th.ID]; ok {
		return t
	}
	cfg := turn.Config{
		ProgressDelay:  d.cfg.Display.ProgressDelay.Duration,
		EditInterval:   d.cfg.Display.ProgressEditInterval.Duration,
		AnswerGrace:    d.timing.AnswerGrace,
		StoppedGrace:   d.timing.StoppedGrace,
		TypingInterval: d.timing.Typing,
		Redact:         format.Redact,
	}
	set := d.settings(th)
	var t *turn.Tracker
	if v, ok, err := d.st.Get(d.ctx, fmt.Sprintf("turn:%d", th.ID)); err == nil && ok {
		var snap turn.Snapshot
		if json.Unmarshal([]byte(v), &snap) == nil {
			t = turn.Restore(cfg, set, snap)
			d.snaps[th.ID] = v
		}
	}
	if t == nil {
		t = turn.New(cfg, set)
	}
	d.trackers[th.ID] = t
	return t
}

func (d *daemon) settings(th store.Thread) turn.Settings {
	return turn.Settings{Mode: th.Mode, Verbose: th.Verbose, Reasoning: th.Reasoning, ShowMacPrompts: d.cfg.Display.ShowMacPrompts}
}

// saveSnapshot stores the tracker's state when it changed.
func (d *daemon) saveSnapshot(threadID int64, t *turn.Tracker) {
	b, err := json.Marshal(t.Snapshot())
	if err != nil {
		return
	}
	s := string(b)
	if d.snaps[threadID] == s {
		return
	}
	if err := d.st.Set(d.ctx, fmt.Sprintf("turn:%d", threadID), s); err != nil {
		d.log.Warn("save turn snapshot", "thread", threadID, "err", err)
		return
	}
	d.snaps[threadID] = s
}

// markdownParts turns Markdown into outbox parts: redacted, split at 4000
// characters, one document when more than four parts are needed, or one rich
// part for a final answer when rich messages are on.
func (d *daemon) markdownParts(md, kind string, allowRich bool) []store.PartContent {
	md = format.Redact(md)
	if allowRich && d.cfg.Display.RichMessages && format.UTF16Len(md) <= maxRichRunes && md != "" {
		return []store.PartContent{{Content: md, Format: store.FormatRich}}
	}
	parts := format.Parts(md, partLimit)
	switch {
	case len(parts) == 0:
		return nil
	case len(parts) > maxTextParts:
		name := "message.md"
		if kind == "answer" || kind == "backfill" {
			name = "answer.md"
		}
		return []store.PartContent{{Content: md, Format: store.FormatDocument, Filename: name}}
	}
	out := make([]store.PartContent, len(parts))
	for i, p := range parts {
		out[i] = store.PartContent{Content: p.HTML, Format: store.FormatHTML}
	}
	return out
}

// work carries out the tracker's actions: progress changes go to the sender
// at once; posts come back as outbox items for the caller to commit.
func (d *daemon) work(th store.Thread, acts []turn.Action) []store.OutboxItem {
	var items []store.OutboxItem
	for _, a := range acts {
		switch a := a.(type) {
		case turn.Post:
			var parts []store.PartContent
			if a.HTML != "" {
				parts = []store.PartContent{{Content: a.HTML, Format: store.FormatHTML}}
			} else {
				parts = d.markdownParts(a.Markdown, a.Kind, a.Kind == "answer")
			}
			if len(parts) == 0 {
				continue
			}
			items = append(items, store.OutboxItem{ThreadID: th.ID, Key: a.Key, Kind: a.Kind, Parts: parts, Silent: a.Silent})
		case turn.ShowProgress:
			d.snd.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: a.HTML})
		case turn.EditProgress:
			d.snd.Progress(th.ID, sender.ProgressOp{Kind: sender.Edit, HTML: a.HTML})
		case turn.RepostProgress:
			d.snd.Progress(th.ID, sender.ProgressOp{Kind: sender.Repost, HTML: a.HTML})
		case turn.FinishProgress:
			d.snd.Progress(th.ID, sender.ProgressOp{Kind: sender.Finish, HTML: a.HTML})
		case turn.Typing:
			if d.lastThread == th.ID {
				d.snd.Typing(th.ChatID, th.MessageThreadID)
			}
		}
	}
	return items
}

// perform carries out the actions and enqueues their posts.
func (d *daemon) perform(th store.Thread, acts []turn.Action) {
	for _, it := range d.work(th, acts) {
		d.enqueue(it)
	}
}

// onBatch handles a batch of transcript lines (design section 4.2, and the
// "Transcript lines" flow of the implementation guide).
func (d *daemon) onBatch(ev evBatch) {
	tl := d.tailers[ev.conv]
	if tl == nil || tl.gen != ev.gen {
		return
	}
	ctx := d.ctx
	conv, err := d.st.Conversation(ctx, ev.conv)
	if err != nil {
		d.log.Warn("batch: conversation", "conversation", ev.conv, "err", err)
		return
	}
	th, err := d.st.Thread(ctx, tl.thread)
	if err != nil || th.Status == store.ThreadClosed || th.CurrentConversationID != conv.ID {
		return
	}
	a, ok := d.adapters[conv.Runtime]
	if !ok {
		return
	}
	if ev.b.Skipped > 0 {
		d.log.Warn("transcript lines skipped (too long)", "conversation", conv.ID, "count", ev.b.Skipped)
	}
	cur, _, err := d.st.Cursor(ctx, conv.Transcript)
	if err != nil {
		d.log.Warn("batch: cursor", "err", err)
		return
	}
	switch {
	case ev.b.Reset && !tl.replaying:
		tl.replaying, tl.replayUntil = true, cur.LastEventAt
	case !ev.b.Reset:
		tl.replaying = false
	}
	guard := cur.LastEventAt
	if tl.replaying {
		guard = tl.replayUntil
	}
	now := d.now()
	tr := d.tracker(th)
	lastAt := cur.LastEventAt
	var items []store.OutboxItem

	for _, ln := range ev.b.Lines {
		evs, err := a.ParseLine(ln.Data, agents.LineContext{SessionID: conv.NativeID, Offset: ln.Offset, CWD: conv.CWD})
		if err != nil {
			d.log.Debug("skipping a transcript line", "conversation", conv.ID, "offset", ln.Offset, "err", err)
			continue
		}
		for _, e := range evs {
			if ev.b.Reset && !e.At.IsZero() && !guard.IsZero() && !e.At.After(guard) {
				continue // replay guard (design section 6)
			}
			if e.At.After(lastAt) {
				lastAt = e.At
			}
			if e.Kind == agents.KindUnknown {
				d.unknown.add(conv.Runtime, e.Text, now) // never reaches the tracker or the registry
				continue
			}
			at := e.At
			if at.IsZero() {
				at = now
			}
			echo := false
			switch e.Kind {
			case agents.UserPrompt:
				echo = d.echo(th, e, now)
				d.signal(conv.ID, registry.Signal{State: "working", Kind: "prompt", At: at})
			case agents.AssistantText:
				if e.Phase == agents.Final {
					d.signal(conv.ID, registry.Signal{State: "idle", At: at})
				}
			case agents.TurnEnd:
				d.signal(conv.ID, registry.Signal{State: "idle", At: at})
			case agents.Title:
				if e.Text != "" && e.Text != conv.Title {
					if err := d.st.SetConversationTitle(ctx, conv.ID, e.Text); err == nil {
						conv.Title = e.Text
					}
				}
			}
			items = append(items, d.work(th, tr.Event(e, echo, now))...)
		}
	}

	c := store.Cursor{
		Transcript: conv.Transcript, DevIno: ev.b.End.DevIno, Offset: ev.b.End.Offset,
		LastEventAt: lastAt, UpdatedAt: now,
	}
	if _, err := d.st.CommitBatch(ctx, c, items); err != nil {
		// The tracker already took these lines. Read them again from the
		// stored cursor; the outbox keys stop a repeat from posting twice.
		d.log.Error("commit batch; reading the lines again", "conversation", conv.ID, "err", err)
		d.stopTailer(conv.ID, false)
		d.spawn(func() {
			if sleepCtx(d.ctx, time.Second) {
				d.emit(evRestart{conv: conv.ID})
			}
		})
		return
	}
	d.snd.Wake()
	d.saveSnapshot(th.ID, tr)
}

// echo tells whether a transcript prompt is a message that rex-tg typed. An
// echo acknowledges the Telegram message (👍) and is not posted again.
func (d *daemon) echo(th store.Thread, e agents.Event, now time.Time) bool {
	at := e.At
	if at.IsZero() || now.Sub(at) < 30*time.Second {
		at = now
	}
	rows, err := d.st.DeliveredSince(d.ctx, th.ID, at.Add(-10*time.Minute))
	if err != nil {
		d.log.Warn("echo: delivered rows", "err", err)
	}
	var cands []inbound.Delivered
	byID := map[int64]store.Inbound{}
	for _, r := range rows {
		cands = append(cands, inbound.Delivered{UpdateID: r.UpdateID, BodyHash: r.BodyHash, Body: r.Body, DeliveredAt: r.DeliveredAt})
		byID[r.UpdateID] = r
	}
	dl := d.delivering[th.ID]
	if dl != nil {
		cands = append(cands, inbound.Delivered{UpdateID: dl.in.UpdateID, BodyHash: dl.in.BodyHash, Body: dl.in.Body, DeliveredAt: dl.started})
	}
	id, ok := inbound.MatchEcho(e.Text, at, cands)
	if !ok {
		return false
	}
	if dl != nil && dl.in.UpdateID == id {
		dl.echoKey = e.Key // the Enter is still being confirmed; onTyped acknowledges
		return true
	}
	if err := d.st.AckInbound(d.ctx, id, e.Key); err != nil {
		d.log.Warn("ack inbound", "err", err)
	}
	if r, ok := byID[id]; ok {
		d.react(r, "👍")
		delete(d.extra, r.UpdateID)
	}
	return true
}
