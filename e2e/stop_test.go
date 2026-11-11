//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rex-tg/internal/config"
	"rex-tg/internal/daemon"
	"rex-tg/internal/rex"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram/tgtest"
)

const stopSessionID = "0197a1b2-c3d4-7e5f-8a6b-0123456789ab"

// stopServers numbers the Rex servers of the stop tests, so tests that run in
// parallel get sockets of their own.
var stopServers atomic.Int32

// stopEnv is the real daemon with a real, isolated Rex server and a fake
// Telegram. The pane runs a small script that starts the dummy agent, waits
// for a "go" file, and then starts the same session again.
type stopEnv struct {
	t        *testing.T
	sock     string
	tg       *tgtest.Server
	rx       *rex.Client
	thread   int64
	exitFile string
	goFile   string
	project  string
}

func newStopEnv(t *testing.T, think time.Duration) *stopEnv {
	t.Helper()
	binDir := t.TempDir()
	piBin := filepath.Join(binDir, "pi")
	if out, err := exec.Command("go", "build", "-o", piBin, "./dummyagent").CombinedOutput(); err != nil {
		t.Fatalf("build dummy agent: %v\n%s", err, out)
	}
	sock := startRex(t, fmt.Sprintf("stop%d", stopServers.Add(1)))

	home := t.TempDir()
	stateDir := t.TempDir()
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctl := t.TempDir()
	e := &stopEnv{t: t, sock: sock, project: project,
		exitFile: filepath.Join(ctl, "exit"), goFile: filepath.Join(ctl, "go")}

	cfg := config.Default(home)
	cfg.State.Dir = stateDir
	cfg.Rex.CLI = rexBin
	cfg.Rex.ScanInterval = config.Duration{Duration: 500 * time.Millisecond}
	cfg.Display.ProgressDelay = config.Duration{Duration: 500 * time.Millisecond}
	cfg.Display.ProgressEditInterval = config.Duration{Duration: 500 * time.Millisecond}
	cfg.InputSettings.JoinWindow = config.Duration{Duration: 200 * time.Millisecond}
	cfg.InputSettings.JoinWindowLong = config.Duration{Duration: 400 * time.Millisecond}
	cfg.InputSettings.BusyAfterTyping = config.Duration{Duration: 300 * time.Millisecond}

	e.tg = tgtest.New(t)
	t.Cleanup(func() {
		if t.Failed() {
			logChat(t, e.tg, chatID)
			for _, tp := range e.tg.Topics(chatID) {
				t.Logf("topic %d: %q", tp.ThreadID, tp.Name)
			}
		}
	})

	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	code, err := daemon.NewPairingCode(context.Background(), st, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	e.rx = rex.New(rexBin, rex.WithServer("unix://"+sock))
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- daemon.Run(ctx, daemon.Options{
			Config:   cfg,
			Token:    e.tg.Token(),
			Home:     home,
			Logger:   slog.New(slog.NewTextHandler(&tlog{t: t}, &slog.HandlerOptions{Level: slog.LevelDebug})),
			Rex:      e.rx,
			Telegram: e.tg.Client(),
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

	e.tg.PushMessage(chatID, 0, ownerID, "/start "+code)
	eventually(t, 15*time.Second, "pairing confirmation", func() bool {
		return botMessageContaining(e.tg, chatID, 0, "Paired") != nil
	})

	// The script runs the agent, waits for the go file, then runs the same
	// session again. The folder has no spaces (a temp folder).
	agentLog := filepath.Join(ctl, "agent.jsonl")
	run := fmt.Sprintf("%s -home %s -spool %s -log %s -think %s -format pi -session-id %s -seed 'earlier turn' -exit-file %s",
		piBin, home, cfg.SpoolDir(), agentLog, think, stopSessionID, e.exitFile)
	script := filepath.Join(ctl, "pane.sh")
	body := fmt.Sprintf("#!/bin/sh\nset -m\n%s\nwhile [ ! -f %s ]; do sleep 0.2; done\nrm -f %s\n%s\nsleep 600\n", run, e.goFile, e.exitFile, run)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := rexCmd(sock, "new", "e2e", "--json", "-c", project, "--shell=none", "--", "sh", script).CombinedOutput(); err != nil {
		t.Fatalf("rex new: %v\n%s", err, out)
	}

	var openBtn string
	var listID int64
	eventually(t, 40*time.Second, "/agents to list the pane with an Open button", func() bool {
		before := len(e.tg.Messages(chatID))
		e.tg.PushMessage(chatID, 0, ownerID, "/agents")
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			for _, m := range e.tg.Messages(chatID)[before:] {
				if !m.FromBot || m.Markup == nil || strings.Contains(m.Text, "waiting for session") {
					continue
				}
				for _, row := range m.Markup.InlineKeyboard {
					for _, b := range row {
						if strings.HasPrefix(b.Text, "Open") && strings.HasPrefix(b.CallbackData, "o:") {
							openBtn, listID = b.CallbackData, m.ID
							return true
						}
					}
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		return false
	})
	e.tg.PushCallback(chatID, 0, ownerID, listID, openBtn)
	eventually(t, 20*time.Second, "topic creation", func() bool {
		for _, tp := range e.tg.Topics(chatID) {
			if !tp.Deleted {
				e.thread = tp.ThreadID
				return true
			}
		}
		return false
	})
	eventually(t, 20*time.Second, "intro card in the topic", func() bool {
		return botMessageContaining(e.tg, chatID, e.thread, "Linked:") != nil
	})
	return e
}

func (e *stopEnv) topicName() string {
	for _, tp := range e.tg.Topics(chatID) {
		if tp.ThreadID == e.thread {
			return tp.Name
		}
	}
	return ""
}

func (e *stopEnv) waitBot(sub string) *tgtest.Message {
	e.t.Helper()
	var m *tgtest.Message
	eventually(e.t, 30*time.Second, fmt.Sprintf("a bot message with %q", sub), func() bool {
		m = botMessageContaining(e.tg, chatID, e.thread, sub)
		return m != nil
	})
	return m
}

func (e *stopEnv) waitName(red bool) {
	e.t.Helper()
	eventually(e.t, 20*time.Second, fmt.Sprintf("topic name with red mark = %v (now %q)", red, e.topicName()), func() bool {
		return strings.HasPrefix(e.topicName(), "🔴 ") == red && e.topicName() != ""
	})
}

func (e *stopEnv) touch(path string) {
	e.t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *stopEnv) countBot(sub string) int {
	n := 0
	for _, m := range e.tg.Messages(chatID) {
		if m.FromBot && !m.Deleted && m.ThreadID == e.thread && strings.Contains(m.Text, sub) {
			n++
		}
	}
	return n
}

// The agent exits while its pane stays open, in the middle of a turn.
func TestStopAgentExitedMidTurn(t *testing.T) {
	t.Parallel()
	e := newStopEnv(t, 60*time.Second)
	if n := e.topicName(); strings.HasPrefix(n, "🔴") {
		t.Fatalf("topic starts with the stopped mark: %q", n)
	}

	e.tg.PushMessage(chatID, e.thread, ownerID, "a long task")
	var progress tgtest.Message
	eventually(t, 30*time.Second, "the progress message of the running turn", func() bool {
		for _, m := range e.tg.Messages(chatID) {
			if m.FromBot && !m.Deleted && m.ThreadID == e.thread && strings.Contains(m.Text, "⏱") {
				progress = m
				return true
			}
		}
		return false
	})

	e.touch(e.exitFile)
	e.waitBot("stopped: it exited, and the pane is still open.")
	if n := e.countBot("🔴 pi stopped: it exited, and the pane is still open."); n != 1 {
		t.Errorf("%d exit notices, want 1", n)
	}
	e.waitName(true)
	eventually(t, 20*time.Second, "the progress message to become a Stopped summary", func() bool {
		for _, m := range e.tg.Messages(chatID) {
			if m.ID == progress.ID {
				return !m.Deleted && strings.HasPrefix(m.Text, "🔴 Stopped") && !strings.Contains(m.Text, "⏱")
			}
		}
		return false
	})
}

// The Rex pane is closed.
func TestStopPaneClosed(t *testing.T) {
	t.Parallel()
	e := newStopEnv(t, 2*time.Second)

	ctx := context.Background()
	sess, err := e.rx.Sessions(ctx)
	if err != nil || len(sess) != 1 {
		t.Fatalf("sessions: %v %v", sess, err)
	}
	blocks, err := e.rx.Blocks(ctx, sess[0].ID)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("blocks: %v %v", blocks, err)
	}
	out, err := rexCmd(e.sock, "block", "close", "-s", string(sess[0].ID), string(blocks[0].ID)).CombinedOutput()
	if err != nil {
		t.Fatalf("rex block close: %v\n%s", err, out)
	}

	e.waitBot("🔴 pi stopped: its Rex pane was closed.")
	e.waitName(true)
	if n := e.countBot("stopped:"); n != 1 {
		t.Errorf("%d stop notices, want 1", n)
	}
}

// The same session runs again in the same pane.
func TestStopThenRunningAgain(t *testing.T) {
	t.Parallel()
	e := newStopEnv(t, 2*time.Second)

	e.touch(e.exitFile)
	e.waitBot("stopped: it exited, and the pane is still open.")
	e.waitName(true)

	e.touch(e.goFile)
	e.waitBot("🟢 pi is running again.")
	e.waitName(false)
	time.Sleep(2 * time.Second)
	if n := e.countBot("is running again"); n != 1 {
		t.Errorf("%d running-again notices, want 1", n)
	}
	if n := e.countBot("stopped:"); n != 1 {
		t.Errorf("%d stop notices, want 1", n)
	}
}
