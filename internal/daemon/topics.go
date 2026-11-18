package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"rex-tg/internal/format"
	"rex-tg/internal/registry"
	"rex-tg/internal/rex"
	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
	"rex-tg/internal/turn"
)

// topicLink is the link to a topic (Bot API 10.4).
func (d *daemon) topicLink(mtid int64) string {
	return fmt.Sprintf("https://t.me/%s/%d", d.botUser, mtid)
}

// convTitle returns the session's title; when the store has none it reads the
// transcript header.
func (d *daemon) convTitle(conv store.Conversation) string {
	if conv.Title != "" {
		return conv.Title
	}
	a, ok := d.adapters[conv.Runtime]
	if !ok {
		return ""
	}
	f, err := os.Open(conv.Transcript)
	if err != nil {
		return ""
	}
	defer f.Close()
	h, err := a.ReadHeader(f)
	if err != nil {
		return ""
	}
	return h.Title
}

func (d *daemon) tildePath(p string) string {
	if p == "" {
		return ""
	}
	if d.home != "" && (p == d.home || strings.HasPrefix(p, d.home+string(filepath.Separator))) {
		return "~" + strings.TrimPrefix(p, d.home)
	}
	return p
}

const topicClosePendingPrefix = "topic_close_pending:"

func topicClosePendingKey(threadID int64) string {
	return fmt.Sprintf("%s%d", topicClosePendingPrefix, threadID)
}

func (d *daemon) topicTerminating(threadID int64) bool {
	_, ok := d.terminatingTopics.Load(threadID)
	return ok
}

func (d *daemon) markTopicClosePending(threadID int64) {
	if err := d.st.Set(d.ctx, topicClosePendingKey(threadID), "1"); err != nil {
		d.log.Warn("save pending topic close", "thread", threadID, "err", err)
	}
}

func (d *daemon) clearTopicClosePending(threadID int64) {
	if err := d.st.Delete(d.ctx, topicClosePendingKey(threadID)); err != nil {
		d.log.Warn("clear pending topic close", "thread", threadID, "err", err)
	}
}

func topicCloseSucceeded(err error) bool {
	return err == nil || telegram.Classify(err) == telegram.ClassThreadNotFound || telegram.Classify(err) == telegram.ClassNotModified
}

// threadOf returns the open topic whose current session is conv.
func (d *daemon) threadOf(conv int64) (store.Thread, bool) {
	th, ok, err := d.st.ThreadByConversation(d.ctx, conv)
	if err != nil || !ok || th.CurrentConversationID != conv || th.Status == store.ThreadClosed {
		return store.Thread{}, false
	}
	if d.topicTerminating(th.ID) {
		return store.Thread{}, false
	}
	return th, true
}

// onChanges applies the registry's changes (flow 1).
func (d *daemon) onChanges(chs []registry.Change) {
	if len(chs) > 0 {
		d.boardDirty() // an agent came, went, or changed its state
	}
	for _, c := range chs {
		switch c.Kind {
		case registry.SessionStopped:
			d.stopTailer(c.ConversationID, true)
		case registry.TakeOver, registry.Resume:
			if !d.topicTerminating(c.ThreadID) {
				d.attach(c)
			}
		case registry.TopicOffline:
			d.topicOffline(c)
		case registry.TopicLive:
			if d.topicTerminating(c.ThreadID) {
				d.markTopicClosePending(c.ThreadID)
				if _, err := d.st.SetThreadStatusIf(d.ctx, c.ThreadID, store.ThreadLive, store.ThreadOffline); err != nil {
					d.log.Warn("keep terminating topic offline", "thread", c.ThreadID, "err", err)
				}
				continue
			}
			if th, err := d.st.Thread(d.ctx, c.ThreadID); err == nil && th.Status != store.ThreadClosed {
				n := d.nextN(fmt.Sprintf("livn:%d", th.ID))
				name := "The agent"
				if conv, err := d.st.Conversation(d.ctx, c.ConversationID); err == nil {
					name = string(conv.Runtime)
					d.rename(th, conv) // the 🔴 goes away
				}
				d.notice(th.ID, fmt.Sprintf("live:%d:%d", th.ID, n), "🟢 "+format.Escape(name)+" is running again.")
				d.dirty[th.ID] = true
			}
		case registry.StateChanged:
			d.onState(c)
		case registry.PaneGone, registry.SessionStarted, registry.ResumeBusy:
			// PaneGone is followed by TopicOffline when a topic was affected.
			// The other two need no action: the topic is opened by the user.
		}
	}
}

