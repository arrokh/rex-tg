package daemon_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const claudeBoxScreen = "answer text\n╭──────────────────────────────╮\n│ >                            │\n╰──────────────────────────────╯\n"

func claudeScreen(footer string) string { return claudeBoxScreen + footer + "\n" }

// claudeTopic starts the daemon with a resolved Claude Code session and an
// open topic.
func claudeTopic(t *testing.T) (*env, int64) {
	t.Helper()
	e := newEnv(t)
	e.seedOwner()
	e.setProcess("2.1.322", "claude", 5151)
	dir := filepath.Join(e.home, ".claude", "projects", "-work-proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sess-vim.jsonl")
	lines := []string{
		fmt.Sprintf(`{"type":"user","uuid":"u-1","sessionId":"sess-vim","cwd":%q,"timestamp":%q,"message":{"role":"user","content":%s}}`, paneCWD, now(), jstr("hello")),
		fmt.Sprintf(`{"type":"assistant","uuid":"a-1","sessionId":"sess-vim","cwd":%q,"timestamp":%q,"message":{"id":"m-1","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"claude is ready"}]}}`, paneCWD, now()),
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.start()
	e.spool("claude", "SessionStart", map[string]any{
		"hook_event_name": "SessionStart", "source": "startup", "session_id": "sess-vim", "transcript_path": path, "cwd": paneCWD,
	})
	thread := e.openTopic()
	e.waitText(thread, "claude is ready")
	return e, thread
}

// The daemon leaves Claude Code's VISUAL mode with one Escape before it pastes,
// because Enter does not submit in VISUAL. The unit tests in
// vim_settle_test.go cover the other modes and an editor that does not react.
func TestClaudeVisualModeIsLeftBeforeThePaste(t *testing.T) {
	e, thread := claudeTopic(t)
	e.fake.SetScreenMachine("VISUAL", map[string]string{
		"VISUAL":      claudeScreen("  -- VISUAL --"),
		"VISUAL LINE": claudeScreen("  -- VISUAL LINE --"),
		"NORMAL":      claudeScreen("  ? for shortcuts"),
	}, map[string]map[string]string{"VISUAL": {"escape": "NORMAL"}, "VISUAL LINE": {"escape": "NORMAL"}})
	e.say(thread, "please check the docs")
	e.waitFor("the paste", func() bool { return len(e.pastes()) == 1 })
	e.waitFor("Enter", func() bool { return len(e.keys()) == 2 })
	if got := strings.Join(e.keys(), " "); got != "escape enter" {
		t.Fatalf("keys = %q, want escape enter", got)
	}
	var escAt, pasteAt, enterAt int
	for i, c := range e.fake.Calls() {
		args := strings.Join(c.Args, " ")
		switch {
		case strings.Contains(args, "send-key") && strings.HasSuffix(args, " escape"):
			escAt = i
		case strings.Contains(args, "block.event"):
			pasteAt = i
		case strings.Contains(args, "send-key") && strings.HasSuffix(args, " enter"):
			enterAt = i
		}
	}
	if !(escAt < pasteAt && pasteAt < enterAt) {
		t.Errorf("order: escape %d, paste %d, enter %d", escAt, pasteAt, enterAt)
	}
}

// A message typed while Claude Code works is queued; Claude Code writes it
// when the turn takes it in. One with an image is written as content blocks
// (seen live). It is the message's echo: 👀 turns into 👍.
func TestQueuedMessageWithAnImageIsAnEcho(t *testing.T) {
	e, thread := claudeTopic(t)
	id := e.say(thread, "also fix this")
	e.waitFor("the eyes reaction", func() bool { m, _ := e.message(id); return m.Reaction == "👀" || m.Reaction == "👍" })
	path := filepath.Join(e.home, ".claude", "projects", "-work-proj", "sess-vim.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(f, `{"type":"attachment","uuid":"q-1","sessionId":"sess-vim","cwd":%q,"timestamp":%q,"attachment":{"type":"queued_command","prompt":[{"type":"text","text":"[Image #1]also fix this"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}],"commandMode":"prompt","origin":{"kind":"human"}}}`+"\n", paneCWD, now())
	f.Close()
	e.waitFor("the thumbs up", func() bool { m, _ := e.message(id); return m.Reaction == "👍" })
	if e.hasText(thread, "In use on the Mac") {
		t.Errorf("the message counts as typed on the Mac\n%s", e.dump())
	}
}
