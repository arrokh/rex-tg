//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"rex-tg/internal/config"
	"rex-tg/internal/rex/rextest"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram/tgtest"
)

const (
	crashSess = "S1"
	crashBlk  = "B1"
	crashCWD  = "/work/proj"
	crashPID  = 4242
)

// syncBuf is a goroutine-safe buffer for the harness output.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// crashEnv is a daemon process (the harness) with a fake Telegram and a fake Rex.
type crashEnv struct {
	t       *testing.T
	bin     string // the harness binary
	home    string
	state   string
	cfg     config.Config
	tg      *tgtest.Server
	fake    *rextest.Fake
	cmd     *exec.Cmd
	out     *syncBuf
	session string
	file    string
	n       int
	thread  int64
}

func newCrashEnv(t *testing.T) *crashEnv {
	t.Helper()
	bin := program(t, "./crashharness", "crashharness")
	home := t.TempDir()
	cfg := config.Default(home)
	cfg.State.Dir = filepath.Join(t.TempDir(), "state")
	e := &crashEnv{t: t, bin: bin, home: home, state: cfg.State.Dir, cfg: cfg, tg: tgtest.New(t), fake: rextest.New(t), session: "pi-crash"}

	f := e.fake
	f.On([]string{"api", "call"}, "{}", 0)
	f.On([]string{"send-key"}, "", 0)
	f.On([]string{"capture"}, "", 0)
	f.On([]string{"whoami"}, `{"rex_version":"test"}`, 0)
	f.On([]string{"ls"}, fmt.Sprintf(`{"sessions":[{"session_id":%q,"label":"main"}]}`, crashSess), 0)
	f.On([]string{"block", "ls"}, fmt.Sprintf(`{"blocks":[{"block_id":%q,"label":"pane one"}]}`, crashBlk), 0)
	f.On([]string{"block", "call", "-s", crashSess, "-b", crashBlk, "process"},
		fmt.Sprintf(`{"foreground":{"pid":%d,"name":"node","argv0":"pi","cwd":%q},"ancestors":[]}`, crashPID, crashCWD), 0)
	rec := map[string]any{"state": "idle", "app": "pi", "updated_at": time.Now().UTC().Format(time.RFC3339Nano)}
	b, _ := json.Marshal(map[string]any{"records": []any{rec}})
	f.On([]string{"block", "call", "-s", crashSess, "-b", crashBlk, "program_status"}, string(b), 0)

	// Pair the owner in the store before the first start.
	if err := os.MkdirAll(e.state, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"owner_user_id": fmt.Sprint(ownerID), "owner_chat_id": fmt.Sprint(chatID)} {
		if err := st.Set(context.Background(), k, v); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	// A pi transcript with one finished turn, and its hook report.
	dir := filepath.Join(home, ".pi", "agent", "sessions", "--"+strings.ReplaceAll(strings.TrimPrefix(crashCWD, "/"), "/", "-")+"--")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	e.file = filepath.Join(dir, "2026-01-01T00-00-00-000Z_"+e.session+".jsonl")
	e.write(fmt.Sprintf(`{"type":"session","version":3,"id":%q,"timestamp":%q,"cwd":%q}`, e.session, tsNow(), crashCWD))
	e.user("start")
	e.final("ready")
	spool := cfg.SpoolDir()
	if err := os.MkdirAll(spool, 0o700); err != nil {
		t.Fatal(err)
	}
	rep, _ := json.Marshal(map[string]any{"runtime": "pi", "event": "session_start",
		"at": time.Now().UTC().Format(time.RFC3339Nano), "rex_session": crashSess, "rex_block": crashBlk,
		"payload": map[string]any{"hook_event_name": "session_start", "reason": "startup",
			"session_file": e.file, "cwd": crashCWD, "session_id": e.session}})
	if err := os.WriteFile(filepath.Join(spool, "1-1-1.json"), rep, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		e.kill()
		if t.Failed() {
			t.Logf("harness output:\n%s", e.out.String())
			e.dump()
		}
	})
	return e
}

func tsNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func (e *crashEnv) write(lines ...string) {
	e.t.Helper()
	f, err := os.OpenFile(e.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			e.t.Fatal(err)
		}
	}
}

func jstring(s string) string { b, _ := json.Marshal(s); return string(b) }

func (e *crashEnv) id() string { e.n++; return fmt.Sprintf("%08x", e.n) }

func (e *crashEnv) user(text string) {
	e.write(fmt.Sprintf(`{"type":"message","id":%q,"timestamp":%q,"message":{"role":"user","content":[{"type":"text","text":%s}]}}`, e.id(), tsNow(), jstring(text)))
}

func (e *crashEnv) final(text string) {
	e.write(fmt.Sprintf(`{"type":"message","id":%q,"timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":%s}],"stopReason":"stop"}}`, e.id(), tsNow(), jstring(text)))
}

func (e *crashEnv) toolCall(callID, cmd string) {
	e.write(fmt.Sprintf(`{"type":"message","id":%q,"timestamp":%q,"message":{"role":"assistant","content":[{"type":"toolCall","id":%q,"name":"bash","arguments":{"command":%s}}],"stopReason":"toolUse"}}`, e.id(), tsNow(), callID, jstring(cmd)))
}

