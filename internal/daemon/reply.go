package daemon

import (
	"strings"

	"rex-tg/internal/format"
	"rex-tg/internal/inbound"
	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// A reply in a topic carries the message it answers to the agent, so that
// "do this one" says which one (design section 8.8). Only replies to what the
// agent said, or to the user's own messages, count: rex-tg's notices, alerts,
// and cards mean nothing to the agent.

// quotedKinds are the outbox kinds that hold the agent's words, or prompts
// typed on the Mac.
var quotedKinds = map[string]bool{
	"answer": true, "block": true, "backfill": true, "reasoning": true,
	"tool": true, "output": true, "image": true, "prompt": true,
}

// replyQuote returns the text that a message in a topic replies to, for the
// agent: the part the user quoted, else the replied message's text or
// caption (core). It is empty when the message replies to nothing that
// counts. Telegram may set reply_to_message to a topic's first message on
// every message in the topic; that is no reply.
func (d *daemon) replyQuote(m *telegram.Message, th store.Thread) string {
	rm := m.ReplyToMessage
	if rm == nil || rm.ForumTopicCreated != nil || (m.MessageThreadID != 0 && rm.MessageID == m.MessageThreadID) {
		return ""
	}
	// One of rex-tg's own messages: the store knows its kind and its text.
	// A legacy rich message may reach the bot without text in a reply, and
	// maybe without a sender (seen live), so the store's copy is the quote.
	kind, sent, err := d.st.SentKind(d.ctx, th.ID, rm.MessageID)
	if err != nil {
		d.log.Warn("reply: sent kind", "thread", th.ID, "err", err)
	}
	switch {
	case sent && !quotedKinds[kind]:
		return ""
	case sent:
	case rm.From == nil || rm.From.IsBot || !d.fromOwner(rm.From, m.Chat.ID):
		return "" // not rex-tg's, and not the owner's own message
	}
	if m.Quote != nil && strings.TrimSpace(m.Quote.Text) != "" {
		return m.Quote.Text
	}
	if rm.Text != "" {
		return rm.Text
	}
	if rm.Caption != "" {
		return rm.Caption
	}
	if sent {
		return d.sentText(th.ID, rm.MessageID)
	}
	return ""
}

// sentText is the text of a message that rex-tg sent, from its stored part:
// Markdown (a rich message, or an answer that went as a .md file) as plain
// text, HTML without its tags, a code file as it is. An image has none.
// inbound.WithReply cuts the quote to its start.
func (d *daemon) sentText(threadID, msg int64) string {
	content, f, ok, err := d.st.SentContent(d.ctx, threadID, msg)
	if err != nil || !ok {
		return ""
	}
	switch f {
	case store.FormatRich:
		return format.Plain(content)
	case store.FormatHTML:
		return sender.PlainFromHTML(content)
	case store.FormatPlain:
		return content
	case store.FormatDocument:
		if name, _, _ := d.st.SentFile(d.ctx, threadID, msg); strings.HasSuffix(strings.ToLower(name), ".md") {
			return format.Plain(content)
		}
		return content
	}
	return "" // an image: its content is a file path
}

// agentQuote returns the quote to type with a message whose text is lead:
// none for an agent or shell command (inbound.TakesQuote).
func agentQuote(lead, quoted string) string {
	if !inbound.TakesQuote(lead) {
		return ""
	}
	return quoted
}
