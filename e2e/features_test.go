//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"errors"
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

// TestFeatureTour drives the features around a linked agent, in a real,
// isolated Rex server with a Claude-format dummy agent in the session
// "rex-tg": the tab name, the Mac hold, the live /agents list, /model and
// /effort, the reminders of an open permission question, and the restart
// after an update of the binary, which keeps the topic linked.
func TestFeatureTour(t *testing.T) {
	t.Parallel()
	agentBin := program(t, "./dummyagent", "claude")
	sock := startRex(t, "tour")
	rx := rex.New(rexBin, rex.WithServer("unix://"+sock))
	ctx := context.Background()

	home := t.TempDir()
	stateDir := t.TempDir()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(base, "shop")
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
	cfg.Alerts.RemindAfter = config.Duration{Duration: 3 * time.Second}
	cfg.Alerts.Reminders = 2

	// The binary that "launchd" starts. Replacing it is the update.
	self := filepath.Join(t.TempDir(), "rex-tg")
	if err := os.WriteFile(self, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}

	tg := tgtest.New(t)
	t.Cleanup(func() {
		if t.Failed() {
			logChat(t, tg, chatID)
			for _, e := range readAgentLog(agentLog) {
				t.Logf("agent log: %+v", e)
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
	code, err := daemon.NewPairingCode(ctx, st, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	run := func() (context.CancelFunc, chan error) {
		c, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- daemon.Run(c, daemon.Options{
				Config:   cfg,
				Token:    tg.Token(),
				Home:     home,
				Logger:   slog.New(slog.NewTextHandler(&tlog{t: t}, &slog.HandlerOptions{Level: slog.LevelDebug})),
				Rex:      rx,
				Telegram: tg.Client(),
				Timing:   daemon.Timing{UpdateCheck: time.Second, RemindCheck: 500 * time.Millisecond},
				SelfPath: self,
			})
		}()
		return cancel, done
	}
	cancel, done := run()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, daemon.ErrUpdated) {
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

	// The agent runs in a tab of the Rex session "rex-tg", which rex-tg names.
	if out, err := rexCmd(sock, "new", "rex-tg", "--json", "-c", project, "--shell=none", "--",
		agentBin, "-home", home, "-spool", cfg.SpoolDir(), "-log", agentLog, "-think", "2s",
		"-format", "claude", "-seed", "earlier turn", "-echo-delay", "300ms").CombinedOutput(); err != nil {
		t.Fatalf("rex new: %v\n%s", err, out)
	}
	sessions, err := rx.Sessions(ctx)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions: %+v %v", sessions, err)
	}
	sid := sessions[0].ID
	blocks, err := rx.Blocks(ctx, sid)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("blocks: %+v %v", blocks, err)
	}
	bid := blocks[0].ID

	// /agents: the live list, with an Open button.
	var board tgtest.Message
	var openBtn string
	eventually(t, 40*time.Second, "/agents with an Open button", func() bool {
		before := len(tg.Messages(chatID))
		tg.PushMessage(chatID, 0, ownerID, "/agents")
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			for _, m := range tg.Messages(chatID)[before:] {
				if !m.FromBot || m.Markup == nil || strings.Contains(m.Text, "waiting for session") {
					continue
				}
				for _, row := range m.Markup.InlineKeyboard {
					for _, b := range row {
						if strings.HasPrefix(b.CallbackData, "o:") {
							board, openBtn = m, b.CallbackData
							return true
						}
					}
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		return false
	})
	tg.PushCallback(chatID, 0, ownerID, board.ID, openBtn)
	var thread int64
	eventually(t, 20*time.Second, "the topic", func() bool {
		for _, tp := range tg.Topics(chatID) {
			if !tp.Deleted {
				thread = tp.ThreadID
				return true
			}
		}
		return false
	})
	eventually(t, 20*time.Second, "the intro card", func() bool {
		return botMessageContaining(tg, chatID, thread, "Linked:") != nil
	})
	boardText := func() string {
		for _, m := range tg.Messages(chatID) {
			if m.ID == board.ID {
				return m.Text
			}
		}
		return ""
	}
	message := func(id int64) tgtest.Message {
		for _, m := range tg.Messages(chatID) {
			if m.ID == id {
				return m
			}
		}
		return tgtest.Message{}
	}

	// Tab name: folder and agent.
	eventually(t, 20*time.Second, `the tab named "shop · claude"`, func() bool {
		ws, err := rx.Windows(ctx, sid)
		return err == nil && len(ws) == 1 && ws[0].Label == "shop · claude"
	})
	t.Logf("tour: tab name ok")

	// The user types in the pane on the Mac: one silent notice, no answer.
	macPrompt := "fix the login page on the mac"
	if err := rx.Paste(ctx, sid, bid, macPrompt); err != nil {
		t.Fatal(err)
	}
	if err := rx.SendKeys(ctx, sid, bid, "enter"); err != nil {
		t.Fatal(err)
	}
	var notice tgtest.Message
	eventually(t, 20*time.Second, "the Mac notice", func() bool {
		m := botMessageContaining(tg, chatID, thread, "In use on the Mac")
		if m != nil {
			notice = *m
		}
		return m != nil
	})
	if !notice.Silent || !strings.Contains(notice.Text, macPrompt) {
		t.Errorf("notice = %+v, want a silent notice with the prompt", notice)
	}
	eventually(t, 20*time.Second, "the board shows the Mac", func() bool {
		return strings.Contains(boardText(), "on the Mac")
	})
	eventually(t, 20*time.Second, "the notice says done, with the Last answer button", func() bool {
		m := message(notice.ID)
		return strings.Contains(m.Text, "done") && m.Markup != nil
	})
	if botMessageContaining(tg, chatID, thread, "ECHO: "+macPrompt) != nil {
		t.Errorf("the held answer was posted")
	}
	t.Logf("tour: Mac notice %q", message(notice.ID).Text)
	t.Logf("tour: board during the hold %q", boardText())

	// "📄 Last answer" posts the held answer.
	n := message(notice.ID)
	tg.PushCallback(chatID, thread, ownerID, n.ID, n.Markup.InlineKeyboard[0][0].CallbackData)
	eventually(t, 20*time.Second, "the held answer", func() bool {
		return botMessageContaining(tg, chatID, thread, "ECHO: "+macPrompt) != nil
	})

	// A Telegram message ends the hold.
	say := func(text string) {
		t.Helper()
		tg.PushMessage(chatID, thread, ownerID, text)
		eventually(t, 30*time.Second, "the answer to "+text, func() bool {
			return botMessageContaining(tg, chatID, thread, "ECHO: "+text) != nil
		})
	}
	say("hello from telegram")
	eventually(t, 20*time.Second, "the notice loses its button", func() bool { return message(notice.ID).Markup == nil })
	eventually(t, 20*time.Second, "the board drops the Mac mark", func() bool {
		return !strings.Contains(boardText(), "on the Mac")
	})

	// /model: a card; a tap types the command once.
	tg.PushMessage(chatID, thread, ownerID, "/model")
	var card tgtest.Message
	eventually(t, 20*time.Second, "the model card", func() bool {
		m := botMessageContaining(tg, chatID, thread, "Tap a value")
		if m != nil {
			card = *m
		}
		return m != nil
	})
	var sonnet string
	for _, row := range card.Markup.InlineKeyboard {
		for _, b := range row {
			if strings.Contains(b.Text, "sonnet") {
				sonnet = b.CallbackData
			}
		}
	}
	if sonnet == "" {
		t.Fatalf("no sonnet button on %+v", card.Markup)
	}
	tg.PushCallback(chatID, thread, ownerID, card.ID, sonnet)
	tg.PushCallback(chatID, thread, ownerID, card.ID, sonnet) // a quick second tap
	eventually(t, 30*time.Second, "/model sonnet typed", func() bool {
		_, ok := findEntry(agentLog, "prompt", "/model sonnet")
		return ok
	})
	eventually(t, 30*time.Second, "its answer", func() bool {
		return botMessageContaining(tg, chatID, thread, "ECHO: /model sonnet") != nil
	})
	tg.PushMessage(chatID, thread, ownerID, "/effort high")
	eventually(t, 30*time.Second, "/effort high typed", func() bool {
		_, ok := findEntry(agentLog, "prompt", "/effort high")
		return ok
	})
	eventually(t, 30*time.Second, "its answer", func() bool {
		return botMessageContaining(tg, chatID, thread, "ECHO: /effort high") != nil
	})
	tg.PushMessage(chatID, thread, ownerID, "/model")
	eventually(t, 20*time.Second, "a card with the new values", func() bool {
		n := 0
		for _, m := range tg.Messages(chatID) {
			if m.FromBot && m.ThreadID == thread && strings.Contains(m.Text, "Tap a value") {
				n++
				if n == 2 {
					card = m
				}
			}
		}
		return n == 2
	})
	t.Logf("tour: second model card %q", card.Text)

	// A permission question: an alert, reminders, and the answer.
	ask := "ask: may I run the tests?"
	tg.PushMessage(chatID, thread, ownerID, ask)
	var alert tgtest.Message
	eventually(t, 30*time.Second, "the alert", func() bool {
		m := botMessageContaining(tg, chatID, thread, "Permission needed")
		if m != nil {
			alert = *m
		}
		return m != nil && m.Markup != nil
	})
	eventually(t, 20*time.Second, "the board says waiting", func() bool {
		return strings.Contains(boardText(), "waiting for you")
	})
	t.Logf("tour: board while waiting %q", boardText())
	reminders := func() (all []tgtest.Message) {
		for _, m := range tg.Messages(chatID) {
			if m.FromBot && m.ReplyTo == alert.ID {
				all = append(all, m)
			}
		}
		return all
	}
	eventually(t, 20*time.Second, "two reminders", func() bool { return len(reminders()) == 2 })
	time.Sleep(4 * time.Second) // the most is two
	rs := reminders()
	if len(rs) != 2 {
		t.Errorf("reminders: %d, want 2", len(rs))
	}
	if !rs[0].Deleted || rs[1].Deleted {
		t.Errorf("the first reminder should be gone and the second shown: %+v", rs)
	}
	t.Logf("tour: reminder %q silent=%v", rs[1].Text, rs[1].Silent)
	var yes string
	for _, row := range alert.Markup.InlineKeyboard {
		for _, b := range row {
			if strings.HasPrefix(b.Text, "1") {
				yes = b.CallbackData
			}
		}
	}
	if yes == "" {
		t.Fatalf("no option 1 on the alert: %+v", alert.Markup)
	}
	tg.PushCallback(chatID, thread, ownerID, alert.ID, yes)
	eventually(t, 30*time.Second, "the answer after the permission", func() bool {
		return botMessageContaining(tg, chatID, thread, "ECHO: "+ask+" (answer 1)") != nil
	})
	eventually(t, 20*time.Second, "the reminder goes", func() bool {
		rs := reminders()
		return len(rs) == 2 && rs[1].Deleted
	})

	// The update: the binary is replaced. The daemon stops for launchd,
	// which starts the new one, and the topic goes on.
	tmp := self + ".new"
	if err := os.WriteFile(tmp, []byte("v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, self); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, daemon.ErrUpdated) {
			t.Fatalf("Run = %v, want ErrUpdated", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the daemon did not stop for the update")
	}
	cancel()
	t.Logf("tour: stopped for the update")
	cancel, done = run()
	say("after the update")
	if ts := tg.Topics(chatID); len(ts) != 1 || ts[0].Deleted {
		t.Errorf("topics after the update: %+v", ts)
	}

	// Each answer once, nothing lost or doubled, no warnings.
	time.Sleep(2 * time.Second)
	for _, p := range []string{macPrompt, "hello from telegram", "/model sonnet", "/effort high", ask + " (answer 1)", "after the update"} {
		got := 0
		for _, m := range tg.Messages(chatID) {
			if m.FromBot && m.ThreadID == thread && !m.Deleted && strings.Contains(m.Text, "ECHO: "+p) {
				got++
			}
		}
		if got != 1 {
			t.Errorf("answer to %q: %d copies, want 1", p, got)
		}
	}
	var typed []string
	for _, e := range readAgentLog(agentLog) {
		if e.Ev == "prompt" {
			typed = append(typed, e.Text)
		}
	}
	want := []string{macPrompt, "hello from telegram", "/model sonnet", "/effort high", ask, "after the update"}
	if fmt.Sprint(typed) != fmt.Sprint(want) {
		t.Errorf("prompts in the pane: %q, want %q", typed, want)
	}
	for _, m := range tg.Messages(chatID) {
		if m.FromBot && !m.Deleted && (strings.Contains(m.Text, "may not have arrived") || strings.Contains(m.Text, "⚠️")) {
			t.Errorf("a warning in the chat: %q", m.Text)
		}
		if !m.FromBot && strings.Contains(m.Reaction, "👎") {
			t.Errorf("👎 on %q", m.Text)
		}
	}

	// The tour, for a person to read.
	var b strings.Builder
	for _, m := range tg.Messages(chatID) {
		if m.ThreadID != thread && m.ID != board.ID {
			continue
		}
		who := "you"
		if m.FromBot {
			who = "bot"
		}
		extra := ""
		if m.Markup != nil {
			var btns []string
			for _, row := range m.Markup.InlineKeyboard {
				for _, x := range row {
					btns = append(btns, x.Text)
				}
			}
			extra = fmt.Sprintf(" buttons=%q", btns)
		}
		fmt.Fprintf(&b, "  #%d %s silent=%v deleted=%v edits=%d react=%q%s: %q\n", m.ID, who, m.Silent, m.Deleted, m.Edits, m.Reaction, extra, m.Text)
	}
	t.Logf("tour: chat at the end:\n%s", b.String())
	var cbs []string
	for _, c := range tg.Calls("answerCallbackQuery") {
		if s, _ := c.Params["text"].(string); s != "" {
			cbs = append(cbs, s)
		}
	}
	js, _ := json.Marshal(cbs)
	t.Logf("tour: button answers %s", js)
}
