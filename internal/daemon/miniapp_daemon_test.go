package daemon_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/telegram/tgtest"
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

// repoTopic is piTopicCfg for an agent whose folder is a real git
// repository: the pane's process, the transcript header, and the hook all
// name that folder instead of paneCWD.
func repoTopic(t *testing.T, id string, tweak func(*env)) (*env, *piLog, int64, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	repoGit(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repoGit(t, repo, "add", "-A")
	repoGit(t, repo, "commit", "-q", "-m", "start")

	e := newEnv(t)
	tweak(e)
	e.seedOwner()
	e.fake.On([]string{"block", "call", "-s", rexSess, "-b", rexBlock, "process"},
		fmt.Sprintf(`{"foreground":{"pid":%d,"name":"node","argv0":"pi","cwd":%q},"ancestors":[]}`, piPID, repo), 0)
	dir := filepath.Join(e.home, ".pi", "agent", "sessions", "--repo--")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := &piLog{t: t, ID: id, Path: filepath.Join(dir, "2026-01-01T00-00-00-000Z_"+id+".jsonl")}
	p.write(fmt.Sprintf(`{"type":"session","version":3,"id":%q,"timestamp":%q,"cwd":%q}`, id, now(), repo))
	p.User("start")
	p.Final("ready")
	e.start()
	e.spool("pi", "session_start", map[string]any{
		"hook_event_name": "session_start", "reason": "startup", "session_file": p.Path, "cwd": repo, "session_id": id,
	})
	thread := e.openTopic()
	e.waitText(thread, "ready")
	e.pi = p
	return e, p, thread, repo
}

func repoGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func buttonTexts(m tgtest.Message) [][]string {
	var out [][]string
	if m.Markup == nil {
		return nil
	}
	for _, row := range m.Markup.InlineKeyboard {
		var r []string
		for _, b := range row {
			r = append(r, b.Text)
		}
		out = append(out, r)
	}
	return out
}

// A turn that edits a file in the agent's git repository gets a button with
// the change count, which opens the diff in the Mini App.
func TestAnswerGetsTheChangesButton(t *testing.T) {
	e, p, thread, repo := repoTopic(t, "pi-changes", func(e *env) {
		e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
	})
	p.User("change a.txt")
	// The snapshot of the turn's start is taken in the background; it has no
	// outward sign, so give it a moment before the agent edits the file.
	time.Sleep(500 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.Final("Edited a.txt.")
	e.waitText(thread, "Edited a.txt.")
	// The button is on the answer when it is sent, or comes by a markup edit.
	e.waitFor("the changes button", func() bool { return webAppURL(e, thread, "📝 1 file · +1 −0") != "" })
	if u := webAppURL(e, thread, "📝 1 file · +1 −0"); !strings.HasPrefix(u, "https://mac.example.ts.net:8443/app?p=") {
		t.Errorf("changes button url = %q", u)
	}
}

// A turn that changes nothing gets no changes button.
func TestAnswerWithoutChangesHasNoChangesButton(t *testing.T) {
	e, p, thread, _ := repoTopic(t, "pi-nochanges", func(e *env) {
		e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
	})
	p.User("just look")
	time.Sleep(500 * time.Millisecond)
	p.Final("Nothing to change.")
	e.waitText(thread, "Nothing to change.")
	e.never("a changes button", 800*time.Millisecond, func() bool { return webAppURL(e, thread, "📝") != "" })
}

// Only the agent's answer and alerts make a sound. A notice, a command
// reply, and the waiting notice are silent.
func TestOnlyAnswersAndAlertsMakeASound(t *testing.T) {
	e, p, thread := piTopic(t, "pi-sound")

	// The notice of the link (sent when the topic was made), and a command reply.
	if intro := e.waitText(thread, "Linked"); !intro.Silent {
		t.Errorf("the link notice made a sound: %+v", intro)
	}
	e.say(thread, "/unmute")
	if reply := e.waitText(thread, "make sound again"); !reply.Silent {
		t.Errorf("a command reply made a sound: %+v", reply)
	}

	// The waiting notice, while the agent is busy.
	p.User("busy work")
	p.ToolCall("c1", "sleep 5")
	e.waitFor("the progress message", func() bool { return e.hasText(thread, "⏱") })
	e.say(thread, "also this")
	if waiting := e.waitText(thread, "Waiting until the agent is idle"); !waiting.Silent {
		t.Errorf("the waiting notice made a sound: %+v", waiting)
	}

	// The answer makes a sound.
	p.ToolResult("c1")
	p.Final("Work is done.")
	if answer := e.waitText(thread, "Work is done."); answer.Silent {
		t.Errorf("the answer was silent: %+v", answer)
	}
	e.waitFor("the queued text typed", func() bool { return len(e.pastes()) == 1 })

	// An alert makes a sound.
	e.setScreen(permScreen)
	e.setStatus("blocked", "permission")
	if alert := e.waitText(thread, "Permission needed"); alert.Silent {
		t.Errorf("the alert was silent: %+v", alert)
	}
}

// An answer with a whole-answer page and quick replies has both rows, the
// Mini App row first.
func TestMiniAppRowComesBeforeQuickReplies(t *testing.T) {
	e, p, thread := piTopicCfg(t, "pi-rows", func(e *env) {
		e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
		e.cfg.MiniApp.AnswerPreview = 500
		e.cfg.Display.QuickReplies = []string{"Continue", "Yes"}
	})
	long := "# Report\n\n" + strings.Repeat("A long paragraph of the report. ", 30) + "\n\n" + strings.Repeat("More text. ", 200) + "\n\nTHE END"
	p.User("write the report")
	p.Final(long)
	answer := e.waitText(thread, "A long paragraph")
	e.waitFor("both rows", func() bool {
		m, _ := e.message(answer.ID)
		answer = m
		return len(buttonTexts(m)) == 2
	})
	rows := buttonTexts(answer)
	if len(rows[0]) != 1 || rows[0][0] != "📄 Whole answer" {
		t.Errorf("first row = %v, want the Mini App row", rows[0])
	}
	if got := strings.Join(rows[1], "|"); got != "Continue|Yes" {
		t.Errorf("second row = %q, want the quick replies", got)
	}
	if answer.Markup.InlineKeyboard[0][0].WebApp == nil {
		t.Error("the first button is no web_app button")
	}
}

// An answer gets the quick replies; a tap sends that text to the agent, as
// if typed, once.
func TestQuickReplyTypesItsText(t *testing.T) {
	e, p, thread := piTopicCfg(t, "pi-quick", func(e *env) {
		e.cfg.Display.QuickReplies = []string{"Continue", "Yes", "Run tests", "Commit"} // off by default
	})
	p.User("check it")
	time.Sleep(10 * time.Millisecond) // the answer's time comes after the prompt's: the pane is idle then
	p.Final("All good. Shall I go on?")
	answer := e.waitText(thread, "Shall I go on?")
	data := buttonData(answer, "Continue")
	if data == "" {
		t.Fatalf("the answer has no quick replies: %+v", answer.Markup)
	}
	// Two to a row, so a phone shows the labels whole.
	if rows := buttonTexts(answer); len(rows) != 2 || strings.Join(rows[0], "|") != "Continue|Yes" || strings.Join(rows[1], "|") != "Run tests|Commit" {
		t.Errorf("rows = %v, want two of two", rows)
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