// attach makes a session the current one of a topic: divider, tailer, backfill.
func (d *daemon) attach(c registry.Change) {
	ctx := d.ctx
	th, err := d.st.Thread(ctx, c.ThreadID)
	if err != nil || th.Status == store.ThreadClosed {
		return
	}
	if d.topicTerminating(th.ID) {
		return
	}
	conv, err := d.st.Conversation(ctx, c.ConversationID)
	if err != nil {
		d.log.Warn("attach: conversation", "err", err)
		return
	}
	for cid, tl := range d.tailers {
		if tl.thread == th.ID && cid != conv.ID {
			d.stopTailer(cid, false)
		}
	}
	if c.Divider != "" {
		n := d.nextN(fmt.Sprintf("divn:%d:%d", th.ID, conv.ID))
		d.enqueue(store.OutboxItem{
			ThreadID: th.ID, Key: fmt.Sprintf("divider:%d:%d:%d", th.ID, conv.ID, n), Kind: "divider", Silent: true,
			Parts: []store.PartContent{{Content: "── " + format.Escape(c.Divider) + " ──", Format: store.FormatHTML}},
		})
	}
	if err := d.startTailer(conv, th.ID, c.Start); err != nil {
		d.log.Warn("start tailer", "thread", th.ID, "err", err)
	}
	d.tracker(th)
	d.dirty[th.ID] = true
	if c.Kind == registry.TakeOver {
		d.rename(th, conv)
	}
}

// desiredName is the topic name for a session (see topicName). It may read
// the transcript, so it runs in a worker.
func (d *daemon) desiredName(th store.Thread, conv store.Conversation) string {
	label := ""
	if p, ok, err := d.st.PaneByConversation(d.ctx, conv.ID); err == nil && ok {
		label = p.Label
	}
	return topicName(d.nameLabel(th, label), conv.Runtime, conv.CWD, d.convTitle(conv))
}

// Keys of a topic's tab names (kv): the label rex-tg gave the tab last, and
// a name that someone else gave it.
func tabSetKey(thread int64) string  { return fmt.Sprintf("tab_set:%d", thread) }
func tabUserKey(thread int64) string { return fmt.Sprintf("tab_user:%d", thread) }

// nameLabel is the name a topic and its tab are named after: a name that you
// (or an agent, with rex window rename) gave the tab, else the pane's label.
func (d *daemon) nameLabel(th store.Thread, paneLabel string) string {
	if u, ok, err := d.st.Get(d.ctx, tabUserKey(th.ID)); err == nil && ok && strings.TrimSpace(u) != "" {
		return u
	}
	return paneLabel
}

// evRename asks the core to rename a topic now (a name source changed).
type evRename struct{ thread int64 }

func (d *daemon) onRename(e evRename) {
	th, err := d.st.Thread(d.ctx, e.thread)
	if err != nil || th.Status == store.ThreadClosed {
		return
	}
	if conv, err := d.st.Conversation(d.ctx, th.CurrentConversationID); err == nil {
		d.rename(th, conv)
	}
}

// nameSyncInterval is how often topic names are compared with their pane and
// session names. A pane name or a session name can change at any time.
const nameSyncInterval = 30 * time.Second

// syncNames renames each topic whose pane name, session name, or state
// changed.
func (d *daemon) syncNames() {
	threads, err := d.st.Threads(d.ctx)
	if err != nil {
		return
	}
	for _, th := range threads {
		if th.Status == store.ThreadClosed || th.CurrentConversationID == 0 {
			continue
		}
		if conv, err := d.st.Conversation(d.ctx, th.CurrentConversationID); err == nil {
			d.rename(th, conv)
		}
	}
}

