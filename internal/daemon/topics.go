package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"rex-tg/internal/agents"
	"rex-tg/internal/format"
	"rex-tg/internal/registry"
	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
	"rex-tg/internal/turn"
)

const maxTopicName = 128

// topicName builds "<runtime> · <project folder> · <title>", at most 128
// characters.
func topicName(rt agents.Runtime, cwd, title string) string {
	parts := []string{string(rt)}
	if cwd != "" {
		if b := filepath.Base(cwd); b != "." && b != "/" && b != "" {
			parts = append(parts, b)
		}
	}
	if t := strings.TrimSpace(title); t != "" {
		parts = append(parts, t)
	}
	return format.Truncate(strings.Join(parts, " · "), maxTopicName)
}

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
				d.notice(th.ID, fmt.Sprintf("live:%d:%d", th.ID, n), "🟢 agent is running again")
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

// rename gives a topic the name of its new session.
func (d *daemon) rename(th store.Thread, conv store.Conversation) {
	name := topicName(conv.Runtime, conv.CWD, d.convTitle(conv))
	if name == th.Name {
		return
	}
	if err := d.st.SetThreadName(d.ctx, th.ID, name); err != nil {
		d.log.Warn("rename topic", "err", err)
		return
	}
	d.spawn(func() {
		err := d.snd.Call(sender.PriorityEdit, func(ctx context.Context, tg *telegram.Client) error {
			return tg.EditForumTopic(ctx, th.ChatID, th.MessageThreadID, name)
		})
		if err != nil && d.ctx.Err() == nil {
			d.log.Warn("editForumTopic", "err", err)
		}
	})
}

func (d *daemon) topicOffline(c registry.Change) {
	th, err := d.st.Thread(d.ctx, c.ThreadID)
	if err != nil || th.Status == store.ThreadClosed {
		return
	}
	n := d.nextN(fmt.Sprintf("offn:%d", th.ID))
	d.notice(th.ID, fmt.Sprintf("offline:%d:%d", th.ID, n), "🔴 agent stopped")
	d.stopTailer(c.ConversationID, true)
	d.closeAlerts(th, store.AlertGone)
	d.dirty[th.ID] = true
}

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
		d.closeAlerts(th, store.AlertGone)
	}
}

// openTopic creates the Telegram topic for the session in a pane (flow 6).
// The `o:<pane id>` button calls it.
func (d *daemon) openTopic(cbID string, paneID int64) {
	if d.opening[paneID] {
		d.answerCallback(cbID, "Opening…", false)
		return
	}
	d.opening[paneID] = true
	chat := d.ownerChat
	d.spawn(func() {
		defer d.emit(evOpenDone{paneID})
		ctx := d.ctx
		views, err := d.reg.Agents(ctx)
		if err != nil {
			d.answerCallback(cbID, "Something went wrong. Try /agents again.", true)
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
			d.answerCallback(cbID, "That pane is gone. Use /agents again.", true)
			return
		case v.Conversation == nil:
			d.answerCallback(cbID, "Waiting for the session file… It resolves at the agent's next prompt or turn end.", true)
			return
		case v.Thread != nil && v.Thread.Status != store.ThreadClosed:
			d.answerCallback(cbID, "This session already has a topic.", false)
			d.say(chat, 0, "↗ "+format.Escape(v.Thread.Name), linkMarkup("Open topic", d.topicLink(v.Thread.MessageThreadID)))
			return
		}
		conv := *v.Conversation
		name := topicName(conv.Runtime, conv.CWD, d.convTitle(conv))
		var ft telegram.ForumTopic
		err = d.snd.Call(sender.PriorityAlert, func(ctx context.Context, tg *telegram.Client) error {
			t, err := tg.CreateForumTopic(ctx, chat, name)
			ft = t
			return err
		})
		if err != nil {
			d.log.Warn("createForumTopic", "err", err)
			d.answerCallback(cbID, "Could not create the topic. Is Threaded Mode on for the bot?", true)
			return
		}
		d.emit(evTopicCreated{chatID: chat, threadID: ft.MessageThreadID, name: name, convID: conv.ID})
		d.answerCallback(cbID, "Topic created", false)
		d.say(chat, 0, "🔗 Opened "+format.Escape(name), linkMarkup("Open topic", d.topicLink(ft.MessageThreadID)))
	})
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
	d.tracker(th)
	d.dirty[th.ID] = true
	d.enqueue(store.OutboxItem{
		ThreadID: th.ID, Key: fmt.Sprintf("intro:%d", th.ID), Kind: "notice", Silent: true,
		Parts: []store.PartContent{{Content: d.introCard(th, conv), Format: store.FormatHTML}},
	})
	if err := d.startTailer(conv, th.ID, registry.TailStart{AtEnd: true, Backfill: true}); err != nil {
		d.log.Warn("start tailer", "thread", th.ID, "err", err)
	}
}

// introCard is the first message of a topic (design section 8.7).
func (d *daemon) introCard(th store.Thread, conv store.Conversation) string {
	title := d.convTitle(conv)
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
	fmt.Fprintf(&b, "🔗 <b>Linked:</b> %s", format.Escape(topicName(conv.Runtime, conv.CWD, "")))
	fmt.Fprintf(&b, "\nSession: %s", format.Escape(format.Truncate(title, 100)))
	if pane != "" {
		fmt.Fprintf(&b, "\nPane: %s", format.Escape(pane))
	}
	if conv.CWD != "" {
		fmt.Fprintf(&b, "\nFolder: %s", format.Escape(d.tildePath(conv.CWD)))
	}
	fmt.Fprintf(&b, "\nMode: %s · Input: waits until idle", format.Escape(th.Mode))
	b.WriteString("\nWrite here to type into the pane. /help lists the commands.")
	return b.String()
}
