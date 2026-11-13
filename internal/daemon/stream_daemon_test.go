package daemon_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/telegram/tgtest"
)

// drafts returns the sendMessageDraft calls for a topic, in order.
func (e *env) drafts(thread int64) []tgtest.Call {
	var out []tgtest.Call
	for _, c := range e.tg.Calls("sendMessageDraft") {
		if n, _ := c.Params["message_thread_id"].(float64); int64(n) == thread {
			out = append(out, c)
		}
	}
	return out
}

// waitDraft waits for a draft of the topic whose text contains sub.
func (e *env) waitDraft(thread int64, sub string) tgtest.Call {
	e.t.Helper()
	var found tgtest.Call
	e.waitFor(fmt.Sprintf("a draft with %q", sub), func() bool {
		for _, c := range e.drafts(thread) {
			if s, _ := c.Params["text"].(string); strings.Contains(s, sub) {
				found = c
				return true
			}
		}
		return false
	})
	return found
}

// writeLive writes what pi's extension writes while pi writes a message.
func writeLive(t *testing.T, dir, session, text string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{"session_id": session, "text": text})
	tmp := filepath.Join(dir, session+".json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, session+".json")); err != nil {
		t.Fatal(err)
	}
}

// While the agent writes, its topic shows the text as a draft: pi's text as
// it grows, then a commentary block (the way Claude Code and Codex stream).
// The answer ends the drafts, and none comes after it. A turn held for the
// Mac gets no draft.
func TestDraftsShowTheTextBeingWritten(t *testing.T) {
	e, p, thread := macHoldTopic(t, "pi-draft")
	u := e.say(thread, "explain the parser")
	e.waitFor("the paste", func() bool { return len(e.pastes()) == 1 })
	e.waitFor("Enter", func() bool { return len(e.keys()) == 1 })
	p.User("explain the parser")
	e.waitFor("👍", func() bool { m, _ := e.message(u); return m.Reaction == "👍" })

	live := e.cfg.LiveDir()
	// The last word may still be half written: it waits for the next one.
	writeLive(t, live, p.ID, "The parser rea")
	first := e.waitDraft(thread, "The parser")
	if s, _ := first.Params["text"].(string); strings.Contains(s, "rea") {
		t.Errorf("draft %q shows a half-written word", s)
	}
	writeLive(t, live, p.ID, "The parser reads **one line** at a time")
	second := e.waitDraft(thread, "<b>one line</b>")
	if first.Params["draft_id"] != second.Params["draft_id"] || first.Params["draft_id"] == float64(0) {
		t.Errorf("draft ids %v and %v, want one non-zero id per turn", first.Params["draft_id"], second.Params["draft_id"])
	}
	if pm, _ := second.Params["parse_mode"].(string); pm != "HTML" {
		t.Errorf("parse_mode %q", pm)
	}

	// The message is complete: the extension removes its file, and the
	// transcript has a commentary block.
	if err := os.Remove(filepath.Join(live, p.ID+".json")); err != nil {
		t.Fatal(err)
	}
	p.write(fmt.Sprintf(`{"type":"message","id":"c-1","timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":"Let me run the tests."},{"type":"toolCall","id":"t1","name":"bash","arguments":{"command":"go test"}}],"stopReason":"toolUse"}}`, now()))
	e.waitDraft(thread, "Let me run the tests.")
	p.ToolResult("t1")
	p.Final("The parser reads one line at a time.")
	e.waitText(thread, "The parser reads one line at a time.")
	answerAt := time.Time{}
	for _, c := range e.tg.Calls("sendMessage") {
		if s, _ := c.Params["text"].(string); strings.Contains(s, "one line at a time.") {
			answerAt = c.At
		}
	}
	// pi may still write its last text after the answer.
	writeLive(t, live, p.ID, "late text")
	e.never("a draft after the answer", 400*time.Millisecond, func() bool {
		for _, c := range e.drafts(thread) {
			if c.At.After(answerAt) {
				return true
			}
		}
		return false
	})
	if err := os.Remove(filepath.Join(live, p.ID+".json")); err != nil {
		t.Fatal(err)
	}

	// A turn that starts on the Mac is held: no draft.
	n := len(e.drafts(thread))
	p.User("typed on the mac")
	e.waitText(thread, "In use on the Mac")
	writeLive(t, live, p.ID, "held text")
	e.never("a draft of a held turn", 400*time.Millisecond, func() bool { return len(e.drafts(thread)) > n })
}

// draftTopic is piTopic with the draft refresh at keep, and a turn from
// Telegram running.
func draftTopic(t *testing.T, id string, keep time.Duration) (*env, *piLog, int64) {
	t.Helper()
	e := newEnv(t)
	e.seedOwner()
	p := e.newPi(id)
	p.User("start")
	p.Final("ready")
	opt := e.options()
	opt.Timing.DraftKeep = keep
	e.startWith(opt)
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "ready")
	u := e.say(thread, "go on")
	e.waitFor("Enter", func() bool { return len(e.keys()) == 1 })
	p.User("go on")
	e.waitFor("👍", func() bool { m, _ := e.message(u); return m.Reaction == "👍" })
	return e, p, thread
}

// A draft that does not change is sent again before Telegram lets it fade.
func TestDraftIsRefreshedBeforeItFades(t *testing.T) {
	e, p, thread := draftTopic(t, "pi-keep", 150*time.Millisecond)
	writeLive(t, e.cfg.LiveDir(), p.ID, "still thinking about it ")
	e.waitFor("the same draft again", func() bool { return len(e.drafts(thread)) >= 3 })
	for _, c := range e.drafts(thread) {
		if s, _ := c.Params["text"].(string); s != "still thinking about it" {
			t.Fatalf("draft %q", s)
		}
	}
}

// When Telegram refuses drafts a few times in a row, rex-tg stops sending
// them until the next start; the answer still comes.
func TestDraftsStopWhenTelegramRefusesThem(t *testing.T) {
	e, p, thread := draftTopic(t, "pi-refused", time.Hour)
	e.tg.FailNext("sendMessageDraft", 100, 400, "Bad Request: method is not available", 0)
	for i := range 6 {
		writeLive(t, e.cfg.LiveDir(), p.ID, fmt.Sprintf("text %d done", i))
		want := min(i+1, 3)
		e.waitFor(fmt.Sprintf("%d drafts", want), func() bool { return len(e.drafts(thread)) >= want })
	}
	e.never("a fourth draft", 300*time.Millisecond, func() bool { return len(e.drafts(thread)) > 3 })
	p.Final("the answer")
	e.waitText(thread, "the answer")
}
