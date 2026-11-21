package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"rex-tg/internal/format"
	"rex-tg/internal/registry"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// attachCommand offers resolved Rex sessions that do not have an open topic.
// It works in unlinked topics and in topics whose local link has stopped.
func (d *daemon) attachCommand(chatID, threadID int64) {
	d.attachCommandPage(chatID, threadID, 0)
}

func (d *daemon) attachCommandPage(chatID, threadID int64, offset int) {
	if threadID == 0 {
		d.say(chatID, 0, "Use /attach inside a Telegram topic.", nil)
		return
	}
	if th, ok, err := d.st.ThreadByTopic(d.ctx, chatID, threadID); err == nil && ok {
		if th.Status == store.ThreadLive {
			d.say(chatID, threadID, "This topic is already linked. Use /unlink before attaching a different agent.", nil)
			return
		}
		if d.topicTerminating(th.ID) {
			d.say(chatID, threadID, "This topic is still closing. Try /attach again in a moment.", nil)
			return
		}
	}
	d.spawn(func() {
		ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
		defer cancel()
		if err := d.scanAndWait(ctx); err != nil {
			d.say(chatID, threadID, "Could not refresh the Rex agents. Try /attach again.", nil)
			return
		}
		views, err := d.reg.Agents(ctx)
		if err != nil {
			d.log.Warn("attach: list agents", "err", err)
			d.say(chatID, threadID, "Could not list the Rex agents.", nil)
			return
		}
		views = attachableViews(views)
		if len(views) == 0 {
			d.say(chatID, threadID, "No unattached coding agent sessions are available.", nil)
			return
		}
		text, markup := renderAttachable(views, offset)
		d.sendNow(chatID, threadID, text, markup)
	})
}

func attachableViews(views []registry.AgentView) []registry.AgentView {
	views = slices.Clone(views)
	views = slices.DeleteFunc(views, func(v registry.AgentView) bool {
		return v.Conversation == nil || v.Thread != nil || v.Pane.ConversationID == 0 || !v.Pane.GoneAt.IsZero()
	})
	slices.SortStableFunc(views, func(a, b registry.AgentView) int {
		return stateRank(a.Pane.State) - stateRank(b.Pane.State)
	})
	return views
}

const attachPageSize = 18