func (e *crashEnv) toolResult(callID string) {
	e.write(fmt.Sprintf(`{"type":"message","id":%q,"timestamp":%q,"message":{"role":"toolResult","toolCallId":%q,"content":[{"type":"text","text":"ok"}],"durationMs":120}}`, e.id(), tsNow(), callID))
}

// start launches the harness.
func (e *crashEnv) start() {
	e.t.Helper()
	e.out = &syncBuf{}
	e.cmd = exec.Command(e.bin, "-state", e.state, "-home", e.home, "-api", e.tg.URL(), "-token", e.tg.Token(), "-rex", e.fake.Bin())
	e.cmd.Stderr = e.out
	e.cmd.Stdout = e.out
	if err := e.cmd.Start(); err != nil {
		e.t.Fatalf("start harness: %v", err)
	}
	cmd := e.cmd
	go func() { _ = cmd.Wait() }()
}

// kill sends SIGKILL, so the daemon gets no chance to clean up.
func (e *crashEnv) kill() {
	if e.cmd == nil || e.cmd.Process == nil {
		return
	}
	_ = e.cmd.Process.Signal(syscall.SIGKILL)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && rextest.ProcessAlive(e.cmd.Process.Pid) {
		time.Sleep(20 * time.Millisecond)
	}
	e.cmd = nil
}

func (e *crashEnv) bots(thread int64) []tgtest.Message {
	var out []tgtest.Message
	for _, m := range e.tg.Messages(chatID) {
		if m.FromBot && m.ThreadID == thread && !m.Deleted {
			out = append(out, m)
		}
	}
	return out
}

func (e *crashEnv) count(thread int64, sub string) int {
	n := 0
	for _, m := range e.bots(thread) {
		if strings.Contains(m.Text, sub) {
			n++
		}
	}
	return n
}

func (e *crashEnv) waitText(thread int64, sub string) tgtest.Message {
	e.t.Helper()
	var found tgtest.Message
	eventually(e.t, 20*time.Second, fmt.Sprintf("a bot message with %q in thread %d", sub, thread), func() bool {
		for _, m := range e.bots(thread) {
			if strings.Contains(m.Text, sub) {
				found = m
				return true
			}
		}
		return false
	})
	return found
}

func (e *crashEnv) dump() {
	var b strings.Builder
	for _, m := range e.tg.Messages(chatID) {
		fmt.Fprintf(&b, "  #%d thread=%d bot=%v react=%q del=%v edits=%d: %q\n", m.ID, m.ThreadID, m.FromBot, m.Reaction, m.Deleted, m.Edits, m.Text)
	}
	e.t.Logf("telegram messages:\n%s", b.String())
}

// pastes returns the texts pasted into the fake pane.
func (e *crashEnv) pastes() []string {
	var out []string
	for _, c := range e.fake.Calls() {
		if !strings.Contains(strings.Join(c.Args, " "), "block.event") {
			continue
		}
		var body struct {
			Event struct {
				Payload struct {
					Text string `json:"text"`
				} `json:"payload"`
			} `json:"event"`
		}
		if json.Unmarshal([]byte(c.Stdin), &body) == nil {
			out = append(out, body.Event.Payload.Text)
		}
	}
	return out
}

