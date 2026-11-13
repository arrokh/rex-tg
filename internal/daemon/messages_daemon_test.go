package daemon_test

import (
	"strings"
	"testing"
)

// A long answer comes as several chat messages, split at paragraphs, with
// no "(1/2)" counters; only the first one makes a sound.
func TestLongAnswerComesAsChatMessages(t *testing.T) {
	e, p, thread := piTopic(t, "pi-long")
	var paras []string
	for _, w := range []string{"alpha", "bravo", "charlie", "delta", "echo"} {
		paras = append(paras, strings.TrimSpace(strings.Repeat(w+" ", 120)))
	}
	p.User("explain it all")
	p.Final(strings.Join(paras, "\n\n"))
	e.waitText(thread, "echo echo")
	var msgs []string
	var silent []bool
	for _, m := range e.botMessages(thread) {
		for _, w := range []string{"alpha", "bravo", "charlie", "delta", "echo"} {
			if strings.HasPrefix(m.Text, w) {
				msgs = append(msgs, m.Text)
				silent = append(silent, m.Silent)
				break
			}
		}
	}
	if len(msgs) < 3 {
		t.Fatalf("got %d messages, want the answer in several", len(msgs))
	}
	for i, m := range msgs {
		if strings.Contains(m, "/"+string(rune('0'+len(msgs)))+")") {
			t.Errorf("message %d has a counter: …%q", i+1, m[len(m)-12:])
		}
		if len([]rune(m)) > 1600 {
			t.Errorf("message %d has %d characters, want about 1500", i+1, len([]rune(m)))
		}
		if silent[i] != (i > 0) {
			t.Errorf("message %d silent = %v", i+1, silent[i])
		}
	}
}
