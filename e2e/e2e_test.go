//go:build e2e

// Package e2e runs the real daemon against a real, isolated Rex server and a
// fake Telegram, with a dummy pi agent in a Rex pane.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"rex-tg/internal/config"
	"rex-tg/internal/daemon"
	"rex-tg/internal/rex"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram/tgtest"
)

const (
	rexBin  = "/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex"
	ownerID = int64(4242)
	chatID  = int64(4242)
	think   = 3 * time.Second
	// echoDelay is the time between the Enter key and the user line in the
	// transcript. With a delay, the 👀 reaction (typed) normally arrives
	// before 👍 (recorded). When the echo wins the race, the sender drops the
	// older 👀 that was not sent yet, so the tests check the end state: the
	// newest reaction is 👍 and no 👎 appears.
	echoDelay = 500 * time.Millisecond
)

// tlog is a slog destination that writes to the test log.
type tlog struct {
	mu sync.Mutex
	t  *testing.T
}

func (w *tlog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.t.Log("daemon: " + strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// eventually polls cond until it is true or the timeout passes.
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for: %s", timeout, what)
}

func rexCmd(sock string, args ...string) *exec.Cmd {
	full := append([]string{"--autostart=false", "-S", "unix://" + sock}, args...)
	return exec.Command(rexBin, full...)
}

// startRex starts an isolated Rex server and returns its socket path.
func startRex(t *testing.T, tag string) string {
	t.Helper()
	if _, err := os.Stat(rexBin); err != nil {
		t.Skipf("Rex CLI not installed: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(home, ".cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(cache, fmt.Sprintf("rextg-e2e-%d-%s.sock", os.Getpid(), tag))
	if len(sock) >= 104 {
		t.Fatalf("socket path too long (%d bytes): %s", len(sock), sock)
	}
	_ = os.Remove(sock)
	stateDir := t.TempDir()
	emptyCfg := filepath.Join(t.TempDir(), "init.lua")
	if err := os.WriteFile(emptyCfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	srv := exec.Command(rexBin, "server", "run", "--listen", "unix://"+sock, "--state-dir", stateDir, "--config", emptyCfg)
	var srvOut bytes.Buffer
	srv.Stdout, srv.Stderr = &srvOut, &srvOut
	srv.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := srv.Start(); err != nil {
		t.Fatalf("start rex server: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = srv.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-srv.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-srv.Process.Pid, syscall.SIGKILL)
			<-done
		}
		_ = os.Remove(sock)
		if t.Failed() {
			t.Logf("rex server output (non-INFO):\n%s", nonInfo(srvOut.String()))
		}
	})

	// Guard: the CLI must talk to our socket and never to the user's main server.
	var who rex.Whoami
	eventually(t, 15*time.Second, "isolated rex server to answer whoami", func() bool {
		out, err := rexCmd(sock, "whoami", "--json").Output()
		if err != nil {
			return false
		}
		return json.Unmarshal(out, &who) == nil
	})
	if !strings.Contains(who.Endpoint, sock) {
		t.Fatalf("rex whoami endpoint %q is not the isolated socket %q; refusing to continue", who.Endpoint, sock)
	}
	return sock
}

type logEntry struct {
	Ev   string `json:"ev"`
	At   string `json:"at"`
	Busy bool   `json:"busy"`
	Text string `json:"text"`
}

func (e logEntry) time() time.Time {
	ts, _ := time.Parse(time.RFC3339Nano, e.At)
	return ts
}

// readAgentLog returns the dummy agent's log entries.
func readAgentLog(path string) []logEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []logEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e logEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

func findEntry(path, ev, text string) (logEntry, bool) {
	for _, e := range readAgentLog(path) {
		if e.Ev == ev && e.Text == text {
			return e, true
		}
	}
	return logEntry{}, false
}

func TestDaemonEndToEnd(t *testing.T) {
	// The dummy is built under the name that the registry detects.
	for _, runtime := range []string{"pi", "claude", "codex"} {
		t.Run(runtime, func(t *testing.T) {
			t.Parallel() // each flow has its own Rex server, folders, and fakes
			runFlow(t, runtime)
		})
	}
}

func runFlow(t *testing.T, runtime string) {
	// Build the dummy agent under the file name "pi".
	binDir := t.TempDir()
	piBin := filepath.Join(binDir, runtime)
	build := exec.Command("go", "build", "-o", piBin, "./dummyagent")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build dummy agent: %v\n%s", err, out)
	}

	sock := startRex(t, runtime)

	home := t.TempDir()
	stateDir := t.TempDir()
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	agentLog := filepath.Join(t.TempDir(), "agent.jsonl")

	cfg := config.Default(home)
	cfg.State.Dir = stateDir
	cfg.Rex.CLI = rexBin
	cfg.Rex.ScanInterval = config.Duration{Duration: 500 * time.Millisecond}
	cfg.Display.ProgressDelay = config.Duration{Duration: 500 * time.Millisecond}
	cfg.Display.ProgressEditInterval = config.Duration{Duration: 500 * time.Millisecond}
	cfg.InputSettings.JoinWindow = config.Duration{Duration: 200 * time.Millisecond}
	cfg.InputSettings.JoinWindowLong = config.Duration{Duration: 400 * time.Millisecond}
	cfg.InputSettings.BusyAfterTyping = config.Duration{Duration: 300 * time.Millisecond}
	cfg.InputSettings.QueueExpiry = config.Duration{Duration: time.Minute}

	tg := tgtest.New(t)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		logChat(t, tg, chatID)
		for _, c := range tg.Calls("answerCallbackQuery") {
			t.Logf("answerCallbackQuery: %v", c.Params)
		}
		for _, c := range tg.Calls("setMessageReaction") {
			t.Logf("setMessageReaction: %v", c.Params)
		}
		for _, e := range readAgentLog(agentLog) {
			t.Logf("agent log: %+v", e)
		}
	})

	// Pair: create the code before Run, then send /start from the owner.
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	code, err := daemon.NewPairingCode(context.Background(), st, time.Now())
	if err != nil {
		t.Fatalf("pairing code: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	rx := rex.New(rexBin, rex.WithServer("unix://"+sock))
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- daemon.Run(ctx, daemon.Options{
			Config:   cfg,
			Token:    tg.Token(),
			Home:     home,
			Logger:   slog.New(slog.NewTextHandler(&tlog{t: t}, &slog.HandlerOptions{Level: slog.LevelDebug})),
			Rex:      rx,
			Telegram: tg.Client(),
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-runDone:
			if err != nil {
				t.Errorf("daemon.Run returned %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("daemon.Run did not stop within 15s of cancel")
		}
	})

	tg.PushMessage(chatID, 0, ownerID, "/start "+code)
	eventually(t, 15*time.Second, "pairing confirmation", func() bool {
		return botMessageContaining(tg, chatID, 0, "Paired") != nil
	})

	// Start the dummy agent in a Rex pane with the project as its folder.
	newCmd := rexCmd(sock, "new", "e2e", "--json", "-c", project, "--shell=none", "--",
		piBin, "-home", home, "-spool", cfg.SpoolDir(), "-log", agentLog, "-think", think.String(), "-format", runtime, "-seed", "earlier turn", "-echo-delay", echoDelay.String())
	if out, err := newCmd.CombinedOutput(); err != nil {
		t.Fatalf("rex new: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		ents, _ := os.ReadDir(cfg.SpoolDir())
		for _, e := range ents {
			t.Logf("spool file left: %s", e.Name())
		}
		c := context.Background()
		if ro, err := store.OpenReadOnly(cfg.DBPath()); err == nil {
			panes, _ := ro.Panes(c)
			for _, p := range panes {
				t.Logf("store pane: %+v", p)
			}
			ro.Close()
		}
		sess, _ := rx.Sessions(c)
		for _, ss := range sess {
			blocks, _ := rx.Blocks(c, ss.ID)
			for _, b := range blocks {
				p, err := rx.Process(c, ss.ID, b.ID)
				t.Logf("process %s: %+v err=%v", b.ID, p, err)
				if cap, err := rx.Capture(c, ss.ID, b.ID); err == nil {
					t.Logf("screen: %q", cap)
				}
			}
		}
	})

	// /agents lists the pane with an Open button.
	var openBtn string
	var agentsMsgID int64
	eventually(t, 40*time.Second, "/agents to list the pane with an Open button", func() bool {
		before := len(tg.Messages(chatID))
		tg.PushMessage(chatID, 0, ownerID, "/agents")
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			msgs := tg.Messages(chatID)
			for _, m := range msgs[before:] {
				// An unresolved pane also gets a button, but Open then only
				// answers "Waiting for the session file".
				if !m.FromBot || m.Markup == nil || strings.Contains(m.Text, "waiting for session") {
					continue
				}
				for _, row := range m.Markup.InlineKeyboard {
					for _, b := range row {
						if strings.HasPrefix(b.Text, "Open") && strings.HasPrefix(b.CallbackData, "o:") {
							openBtn, agentsMsgID = b.CallbackData, m.ID
							return true
						}
					}
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		return false
	})

	// Press Open: a topic and the intro card appear.
	tg.PushCallback(chatID, 0, ownerID, agentsMsgID, openBtn)
	var threadID int64
	eventually(t, 20*time.Second, "topic creation", func() bool {
		for _, tp := range tg.Topics(chatID) {
			if !tp.Deleted {
				threadID = tp.ThreadID
				return true
			}
		}
		return false
	})
	eventually(t, 20*time.Second, "intro card in the topic", func() bool {
		return botMessageContaining(tg, chatID, threadID, "Linked:") != nil
	})

	// First prompt: pasted into the real pane, answered once, echo suppressed.
	p1 := "hello from telegram"
	u1 := tg.PushMessage(chatID, threadID, ownerID, p1)
	eventually(t, 30*time.Second, "dummy agent to receive the first prompt", func() bool {
		_, ok := findEntry(agentLog, "prompt", p1)
		return ok
	})
	e1, _ := findEntry(agentLog, "prompt", p1)
	if e1.Busy {
		t.Errorf("first prompt reached the agent while it was busy")
	}
	eventually(t, 30*time.Second, "ECHO answer for the first prompt in the topic", func() bool {
		return botMessageContaining(tg, chatID, threadID, "ECHO: "+p1) != nil
	})
	eventually(t, 20*time.Second, "reactions on the first message", func() bool {
		return endsRecorded(reactions(tg, chatID, u1.Message.MessageID))
	})

	// Second prompt starts a run; the third is sent while the agent works.
	p2, p3 := "second prompt", "third prompt while working"
	tg.PushMessage(chatID, threadID, ownerID, p2)
	eventually(t, 30*time.Second, "dummy agent to receive the second prompt", func() bool {
		_, ok := findEntry(agentLog, "prompt", p2)
		return ok
	})
	if _, done := findEntry(agentLog, "done", p2); done {
		t.Fatalf("agent finished the second prompt before the third was sent; raise the think time")
	}
	tg.PushMessage(chatID, threadID, ownerID, p3)
	eventually(t, 60*time.Second, "dummy agent to receive the third prompt", func() bool {
		_, ok := findEntry(agentLog, "prompt", p3)
		return ok
	})
	e3, _ := findEntry(agentLog, "prompt", p3)
	d2, ok := findEntry(agentLog, "done", p2)
	if !ok {
		t.Fatalf("third prompt arrived but the second run never finished")
	}
	if e3.Busy || e3.time().Before(d2.time()) {
		t.Errorf("third prompt was typed while the agent was working (prompt at %s, second run done at %s, busy=%v)", e3.At, d2.At, e3.Busy)
	}
	for _, p := range []string{p2, p3} {
		p := p
		eventually(t, 30*time.Second, "ECHO answer for "+p, func() bool {
			return botMessageContaining(tg, chatID, threadID, "ECHO: "+p) != nil
		})
	}

	// Let late duplicates arrive, then check exactly-once and echo suppression.
	time.Sleep(3 * time.Second)
	for _, p := range []string{p1, p2, p3} {
		var mine []string
		for _, m := range tg.Messages(chatID) {
			if m.FromBot && m.ThreadID == threadID && !m.Deleted && strings.Contains(m.Text, p) {
				mine = append(mine, m.Text)
			}
		}
		if len(mine) != 1 || !strings.Contains(mine[0], "ECHO: "+p) {
			t.Errorf("prompt %q: want exactly one bot message (the ECHO answer), got %d: %q", p, len(mine), mine)
		}
	}
	for _, p := range []string{p2, p3} {
		if got := reactions(tg, chatID, userMessageID(tg, chatID, threadID, p)); !endsRecorded(got) {
			t.Errorf("reactions for %q: got %q, want the newest to be 👍 and no 👎", p, strings.Join(got, ""))
		}
	}
}

// endsRecorded reports whether a message's reactions, in call order, end with
// 👍 (the agent recorded the prompt) and contain no 👎.
func endsRecorded(seq []string) bool {
	if len(seq) == 0 || seq[len(seq)-1] != "👍" {
		return false
	}
	for _, r := range seq {
		if r == "👎" {
			return false
		}
	}
	return true
}

// botMessageContaining returns the first live bot message in the thread with s.
func botMessageContaining(tg *tgtest.Server, chat, thread int64, s string) *tgtest.Message {
	for _, m := range tg.Messages(chat) {
		if m.FromBot && !m.Deleted && m.ThreadID == thread && strings.Contains(m.Text, s) {
			m := m
			return &m
		}
	}
	return nil
}

// userMessageID finds the id of the owner's message with this text.
func userMessageID(tg *tgtest.Server, chat, thread int64, text string) int64 {
	for _, m := range tg.Messages(chat) {
		if !m.FromBot && m.ThreadID == thread && m.Text == text {
			return m.ID
		}
	}
	return 0
}

// reactions lists the emoji set on a message, in call order.
func reactions(tg *tgtest.Server, chat, msgID int64) []string {
	var out []string
	for _, c := range tg.Calls("setMessageReaction") {
		if id, _ := c.Params["message_id"].(float64); int64(id) != msgID {
			continue
		}
		list, _ := c.Params["reaction"].([]any)
		for _, r := range list {
			if m, ok := r.(map[string]any); ok {
				if e, _ := m["emoji"].(string); e != "" {
					out = append(out, e)
				}
			}
		}
	}
	return out
}

func logChat(t *testing.T, tg *tgtest.Server, chat int64) {
	var b strings.Builder
	for _, m := range tg.Messages(chat) {
		fmt.Fprintf(&b, "  #%d thread=%d bot=%v deleted=%v react=%q: %q\n", m.ID, m.ThreadID, m.FromBot, m.Deleted, m.Reaction, m.Text)
	}
	t.Logf("messages in chat:\n%s", b.String())
}

// nonInfo drops the server's routine INFO lines.
func nonInfo(s string) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" && !strings.Contains(l, " INFO ") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}