// rename gives a topic the name of its session and pane, when it changed.
// The title may come from the transcript, so the work runs in a worker. The
// new name is saved only once Telegram took it, so a rename that failed is
// tried again at the next check (syncNames).
func (d *daemon) rename(th store.Thread, conv store.Conversation) {
	d.spawn(func() {
		// Another rename may have run since the caller read th: work from
		// the stored state, so an older rename cannot undo a newer one.
		if fresh, err := d.st.Thread(d.ctx, th.ID); err == nil {
			th = fresh
		}
		if th.Status == store.ThreadClosed {
			return
		}
		var name string
		if th.Status == store.ThreadOffline {
			// The session left its pane, so the pane's name is not known any
			// more: keep the name and mark it.
			old := strings.TrimPrefix(th.Name, offlineMark)
			if t := tidyName(old, folderBase(conv.CWD)); t != "" {
				old = shortName(t, maxTopicName) // a name from before tidyName
			}
			name = statusName(th, old)
		} else {
			name = d.desiredName(th, conv)
		}
		if name == th.Name {
			d.syncTab(th, conv) // the tab may differ although the topic does not
			return
		}
		err := d.snd.Call(sender.PriorityEdit, func(ctx context.Context, tg *telegram.Client) error {
			return tg.EditForumTopic(ctx, th.ChatID, th.MessageThreadID, name)
		})
		switch c := telegram.Classify(err); c {
		case telegram.ClassNone, telegram.ClassNotModified:
			if err := d.st.SetThreadName(d.ctx, th.ID, name); err != nil {
				d.log.Warn("rename topic", "err", err)
			}
			// The evidence of a rename: Telegram sends the bot no notice of
			// its own renames in a private chat (seen live). "not_modified":
			// Telegram says the topic has the name already.
			d.log.Info("topic renamed", "thread", th.ID, "telegram", c.String())
			d.syncTab(th, conv)
		case telegram.ClassThreadNotFound:
			d.emit(evSender{sender.Event{Kind: sender.ThreadGone, ThreadID: th.ID}}) // the user deleted it
		default:
			if d.ctx.Err() == nil {
				d.log.Warn("editForumTopic", "err", err)
			}
		}
	})
}

// tabSessionTTL is how long syncTab reuses the list of Rex sessions: one
// name check runs syncTab for every topic, and they need one list only.
const tabSessionTTL = 20 * time.Second

// tabSessionList returns the Rex sessions, at most tabSessionTTL old. The
// caller holds tabMu.
func (d *daemon) tabSessionList() ([]rex.Session, error) {
	if now := d.now(); d.tabSessions != nil && now.Sub(d.tabSessionsAt) < tabSessionTTL {
		return d.tabSessions, nil
	}
	ss, err := d.rx.Sessions(d.ctx)
	if err != nil {
		return nil, err
	}
	d.tabSessions, d.tabSessionsAt = ss, d.now()
	return ss, nil
}

// inOwnSession tells whether a Rex session is the one of /new
// (rex.new_session). The caller holds tabMu. A cached list that predates the
// session that /new just made is read again once.
func (d *daemon) inOwnSession(session string) (bool, error) {
	sessions, err := d.tabSessionList()
	if err != nil {
		return false, err
	}
	own, ok := d.ownSession(sessions)
	if !ok && d.now().Sub(d.tabSessionsAt) > time.Second {
		d.tabSessions = nil
		if sessions, err = d.tabSessionList(); err != nil {
			return false, err
		}
		own, ok = d.ownSession(sessions)
	}
	return ok && own.ID == session, nil
}