func renderAttachable(views []registry.AgentView, offset int) (string, *telegram.InlineKeyboardMarkup) {
	if offset < 0 || offset >= len(views) {
		offset = 0
	}
	end := min(offset+attachPageSize, len(views))
	var b strings.Builder
	fmt.Fprintf(&b, "<b>Attach an agent (%d available)</b>\nChoose a Rex agent for this topic. Showing %d–%d.", len(views), offset+1, end)
	rows := make([][]telegram.InlineKeyboardButton, 0, end-offset+1)
	for _, v := range views[offset:end] {
		conv := v.Conversation
		name := topicName(v.Pane.Label, v.Runtime, conv.CWD, conv.Title)
		emoji, state := stateEmoji(v.Pane.State)
		info := format.Escape(string(v.Runtime))
		if folder := folderBase(conv.CWD); folder != "" {
			info += " · <code>" + format.Escape(format.Truncate(folder, 36)) + "</code>"
		}
		fmt.Fprintf(&b, "\n%s <b>%s</b> · %s · %s", emoji, esc(noAutoLink(name)), state, info)
		rows = append(rows, []telegram.InlineKeyboardButton{{Text: "Attach " + name, CallbackData: fmt.Sprintf("at:%d", v.Pane.ID)}})
	}
	var nav []telegram.InlineKeyboardButton
	if offset > 0 {
		nav = append(nav, telegram.InlineKeyboardButton{Text: "‹ Previous", CallbackData: fmt.Sprintf("ap:%d", max(0, offset-attachPageSize))})
	}
	if end < len(views) {
		nav = append(nav, telegram.InlineKeyboardButton{Text: "Next ›", CallbackData: fmt.Sprintf("ap:%d", end)})
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	return b.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// attachAgentButton rechecks the selected pane after the user taps its button.
func (d *daemon) attachAgentButton(updateID int64, cb *telegram.CallbackQuery, paneID int64) {
	if cb.Message.MessageThreadID == 0 {
		d.answerCallback(cb.ID, "Use this button inside a Telegram topic.", true)
		return
	}
	if th, ok, err := d.st.ThreadByTopic(d.ctx, cb.Message.Chat.ID, cb.Message.MessageThreadID); err == nil && ok {
		if th.Status == store.ThreadLive {
			d.answerCallback(cb.ID, "This topic is already linked. Unlink it first.", true)
			return
		}
		if d.topicTerminating(th.ID) {
			d.answerCallback(cb.ID, "This topic is still closing. Try again in a moment.", true)
			return
		}
	}
	d.answerCallback(cb.ID, "Checking agent…", false)
	chatID, threadID := cb.Message.Chat.ID, cb.Message.MessageThreadID
	d.spawn(func() {
		ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
		defer cancel()
		if err := d.scanAndWait(ctx); err != nil {
			d.say(chatID, threadID, "Could not refresh the Rex agents. Run /attach again.", nil)
			return
		}
		views, err := d.reg.Agents(ctx)
		if err != nil {
			d.log.Warn("attach: refresh agents", "err", err)
			d.say(chatID, threadID, "Could not refresh the Rex agents. Run /attach again.", nil)
			return
		}
		var chosen *registry.AgentView
		for i := range views {
			v := &views[i]
			if v.Pane.ID == paneID && v.Conversation != nil && v.Thread == nil && v.Pane.ConversationID == v.Conversation.ID {
				chosen = v
				break
			}
		}
		if chosen == nil {
			d.say(chatID, threadID, "That agent is no longer available or is already attached. Run /attach again.", nil)
			return
		}
		d.emit(evAttachAgent{updateID: updateID, chatID: chatID, threadID: threadID, paneID: paneID, convID: chosen.Conversation.ID})
	})
}

// onAttachAgent performs the local link on the core loop, where topic state is
// serialized with registry and sender changes.
func (d *daemon) onAttachAgent(e evAttachAgent) {
	if th, ok, err := d.st.ThreadByTopic(d.ctx, e.chatID, e.threadID); err != nil {
		d.log.Warn("attach: find topic", "err", err)
		return
	} else if ok {
		if th.Status == store.ThreadLive {
			d.say(e.chatID, e.threadID, "This topic is already linked. Use /unlink first.", nil)
			return
		}
		if d.topicTerminating(th.ID) {
			d.say(e.chatID, e.threadID, "This topic is still closing. Try /attach again in a moment.", nil)
			return
		}
	}
	conv, err := d.st.Conversation(d.ctx, e.convID)
	if err != nil {
		d.say(e.chatID, e.threadID, "That agent session is no longer available. Run /attach again.", nil)
		return
	}
	name := topicName("", conv.Runtime, conv.CWD, d.convTitle(conv))
	th, err := d.st.AttachToTopic(d.ctx, e.paneID, e.chatID, e.threadID, conv.ID, name, d.now())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrTopicActive):
			d.say(e.chatID, e.threadID, "This topic is already linked. Use /unlink first.", nil)
		case errors.Is(err, store.ErrTopicTaken):
			d.say(e.chatID, e.threadID, "That agent is already attached to another topic.", nil)
		case errors.Is(err, store.ErrAgentNotRunning):
			d.say(e.chatID, e.threadID, "That agent stopped before it could be attached. Run /attach again.", nil)
		case errors.Is(err, store.ErrTopicOutboxSending):
			d.say(e.chatID, e.threadID, "This topic still has a message in flight. Wait a moment, then run /attach again.", nil)
		default:
			d.log.Warn("attach: save topic link", "thread", e.threadID, "err", err)
			d.say(e.chatID, e.threadID, "Could not attach that agent. Run /attach again.", nil)
		}
		return
	}
	d.closeDraft(th.ID)
	delete(d.trackers, th.ID)
	delete(d.snaps, th.ID)
	if err := d.st.Delete(d.ctx, fmt.Sprintf("turn:%d", th.ID)); err != nil {
		d.log.Warn("attach: reset turn", "thread", th.ID, "err", err)
	}
	d.clearTopicClosePending(th.ID)
	if m := d.cfg.Display.Mode; m != "" && m != th.Mode {
		if err := d.st.SetThreadSettings(d.ctx, th.ID, m, th.Verbose, th.Reasoning, th.Muted); err != nil {
			d.log.Warn("apply display mode to attached topic", "thread", th.ID, "err", err)
		} else {
			th.Mode = m
		}
	}
	if !d.enqueue(store.OutboxItem{
		ThreadID: th.ID, Key: fmt.Sprintf("attach:%d:%d", th.ID, e.updateID), Kind: "notice", Silent: true,
		Parts: []store.PartContent{{Content: d.introCard(th, conv, ""), Format: store.FormatHTML}},
	}) {
		d.log.Warn("attach: enqueue confirmation", "thread", th.ID)
	}
	d.attach(registry.Change{
		Kind: registry.Resume, ThreadID: th.ID, ConversationID: conv.ID,
		Start: registry.TailStart{AtEnd: true, Backfill: true},
	})
	d.rename(th, conv)
	d.boardDirty()
}
