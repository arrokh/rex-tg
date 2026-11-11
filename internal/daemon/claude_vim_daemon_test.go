package daemon_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestClaudeVisualThatStaysGetsOneEscapeAndTheMessageWaits(t *testing.T) {
	e, thread := claudeTopic(t)
	e.fake.SetScreenMachine("VISUAL", map[string]string{
		"VISUAL": claudeScreen("  -- VISUAL --"),
		"NORMAL": claudeScreen("  ? for shortcuts"),
	}, nil)
	e.say(thread, "hello")
	e.waitText(thread, "did not react")
	e.never("a second key", 4*time.Second, func() bool { return len(e.keys()) > 1 })
	if got := e.keys(); len(got) != 1 || got[0] != "escape" {
		t.Fatalf("keys = %v, want one escape", got)
	}
	if len(e.pastes()) != 0 {
		t.Fatalf("pasted while VISUAL stayed: %v", e.pastes())
	}
	e.fake.SetScreenState("NORMAL") // the editor caught up
	e.waitFor("the paste", func() bool { return len(e.pastes()) == 1 })
	e.waitFor("Enter", func() bool { return len(e.keys()) == 2 })
	if got := e.keys()[1]; got != "enter" {
		t.Errorf("keys = %v, want escape then enter", e.keys())
	}
	if n := e.countText(thread, "did not react"); n != 1 {
		t.Errorf("the note was posted %d times, want once", n)
	}
}

func TestClaudeWithoutIndicatorGetsPasteAndEnterOnly(t *testing.T) {
	for name, screen := range map[string]string{
		"normal":         claudeScreen("  ? for shortcuts"),
		"insert":         claudeScreen("  -- INSERT --"),
		"text in output": "-- VISUAL --\n" + claudeScreen("  ? for shortcuts"),
		"not vim at all": "plain screen\n",
	} {
		t.Run(name, func(t *testing.T) {
			e, thread := claudeTopic(t)
			e.setScreen(screen)
			e.say(thread, "hello")
			e.waitFor("the paste", func() bool { return len(e.pastes()) == 1 })
			e.waitFor("Enter", func() bool { return len(e.keys()) == 1 })
			e.never("another key", 500*time.Millisecond, func() bool { return len(e.keys()) > 1 })
			if got := e.keys(); got[0] != "enter" {
				t.Errorf("keys = %v, want enter only", got)
			}
		})
	}
}

// After the Escape, a screen without the prompt box (a redraw caught halfway)
// does not count as "VISUAL is gone": nothing is pasted until the box shows
// without the label.
func TestClaudeVisualNeedsTheBoxBackBeforeThePaste(t *testing.T) {
	e, thread := claudeTopic(t)
	e.fake.SetScreenMachine("VISUAL", map[string]string{
		"VISUAL": claudeScreen("  -- VISUAL --"),
		"REDRAW": "answer text\n",
		"NORMAL": claudeScreen("  ? for shortcuts"),
	}, map[string]map[string]string{"VISUAL": {"escape": "REDRAW"}})
	e.say(thread, "hello")
	e.waitText(thread, "did not react")
	if len(e.pastes()) != 0 {
		t.Fatalf("pasted while the box was missing: %v", e.pastes())
	}
	e.fake.SetScreenState("NORMAL")
	e.waitFor("the paste", func() bool { return len(e.pastes()) == 1 })
	e.waitFor("Enter", func() bool { return len(e.keys()) == 2 })
	if got := strings.Join(e.keys(), " "); got != "escape enter" {
		t.Fatalf("keys = %q, want one escape, then enter", got)
	}
}