// syncTab gives the Rex tab of an agent the name that goes with its topic
// (worker): the topic's name, or the folder and agent form when the topic has
// only the folder fallback, with a 🔴 in front while the topic is offline. th
// is the stored state. Nothing happens when the tab already has the name, so
// the 30-second name check costs no rename. Only tabs of the session for /new
// (rex.new_session) are renamed: rex-tg made them, and the other tabs are
// the user's. A failure is only logged: the next name check tries again.
func (d *daemon) syncTab(th store.Thread, conv store.Conversation) {
	want := strings.TrimSpace(d.cfg.Rex.NewSession)
	if want == "" {
		return
	}
	p, ok, err := d.st.PaneByConversation(d.ctx, conv.ID)
	if err == nil && !ok && th.Status == store.ThreadOffline {
		// The agent left, but its pane is open and empty: that is its tab.
		// (A pane that another session took is not.)
		var panes []store.Pane
		if panes, err = d.st.Panes(d.ctx); err == nil {
			if i := slices.IndexFunc(panes, func(q store.Pane) bool { return q.ConversationID == 0 && q.LastConversationID == conv.ID }); i >= 0 {
				p, ok = panes[i], true
			}
		}
	}
	if err != nil || !ok || p.Session == "" || p.Block == "" || !p.GoneAt.IsZero() {
		return
	}
	d.tabMu.Lock() // two syncs must not pick the same unique name
	defer d.tabMu.Unlock()
	isOwn, err := d.inOwnSession(p.Session)
	if err != nil {
		d.log.Warn("rename tab: list sessions", "err", err)
		return
	}
	if !isOwn {
		return // a tab of the user's own sessions
	}
	blocks, err := d.rx.Blocks(d.ctx, p.Session)
	if err != nil {
		d.log.Warn("rename tab: list panes", "err", err)
		return
	}
	j := slices.IndexFunc(blocks, func(b rex.Block) bool { return b.ID == p.Block })
	if j < 0 || blocks[j].WindowID == "" {
		return
	}
	window := blocks[j].WindowID
	hasAgent := func(block string) bool {
		q, ok, err := d.st.PaneByBlock(d.ctx, p.Host, block)
		return err == nil && ok && q.ConversationID != 0
	}
	if tabOwner(blocks, window, p.Block, hasAgent) != p.Block {
		return // another agent in this tab names it
	}
	windows, err := d.rx.Windows(d.ctx, p.Session)
	if err != nil {
		d.log.Warn("rename tab: list tabs", "err", err)
		return
	}
	k := slices.IndexFunc(windows, func(w rex.Window) bool { return w.ID == window })
	if k < 0 {
		return
	}
	current := windows[k].Label
	set, hasSet, _ := d.st.Get(d.ctx, tabSetKey(th.ID))
	if hasSet && current != set && strings.TrimSpace(current) != "" && th.Status != store.ThreadOffline {
		// Someone else named the tab (you, or an agent with rex window
		// rename): the name stays, and the topic takes it. A " #2" goes only
		// when rex-tg had added it; one that was typed stays.
		name := strings.TrimPrefix(current, offlineMark)
		if sfx := tabSuffix.FindString(set); sfx != "" && strings.HasSuffix(name, sfx) {
			name = strings.TrimSuffix(name, sfx)
		}
		name = cleanLabel(name)
		if paneName(name) == "" { // "shell", "zsh": no name that anyone chose
			_ = d.st.Set(d.ctx, tabSetKey(th.ID), current)
			return
		}
		if name == tabLabel(conv.CWD, string(conv.Runtime)) {
			// Back to the "<folder> · <agent>" form: rex-tg names the tab
			// and the topic again.
			_ = d.st.Delete(d.ctx, tabUserKey(th.ID))
			_ = d.st.Set(d.ctx, tabSetKey(th.ID), current)
			d.log.Info("tab named back to its agent form; rex-tg names it again", "thread", th.ID)
			d.emit(evRename{thread: th.ID})
			return
		}
		if err := d.st.Set(d.ctx, tabUserKey(th.ID), name); err != nil {
			d.log.Warn("tab name: keep", "err", err)
			return
		}
		_ = d.st.Set(d.ctx, tabSetKey(th.ID), current)
		d.log.Info("tab renamed outside rex-tg; the topic follows it", "thread", th.ID)
		d.emit(evRename{thread: th.ID})
		return
	}
	var label string
	if th.Status == store.ThreadOffline {
		label = offlineTabName(current)
	} else {
		var others []string
		for _, w := range windows {
			if w.ID != window {
				others = append(others, w.Label)
			}
		}
		if u, ok, _ := d.st.Get(d.ctx, tabUserKey(th.ID)); ok && strings.TrimSpace(u) != "" {
			label = uniqueTabName(u, others) // the name someone gave it, as it was typed
		} else {
			label = uniqueTabName(tabName(p.Label, conv.Runtime, conv.CWD, d.convTitle(conv), current), others)
		}
	}
	if label == "" || label == current {
		if !hasSet || set != current {
			_ = d.st.Set(d.ctx, tabSetKey(th.ID), current) // what the tab is named now, as rex-tg wants
		}
		return
	}
	if err := d.rx.RenameWindow(d.ctx, p.Session, window, label); err != nil {
		d.log.Warn("rename tab", "window", window, "err", err)
		return
	}
	_ = d.st.Set(d.ctx, tabSetKey(th.ID), label)
}

