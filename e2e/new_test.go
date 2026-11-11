//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/config"
	"rex-tg/internal/daemon"
	"rex-tg/internal/rex"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram/tgtest"
)

// TestNewAgent starts the dummy pi agent twice with /new in a real, isolated
// Rex server. The first start makes the Rex session named after the folder,
// through the buttons and a typed path. The second, `/new pi <folder>`, adds
// a tab to that session. Each agent gets its own topic, and the first one
// answers a prompt. The folder's name has a space, which must survive the
// shell that starts the agent.
func TestNewAgent(t *testing.T) {
	t.Parallel()
	piBin := program(t, "./dummyagent", "pi")
	sock := startRex(t, "new")

	home := t.TempDir()
	stateDir := t.TempDir()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(base, "my app")
	if err := os.Mkdir(project, 0o755); err != nil {
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
	cfg.Agents.Pi.Launch = []string{piBin, "-home", home, "-spool", cfg.SpoolDir(), "-log", agentLog, "-think", "1s", "-format", "pi"}

	tg := tgtest.New(t)
	t.Cleanup(func() {
		if t.Failed() {
			logChat(t, tg, chatID)
			for _, c := range tg.Calls("answerCallbackQuery") {
				t.Logf("answerCallbackQuery: %v", c.Params)
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

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- daemon.Run(ctx, daemon.Options{
			Config:   cfg,
			Token:    tg.Token(),
			Home:     home,
			Logger:   slog.New(slog.NewTextHandler(&tlog{t: t}, &slog.HandlerOptions{Level: slog.LevelDebug})),
			Rex:      rex.New(rexBin, rex.WithServer("unix://"+sock)),
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

	// First agent: the buttons, then a typed path.
	tg.PushMessage(chatID, 0, ownerID, "/new")
	var q *tgtest.Message
	eventually(t, 15*time.Second, "the agent question", func() bool {
		q = botMessageContaining(tg, chatID, 0, "Which coding agent?")
		return q != nil
	})
	pi := ""
	for _, row := range q.Markup.InlineKeyboard {
		for _, b := range row {
			if b.Text == "pi" {
				pi = b.CallbackData
			}
		}
	}
	if pi == "" {
		t.Fatalf("no pi button: %+v", q.Markup)
	}
	tg.PushCallback(chatID, 0, ownerID, q.ID, pi)
	eventually(t, 15*time.Second, "the folder question", func() bool {
		return botMessageContaining(tg, chatID, 0, "Which folder?") != nil
	})
	tg.PushMessage(chatID, 0, ownerID, project)
	first := waitTopics(t, tg, 1)
	eventually(t, 20*time.Second, "the intro card of the first agent", func() bool {
		return botMessageContaining(tg, chatID, first, "Linked:") != nil
	})
	if m := botMessageContaining(tg, chatID, 0, "Started pi"); m == nil || !strings.Contains(m.Text, "new Rex session") {
		t.Errorf("start note: %+v", m)
	}

	tg.PushMessage(chatID, first, ownerID, "hello from the phone")
	eventually(t, 30*time.Second, "the agent's answer", func() bool {
		return botMessageContaining(tg, chatID, first, "ECHO: hello from the phone") != nil
	})

	// Second agent in the same folder: a new tab of the same session.
	tg.PushMessage(chatID, 0, ownerID, "/new pi "+project)
	second := waitTopics(t, tg, 2)
	eventually(t, 20*time.Second, "the intro card of the second agent", func() bool {
		return botMessageContaining(tg, chatID, second, "Linked:") != nil
	})
	eventually(t, 10*time.Second, "the note about the new tab", func() bool {
		return botMessageContaining(tg, chatID, 0, "a new tab of the Rex session") != nil
	})

	// Rex has one session named after the folder, with a "pi" tab for each agent.
	var ls struct {
		Sessions []rex.Session `json:"sessions"`
	}
	if out, err := rexCmd(sock, "ls", "--json").Output(); err != nil || json.Unmarshal(out, &ls) != nil {
		t.Fatalf("rex ls: %v\n%s", err, out)
	}
	if len(ls.Sessions) != 1 || ls.Sessions[0].Label != "my app" {
		t.Fatalf("sessions: %+v, want one named \"my app\"", ls.Sessions)
	}
	var wins struct {
		Windows []struct {
			Label string `json:"label"`
		} `json:"windows"`
	}
	out, err := rexCmd(sock, "window", "ls", "-s", ls.Sessions[0].ID, "--json").Output()
	if err != nil || json.Unmarshal(out, &wins) != nil {
		t.Fatalf("rex window ls: %v\n%s", err, out)
	}
	if len(wins.Windows) != 2 || wins.Windows[0].Label != "pi" || wins.Windows[1].Label != "pi" {
		t.Fatalf("tabs: %+v, want two named pi", wins.Windows)
	}
}

// waitTopics waits until the chat has n topics and returns the newest one.
func waitTopics(t *testing.T, tg *tgtest.Server, n int) int64 {
	t.Helper()
	var newest int64
	eventually(t, 40*time.Second, fmt.Sprintf("%d topics", n), func() bool {
		ts := tg.Topics(chatID)
		if len(ts) < n {
			return false
		}
		newest = ts[len(ts)-1].ThreadID
		return true
	})
	return newest
}
