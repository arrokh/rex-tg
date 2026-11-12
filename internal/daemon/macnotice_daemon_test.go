package daemon_test

import (
	"rex-tg/internal/telegram/tgtest"
	"strings"
	"testing"
	"time"
)

// macHoldTopic is a pi topic with the hold of Mac turns on.
func macHoldTopic(t *testing.T, id string) (*env, *piLog, int64) {
	t.Helper()
	return piTopicCfg(t, id, func(e *env) { e.cfg.Display.MacNotice = true })
}

func TestMacTurnsShowOneNoticeUntilTelegramWrites(t *testing.T) {
	e, p, thread := macHoldTopic(t, "pi-mac-hold")
	before := len(e.botMessages(thread))

	// Two turns typed on the Mac: one silent notice, edited, and nothing else.
	p.User("fix the login page")
	n := e.waitText(thread, "In use on the Mac")
	if !n.Silent || !strings.Contains(n.Text, "fix the login page") {
		t.Errorf("notice = %+v, want a silent notice with the prompt", n)
	}
	p.ToolCall("c1", "ls")
	p.ToolResult("c1")
	p.Final("fixed the login page")
	e.waitFor("the notice says done", func() bool {
		m, _ := e.message(n.ID)
		return strings.Contains(m.Text, "done") && m.Markup != nil
	})
	p.User("now the signup page")
	e.waitFor("the notice shows the second prompt", func() bool {
		m, _ := e.message(n.ID)
		return strings.Contains(m.Text, "now the signup page")
	})
	p.Final("fixed the signup page")
	e.waitFor("the notice says done again", func() bool {
		m, _ := e.message(n.ID)
		return strings.Contains(m.Text, "done")
	})
	e.never("a post of the Mac turns", 400*time.Millisecond, func() bool {
		return len(e.botMessages(thread)) != before+1 || e.hasText(thread, "You (on Mac)") || e.hasText(thread, "fixed the")
	})

	// "📄 Last answer" posts the newest held answer, once.
	m, _ := e.message(n.ID)
	e.pressButton(m, m.Markup.InlineKeyboard[0][0].CallbackData)
	e.waitText(thread, "fixed the signup page")
	e.pressButton(m, m.Markup.InlineKeyboard[0][0].CallbackData)
	e.never("a second copy", 400*time.Millisecond, func() bool { return e.countText(thread, "fixed the signup page") > 1 })

	// A message from Telegram ends the hold: its turn is shown as usual.
	e.say(thread, "and the reset page?")
	e.waitFor("the paste", func() bool { return len(e.pastes()) == 1 })
	p.User("and the reset page?")
	p.Final("reset page done")
	e.waitText(thread, "reset page done")

	// Back at the Mac: a new notice at the bottom of the topic, without the
	// answer of the old hold behind a button.
	p.User("one more on the mac")
	e.waitFor("a second notice", func() bool { return e.countText(thread, "In use on the Mac") == 2 })
	var n2 tgtest.Message
	for _, m := range e.botMessages(thread) {
		if strings.Contains(m.Text, "In use on the Mac") {
			n2 = m
		}
	}
	if n2.Markup != nil {
		t.Errorf("the new notice offers the old hold's answer: %+v", n2.Markup)
	}
	e.waitFor("the old notice loses its button", func() bool { m, _ := e.message(n.ID); return m.Markup == nil })
}

// A Telegram message whose prompt line does not match the typed text (the
// agent changed it) must not be held: its answer goes to Telegram.
func TestTypedMessageIsNotHeldWhenItsEchoIsMissed(t *testing.T) {
	e, p, thread := macHoldTopic(t, "pi-mac-miss")
	p.User("on the mac")
	e.waitText(thread, "In use on the Mac")
	p.Final("mac answer")

	e.say(thread, "check the logs")
	e.waitFor("the paste", func() bool { return len(e.pastes()) == 1 })
	p.User("[expanded by the agent] something else entirely")
	p.Final("the logs are fine")
	e.waitText(thread, "the logs are fine")
}
