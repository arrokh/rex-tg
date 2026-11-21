package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	// maxChatMessages is the most messages of message_length for one text;
	// a longer one uses messages of up to partLimit.
	maxChatMessages = 6
	maxRichRunes    = 32768
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
	conv     store.Conversation
	thread   int64
	token    int
	ok       bool
	pos      tail.Position
	newest   time.Time // the last event time, so a later replay of the file is recognized
	finals   []agents.Event
	backfill int
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
			// A live topic with no cursor after restart may be recovering an
			// interrupted start-at-end scan; retain its configured backfill.
			d.scanThenFollow(conv, threadID, d.cfg.Display.Backfill)
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
		res.thread, res.token, res.backfill = threadID, token, n
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
	if e.ok {
		cur, _, err := d.st.Cursor(d.ctx, e.conv.Transcript)
		if err != nil {
			d.log.Warn("read cursor for backfill", "err", err)
			d.retryTailScan(e)
			return
		}
		cur.Transcript, cur.DevIno, cur.Offset, cur.UpdatedAt = e.conv.Transcript, e.pos.DevIno, e.pos.Offset, d.now()
		if e.newest.After(cur.LastEventAt) {
			cur.LastEventAt = e.newest
		}
		var items []store.OutboxItem
		for _, f := range e.finals {
			it := store.OutboxItem{
				ThreadID: e.thread, Key: "backfill:" + f.Key, Kind: "backfill", Silent: true,
				Parts: d.markdownParts(f.Text, "backfill", false),
			}
			if len(it.Parts) > 0 {
				items = append(items, it)
			}
		}
		if _, err := d.st.CommitTranscriptBatch(d.ctx, cur, items, "", ""); err != nil {
			d.log.Warn("commit transcript backfill", "conversation", e.conv.ID, "err", err)
			d.retryTailScan(e)
			return
		}
		for _, it := range items {
			d.markBelow(it.ThreadID)
		}
		d.snd.Wake()
	}
	delete(d.pending, e.conv.ID)
	d.follow(e.conv, e.thread, e.pos)
	if !p.stopAt.IsZero() { // the session stopped while the scan ran
		d.tailers[e.conv.ID].stopAt = p.stopAt
	}
}

