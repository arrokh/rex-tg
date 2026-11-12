package daemon_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/telegram/tgtest"
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

// waitHeldTurnSaved waits until a held turn ended: its notice says done and
// has the "📄 Last answer" button, and the tracker's state is saved with the
// lines read (a stop before that reads the lines again after the start).
func waitHeldTurnSaved(e *env, notice int64) {
	e.t.Helper()
	e.waitFor("the notice says done, with its button", func() bool {
		m, _ := e.message(notice)
		return strings.Contains(m.Text, "done") && m.Markup != nil
	})
	e.waitFor("the ended turn saved", func() bool {
		st := e.peek()
		keys, _ := st.KeysWithPrefix(context.Background(), "turn:")
		for _, k := range keys {
			if v, ok, _ := st.Get(context.Background(), k); ok && strings.Contains(v, `"phase":"ended"`) && strings.Contains(v, `"on_mac":true`) {
				return true
			}
		}
		return false
	})
}

// A restart during a hold keeps the notice. The Telegram message that ends
// the hold then removes its "📄 Last answer" button, though the new daemon
// never edited the notice.
func TestMacNoticeLosesItsButtonAfterARestart(t *testing.T) {
	e, p, thread := macHoldTopic(t, "pi-mac-restart")
	p.User("fix the login page")
	n := e.waitText(thread, "In use on the Mac")
	p.Final("fixed the login page")
	waitHeldTurnSaved(e, n.ID)
	e.stop()
	e.start()

	e.say(thread, "and the reset page?")
	e.waitFor("the paste", func() bool { return len(e.pastes()) == 1 })
	p.User("and the reset page?")
	p.Final("reset page done")
	e.waitText(thread, "reset page done")
	e.waitFor("the old notice loses its button", func() bool { m, _ := e.message(n.ID); return m.Markup == nil })
}

// Turning MacNotice off and restarting removes the button of a notice from
// before, and with it the held answer.
func TestMacNoticeOffAfterARestartClearsTheHold(t *testing.T) {
	e, p, thread := macHoldTopic(t, "pi-mac-off")
	p.User("fix the login page")
	n := e.waitText(thread, "In use on the Mac")
	p.Final("fixed the login page")
	waitHeldTurnSaved(e, n.ID)
	old, _ := e.message(n.ID)
	e.stop()
	e.cfg.Display.MacNotice = false
	e.start()

	e.waitFor("the notice loses its button", func() bool { m, _ := e.message(n.ID); return m.Markup == nil })
	e.pressButton(old, old.Markup.InlineKeyboard[0][0].CallbackData) // a tap that was on its way
	e.waitFor("the refusal", func() bool {
		for _, s := range e.callbackTexts() {
			if strings.Contains(s, "No answer") {
				return true
			}
		}
		return false
	})
	e.never("the old answer posted", 400*time.Millisecond, func() bool { return e.hasText(thread, "fixed the login page") })
}
