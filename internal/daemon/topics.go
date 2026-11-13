package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// threadOf returns the open topic whose current session is conv.
func (d *daemon) threadOf(conv int64) (store.Thread, bool) {
	th, ok, err := d.st.ThreadByConversation(d.ctx, conv)
	if err != nil || !ok || th.CurrentConversationID != conv || th.Status == store.ThreadClosed {
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
			d.attach(c)
		case registry.TopicOffline:
			d.topicOffline(c)
		case registry.TopicLive:
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
func (d *daemon) desiredName(conv store.Conversation) string {
	label := ""
	if p, ok, err := d.st.PaneByConversation(d.ctx, conv.ID); err == nil && ok {
		label = p.Label
	}
	return topicName(label, conv.Runtime, conv.CWD, d.convTitle(conv))
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
			name = statusName(th, strings.TrimPrefix(th.Name, offlineMark))
		} else {
			name = d.desiredName(conv)
		}
		if name == th.Name {
			d.syncTab(th, conv) // the tab may differ although the topic does not
			return
		}
		err := d.snd.Call(sender.PriorityEdit, func(ctx context.Context, tg *telegram.Client) error {
			return tg.EditForumTopic(ctx, th.ChatID, th.MessageThreadID, name)
		})
		switch telegram.Classify(err) {
		case telegram.ClassNone, telegram.ClassNotModified:
			if err := d.st.SetThreadName(d.ctx, th.ID, name); err != nil {
				d.log.Warn("rename topic", "err", err)
			}
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
	sessions, err := d.tabSessionList()
	if err != nil {
		d.log.Warn("rename tab: list sessions", "err", err)
		return
	}
	i := slices.IndexFunc(sessions, func(s rex.Session) bool { return s.ID == p.Session })
	if i < 0 || !strings.EqualFold(strings.TrimSpace(sessions[i].Label), want) {
		return
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
		label = uniqueTabName(tabName(p.Label, conv.Runtime, conv.CWD, d.convTitle(conv), current), others)
	}
	if label == "" || label == current {
		return
	}
	if err := d.rx.RenameWindow(d.ctx, p.Session, window, label); err != nil {
		d.log.Warn("rename tab", "window", window, "err", err)
	}
}

// topicOffline shows in the topic that its agent stopped: a notice that says
// why, the end of a turn that was running (no more "typing…"), and a 🔴 in
// front of the topic's name until the agent runs again.
func (d *daemon) topicOffline(c registry.Change) {
	th, err := d.st.Thread(d.ctx, c.ThreadID)
	if err != nil || th.Status == store.ThreadClosed {
		return
	}
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
	if cerr == nil {
		d.rename(th, conv)
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
	return "🔴 " + format.Escape(name) + " stopped" + why + ".\nMessages here are not sent now. If you resume the session, this topic continues."
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

	if c.State == "blocked" && (c.StateKind == store.AlertPermission || c.StateKind == store.AlertQuestion) {
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
		d.emit(evTopicCreated{chatID: chat, threadID: ft.MessageThreadID, name: name, convID: conv.ID, title: title})
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

// onTopicCreated links the new topic and posts the intro card and backfill.
func (d *daemon) onTopicCreated(e evTopicCreated) {
	ctx := d.ctx
	conv, err := d.st.Conversation(ctx, e.convID)
	if err != nil {
		d.log.Warn("link topic: conversation", "err", err)
		return
	}
	th, err := d.st.LinkTopic(ctx, e.chatID, e.threadID, conv.ID, e.name, d.now())
	if err != nil {
		d.log.Warn("link topic", "err", err)
		d.spawn(func() {
			_ = d.snd.Call(sender.PriorityEdit, func(ctx context.Context, tg *telegram.Client) error {
				return tg.CloseForumTopic(ctx, e.chatID, e.threadID)
			})
		})
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
	d.tracker(th)
	d.dirty[th.ID] = true
	d.enqueue(store.OutboxItem{
		ThreadID: th.ID, Key: fmt.Sprintf("intro:%d", th.ID), Kind: "notice", Silent: true,
		Parts: []store.PartContent{{Content: d.introCard(th, conv, e.title), Format: store.FormatHTML}},
	})
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
	fmt.Fprintf(&b, "\nMode: %s · Input: waits until idle", format.Escape(th.Mode))
	b.WriteString("\nWrite here to type into the pane. /help lists the commands.")
	return b.String()
}
