package daemon_test

import (
	"strings"
	"testing"
	"time"
)

func TestUnlinkCancelsAnInflightDelivery(t *testing.T) {
	e, _, thread := piTopicCfg(t, "pi-unlink-inflight", func(e *env) {
		e.cfg.Agents.Pi.PasteEnterDelay.Duration = 700 * time.Millisecond
	})
	e.say(thread, "the old task")
	e.waitFor("the paste", func() bool { return len(e.pastes()) == 1 })
	e.say(thread, "/unlink")
	e.waitText(thread, "Unlinked")
	e.never("Enter after unlink", 800*time.Millisecond, func() bool { return len(e.keys()) > 0 })
}

func TestSendNowCannotForceAControlCommand(t *testing.T) {
	e, p, thread := controlsTopic(t, "pi-control-send-now")
	working(e, p, thread)
	e.say(thread, "/model beta-1")
	waiting := e.waitText(thread, "Waiting until the agent is idle")
	e.pressButton(waiting, buttonData(waiting, "Send now"))
	e.waitFor("the control to stay queued", func() bool {
		for _, text := range e.callbackTexts() {
			if strings.Contains(text, "Commands and prompt notes wait") {
				return true
			}
		}
		return false
	})
	e.never("a control typed into the running turn", 250*time.Millisecond, func() bool { return len(e.pastes()) != 0 })
	if m, _ := e.message(waiting.ID); buttonData(m, "Cancel") == "" {
		t.Fatal("the queued control lost its Cancel button")
	}
}