// topicOffline records the end of an agent session, posts a final notice,
// then closes the Telegram topic while keeping its history.
func (d *daemon) topicOffline(c registry.Change) {
	th, err := d.st.Thread(d.ctx, c.ThreadID)
	if err != nil || th.Status == store.ThreadClosed {
		return
	}
	if _, loaded := d.terminatingTopics.LoadOrStore(th.ID, &topicTermination{}); loaded {
		return
	}
	d.markTopicClosePending(th.ID)
	conv, cerr := d.st.Conversation(d.ctx, c.ConversationID)
	name := "The agent"
	if cerr == nil {
		name = string(conv.Runtime)
	}
	n := d.nextN(fmt.Sprintf("offn:%d", th.ID))
	d.notice(th.ID, fmt.Sprintf("offline:%d:%d", th.ID, n), offlineText(name, c.Reason))
	if tr, ok := d.trackers[th.ID]; ok {
		d.perform(th, tr.State(turn.StateStopped, d.now()))
	}
	d.stopTailer(c.ConversationID, true)
	d.closeAlerts(th, store.AlertGone)
	d.dirty[th.ID] = true
	d.maybeCloseStoppedTopic(th.ID)
}

// closeRestoredOfflineTopics resumes persisted close attempts before the
// registry worker starts. The marker also covers a resume racing with close or
// a daemon stop before the core handles the registry event.
func (d *daemon) closeRestoredOfflineTopics() {
	threads, err := d.st.Threads(d.ctx, store.ThreadOffline)
	if err != nil {
		d.log.Warn("restore stopped topics", "err", err)
		return
	}
	targets := make(map[int64]store.Thread, len(threads))
	for _, th := range threads {
		targets[th.ID] = th
	}
	keys, err := d.st.KeysWithPrefix(d.ctx, topicClosePendingPrefix)
	if err != nil {
		d.log.Warn("list pending topic closes", "err", err)
	}
	for _, key := range keys {
		id, err := strconv.ParseInt(strings.TrimPrefix(key, topicClosePendingPrefix), 10, 64)
		if err != nil {
			d.log.Warn("invalid pending topic close key", "key", key, "err", err)
			continue
		}
		th, err := d.st.Thread(d.ctx, id)
		if err != nil {
			d.log.Warn("load pending topic close", "thread", id, "err", err)
			continue
		}
		targets[id] = th
	}
	for _, th := range targets {
		if d.ctx.Err() != nil {
			return
		}
		d.terminatingTopics.Store(th.ID, &topicTermination{})
		d.markTopicClosePending(th.ID)
		d.closeStoppedTopic(th)
	}
}

// maybeCloseStoppedTopic waits for the stopped turn and any in-flight paste to
// finish before closing Telegram. Their final progress and safety notices stay
// ahead of the close call.
func (d *daemon) maybeCloseStoppedTopic(threadID int64) {
	if !d.topicTerminating(threadID) || d.delivering[threadID] != nil {
		return
	}
	if tr := d.trackers[threadID]; tr != nil && tr.Snapshot().Phase == "ending" {
		return
	}
	th, err := d.st.Thread(d.ctx, threadID)
	if err != nil || th.Status == store.ThreadClosed {
		return
	}
	d.closeStoppedTopic(th)
}