// retryTailScan leaves the pending start in place and rescans from byte 0
// after a transient storage failure. The scan result is never committed until
// its backfill and cursor can be stored together.
func (d *daemon) retryTailScan(e evTailScan) {
	d.spawn(func() {
		if !sleepCtx(d.ctx, d.timing.TailRestart) {
			return
		}
		res := d.scanEnd(e.conv, e.backfill)
		res.thread, res.token, res.backfill = e.thread, e.token, e.backfill
		d.emit(evTailScan(res))
	})
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
		ProgressDelay:   d.cfg.Display.ProgressDelay.Duration,
		EditInterval:    d.cfg.Display.ProgressEditInterval.Duration,
		AnimateInterval: d.cfg.Display.ProgressAnimation.Duration,
		SummaryAfter:    d.cfg.Display.SummaryAfter.Duration,
		AnswerGrace:     d.timing.AnswerGrace,
		StoppedGrace:    d.timing.StoppedGrace,
		TypingInterval:  d.timing.Typing,
		Redact:          format.Redact,
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
	return turn.Settings{Mode: th.Mode, Verbose: th.Verbose, Reasoning: th.Reasoning,
		ShowMacPrompts: d.cfg.Display.ShowMacPrompts, MacNotice: d.cfg.Display.MacNotice}
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

// markdownParts turns Markdown into outbox parts: redacted and split like
// chat messages (format.Messages: about [display] message_length characters
// each, at paragraphs and headings, no "(1/2)" counters), one document when
// the text is longer than four full messages, or one rich part for a final
// answer when rich messages are on.
func (d *daemon) markdownParts(md, kind string, allowRich bool) []store.PartContent {
	md = format.Redact(md)
	// A code block with lines too wide for a phone goes as a file after the
	// message, which keeps a short preview of it.
	short, code := format.SplitWideCode(md)
	parts := d.textParts(short, kind, allowRich)
	if len(parts) == 0 {
		return nil
	}
	if len(parts) == 1 && parts[0].Format == store.FormatDocument {
		return d.textParts(md, kind, allowRich) // too long for messages: one file holds it all
	}
	for _, f := range code {
		parts = append(parts, store.PartContent{Content: f.Content, Format: store.FormatDocument, Filename: f.Name})
	}
	return parts
}

// textParts is the message, or messages, of markdown (markdownParts).
func (d *daemon) textParts(md, kind string, allowRich bool) []store.PartContent {
	if allowRich && d.cfg.Display.RichMessages && format.UTF16Len(md) <= maxRichRunes && md != "" {
		return []store.PartContent{{Content: md, Format: store.FormatRich}}
	}
	parts := d.messageParts(md)
	total := 0
	for _, p := range parts {
		total += format.VisibleLen(p.HTML)
	}
	switch {
	case len(parts) == 0:
		return nil
	case total > maxTextParts*partLimit:
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

// messageParts is the exact text-message split used by textParts, before the
// long-answer document fallback.
func (d *daemon) messageParts(md string) []format.Part {
	parts := format.Messages(md, d.cfg.Display.MessageLength, partLimit)
	if len(parts) > maxChatMessages {
		// Too many to read on a phone: fuller messages, still split at
		// paragraphs.
		parts = format.Messages(md, partLimit, partLimit)
	}
	return parts
}

// wouldSplitIntoMessages tells whether textParts would send more than one
// Telegram text message, rather than one message or a single Markdown file.
func (d *daemon) wouldSplitIntoMessages(md string) bool {
	parts := d.messageParts(md)
	if len(parts) < 2 {
		return false
	}
	total := 0
	for _, p := range parts {
		total += format.VisibleLen(p.HTML)
	}
	return total <= maxTextParts*partLimit
}

// work carries out the tracker's actions: progress changes go to the sender
// at once; posts come back as outbox items for the caller to commit.
func (d *daemon) work(th store.Thread, acts []turn.Action) []store.OutboxItem {
	var items []store.OutboxItem
	for _, a := range acts {
		switch a := a.(type) {
		case turn.Post:
			if a.Kind == "answer" {
				d.closeDraft(th.ID) // before the answer's row exists
			}
			var parts []store.PartContent
			if a.HTML != "" {
				parts = []store.PartContent{{Content: a.HTML, Format: store.FormatHTML}}
			} else if long, ok := d.longAnswerIf(a.Kind == "answer", th, a.Key, a.Markdown); ok {
				parts = long // its start; the whole answer is a Mini App page
			} else {
				parts = d.markdownParts(a.Markdown, a.Kind, a.Kind == "answer")
			}
			if len(parts) == 0 {
				continue
			}
			// Only the agent's answer makes a sound; its other posts (block
			// mode text, prompts typed on the Mac, tool rows) are progress.
			items = append(items, store.OutboxItem{ThreadID: th.ID, Key: a.Key, Kind: a.Kind, Parts: parts, Silent: a.Silent || a.Kind != "answer"})
			if a.Kind == "answer" && a.HTML == "" {
				if img, ok := d.imageItem(th, a); ok {
					items = append(items, img)
					d.shownReset(th.ID) // the named files win
				} else if img, ok := d.shownItem(th, a); ok {
					items = append(items, img)
				}
				if offer, ok := d.fileOfferItem(th, a); ok {
					items = append(items, offer)
				}
			}
		case turn.ShowProgress:
			d.snd.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: a.HTML})
		case turn.EditProgress:
			d.snd.Progress(th.ID, sender.ProgressOp{Kind: sender.Edit, HTML: a.HTML})
		case turn.RepostProgress:
			d.snd.Progress(th.ID, sender.ProgressOp{Kind: sender.Repost, HTML: a.HTML})
		case turn.FinishProgress:
			d.snd.Progress(th.ID, sender.ProgressOp{Kind: sender.Finish, HTML: a.HTML})
		case turn.RemoveProgress:
			d.snd.Progress(th.ID, sender.ProgressOp{Kind: sender.Remove})
		case turn.Typing:
			if d.lastThread == th.ID {
				d.snd.Typing(th.ChatID, th.MessageThreadID)
			}
		case turn.MacNotice:
			d.closeDraft(th.ID)
			d.showMacNotice(th, a.HTML)
			d.boardDirty()
		case turn.MacBack:
			d.endMacNotice(th)
			d.boardDirty()
		case turn.HeldAnswer:
			d.closeDraft(th.ID)
			d.holdAnswer(th.ID, a)
			if n := d.storedMacNote(th); n != nil {
				if n.want == "" { // after a restart: the text the tracker kept
					n.want = d.tracker(th).MacNoticeHTML()
				}
				if n.want != "" {
					d.pushMacNotice(th, n) // the notice gets its "📄 Last answer" button
				}
			}
		}
	}
	return items
}

// imageItem makes the outbox item that sends the images a final answer names
// (design section 9.4a), or reports that there are none. The other file types
// get a button each instead (fileOfferItem).
func (d *daemon) imageItem(th store.Thread, a turn.Post) (store.OutboxItem, bool) {
	exts := sendExts(d.cfg.Display.SendImages, nil)
	if len(exts) == 0 || th.CurrentConversationID == 0 {
		return store.OutboxItem{}, false
	}
	conv, err := d.st.Conversation(d.ctx, th.CurrentConversationID)
	if err != nil {
		return store.OutboxItem{}, false
	}
	home, _ := os.UserHomeDir()
	var parts []store.PartContent
	for _, p := range findFiles(a.Markdown, home, conv.CWD, exts) {
		if real, ok := placeOK(p, home, conv.CWD); ok { // the sender reads the checked path
			parts = append(parts, store.PartContent{Content: real, Format: store.FormatImage, Filename: filepath.Base(p)})
		}
	}
	if len(parts) == 0 {
		return store.OutboxItem{}, false
	}
	return store.OutboxItem{ThreadID: th.ID, Key: a.Key + ":images", Kind: "image", Parts: parts, Silent: true}, true
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
	base := d.tracker(th)
	tr := base.Clone()
	// Keep the candidate isolated from every other tracker update until its
	// cursor, outbox rows, and snapshot commit together.
	d.trackers[th.ID] = tr
	lastAt := cur.LastEventAt
	finishedTree := ""
	var items []store.OutboxItem

	for _, ln := range ev.b.Lines {
		d.noteSeen(conv.ID, a, ln.Data) // model and effort, see controls.go
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
				d.shownReset(th.ID) // a new turn: the images of the last one are not its own
				d.turnStarted(conv.ID, conv.CWD, e.Key, tr.Running())
				if !e.Auto { // no person typed it, so it is no Telegram message
					echo = d.echo(th, e, now)
					if !echo && d.cfg.Display.MacNotice && d.typedRecently(th.ID, e.At, now) {
						// The text did not match, but rex-tg typed into this
						// pane a moment ago. Never hold that turn: its answer
						// would not reach Telegram.
						echo = true
					}
				}
				d.signal(conv.ID, registry.Signal{State: "working", Kind: "prompt", At: at})
			case agents.AssistantText:
				if e.Phase == agents.Final {
					d.signal(conv.ID, registry.Signal{State: "idle", At: at})
					finishedTree = d.turnTrees[conv.ID].key
					d.turnAnswered(conv.ID, th.ID, e.Key)
				} else {
					d.checkPromptGone(conv.ID, now)
				}
			case agents.ToolCall, agents.ToolResult:
				d.checkPromptGone(conv.ID, now)
				if e.Kind == agents.ToolCall {
					d.noteShownCall(th.ID, e)
				} else {
					d.noteShownResult(th.ID, e)
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
	snapshot, err := json.Marshal(tr.Snapshot())
	if err != nil {
		d.trackers[th.ID] = base
		d.retryBatch(conv.ID, err)
		return
	}
	snapshotValue := string(snapshot)
	if _, err := d.st.CommitTranscriptBatch(ctx, c, items, fmt.Sprintf("turn:%d", th.ID), snapshotValue); err != nil {
		d.trackers[th.ID] = base
		d.retryBatch(conv.ID, err)
		return
	}
	d.snaps[th.ID] = snapshotValue
	if start, ok := d.turnTrees[conv.ID]; ok && finishedTree != "" && start.key == finishedTree {
		delete(d.turnTrees, conv.ID)
	}
	d.snd.Wake()
}

// retryBatch stops the follower and restarts it from the last durable cursor.
// The staged tracker is discarded by the caller before this runs.
func (d *daemon) retryBatch(convID int64, err error) {
	d.log.Error("commit batch; reading the lines again", "conversation", convID, "err", err)
	d.stopTailer(convID, false)
	d.spawn(func() {
		if sleepCtx(d.ctx, d.timing.TailRestart) {
			d.emit(evRestart{conv: convID})
		}
	})
}

// typedRecentlyWindow is how long after typing a prompt that does not match
// the typed text still counts as possibly from Telegram (the Mac hold).
const typedRecentlyWindow = 30 * time.Second

// typedRecently tells whether rex-tg is typing into a topic's pane, or typed
// a message there whose prompt the transcript has not shown yet, within
// typedRecentlyWindow before now or around the prompt's own time (at). A
// message whose prompt was seen is no longer in question. The prompt's own
// time counts because an agent may write a prompt long after it took it:
// Claude Code writes a message typed while it works when the turn takes it
// in.
func (d *daemon) typedRecently(thread int64, at, now time.Time) bool {
	if dl := d.delivering[thread]; dl != nil && dl.echoKey == "" {
		return true
	}
	from := now.Add(-typedRecentlyWindow)
	if !at.IsZero() && at.Add(-typedRecentlyWindow).Before(from) {
		from = at.Add(-typedRecentlyWindow)
	}
	rows, err := d.st.DeliveredSince(d.ctx, thread, from)
	if err != nil {
		return false
	}
	for _, r := range rows {
		near := !at.IsZero() && r.DeliveredAt.Sub(at).Abs() <= typedRecentlyWindow
		if near || !r.DeliveredAt.Before(now.Add(-typedRecentlyWindow)) {
			return true
		}
	}
	return false
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
		// A previous attempt may already have acknowledged this event before
		// its transcript transaction failed. Preserve its Telegram origin on
		// replay, including after restart, without acknowledging it twice.
		acked, err := d.st.AcknowledgedEcho(d.ctx, th.ID, e.Key)
		if err != nil {
			d.log.Warn("echo: acknowledged event", "err", err)
		}
		return acked
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