// openTopic starts the harness and opens the pane's topic.
func (e *crashEnv) openTopic() {
	e.t.Helper()
	e.start()
	var list tgtest.Message
	eventually(e.t, 30*time.Second, "an agents list with a resolved pane", func() bool {
		before := len(e.bots(0))
		e.tg.PushMessage(chatID, 0, ownerID, "/agents")
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			ms := e.bots(0)
			for _, m := range ms[before:] {
				if strings.Contains(m.Text, "Active agents") && !strings.Contains(m.Text, "waiting for session") && m.Markup != nil {
					list = m
					return true
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		return false
	})
	data := ""
	for _, row := range list.Markup.InlineKeyboard {
		for _, b := range row {
			if strings.HasPrefix(b.CallbackData, "o:") {
				data = b.CallbackData
			}
		}
	}
	if data == "" {
		e.t.Fatalf("no Open button: %+v", list.Markup)
	}
	e.tg.PushCallback(chatID, 0, ownerID, list.ID, data)
	eventually(e.t, 20*time.Second, "the topic", func() bool {
		ts := e.tg.Topics(chatID)
		if len(ts) == 0 {
			return false
		}
		e.thread = ts[0].ThreadID
		return true
	})
	e.waitText(e.thread, "Linked")
	e.waitText(e.thread, "ready")
	// Let the first turn settle so later messages are the ones under test.
	time.Sleep(500 * time.Millisecond)
}

// noticeCount counts "may not have arrived" notices in the topic.
func (e *crashEnv) noticeCount() int { return e.count(e.thread, "may not have arrived") }

// (a) kill -9 while the request that sends an answer is in flight.
func TestCrashDuringSend(t *testing.T) {
	t.Parallel()
	e := newCrashEnv(t)
	e.openTopic()

	// A final answer goes out as a rich message (the default).
	e.tg.Delay("sendRichMessage", 30*time.Second)
	before := len(e.tg.Calls("sendRichMessage"))
	// No prompt line: a prompt post would be held first and time out (15 s
	// client limit) before the answer is sent.
	e.final("the held answer")
	eventually(t, 20*time.Second, "the answer request to reach Telegram", func() bool {
		calls := e.tg.Calls("sendRichMessage")
		for _, c := range calls[before:] {
			rm, _ := c.Params["rich_message"].(map[string]any)
			if s, _ := rm["markdown"].(string); strings.Contains(s, "the held answer") {
				return true
			}
		}
		return false
	})
	e.kill()
	e.tg.Delay("sendRichMessage", 0)
	e.start()

	// The request died with the daemon, so Telegram never posted the answer.
	// After the restart, the check of the chat's message numbers shows that,
	// and the answer is sent once more: it is in the chat once, with no notice.
	eventually(t, 20*time.Second, "the answer after the restart", func() bool {
		return e.count(e.thread, "the held answer") > 0
	})
	time.Sleep(3 * time.Second)
	if n := e.count(e.thread, "the held answer"); n != 1 {
		t.Errorf("the answer was posted %d times, want 1", n)
	}
	if n := e.noticeCount(); n != 0 {
		t.Errorf("%d \"may not have arrived\" notices, want none", n)
	}
}

// (a2) kill -9 after Telegram posted the answer, before it answered: the
// answer is in the chat, but the daemon never learned it. After the restart,
// the check finds a message number that rex-tg does not know, so it never
// sends the answer again by itself: one notice says it may be missing.
func TestCrashAfterTelegramPostedTheAnswer(t *testing.T) {
	t.Parallel()
	e := newCrashEnv(t)
	e.openTopic()

	e.tg.DelayAnswer("sendRichMessage", 30*time.Second)
	e.final("the posted answer")
	eventually(t, 20*time.Second, "the answer to be posted", func() bool {
		return e.count(e.thread, "the posted answer") > 0
	})
	e.kill()
	e.tg.DelayAnswer("sendRichMessage", 0)
	e.start()

	eventually(t, 20*time.Second, "the notice after the restart", func() bool { return e.noticeCount() > 0 })
	time.Sleep(3 * time.Second)
	if n := e.count(e.thread, "the posted answer"); n != 1 {
		t.Errorf("the answer was posted %d times, want 1", n)
	}
	if n := e.noticeCount(); n != 1 {
		t.Errorf("%d \"may not have arrived\" notices, want 1", n)
	}
}

// (b) kill -9 while the daemon types a message into the pane.
func TestCrashDuringTyping(t *testing.T) {
	t.Parallel()
	e := newCrashEnv(t)
	e.openTopic()

	// Enter hangs, so the kill lands between the paste and the submit.
	e.fake.OnDelay([]string{"send-key"}, "", 30*time.Second)
	e.tg.PushMessage(chatID, e.thread, ownerID, "type me once")
	eventually(t, 20*time.Second, "the paste into the pane", func() bool { return len(e.pastes()) == 1 })
	time.Sleep(300 * time.Millisecond)
	e.kill()
	e.fake.On([]string{"send-key"}, "", 0)
	e.start()

	eventually(t, 20*time.Second, "the typing notice", func() bool { return e.count(e.thread, "Check with /screen") > 0 })
	time.Sleep(3 * time.Second)
	if got := e.pastes(); len(got) != 1 {
		t.Errorf("the message was typed %d times (%q), want exactly 1", len(got), got)
	}
	if n := e.count(e.thread, "Check with /screen"); n != 1 {
		t.Errorf("%d typing notices, want 1", n)
	}
}

// (c) kill -9 while a progress message is shown, and the turn ends while the
// daemon is down.
func TestCrashDuringProgress(t *testing.T) {
	t.Parallel()
	e := newCrashEnv(t)
	e.openTopic()

	e.user("long task")
	e.toolCall("t1", "make all")
	var progress int64
	eventually(t, 20*time.Second, "the progress message", func() bool {
		for _, m := range e.bots(e.thread) {
			if strings.Contains(m.Text, "make all") && strings.Contains(m.Text, "⏱") {
				progress = m.ID
				return true
			}
		}
		return false
	})
	e.kill()
	e.toolResult("t1")
	e.final("make finished")
	e.start()

	e.waitText(e.thread, "make finished")
	eventually(t, 20*time.Second, "the progress message to end as a summary or be removed", func() bool {
		for _, m := range e.tg.Messages(chatID) {
			if m.ID == progress {
				return m.Deleted || (strings.Contains(m.Text, "Done") && !strings.Contains(m.Text, "⏱"))
			}
		}
		return false
	})
	time.Sleep(time.Second)
	if n := e.count(e.thread, "⏱"); n != 0 {
		t.Errorf("%d stale progress messages remain", n)
	}
	if n := e.count(e.thread, "make finished"); n != 1 {
		t.Errorf("the answer was posted %d times, want 1", n)
	}
}