// closeStoppedTopic closes Telegram only after higher-priority outbox messages
// (including the stop notice) have had a chance to send. The local thread is
// closed when the call finishes, even if Telegram's outcome is unknown.
func (d *daemon) closeStoppedTopic(th store.Thread) {
	v, ok := d.terminatingTopics.Load(th.ID)
	if !ok || !v.(*topicTermination).closeStarted.CompareAndSwap(false, true) {
		return
	}
	d.spawn(func() {
		err := d.closeTopicInTelegram(th)
		remoteClosed := topicCloseSucceeded(err)
		if !remoteClosed && d.ctx.Err() == nil {
			d.log.Warn("close stopped topic; retrying on next start", "thread", th.ID, "err", err)
		}
		d.emit(evTopicClosed{threadID: th.ID, remoteClosed: remoteClosed})
	})
}

func (d *daemon) closeTopicInTelegram(th store.Thread) error {
	return d.snd.Call(sender.PriorityEdit, func(ctx context.Context, tg *telegram.Client) error {
		return tg.CloseForumTopic(ctx, th.ChatID, th.MessageThreadID)
	})
}

// finishStoppedTopic detaches the ended session after Telegram's close
// attempt. A failed attempt stays marked so the next start can retry it.
func (d *daemon) finishStoppedTopic(threadID int64, remoteClosed bool) {
	d.closeThread(threadID)
	if remoteClosed {
		if th, err := d.st.Thread(d.ctx, threadID); err == nil && th.Status == store.ThreadClosed {
			d.clearTopicClosePending(threadID)
		}
	}
}

// offlineText is the notice for a stopped agent.
func offlineText(name, reason string) string {
	why := ""
	switch reason {
	case registry.ReasonPaneClosed:
		why = ": its Rex pane was closed"
	case registry.ReasonAgentExited:
		why = ": it exited, and the pane is still open"
	case registry.ReasonReplaced:
		why = ": another session took its pane"
	}
	return "🔴 " + format.Escape(name) + " stopped" + why + ".\nThis topic is no longer linked to the agent; its history stays here. If you resume the session, use /agents to open it in a new topic."
}

// statusName is the topic name to show for a thread: a 🔴 in front while
// its agent is stopped.
func statusName(th store.Thread, name string) string {
	if th.Status == store.ThreadOffline {
		return offlineMark + name
	}
	return name
}

// offlineMark leads the name of a topic whose agent stopped.
const offlineMark = "🔴 "

// onState feeds a pane state to the topic's turn tracker and opens or closes
// alerts (flow 1).
func (d *daemon) onState(c registry.Change) {
	th, ok := d.threadOf(c.ConversationID)
	if !ok {
		return
	}
	now := d.now()
	tr := d.tracker(th)
	d.perform(th, tr.State(turn.State(c.State), now))
	d.saveSnapshot(th.ID, tr)
	d.dirty[th.ID] = true
	delete(d.nextPrep, th.ID)

	if c.State == "blocked" {
		d.noteBlocked(c.ConversationID, now)
	} else {
		d.forgetBlocked(c.ConversationID)
	}
	if c.State == "blocked" && (c.StateKind == store.AlertPermission || c.StateKind == store.AlertQuestion || c.StateKind == "") {
		// An untyped blocked state is safe to expose only if alertContent can
		// recognize the screen as a question or explicit Yes/No confirmation.
		d.openAlert(th, c.StateKind)
		return
	}
	if c.State != "blocked" {
		d.promptEnded(th)
	}
}

