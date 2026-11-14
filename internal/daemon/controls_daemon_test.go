package daemon_test

import (
	"context"
	"fmt"
	"rex-tg/internal/telegram/tgtest"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/agents/claude"
	"rex-tg/internal/agents/codex"
	"rex-tg/internal/agents/pi"
	"rex-tg/internal/inbound"
)

// controlsTopic is a pi topic with short busy time (so several commands can
// follow each other) and two model buttons from the config.
func controlsTopic(t *testing.T, id string) (*env, *piLog, int64) {
	t.Helper()
	return piTopicCfg(t, id, func(e *env) {
		e.cfg.InputSettings.BusyAfterTyping.Duration = 50 * time.Millisecond
		e.cfg.Agents.Pi.Choices = map[string][]string{"model": {"alpha", "beta-1"}}
	})
}

func TestModelCardTapTypesTheCommandOnce(t *testing.T) {
	e, p, thread := controlsTopic(t, "pi-model-card")
	p.write(fmt.Sprintf(`{"type":"model_change","id":"m1","timestamp":%q,"provider":"anthropic","modelId":"test-model"}`, now()))

	e.say(thread, "/model")
	card := e.waitText(thread, "Tap a value")
	if !strings.Contains(card.Text, "anthropic/test-model") {
		t.Errorf("the card does not show the current model: %q", card.Text)
	}
	data := buttonData(card, "beta-1")
	if !strings.HasPrefix(data, "cf:model:1:") || len(data) > 64 {
		t.Fatalf("button data = %q", data)
	}

	e.pressButton(card, data)
	e.pressButton(card, data) // a quick second tap on the same card
	e.waitFor("the command to be pasted", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; got != "/model beta-1" {
		t.Errorf("pasted %q", got)
	}
	e.waitFor("Enter", func() bool { return len(e.keys()) == 1 })
	e.waitText(thread, "Sending")
	e.never("a second paste", 600*time.Millisecond, func() bool { return len(e.pastes()) > 1 })

	e.waitFor("the second tap refused", func() bool {
		for _, s := range e.callbackTexts() {
			if strings.Contains(s, "used already") {
				return true
			}
		}
		return false
	})

	// On a new card, a button whose choice changed is refused, and nothing
	// is typed.
	before := e.countText(thread, "Tap a value")
	e.say(thread, "/model")
	e.waitFor("a new card", func() bool { return e.countText(thread, "Tap a value") == before+1 })
	var card2 tgtest.Message
	for _, m := range e.botMessages(thread) {
		if strings.Contains(m.Text, "Tap a value") {
			card2 = m
		}
	}
	e.pressButton(card2, "cf:model:1:0000")
	e.waitFor("the out-of-date answer", func() bool {
		for _, s := range e.callbackTexts() {
			if strings.Contains(s, "out of date") {
				return true
			}
		}
		return false
	})
	e.never("typing after a stale button", 400*time.Millisecond, func() bool { return len(e.pastes()) > 1 })
}

func TestEffortCommandIsTypedAndChecked(t *testing.T) {
	e, p, thread := controlsTopic(t, "pi-effort")
	cmd := e.say(thread, "/effort high")
	e.waitFor("the thinking command", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; got != "/thinking high" {
		t.Errorf("pasted %q", got)
	}
	// A slash command writes no prompt: its 👍 comes once it is typed.
	e.waitFor("the 👍 of the typed command", func() bool { m, _ := e.message(cmd); return m.Reaction == "👍" })

	// Values are checked before anything is typed.
	e.say(thread, "/effort bogus")
	e.waitText(thread, "Use: off, minimal")
	e.say(thread, "/model two words")
	e.waitText(thread, "is not a valid model")
	e.never("typing a bad value", 400*time.Millisecond, func() bool { return len(e.pastes()) > 1 })

	// The card of a control with fixed values has buttons, and /info shows
	// the values that the transcript gave.
	p.write(fmt.Sprintf(`{"type":"thinking_level_change","id":"t1","timestamp":%q,"thinkingLevel":"medium"}`, now()))
	e.waitFor("the level to be seen", func() bool { return len(e.controlsSeen()) > 0 })
	e.say(thread, "/info")
	e.waitText(thread, "Thinking level: <code>medium</code>")

	// //model still goes to the agent unchanged. Like any slash command
	// typed for the agent (/restart, /compact), it gets its 👍 once typed:
	// no prompt line would ever confirm it.
	own := e.say(thread, "//model x")
	e.waitFor("the agent's own command", func() bool { return len(e.pastes()) == 2 })
	if got := e.pastes()[1]; got != "/model x" {
		t.Errorf("pasted %q, want /model x", got)
	}
	e.waitFor("the 👍 of the agent's own command", func() bool { m, _ := e.message(own); return m.Reaction == "👍" })

	// A command that rex-tg does not know (Claude Code's /restart) goes to
	// the agent as it is, gets its 👍 once typed, and its row stays
	// delivered: no transcript line confirms it.
	restart := e.say(thread, "/restart")
	e.waitFor("the restart command", func() bool { return len(e.pastes()) == 3 })
	if got := e.pastes()[2]; got != "/restart" {
		t.Errorf("pasted %q, want /restart", got)
	}
	e.waitFor("the 👍 of /restart", func() bool { m, _ := e.message(restart); return m.Reaction == "👍" })
	th, ok, err := e.peek().ThreadByTopic(context.Background(), chatID, thread)
	if err != nil || !ok {
		t.Fatalf("topic: %v %v", ok, err)
	}
	rows, err := e.peek().DeliveredSince(context.Background(), th.ID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		found = found || r.Body == "/restart"
	}
	if !found {
		t.Errorf("the /restart row is not delivered: %+v", rows)
	}
}

// controlsSeen returns the stored setting values of the pane's session (the
// "ctl:<conversation>" row of the key-value table), or "" when there are none.
func (e *env) controlsSeen() string {
	st := e.peek()
	p, ok, err := st.PaneByBlock(context.Background(), "local", rexBlock)
	if err != nil || !ok {
		return ""
	}
	v, _, _ := st.Get(context.Background(), fmt.Sprintf("ctl:%d", p.ConversationID))
	return v
}

// Every control of every agent must be a command that the router knows, or
// the command would be typed into the agent as text.
func TestEveryControlIsABotCommand(t *testing.T) {
	for _, a := range []agents.Adapter{pi.New(), claude.New(), codex.New()} {
		c, ok := a.(agents.Controller)
		if !ok {
			t.Errorf("%s has no controls", a.Runtime())
			continue
		}
		for _, ctl := range c.Controls() {
			if r := inbound.RouteText("/"+ctl.Name, true, ""); r.Kind != inbound.RouteCommand || r.Command != ctl.Name {
				t.Errorf("%s: /%s is not routed as a command: %+v", a.Runtime(), ctl.Name, r)
			}
		}
	}
}

// A control command that waits for a busy agent survives a restart of the
// daemon: once typed, its value counts, and its record goes.
func TestQueuedControlSurvivesARestart(t *testing.T) {
	e, p, thread := controlsTopic(t, "pi-ctl-restart")
	p.User("busy work")
	p.ToolCall("c1", "sleep 5")
	e.waitFor("the progress message", func() bool { return e.hasText(thread, "⏱") })
	cmd := e.say(thread, "/effort high")
	e.waitText(thread, "Waiting until the agent is idle")
	e.waitFor("the pending command kept", func() bool {
		keys, _ := e.peek().KeysWithPrefix(context.Background(), "ctl_pending:")
		return len(keys) == 1
	})
	e.stop()
	e.start()
	p.ToolResult("c1")
	p.Final("done with the work")
	e.waitFor("the command typed", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; got != "/thinking high" {
		t.Fatalf("pasted %q", got)
	}
	// Its 👍 comes once it is typed and its value is kept.
	e.waitFor("the 👍", func() bool { m, _ := e.message(cmd); return m.Reaction == "👍" })
	e.say(thread, "/info")
	e.waitText(thread, "Thinking level: <code>high</code>")
	keys, _ := e.peek().KeysWithPrefix(context.Background(), "ctl_pending:")
	if len(keys) != 0 {
		t.Errorf("pending records left: %v", keys)
	}
}
