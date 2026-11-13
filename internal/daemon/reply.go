package daemon

import (
	"strings"

	"rex-tg/internal/inbound"
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
	if rm.From == nil {
		return ""
	}
	if rm.From.IsBot {
		kind, ok, err := d.st.SentKind(d.ctx, th.ID, rm.MessageID)
		if err != nil {
			d.log.Warn("reply: sent kind", "thread", th.ID, "err", err)
		}
		if !ok || !quotedKinds[kind] {
			return ""
		}
	} else if !d.fromOwner(rm.From, m.Chat.ID) {
		return ""
	}
	if m.Quote != nil && strings.TrimSpace(m.Quote.Text) != "" {
		return m.Quote.Text
	}
	if rm.Text != "" {
		return rm.Text
	}
	return rm.Caption
}

// agentQuote returns the quote to type with a message whose text is lead:
// none for an agent or shell command (inbound.TakesQuote).
func agentQuote(lead, quoted string) string {
	if !inbound.TakesQuote(lead) {
		return ""
	}
	return quoted
}