// openTopic creates the Telegram topic for the session in a pane (flow 6).
// The `o:<pane id>` button calls it, and /new once its agent is ready.
// replyThread is the topic where the request came from (0 outside topics);
// the link to the new topic is posted there. answer reports the outcome of a
// button press: alert is set for a failure, which shows as a dialog.
func (d *daemon) openTopic(paneID, replyThread int64, answer func(text string, alert bool)) {
	if d.opening[paneID] {
		answer("Opening…", false)
		return
	}
	d.opening[paneID] = true
	chat := d.ownerChat
	d.spawn(func() {
		defer d.emit(evOpenDone{paneID})
		ctx := d.ctx
		views, err := d.reg.Agents(ctx)
		if err != nil {
			answer("Something went wrong. Try /agents again.", true)
			return
		}
		var v *registry.AgentView
		for i := range views {
			if views[i].Pane.ID == paneID {
				v = &views[i]
			}
		}
		switch {
		case v == nil:
			answer("That pane is gone. Use /agents again.", true)
			return
		case v.Thread != nil && d.topicTerminating(v.Thread.ID):
			answer("That topic is closing. Open the agent again from /agents in a moment.", true)
			return
		case v.Conversation == nil:
			answer("Waiting for the session file… It resolves at the agent's next prompt or turn end.", true)
			return
		}
		conv := *v.Conversation
		title := d.convTitle(conv)
		name := topicName(v.Pane.Label, conv.Runtime, conv.CWD, title)
		var gone int64 // a topic that the user deleted: closed once the new one exists
		if v.Thread != nil && v.Thread.Status != store.ThreadClosed {
			th := *v.Thread
			if d.topicExists(th, statusName(th, name)) {
				answer("", false)
				d.say(chat, replyThread, "↗ "+format.Escape(noAutoLink(name)), linkMarkup("Open topic", d.topicLink(th.MessageThreadID)))
				return
			}
			gone = th.ID // the pane and the session still exist: make a new topic
		}
		var ft telegram.ForumTopic
		err = d.snd.Call(sender.PriorityAlert, func(ctx context.Context, tg *telegram.Client) error {
			t, err := tg.CreateForumTopic(ctx, chat, name)
			ft = t
			return err
		})
		if err != nil {
			d.log.Warn("createForumTopic", "err", err)
			switch telegram.Classify(err) {
			case telegram.ClassUnknownOutcome:
				// The topic may exist anyway. By now the button press may be
				// too old to answer, so the note goes into the chat.
				answer("", false)
				d.say(chat, replyThread, "⚠️ Telegram did not answer in time. If no new topic appeared, open the agent again from /agents.", nil)
			case telegram.ClassNotSent:
				answer("", false)
				d.say(chat, replyThread, "⚠️ Could not reach Telegram. Open the agent again from /agents when the connection is back.", nil)
			default:
				answer("Could not create the topic. Is Threaded Mode on for the bot?", true)
			}
			return
		}
		if gone != 0 {
			// Both events go through the main loop in this order, so the old
			// topic is closed before the session is linked to the new one.
			d.emit(evSender{sender.Event{Kind: sender.ThreadGone, ThreadID: gone}})
		}
		ready := make(chan bool, 1)
		d.emit(evTopicCreated{chatID: chat, threadID: ft.MessageThreadID, name: name, convID: conv.ID, title: title, ready: ready})
		select {
		case initialized := <-ready:
			if !initialized {
				answer("Could not finish setting up the topic. Use /agents to open it.", true)
				return
			}
		case <-ctx.Done():
			return
		}
		answer("Topic created", false)
		d.say(chat, replyThread, "🔗 Opened "+format.Escape(noAutoLink(name)), linkMarkup("Open topic", d.topicLink(ft.MessageThreadID)))
	})
}

// topicExists checks that a topic is still there, because Telegram does not
// report a topic that the user deleted. It sets the topic's name, which also
// keeps the name current: Telegram answers TOPIC_NOT_MODIFIED for the same
// name and "message thread not found" for a deleted topic. Any other answer,
// such as no answer in time, counts as "still there".
func (d *daemon) topicExists(th store.Thread, name string) bool {
	err := d.snd.Call(sender.PriorityAlert, func(ctx context.Context, tg *telegram.Client) error {
		return tg.EditForumTopic(ctx, th.ChatID, th.MessageThreadID, name)
	})
	switch telegram.Classify(err) {
	case telegram.ClassThreadNotFound:
		return false
	case telegram.ClassNone, telegram.ClassNotModified:
		if name != th.Name {
			if err := d.st.SetThreadName(d.ctx, th.ID, name); err != nil {
				d.log.Warn("rename topic", "err", err)
			}
		}
	default:
		d.log.Warn("check topic", "thread", th.ID, "err", err)
	}
	return true
}

