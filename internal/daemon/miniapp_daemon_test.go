package daemon_test

import (
	"strings"
	"testing"
	"time"
)

func webAppURL(e *env, thread int64, text string) string {
	for _, m := range e.botMessages(thread) {
		if m.Markup == nil {
			continue
		}
		for _, row := range m.Markup.InlineKeyboard {
			for _, b := range row {
				if strings.Contains(b.Text, text) && b.WebApp != nil {
					return b.WebApp.URL
				}
			}
		}
	}
	return ""
}

// An answer gets the quick replies; a tap sends that text to the agent, as
// if typed, once.
func TestQuickReplyTypesItsText(t *testing.T) {
	e, p, thread := piTopic(t, "pi-quick")
	p.User("check it")
	time.Sleep(10 * time.Millisecond) // the answer's time comes after the prompt's: the pane is idle then
	p.Final("All good. Shall I go on?")
	answer := e.waitText(thread, "Shall I go on?")
	data := buttonData(answer, "Continue")
	if data == "" {
		t.Fatalf("the answer has no quick replies: %+v", answer.Markup)
	}
	e.pressButton(answer, data)
	e.waitFor("the quick reply typed", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; got != "Continue" {
		t.Errorf("typed %q", got)
	}
}

// With the Mini App on, a long answer shows its start, and a button opens
// the whole answer there.
func TestLongAnswerOpensInTheMiniApp(t *testing.T) {
	e, p, thread := piTopicCfg(t, "pi-long-app", func(e *env) {
		e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
		e.cfg.MiniApp.AnswerPreview = 2000
	})
	long := "# Report\n\n" + strings.Repeat("A long paragraph of the report. ", 30) + "\n\n" + strings.Repeat("More text. ", 200) + "\n\nTHE END"
	p.User("write the report")
	p.Final(long)
	e.waitText(thread, "A long paragraph")
	u := webAppURL(e, thread, "Whole answer")
	if !strings.HasPrefix(u, "https://mac.example.ts.net:8443/app?p=") {
		t.Fatalf("no Mini App button: %q", u)
	}
	if e.hasText(thread, "THE END") {
		t.Error("the whole answer was posted in the chat")
	}
}
