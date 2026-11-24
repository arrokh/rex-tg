package daemon_test

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A reply carries the message it answers to the agent: the agent's answer as
// a quote after the text, or only the part the user quoted. A reply to one of
// rex-tg's own messages sends only the text, and so do an agent command and
// a shell command (!ls). A reply to the user's own message quotes it too.
func TestReplyQuotesTheAnsweredMessage(t *testing.T) {
	e, p, thread := piTopic(t, "pi-reply")
	var sent []int64 // the user's messages, in order
	steps := []struct {
		name          string
		replyTo       func() int64
		quote, text   string
		want, answers string
	}{
		{"a reply to an answer", func() int64 { return e.waitText(thread, "ready").ID }, "",
			"do it again", "do it again\n\nIn reply to:\n> ready", "First point.\n\nSecond point."},
		{"a quote of part of an answer", func() int64 { return e.waitText(thread, "Second point.").ID }, "Second point.",
			"only this one", "only this one\n\nIn reply to:\n> Second point.", "on it"},
		{"a reply to a rex-tg message", func() int64 { return e.botMessages(thread)[0].ID }, "",
			"hello", "hello", "hi"},
		{"an agent command", func() int64 { return e.waitText(thread, "on it").ID }, "",
			"//compact", "/compact", "compacted"},
		{"a reply to the user's own message", func() int64 { return sent[0] }, "",
			"and this", "and this\n\nIn reply to:\n> do it again", "done"},
		{"a shell command", func() int64 { return e.waitText(thread, "done").ID }, "",
			"!ls", "!ls", "listed"},
		{"the topic's first message is no reply", func() int64 { return thread }, "",
			"plain words", "plain words", "fine"},
	}
	st := e.peek()
	th, ok, err := st.ThreadByTopic(context.Background(), chatID, thread)
	if err != nil || !ok {
		t.Fatalf("thread: %v %v", ok, err)
	}
	for i, s := range steps {
		to := s.replyTo()
		// A person replies seconds later; the test waits until the sender
		// has recorded the replied message's number, as it has by then.
		if m, _ := e.message(to); m.FromBot {
			e.waitFor(s.name+": the replied message recorded", func() bool {
				_, ok, _ := st.SentKind(context.Background(), th.ID, to)
				return ok || !strings.Contains(s.want, "In reply to")
			})
		}
		u := e.tg.PushReply(chatID, thread, ownerID, to, s.quote, s.text)
		sent = append(sent, u.Message.MessageID)
		e.waitFor(s.name+": the paste", func() bool { return len(e.pastes()) == i+1 })
		if got := e.pastes()[i]; got != s.want {
			t.Fatalf("%s: pasted %q, want %q", s.name, got, s.want)
		}
		e.waitFor(s.name+": Enter", func() bool { return len(e.keys()) == i+1 })
		// The agent takes the prompt (👍 shows that rex-tg saw it) and
		// answers, so the pane is idle again.
		// The agent's slash command does not write a user prompt. Its ack
		// confirms typing, not a new prompt event in the transcript.
		if !strings.HasPrefix(s.want, "/") {
			p.User(s.want)
		}
		e.waitFor(s.name+": 👍", func() bool { m, _ := e.message(u.Message.MessageID); return m.Reaction == "👍" })
		p.Final(s.answers)
		e.waitText(thread, strings.SplitN(s.answers, "\n", 2)[0])
	}
	for _, m := range e.tg.Messages(chatID) {
		if !m.FromBot && m.Reaction == "👎" {
			t.Errorf("message %q got 👎\n%s", m.Text, e.dump())
		}
	}
}

// A reply to a formatted answer quotes the plain text rex-tg sent.
func TestReplyToAFormattedAnswerQuotesIt(t *testing.T) {
	e, p, thread := piTopic(t, "pi-reply-formatted")
	p.User("plan it")
	time.Sleep(10 * time.Millisecond) // the answer comes after the prompt: the pane is idle then
	p.Final("My plan: **three PRs**, then a retest.")
	answer := e.waitText(thread, "My plan")
	st := e.peek()
	th, _, _ := st.ThreadByTopic(context.Background(), chatID, thread)
	e.waitFor("the answer recorded", func() bool { _, ok, _ := st.SentKind(context.Background(), th.ID, answer.ID); return ok })
	e.tg.PushReply(chatID, thread, ownerID, answer.ID, "", "agreed, as drafts")
	e.waitFor("the paste", func() bool { return len(e.pastes()) == 1 })
	if got, want := e.pastes()[0], "agreed, as drafts\n\nIn reply to:\n> My plan: three PRs, then a retest."; got != want {
		t.Fatalf("pasted %q, want %q", got, want)
	}
}

// The "may not have arrived" notice is rex-tg's check note, edited: it is
// no part of the answer. A reply to it quotes nothing, not the old answer.
func TestReplyToALostReplyNoticeQuotesNothing(t *testing.T) {
	e, p, thread := piTopic(t, "pi-reply-notice")
	e.tg.LoseAnswerNext("sendMessage", 1) // posted, but the answer is lost: the outcome is unknown
	p.User("report")
	time.Sleep(10 * time.Millisecond) // the answer comes after the prompt: the pane is idle then
	p.Final("the lost answer")
	notice := e.waitText(thread, "may not have arrived")
	e.tg.PushReply(chatID, thread, ownerID, notice.ID, "", "what was that?")
	e.waitFor("the paste", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; got != "what was that?" {
		t.Errorf("pasted %q, want the text alone", got)
	}
}