func linkMarkup(label, url string) *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{{Text: "↗ " + label, URL: url}}}}
}

// closeForumTopic closes a Telegram topic that could not be linked or initialized.
func (d *daemon) closeForumTopic(chatID, threadID int64) {
	d.spawn(func() {
		if err := d.snd.Call(sender.PriorityEdit, func(ctx context.Context, tg *telegram.Client) error {
			return tg.CloseForumTopic(ctx, chatID, threadID)
		}); err != nil && d.ctx.Err() == nil {
			d.log.Warn("close unfinished topic", "thread", threadID, "err", err)
		}
	})
}

// onTopicCreated links the new topic and posts the intro card and backfill.
// It acknowledges setup only after the intro is durably in the outbox, so the
// sender cannot publish the source-chat link before the topic is initialized.
func (d *daemon) onTopicCreated(e evTopicCreated) {
	ready := false
	if e.ready != nil {
		defer func() { e.ready <- ready }()
	}
	ctx := d.ctx
	conv, err := d.st.Conversation(ctx, e.convID)
	if err != nil {
		d.log.Warn("link topic: conversation", "err", err)
		d.closeForumTopic(e.chatID, e.threadID)
		return
	}
	th, err := d.st.LinkTopic(ctx, e.chatID, e.threadID, conv.ID, e.name, d.now())
	if err != nil {
		d.log.Warn("link topic", "err", err)
		d.closeForumTopic(e.chatID, e.threadID)
		return
	}
	// A new topic starts in the configured display mode.
	if m := d.cfg.Display.Mode; m != "" && m != th.Mode {
		if err := d.st.SetThreadSettings(ctx, th.ID, m, th.Verbose, th.Reasoning, th.Muted); err != nil {
			d.log.Warn("apply display mode", "thread", th.ID, "err", err)
		} else {
			th.Mode = m
		}
	}
	if !d.enqueue(store.OutboxItem{
		ThreadID: th.ID, Key: fmt.Sprintf("intro:%d", th.ID), Kind: "notice", Silent: true,
		Parts: []store.PartContent{{Content: d.introCard(th, conv, e.title), Format: store.FormatHTML}},
	}) {
		// A linked topic without an intro could receive transcript output before
		// the user can open it. Close it and let /agents create a fresh one.
		d.closeThread(th.ID)
		d.closeForumTopic(e.chatID, e.threadID)
		return
	}
	ready = true
	d.tracker(th)
	d.dirty[th.ID] = true
	if err := d.startTailer(conv, th.ID, registry.TailStart{AtEnd: true, Backfill: true}); err != nil {
		d.log.Warn("start tailer", "thread", th.ID, "err", err)
	}
	d.rename(th, conv) // the tab takes the topic's name
}

// introCard is the first message of a topic (design section 8.7).
func (d *daemon) introCard(th store.Thread, conv store.Conversation, title string) string {
	if title == "" {
		title = conv.NativeID
	}
	pane := ""
	if p, ok, err := d.st.PaneByConversation(d.ctx, conv.ID); err == nil && ok {
		pane = p.Label
		if pane == "" {
			pane = p.Block
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🔗 <b>Linked:</b> %s", agentHTML(conv.Runtime, conv.CWD))
	fmt.Fprintf(&b, "\nSession: %s", format.Escape(noAutoLink(format.Truncate(format.Redact(title), 100))))
	if pane != "" {
		fmt.Fprintf(&b, "\nPane: %s", format.Escape(pane))
	}
	if conv.CWD != "" {
		fmt.Fprintf(&b, "\nFolder: <code>%s</code>", format.Escape(d.tildePath(conv.CWD)))
	}
	input := "waits until idle"
	if d.cfg.InputSettings.WhileBusy == "steer" {
		input = "joins the running turn"
	}
	fmt.Fprintf(&b, "\nMode: %s · Input: %s", format.Escape(th.Mode), input)
	b.WriteString("\nWrite here to type into the pane. /help lists the commands.")
	return b.String()
}
