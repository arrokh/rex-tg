package inbound

import "strings"

// MaxQuoteRunes is the longest quote that WithReply types with a reply.
const MaxQuoteRunes = 600

// TakesQuote tells whether a message whose text is lead may carry a reply's
// quote. An agent command (/compact) would take the quote as its argument,
// and a shell command (!ls, in Claude Code and pi) would run the quote's
// lines as shell, with each "> …" a redirect.
func TakesQuote(lead string) bool {
	l := strings.TrimSpace(lead)
	return !strings.HasPrefix(l, "/") && !strings.HasPrefix(l, "!")
}

// WithReply adds the message that a Telegram reply answers to the text for
// the agent: a blank line, "In reply to:", and the quoted lines as a Markdown
// quote. The text comes first, so the start of the prompt stays what the user
// wrote (the echo check compares it). A quote longer than MaxQuoteRunes is
// cut with "…". An empty quote changes nothing.
func WithReply(text, quoted string) string {
	q := Normalize(quoted)
	if q == "" {
		return text
	}
	if r := []rune(q); len(r) > MaxQuoteRunes {
		q = strings.TrimRight(string(r[:MaxQuoteRunes-1]), " \n") + "…"
	}
	lines := strings.Split(q, "\n")
	for i, l := range lines {
		if l == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + l
		}
	}
	return text + "\n\nIn reply to:\n" + strings.Join(lines, "\n")
}
