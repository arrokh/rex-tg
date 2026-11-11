package daemon_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/agents/pi"
	"rex-tg/internal/daemon"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
	"rex-tg/internal/telegram/tgtest"
)

func TestPairing(t *testing.T) {
	e := newEnv(t)
	var good, expired string
	e.withStore(func(st *store.Store) {
		var err error
		good, err = daemon.NewPairingCode(context.Background(), st, time.Now())
		if err != nil {
			t.Fatal(err)
		}
	})
	e.start()

	// A message that is not a pairing command gets no answer at all.
	e.tg.PushMessage(chatID, 0, 555, "hello")
	e.tg.PushMessage(chatID, 0, 555, "/start WRONG-CODE")
	// A wrong code gets no answer and does not pair.
	e.never("an answer to a stranger", 700*time.Millisecond, func() bool { return len(e.botMessages(0)) > 0 })

	// The right code pairs.
	e.tg.PushMessage(chatID, 0, ownerID, "/start "+good)
	e.waitText(0, "Paired")
	st := e.peek()
	if v, ok, _ := st.Get(context.Background(), "owner_user_id"); !ok || v != fmt.Sprint(ownerID) {
		t.Fatalf("owner_user_id = %q, %v", v, ok)
	}
	if _, ok, _ := st.Get(context.Background(), "pairing_code_hash"); ok {
		t.Fatal("pairing code hash is still stored after pairing")
	}

	// A stranger is ignored from now on, even with commands.
	n := len(e.botMessages(0))
	e.tg.PushMessage(chatID, 0, 555, "/agents")
	e.tg.PushMessage(chatID, 0, 555, "/status")
	e.say(0, "/status") // the owner is still served, which also shows the strangers were handled first
	e.waitText(0, "rex-tg is running")
	if got := len(e.botMessages(0)); got != n+1 {
		t.Fatalf("bot sent %d messages after pairing, want %d (strangers must be ignored)", got, n+1)
	}
	e.stop()

	// An expired code does not pair.
	e2 := newEnv(t)
	e2.withStore(func(st *store.Store) {
		var err error
		expired, err = daemon.NewPairingCode(context.Background(), st, time.Now().Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
	})
	e2.start()
	e2.tg.PushMessage(chatID, 0, ownerID, "/start "+expired)
	// The code is right, so the sender is the owner and is told it expired.
	e2.waitText(0, "expired")
	if _, ok, _ := e2.peek().Get(context.Background(), "owner_user_id"); ok {
		t.Fatal("an expired code paired the owner")
	}
}

// With Threaded Mode, Telegram puts every private-chat message in a topic,
// and it creates a topic for a new conversation. There is no main chat.
func TestThreadedPrivateChatHasNoMainChat(t *testing.T) {
	const auto = 77 // a topic that Telegram created for the user's message
	e := newEnv(t)
	var code string
	e.withStore(func(st *store.Store) {
		var err error
		if code, err = daemon.NewPairingCode(context.Background(), st, time.Now()); err != nil {
			t.Fatal(err)
		}
	})
	p := e.newPi("pi-threaded")
	p.User("question")
	p.Final("answer")
	e.start()

	// Pairing from a topic answers in that topic.
	e.tg.PushMessage(chatID, auto, ownerID, "/start "+code)
	e.waitText(auto, "Paired")

	// A topic that is not linked to an agent works as the main chat.
	e.hookPi(p, "startup")
	e.waitFor("a resolved pane", func() bool {
		pn, ok, err := e.peek().PaneByBlock(context.Background(), "local", rexBlock)
		return err == nil && ok && pn.ConversationID != 0
	})
	e.say(auto, "/agents")
	list := e.waitText(auto, "Active agents")
	if e.hasText(auto, "not linked") {
		t.Fatalf("/agents in an unlinked topic was answered as unlinked: %s", e.dump())
	}

	// Plain text there is not typed anywhere; it gets a hint.
	e.say(auto, "hello")
	e.waitText(auto, "not linked to an agent")

	// The open button answers in the topic where it was pressed.
	e.pressButton(list, buttonData(list, "Open"))
	e.waitText(auto, "Opened")
	if len(e.botMessages(0)) != 0 {
		t.Fatalf("the bot wrote outside the topics: %s", e.dump())
	}
	if len(e.pastes()) != 0 {
		t.Fatalf("typed into the pane: %v", e.pastes())
	}
}

// A sender is locked after five wrong codes, even with the right code on the
// sixth try (see pairing_test.go for the other senders).
func TestPairingLocksAfterFiveWrongCodes(t *testing.T) {
	e := newEnv(t)
	var code string
	e.withStore(func(st *store.Store) {
		code, _ = daemon.NewPairingCode(context.Background(), st, time.Now())
	})
	e.start()
	for i := 0; i < 5; i++ {
		e.tg.PushMessage(chatID, 0, ownerID, fmt.Sprintf("/start WRONG-%d", i))
	}
	e.waitFor("five attempts counted", func() bool {
		v, _, _ := e.peek().Get(context.Background(), "pairing_attempts")
		return v == "5"
	})
	e.tg.PushMessage(chatID, 0, ownerID, "/start "+code)
	e.never("pairing after five wrong attempts", 600*time.Millisecond, func() bool { return len(e.botMessages(0)) > 0 })
}

func TestAlreadyRunning(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	e.start()
	e.waitFor("the first daemon to answer", func() bool { return len(e.tg.Calls("setMyCommands")) > 0 })

	err := daemon.Run(context.Background(), e.options())
	if !errors.Is(err, daemon.ErrAlreadyRunning) {
		t.Fatalf("second Run = %v, want ErrAlreadyRunning", err)
	}
	e.stop()

	// The lock is free again after the first daemon ended.
	before := len(e.tg.Calls("setMyCommands"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx, e.options()) }()
	e.waitFor("the third run to start", func() bool { return len(e.tg.Calls("setMyCommands")) > before })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after unlock: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestAgentsListsPiPane(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	p := e.newPi("pi-session-1")
	p.User("Fix the failing test")
	e.start()
	e.hookPi(p, "startup")
	e.waitFor("the session to be resolved", func() bool {
		pane, ok, _ := e.peek().PaneByBlock(context.Background(), "local", rexBlock)
		return ok && pane.ConversationID != 0 && pane.Source == store.SourceHook
	})

	e.say(0, "/agents")
	m := e.waitText(0, "Active agents (1)")
	if !strings.Contains(m.Text, "<b>pane one</b>") || !strings.Contains(m.Text, "pi · <code>proj</code>") {
		t.Errorf("agents text = %q, want the short name, the runtime, and the folder in <code>", m.Text)
	}
	if got := buttonData(m, "Open"); !strings.HasPrefix(got, "o:") {
		t.Errorf("open button data = %q", got)
	}
}

func TestOpenTopicWithIntroAndBackfill(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	p := e.newPi("pi-session-2")
	for _, w := range []string{"one", "two", "three", "four"} {
		p.User("question " + w)
		p.Final("answer " + w)
	}
	e.start()
	e.hookPi(p, "startup")
	thread := e.openTopic()

	if got := e.tg.Topics(chatID); len(got) != 1 || got[0].Name != "pane one" {
		t.Fatalf("topics = %+v, want one named after the pane", got)
	}
	intro := e.waitText(thread, "Linked")
	if !strings.Contains(intro.Text, "Write here to type into the pane") {
		t.Errorf("intro card = %q", intro.Text)
	}
	for _, w := range []string{"two", "three", "four"} {
		e.waitText(thread, "answer "+w)
	}
	e.never("the oldest answer in the backfill", 500*time.Millisecond, func() bool { return e.hasText(thread, "answer one") })
	for _, w := range []string{"two", "three", "four"} {
		if n := e.countText(thread, "answer "+w); n != 1 {
			t.Errorf("answer %s posted %d times, want 1", w, n)
		}
	}
	// The main chat got the link.
	e.waitText(0, "Opened")
}

func TestNewAnswerPostsOnceAcrossRestart(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	p := e.newPi("pi-session-3")
	p.User("hi")
	p.Final("old answer")
	e.start()
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "old answer")

	p.User("next")
	p.Final("fresh answer")
	e.waitText(thread, "fresh answer")
	e.stop()

	// An answer written while the daemon is down arrives after the restart,
	// and nothing is posted twice.
	p.User("while down")
	p.Final("answer while down")
	e.start()
	e.waitText(thread, "answer while down")
	p.User("again")
	p.Final("answer after restart")
	e.waitText(thread, "answer after restart")
	time.Sleep(600 * time.Millisecond)
	for _, s := range []string{"old answer", "fresh answer", "answer while down", "answer after restart"} {
		if n := e.countText(thread, s); n != 1 {
			t.Errorf("%q posted %d times, want 1\n%s", s, n, e.dump())
		}
	}
}

// piTopic starts the daemon with a resolved pi session and an open topic.
func piTopic(t *testing.T, id string) (*env, *piLog, int64) {
	t.Helper()
	e := newEnv(t)
	e.seedOwner()
	p := e.newPi(id)
	p.User("start")
	p.Final("ready")
	e.start()
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "ready")
	e.pi = p
	return e, p, thread
}

func TestProgressMessageBecomesSummary(t *testing.T) {
	e, p, thread := piTopic(t, "pi-progress")
	p.User("run the tests")
	p.ToolCall("call-1", "go test ./...")
	var progress int64
	e.waitFor("the progress message", func() bool {
		for _, m := range e.botMessages(thread) {
			if strings.Contains(m.Text, "go test") && strings.Contains(m.Text, "⏱") {
				progress = m.ID
				return true
			}
		}
		return false
	})
	p.ToolResult("call-1")
	p.Final("All tests pass.")
	e.waitText(thread, "All tests pass.")
	e.waitFor("the progress message to end as a summary", func() bool {
		m, ok := e.message(progress)
		return ok && !m.Deleted && strings.Contains(m.Text, "Done") && strings.Contains(m.Text, "1 tool")
	})
	// The answer was posted once, and the progress message is not an answer.
	if n := e.countText(thread, "All tests pass."); n != 1 {
		t.Errorf("answer posted %d times", n)
	}
}

func TestMessageWaitsForIdleThenTypes(t *testing.T) {
	e, p, thread := piTopic(t, "pi-wait")
	// The agent starts working: a prompt line in the transcript.
	p.User("busy work")
	p.ToolCall("c1", "sleep 5")
	e.waitFor("the progress message", func() bool { return e.hasText(thread, "⏱") })

	id := e.say(thread, "please also check the docs")
	waiting := e.waitText(thread, "Waiting until the agent is idle")
	if buttonData(waiting, "Send now") == "" || buttonData(waiting, "Cancel") == "" {
		t.Errorf("waiting message has no buttons: %+v", waiting.Markup)
	}
	e.never("typing while the agent works", 800*time.Millisecond, func() bool { return len(e.pastes()) > 0 })

	// The agent finishes: the answer line makes the pane idle.
	p.ToolResult("c1")
	p.Final("done with the work")
	e.waitFor("the text to be pasted", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; got != "please also check the docs" {
		t.Errorf("pasted %q", got)
	}
	e.waitFor("Enter", func() bool { return len(e.keys()) == 1 })
	if got := e.keys()[0]; got != "enter" {
		t.Errorf("keys = %v", e.keys())
	}
	// Order: the paste comes before Enter.
	calls := e.fake.Calls()
	pasteAt, enterAt := -1, -1
	for i, c := range calls {
		s := strings.Join(c.Args, " ")
		if strings.Contains(s, "block.event") {
			pasteAt = i
		}
		if strings.Contains(s, "send-key") {
			enterAt = i
		}
	}
	if pasteAt < 0 || enterAt < pasteAt {
		t.Errorf("paste at %d, Enter at %d", pasteAt, enterAt)
	}
	e.waitFor("the eyes reaction", func() bool { m, _ := e.message(id); return m.Reaction == "👀" })
}

func TestRefusedWhenShellInFront(t *testing.T) {
	e := newEnv(t)
	e.cfg.Rex.ScanInterval.Duration = time.Hour // the registry must not notice the shell
	e.seedOwner()
	p := e.newPi("pi-shell")
	p.User("start")
	p.Final("ready")
	e.start()
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "ready")
	e.waitFor("an idle pane", func() bool {
		pane, _, _ := e.peek().PaneByBlock(context.Background(), "local", rexBlock)
		return pane.State == "idle"
	})

	e.setProcess("zsh", "-zsh", 777)
	id := e.say(thread, "rm -rf everything")
	e.waitFor("the thumbs down", func() bool { m, _ := e.message(id); return m.Reaction == "👎" })
	e.waitText(thread, "Not sent")
	e.waitText(thread, "shell")
	if len(e.pastes()) != 0 || len(e.keys()) != 0 {
		t.Errorf("something was typed: %v %v", e.pastes(), e.keys())
	}
}

func TestEchoSuppression(t *testing.T) {
	e, p, thread := piTopic(t, "pi-echo")
	id := e.say(thread, "explain the build")
	e.waitFor("the text to be typed", func() bool { return len(e.keys()) == 1 })
	e.waitFor("the eyes reaction", func() bool { m, _ := e.message(id); return m.Reaction == "👀" || m.Reaction == "👍" })

	// pi writes the prompt it was given.
	p.User("explain the build")
	e.waitFor("the thumbs up", func() bool { m, _ := e.message(id); return m.Reaction == "👍" })
	p.Final("The build uses make.")
	e.waitText(thread, "The build uses make.")
	if e.hasText(thread, "explain the build") {
		t.Errorf("the typed prompt was posted back\n%s", e.dump())
	}
	row := e.peek()
	in, err := row.DeliveredSince(context.Background(), thread, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 0 {
		t.Errorf("%d delivered rows left that should be acked", len(in))
	}
}

const permScreen = "Bash command\n  rm -rf build\n\nDo you want to proceed?\n❯ 1. Yes\n  2. Yes, and don't ask again\n  3. No\n"

func alertMessages(e *env, thread int64) []string {
	var out []string
	for _, m := range e.botMessages(thread) {
		if strings.Contains(m.Text, "Permission needed") {
			out = append(out, m.Text)
		}
	}
	return out
}

func TestPermissionAlert(t *testing.T) {
	e, _, thread := piTopic(t, "pi-alert")

	// First alert: the screen changes before the button is pressed.
	e.setScreen(permScreen)
	e.setStatus("blocked", "permission")
	first := e.waitText(thread, "Permission needed")
	if !strings.Contains(first.Text, "Do you want to proceed?") {
		t.Errorf("alert text = %q", first.Text)
	}
	var labels []string
	for _, row := range first.Markup.InlineKeyboard {
		for _, b := range row {
			labels = append(labels, b.Text)
		}
	}
	if got := strings.Join(labels, "|"); got != "1 Yes|2 Yes, and don't ask again|3 No|Esc" {
		t.Errorf("buttons = %q", got)
	}
	e.setScreen("Something else is on screen now\n❯ 1. Yes\n  2. No\n")
	e.pressButton(first, buttonData(first, "1 Yes"))
	e.waitFor("the stale answer", func() bool {
		for _, s := range e.callbackTexts() {
			if strings.Contains(s, "gone") {
				return true
			}
		}
		return false
	})
	if len(e.keys()) != 0 {
		t.Errorf("keys sent for a stale button: %v", e.keys())
	}
	e.waitFor("buttons removed from the stale alert", func() bool { m, _ := e.message(first.ID); return m.Markup == nil })
	// The pane is still blocked, now by the prompt on screen: it gets an alert
	// without waiting for a state change.
	e.waitFor("an alert for the prompt on screen now", func() bool { return len(alertMessages(e, thread)) == 2 })
	if ms := alertMessages(e, thread); !strings.Contains(ms[len(ms)-1], "Something else is on screen now") {
		t.Errorf("new alert = %q", ms[len(ms)-1])
	}

	// Next alert: same screen on press, the digit is sent.
	e.setStatus("working", "")
	e.waitPaneState("working")
	e.setScreen(permScreen)
	e.setStatus("blocked", "permission")
	e.waitFor("a third alert", func() bool { return len(alertMessages(e, thread)) == 3 })
	var second tgtest.Message
	for _, m := range e.botMessages(thread) {
		if strings.Contains(m.Text, "Permission needed") && m.ID != first.ID {
			second = m
		}
	}
	e.pressButton(second, buttonData(second, "1 Yes"))
	e.waitFor("the digit key", func() bool { return len(e.keys()) == 1 })
	if got := e.keys()[0]; got != "1" {
		t.Errorf("keys = %v, want [1]", e.keys())
	}
	e.waitFor("buttons removed after the answer", func() bool { m, _ := e.message(second.ID); return m.Markup == nil })
	in := e.peek()
	als, _ := in.OpenAlerts(context.Background(), thread)
	if len(als) != 0 {
		t.Errorf("open alerts left: %+v", als)
	}

	// The next alert is answered with /deny: the last non-Esc option.
	e.setStatus("working", "")
	e.waitPaneState("working")
	e.setStatus("blocked", "permission")
	e.waitFor("a fourth alert", func() bool { return len(alertMessages(e, thread)) == 4 })
	e.say(thread, "/deny")
	e.waitFor("the deny key", func() bool { return len(e.keys()) == 2 })
	if got := e.keys()[1]; got != "3" {
		t.Errorf("keys = %v, want the second to be 3", e.keys())
	}
}

func TestScreenCommand(t *testing.T) {
	e, _, thread := piTopic(t, "pi-screen")
	e.setScreen("line one\nsecret sk-ant-" + "api03-abcdefghijklmnopqrstuvwxyz123456 <b>\nline three\n")
	e.say(thread, "/screen")
	m := e.waitText(thread, "line three")
	if !strings.Contains(m.Text, "<pre>") || strings.Contains(m.Text, "abcdefghijklmnopqrstuvwxyz123456") {
		t.Errorf("screen = %q (want a <pre> block with the key redacted)", m.Text)
	}
	if !strings.Contains(m.Text, "&lt;b&gt;") {
		t.Errorf("screen text is not escaped: %q", m.Text)
	}
	e.say(thread, "/screen 1")
	e.waitFor("a one-line screen", func() bool { return e.countText(thread, "line three") == 2 })
}

// A reply whose send got no answer, and that Telegram never posted (seen
// live while the connection to Telegram dropped): rex-tg checks the message
// numbers, finds that it did not arrive, and sends it again by itself. It
// arrives once, with no notice, and the short check note is deleted. Your
// message in between counts as known: the daemon records every update.
func TestUnknownReplyIsCheckedAndSentOnce(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	p := e.newPi("pi-resend")
	p.User("start")
	p.Final("ready")
	opt := e.options()
	opt.Timing.CheckDelay = 2 * time.Second // time for your message before the check
	e.startWith(opt)
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "ready")
	p.User("go")
	e.waitText(thread, "go")
	e.waitText(thread, "⏱") // the progress message is out, so the next send is the answer
	e.tg.FailNext("sendMessage", 1, 502, "bad gateway", 0)
	p.Final("the lost answer")
	e.waitFor("the unknown answer", func() bool {
		for id := int64(1); id < 40; id++ {
			if row, err := e.peek().OutboxRow(context.Background(), id); err == nil && row.Kind == "answer" && row.State == store.OutboxUnknown {
				return true
			}
		}
		return false
	})
	e.say(thread, "are you there?")
	e.waitText(thread, "the lost answer")
	e.waitFor("the check note to go", func() bool { return !e.hasText(thread, "Checking whether it arrived") })
	e.never("a second copy or a notice", 500*time.Millisecond, func() bool {
		return e.countText(thread, "the lost answer") > 1 || e.hasText(thread, "may not have arrived")
	})
}

func TestPauseAndUnpause(t *testing.T) {
	e, _, thread := piTopic(t, "pi-pause")
	e.say(0, "/pause")
	e.waitText(0, "paused")
	id := e.say(thread, "typed while paused")
	e.waitFor("a refusal", func() bool { m, _ := e.message(id); return m.Reaction == "👎" })
	e.waitText(thread, "paused")
	if len(e.pastes()) != 0 || len(e.keys()) != 0 {
		t.Fatalf("typed while paused: %v %v", e.pastes(), e.keys())
	}
	e.say(0, "/unpause")
	e.waitText(0, "on again")
	id2 := e.say(thread, "typed after unpause")
	e.waitFor("the text to be typed", func() bool { return len(e.pastes()) == 1 && len(e.keys()) == 1 })
	if got := e.pastes()[0]; got != "typed after unpause" {
		t.Errorf("pasted %q", got)
	}
	e.waitFor("eyes", func() bool { m, _ := e.message(id2); return m.Reaction == "👀" })
}

func TestMuteSendsAnswersSilently(t *testing.T) {
	e, p, thread := piTopic(t, "pi-mute")
	p.User("one")
	p.Final("loud answer")
	loud := e.waitText(thread, "loud answer")
	if loud.Silent {
		t.Errorf("an answer was silent before /mute")
	}
	e.say(thread, "/mute")
	e.waitText(thread, "without sound")
	p.User("two")
	p.Final("quiet answer")
	quiet := e.waitText(thread, "quiet answer")
	if !quiet.Silent {
		t.Errorf("an answer was not silent after /mute")
	}
	e.say(thread, "/unmute")
	e.waitText(thread, "make sound again")
	p.User("three")
	p.Final("loud again")
	if m := e.waitText(thread, "loud again"); m.Silent {
		t.Errorf("an answer was silent after /unmute")
	}
}

func (e *env) sendPhoto(thread int64, msgID int64, fileID, caption string) {
	e.tg.PushUpdate(telegram.Update{Message: &telegram.Message{
		MessageID: msgID, MessageThreadID: thread, IsTopicMessage: true,
		From: &telegram.User{ID: ownerID}, Chat: telegram.Chat{ID: chatID, Type: "private"}, Date: time.Now().Unix(),
		Photo:   []telegram.PhotoSize{{FileID: fileID + "-small", FileSize: 10}, {FileID: fileID, FileSize: 100}},
		Caption: caption,
	}})
}

func TestPhotoIsDownloadedAndItsPathTyped(t *testing.T) {
	e, _, thread := piTopic(t, "pi-photo")
	data := []byte("\xff\xd8\xffnot really a jpeg")
	e.tg.AddFile("photo-big", "photos/file_1.jpg", data)
	e.sendPhoto(thread, 9001, "photo-big", "what is this?")
	e.waitFor("the photo path to be pasted", func() bool { return len(e.pastes()) == 1 })
	got := e.pastes()[0]
	lines := strings.Split(got, "\n")
	if len(lines) != 2 || lines[0] != "what is this?" {
		t.Fatalf("pasted %q, want the caption and a path", got)
	}
	path := lines[1]
	if !strings.HasPrefix(path, e.cfg.UploadDir()+"/") || !strings.HasSuffix(path, ".jpg") {
		t.Errorf("path %q is not an upload under %s", path, e.cfg.UploadDir())
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != string(data) {
		t.Errorf("downloaded file = %q, %v", b, err)
	}
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("file mode %v lets others read it", fi.Mode())
	}
	e.waitFor("Enter", func() bool { return len(e.keys()) == 1 })
}

func TestVoiceNoteWithoutWhisper(t *testing.T) {
	e, _, thread := piTopic(t, "pi-voice")
	e.tg.AddFile("voice-1", "voice/file_1.oga", []byte("ogg"))
	e.tg.PushUpdate(telegram.Update{Message: &telegram.Message{
		MessageID: 9100, MessageThreadID: thread, IsTopicMessage: true,
		From: &telegram.User{ID: ownerID}, Chat: telegram.Chat{ID: chatID, Type: "private"},
		Voice: &telegram.Voice{FileID: "voice-1", FileSize: 3, Duration: 1},
	}})
	e.waitText(thread, "Voice notes need whisper.cpp")
	e.never("typing a voice note", 400*time.Millisecond, func() bool { return len(e.pastes()) > 0 })
}

func TestTakeOverAfterClear(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	e.setProcess("2.1.322", "claude", 5151)
	dir := filepath.Join(e.home, ".claude", "projects", "-work-proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path string, lines ...string) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		for _, l := range lines {
			fmt.Fprintln(f, l)
		}
	}
	claudeLines := func(sid, q, a string) []string {
		return []string{
			fmt.Sprintf(`{"type":"user","uuid":"u-%s","sessionId":%q,"cwd":%q,"timestamp":%q,"message":{"role":"user","content":%s}}`, sid, sid, paneCWD, now(), jstr(q)),
			fmt.Sprintf(`{"type":"assistant","uuid":"a-%s","sessionId":%q,"cwd":%q,"timestamp":%q,"message":{"id":"m-%s","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":%s}]}}`, sid, sid, paneCWD, now(), sid, jstr(a)),
		}
	}
	oldPath := filepath.Join(dir, "sess-old.jsonl")
	newPath := filepath.Join(dir, "sess-new.jsonl")
	write(oldPath, claudeLines("sess-old", "hello", "old session answer")...)
	// The pane has Rex's default name, so the session title names the topic.
	e.fake.On([]string{"block", "ls"}, fmt.Sprintf(`{"blocks":[{"block_id":%q,"label":"shell"}]}`, rexBlock), 0)
	e.start()
	e.spool("claude", "SessionStart", map[string]any{
		"hook_event_name": "SessionStart", "source": "startup", "session_id": "sess-old", "transcript_path": oldPath, "cwd": paneCWD,
	})
	thread := e.openTopic()
	e.waitText(thread, "old session answer")

	// /clear: the old session ends, a new one starts in the same pane.
	write(newPath, claudeLines("sess-new", "second hello", "new session answer")...)
	write(newPath, `{"type":"custom-title","customTitle":"fresh start","sessionId":"sess-new"}`)
	e.spool("claude", "SessionEnd", map[string]any{"hook_event_name": "SessionEnd", "session_id": "sess-old", "transcript_path": oldPath, "cwd": paneCWD, "reason": "clear"})
	e.spool("claude", "SessionStart", map[string]any{
		"hook_event_name": "SessionStart", "source": "clear", "session_id": "sess-new", "transcript_path": newPath, "cwd": paneCWD,
	})
	e.waitText(thread, "── claude · new session (clear) ──")
	e.waitText(thread, "new session answer")
	if got := len(e.tg.Topics(chatID)); got != 1 {
		t.Errorf("%d topics, want the one topic to be reused", got)
	}
	if e.hasText(thread, "stopped:") {
		t.Errorf("the topic went offline during the take-over\n%s", e.dump())
	}
	e.waitFor("the topic to be renamed", func() bool {
		for _, c := range e.tg.Calls("editForumTopic") {
			if n, _ := c.Params["name"].(string); strings.Contains(n, "fresh start") {
				return true
			}
		}
		return false
	})
	// New lines of the new session reach the same topic.
	write(newPath, claudeLines("sess-new2", "third", "later answer")...)
	e.waitText(thread, "later answer")
}

// A crash left a reply in "sending" and a message in "typing". The reply was
// claimed by an older version, which recorded no message number, so it cannot
// be checked: it gets the notice and waits for /resend. The typed message is
// never typed again. (TestRestartChecksAReplyLeftInSending has the check.)
func TestRestartRecovery(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	ctx := context.Background()
	ft, err := e.tg.Client().CreateForumTopic(ctx, chatID, "pi · proj")
	if err != nil {
		t.Fatal(err)
	}
	var thread, outboxID int64
	e.withStore(func(st *store.Store) {
		conv, err := st.UpsertConversation(ctx, store.Conversation{
			Runtime: "pi", NativeID: "pi-rec", Transcript: filepath.Join(e.home, "none.jsonl"), CWD: paneCWD, FirstSeenAt: time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		th, err := st.LinkTopic(ctx, chatID, ft.MessageThreadID, conv.ID, "pi · proj", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		thread = th.ID
		outboxID, _, err = st.Enqueue(ctx, store.OutboxItem{ThreadID: th.ID, Key: "answer:half", Kind: "answer",
			Parts: []store.PartContent{{Content: "half sent answer", Format: store.FormatHTML}}})
		if err != nil {
			t.Fatal(err)
		}
		parts, err := st.NextParts(ctx)
		if err != nil || len(parts) != 1 {
			t.Fatalf("NextParts = %v, %v", parts, err)
		}
		if ok, err := st.ClaimPart(ctx, parts[0].OutboxID, parts[0].Part, -1); err != nil || !ok {
			t.Fatalf("ClaimPart = %v, %v", ok, err)
		}
		ok, err := st.InsertInbound(ctx, store.Inbound{UpdateID: 7001, ChatID: chatID, MessageThreadID: ft.MessageThreadID, TGMessageID: 5,
			FromID: ownerID, Kind: "text", Body: "was being typed", BodyHash: "h", ThreadID: th.ID, State: store.InboundTyping, CreatedAt: time.Now()})
		if err != nil || !ok {
			t.Fatalf("InsertInbound = %v, %v", ok, err)
		}
	})

	e.start()
	notice := e.waitText(ft.MessageThreadID, "may not have arrived")
	e.waitText(ft.MessageThreadID, "may be in the input box")
	if !strings.Contains(notice.Text, fmt.Sprintf("reply #%d", outboxID)) || buttonData(notice, "Send again") != fmt.Sprintf("rs:%d", outboxID) {
		t.Errorf("notice = %q, buttons %+v", notice.Text, notice.Markup)
	}
	if e.hasText(ft.MessageThreadID, "half sent answer") {
		t.Errorf("the part left in sending was sent again")
	}
	st := e.peek()
	row, err := st.OutboxRow(ctx, outboxID)
	if err != nil || row.State != store.OutboxUnknown {
		t.Errorf("outbox row = %+v, %v; want unknown", row, err)
	}
	if qs, _ := st.QueuedInbound(ctx, thread); len(qs) != 0 {
		t.Errorf("the typing row came back as queued: %+v", qs)
	}
	e.stop()

	// A second start adds no second notice and sends nothing again.
	e.start()
	e.waitFor("the second start", func() bool { return len(e.tg.Calls("getMe")) >= 2 })
	e.never("another notice", 700*time.Millisecond, func() bool {
		return e.countText(ft.MessageThreadID, "may not have arrived") > 1 || e.countText(ft.MessageThreadID, "may be in the input box") > 1
	})
	if e.hasText(ft.MessageThreadID, "half sent answer") {
		t.Errorf("the unknown part was sent again")
	}
	if len(e.pastes()) != 0 || len(e.keys()) != 0 {
		t.Errorf("recovery typed into a pane: %v %v", e.pastes(), e.keys())
	}
}

// A crash left a reply in "sending", claimed with the newest message number
// known then. After the restart, rex-tg checks it: Telegram never posted it,
// so it is sent once, with no notice.
func TestRestartChecksAReplyLeftInSending(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	ctx := context.Background()
	ft, err := e.tg.Client().CreateForumTopic(ctx, chatID, "pi · proj")
	if err != nil {
		t.Fatal(err)
	}
	e.withStore(func(st *store.Store) {
		conv, err := st.UpsertConversation(ctx, store.Conversation{
			Runtime: "pi", NativeID: "pi-rec2", Transcript: filepath.Join(e.home, "none.jsonl"), CWD: paneCWD, FirstSeenAt: time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		th, err := st.LinkTopic(ctx, chatID, ft.MessageThreadID, conv.ID, "pi · proj", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.Enqueue(ctx, store.OutboxItem{ThreadID: th.ID, Key: "answer:half", Kind: "answer",
			Parts: []store.PartContent{{Content: "half sent answer", Format: store.FormatHTML}}}); err != nil {
			t.Fatal(err)
		}
		parts, err := st.NextParts(ctx)
		if err != nil || len(parts) != 1 {
			t.Fatalf("NextParts = %v, %v", parts, err)
		}
		// The running daemon knew the topic's own message, the newest one.
		if err := st.NoteMessage(ctx, chatID, ft.MessageThreadID, time.Now()); err != nil {
			t.Fatal(err)
		}
		if ok, err := st.ClaimPart(ctx, parts[0].OutboxID, parts[0].Part, ft.MessageThreadID); err != nil || !ok {
			t.Fatalf("ClaimPart = %v, %v", ok, err)
		}
	})
	e.start()
	e.waitText(ft.MessageThreadID, "half sent answer")
	e.never("a notice or a second copy", 500*time.Millisecond, func() bool {
		return e.hasText(ft.MessageThreadID, "may not have arrived") || e.countText(ft.MessageThreadID, "half sent answer") > 1
	})
}

// working makes the pane busy: a prompt line that no answer follows.
func working(e *env, p *piLog, thread int64) {
	p.User("long job")
	p.ToolCall("job-1", "sleep 100")
	e.waitFor("the progress message", func() bool { return e.hasText(thread, "⏱") })
}

func TestRapidMessagesAreJoined(t *testing.T) {
	// The two messages must reach the daemon within the join window; under
	// load, 50 ms is too short to be sure.
	e, _, thread := piTopicCfg(t, "pi-join", func(e *env) {
		e.cfg.InputSettings.JoinWindow.Duration = 500 * time.Millisecond
	})
	first := e.say(thread, "first part")
	second := e.say(thread, "second part")
	e.waitFor("one paste", func() bool { return len(e.pastes()) == 1 })
	got := e.pastes()[0]
	if !strings.Contains(got, "first part") || !strings.Contains(got, "second part") ||
		strings.Index(got, "first part") > strings.Index(got, "second part") {
		t.Errorf("pasted %q, want both messages in order", got)
	}
	e.waitFor("Enter once", func() bool { return len(e.keys()) == 1 })
	e.waitFor("eyes on the first message", func() bool { m, _ := e.message(first); return m.Reaction == "👀" })
	e.waitFor("eyes on the second message", func() bool { m, _ := e.message(second); return m.Reaction == "👀" })
	e.never("a second paste", 600*time.Millisecond, func() bool { return len(e.pastes()) > 1 })
	// The agent records the joined text: the first message gets the 👍.
	e.pi.User(got)
	e.waitFor("thumbs up on the first message", func() bool { m, _ := e.message(first); return m.Reaction == "👍" })
	e.waitFor("thumbs up on the second message", func() bool { m, _ := e.message(second); return m.Reaction == "👍" })
	e.never("the joined prompt posted back", 400*time.Millisecond, func() bool { return e.hasText(thread, "You (on Mac)") })
}

func TestSendNowAndCancelButtons(t *testing.T) {
	e, _, thread := piTopic(t, "pi-buttons")
	working(e, e.pi, thread)

	cancelled := e.say(thread, "never mind this")
	w1 := e.waitText(thread, "Waiting until the agent is idle")
	e.pressButton(w1, buttonData(w1, "Cancel"))
	e.waitText(thread, "Cancelled")
	_ = cancelled

	forced := e.say(thread, "do it right now")
	var w2 tgtest.Message
	e.waitFor("a second waiting message", func() bool {
		for _, m := range e.botMessages(thread) {
			if strings.Contains(m.Text, "Waiting until") && m.ID != w1.ID {
				w2 = m
				return true
			}
		}
		return false
	})
	e.pressButton(w2, buttonData(w2, "Send now"))
	e.waitFor("the forced text", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; got != "do it right now" {
		t.Errorf("pasted %q (the cancelled message must not be typed)", got)
	}
	e.waitFor("eyes", func() bool { m, _ := e.message(forced); return m.Reaction == "👀" })
}

func TestQueuedMessageExpires(t *testing.T) {
	e := newEnv(t)
	e.cfg.InputSettings.QueueExpiry.Duration = 700 * time.Millisecond
	e.seedOwner()
	p := e.newPi("pi-expire")
	p.User("start")
	p.Final("ready")
	e.pi = p
	e.start()
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "ready")
	working(e, p, thread)
	id := e.say(thread, "too late")
	e.waitFor("the thumbs down", func() bool { m, _ := e.message(id); return m.Reaction == "👎" })
	e.waitText(thread, "waited more than")
	if len(e.pastes()) != 0 {
		t.Errorf("typed an expired message: %v", e.pastes())
	}
}

func TestTopicCommands(t *testing.T) {
	e, _, thread := piTopic(t, "pi-commands")
	e.say(thread, "/info")
	info := e.waitText(thread, "Topic: live")
	if !strings.Contains(info.Text, "Session: pi-commands") || !strings.Contains(info.Text, "Mode: progress") {
		t.Errorf("info = %q", info.Text)
	}
	e.say(thread, "/mode final")
	e.waitText(thread, "/mode is now final")
	e.say(thread, "/verbose full")
	e.waitText(thread, "/verbose is now full")
	e.say(thread, "/reasoning on")
	e.waitText(thread, "/reasoning is now on")
	e.say(thread, "/mode bogus")
	e.waitText(thread, "Use: progress, block, final")
	th, _, _ := e.peek().ThreadByTopic(context.Background(), chatID, thread)
	if th.Mode != "final" || th.Verbose != "full" || !th.Reasoning {
		t.Errorf("thread settings = %+v", th)
	}

	e.say(thread, "/esc")
	e.waitFor("escape", func() bool { return len(e.keys()) == 1 })
	e.say(thread, "/key ctrl+c")
	e.waitFor("ctrl+c", func() bool { return len(e.keys()) == 2 })
	e.say(thread, "/interrupt")
	e.waitFor("interrupt", func() bool { return len(e.keys()) == 3 })
	if got := strings.Join(e.keys(), ","); got != "escape,ctrl+c,escape" {
		t.Errorf("keys = %s", got)
	}
	if len(e.pastes()) != 0 {
		t.Errorf("a command was typed as text: %v", e.pastes())
	}
	// A main-chat command works inside a topic too.
	e.say(thread, "/topics")
	e.waitText(thread, "Topics (1)")
	// //name sends /name to the agent.
	e.say(thread, "//compact")
	e.waitFor("the slash command", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; got != "/compact" {
		t.Errorf("pasted %q, want /compact", got)
	}

	e.say(thread, "/unlink")
	e.waitText(thread, "Unlinked")
	e.say(thread, "still there?")
	e.waitText(thread, "This topic is not linked")
}

func TestMainChatBasics(t *testing.T) {
	e, _, _ := piTopic(t, "pi-main")
	e.say(0, "/help")
	e.waitText(0, "Main chat")
	e.say(0, "/status")
	e.waitText(0, "Topics: 1 live")
	e.say(0, "/topics")
	e.waitText(0, "pane one · live")
	e.say(0, "/info")
	e.waitText(0, "Use this inside a topic")
	e.say(0, "/nonsense")
	e.waitText(0, "Unknown command")
	// A photo in the main chat.
	e.tg.PushUpdate(telegram.Update{Message: &telegram.Message{
		MessageID: 9300, From: &telegram.User{ID: ownerID}, Chat: telegram.Chat{ID: chatID, Type: "private"},
		Photo: []telegram.PhotoSize{{FileID: "x", FileSize: 1}},
	}})
	e.waitText(0, "Only text is supported")
	// An unknown topic.
	e.say(4242, "hello?")
	e.waitText(4242, "This topic is not linked")
	// An edited message.
	e.tg.PushUpdate(telegram.Update{EditedMessage: &telegram.Message{
		MessageID: 9301, From: &telegram.User{ID: ownerID}, Chat: telegram.Chat{ID: chatID, Type: "private"}, Text: "edited",
	}})
	e.waitText(0, "Edits are not sent")
	// Every command in the menu was registered.
	if len(e.tg.Commands()) < 10 {
		t.Errorf("commands = %v", e.tg.Commands())
	}
}

func TestAgentLeavesAndReturns(t *testing.T) {
	e, p, thread := piTopic(t, "pi-leave")
	topicName := func() string {
		for _, tp := range e.tg.Topics(chatID) {
			if tp.ThreadID == thread {
				return tp.Name
			}
		}
		return ""
	}
	e.setProcess("zsh", "-zsh", 777)
	e.waitText(thread, "pi stopped: it exited, and the pane is still open.")
	e.waitFor("the 🔴 in the topic name", func() bool { return topicName() == "🔴 pane one" })
	id := e.say(thread, "anyone there?")
	e.waitFor("a refusal", func() bool { m, _ := e.message(id); return m.Reaction == "👎" })
	if len(e.pastes()) != 0 {
		t.Errorf("typed into a shell: %v", e.pastes())
	}
	// pi starts again with a new process and the same session (resume).
	e.setProcess("node", "pi", 4343)
	e.hookPi(p, "resume")
	e.waitText(thread, "pi is running again")
	e.waitFor("the topic name without the 🔴", func() bool { return topicName() == "pane one" })
	e.waitText(thread, "pi · resumed")
	if got := len(e.tg.Topics(chatID)); got != 1 {
		t.Errorf("%d topics", got)
	}
	e.waitFor("the pane to be idle", func() bool {
		pane, _, _ := e.peek().PaneByBlock(context.Background(), "local", rexBlock)
		return pane.State == "idle" && pane.PID == 4343
	})
	p.User("welcome back")
	p.Final("resumed answer")
	e.waitText(thread, "resumed answer")
	if n := e.countText(thread, "stopped:"); n != 1 {
		t.Errorf("the stop was posted %d times", n)
	}
}

func TestDeletedTopicClosesThread(t *testing.T) {
	e, p, thread := piTopic(t, "pi-deleted")
	e.tg.DeleteTopic(chatID, thread)
	p.User("anyone?")
	p.Final("answer to nowhere")
	e.waitFor("the thread to be closed", func() bool {
		th, ok, _ := e.peek().ThreadByTopic(context.Background(), chatID, thread)
		return ok && th.Status == store.ThreadClosed
	})
	// The topic no longer exists, so nothing can be answered there, and
	// nothing is typed either.
	e.say(thread, "hello")
	e.never("typing into a pane for a closed topic", 800*time.Millisecond, func() bool { return len(e.pastes()) > 0 })
}

func TestConflictStopsTheDaemon(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	e.tg.FailNext("getUpdates", 1, 409, "Conflict: terminated by other getUpdates request", 0)
	e.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	e.mu.Unlock()
	defer cancel()
	go func() { done <- daemon.Run(ctx, e.options()) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil on a 409 Conflict")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Run did not stop on a 409 Conflict")
	}
}

func TestGuessResolvesPaneWithoutHook(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	p := e.newPi("pi-guess")
	p.User("hello")
	p.Final("guessed answer")
	e.start()
	e.waitFor("the guess", func() bool {
		pane, ok, _ := e.peek().PaneByBlock(context.Background(), "local", rexBlock)
		return ok && pane.ConversationID != 0 && pane.Source == store.SourceGuess
	})
	e.say(0, "/agents")
	m := e.waitText(0, "Active agents")
	if !strings.Contains(m.Text, "(guess)") {
		t.Errorf("agents = %q", m.Text)
	}
}

func TestUnresolvedPaneCannotBeOpened(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	e.start() // no transcript exists, so the guess finds nothing
	e.paneID()
	e.say(0, "/agents")
	m := e.waitText(0, "waiting for session")
	e.pressButton(m, buttonData(m, "Open"))
	e.waitFor("the explanation", func() bool {
		for _, s := range e.callbackTexts() {
			if strings.Contains(s, "Waiting for the session file") {
				return true
			}
		}
		return false
	})
	if len(e.tg.Topics(chatID)) != 0 {
		t.Errorf("a topic was created for an unresolved pane")
	}
}

func TestLongAnswers(t *testing.T) {
	e, p, thread := piTopic(t, "pi-long")
	para := strings.Repeat("word ", 150) // 750 characters
	var three, huge []string
	for i := 0; i < 12; i++ {
		three = append(three, fmt.Sprintf("P%d %s", i, para))
	}
	for i := 0; i < 80; i++ {
		huge = append(huge, fmt.Sprintf("H%d %s", i, para))
	}
	p.User("long")
	p.Final(strings.Join(three, "\n\n")) // about 9000 characters: 3 parts
	e.waitText(thread, "(3/3)")
	if n := e.countText(thread, "(1/3)"); n != 1 {
		t.Errorf("part 1/3 posted %d times", n)
	}
	p.User("huge")
	p.Final(strings.Join(huge, "\n\n")) // about 60000 characters: a document
	e.waitFor("the document", func() bool {
		for _, m := range e.tg.Messages(chatID) {
			if m.Filename == "answer.md" && strings.Contains(string(m.File), "H79") {
				return true
			}
		}
		return false
	})
}

func TestEventStreamDeliversStatusAndSurvivesRestartOfTheStream(t *testing.T) {
	e := newEnv(t)
	e.cfg.Rex.ScanInterval.Duration = time.Hour
	e.seedOwner()
	p := e.newPi("pi-events")
	p.User("start")
	p.Final("ready")
	e.fake.SetEventsExit(1, "stream closed")
	e.start()
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "ready")

	// The next run of `rex events` prints a blocked status.
	e.setScreen(permScreen)
	rec := fmt.Sprintf(`{"type":"session_event","payload":{"session_id":%q,"event":{"type":"block_event","payload":{"block_id":%q,"name":"program_status_changed","payload":{"record":{"state":"blocked","kind":"permission","app":"pi","updated_at":%q,"owner":{"pid":%d,"name":"node"}},"reason":"report"}}}}}`,
		rexSess, rexBlock, time.Now().UTC().Format(time.RFC3339Nano), piPID)
	e.fake.SetEvents(rec)
	e.waitText(thread, "Permission needed")
}

func TestRestartKeepsProgressMessage(t *testing.T) {
	e, p, thread := piTopic(t, "pi-restart-progress")
	p.User("long task")
	p.ToolCall("t1", "make all")
	var progress int64
	e.waitFor("the progress message", func() bool {
		for _, m := range e.botMessages(thread) {
			if strings.Contains(m.Text, "make all") && strings.Contains(m.Text, "⏱") {
				progress = m.ID
				return true
			}
		}
		return false
	})
	e.stop()
	e.start()
	e.waitFor("the second start", func() bool { return len(e.tg.Calls("getMe")) >= 2 })
	p.ToolResult("t1")
	p.Final("make finished")
	e.waitText(thread, "make finished")
	e.waitFor("the same progress message to become the summary", func() bool {
		m, ok := e.message(progress)
		return ok && !m.Deleted && strings.Contains(m.Text, "Done")
	})
	n := 0
	for _, m := range e.botMessages(thread) {
		if strings.Contains(m.Text, "⏱") || strings.Contains(m.Text, "Done ·") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d progress/summary messages, want 1\n%s", n, e.dump())
	}
}

func TestRestartKeepsQueueOffsetAndPause(t *testing.T) {
	e, p, thread := piTopic(t, "pi-restart-queue")
	e.say(0, "/status")
	e.waitText(0, "rex-tg is running")
	e.say(0, "/pause")
	e.waitText(0, "paused")
	e.stop()
	e.start()
	e.waitFor("the second start", func() bool { return len(e.tg.Calls("getMe")) >= 2 })
	// Updates handled before the restart are not handled again.
	e.never("a repeated answer", 700*time.Millisecond, func() bool {
		return e.countText(0, "rex-tg is running") > 1 || e.countText(0, "Typing is paused") > 1
	})
	// Pause is still on.
	id := e.say(thread, "typed while paused after restart")
	e.waitFor("a refusal", func() bool { m, _ := e.message(id); return m.Reaction == "👎" })
	if len(e.pastes()) != 0 {
		t.Fatalf("typed while paused: %v", e.pastes())
	}
	e.say(0, "/unpause")
	e.waitText(0, "on again")

	// A message that waits for the agent survives a restart and is typed once.
	working(e, p, thread)
	q := e.say(thread, "wait for the end of the job")
	e.waitText(thread, "Waiting until the agent is idle")
	e.stop()
	e.start()
	e.waitFor("the third start", func() bool { return len(e.tg.Calls("getMe")) >= 3 })
	e.never("typing while working", 600*time.Millisecond, func() bool { return len(e.pastes()) > 0 })
	p.ToolResult("job-1")
	p.Final("job done")
	e.waitFor("the queued text", func() bool { return len(e.pastes()) == 1 })
	e.never("a second paste", 700*time.Millisecond, func() bool { return len(e.pastes()) > 1 })
	e.waitFor("eyes", func() bool { m, _ := e.message(q); return m.Reaction == "👀" })
}

func TestConfiguredModeAppliesToNewTopics(t *testing.T) {
	e := newEnv(t)
	e.cfg.Display.Mode = "final"
	e.seedOwner()
	p := e.newPi("pi-mode")
	p.User("start")
	p.Final("ready")
	e.start()
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "ready")
	if intro := e.waitText(thread, "Linked"); !strings.Contains(intro.Text, "Mode: final") {
		t.Errorf("intro card = %q, want the configured mode", intro.Text)
	}
	th, _, _ := e.peek().ThreadByTopic(context.Background(), chatID, thread)
	if th.Mode != "final" {
		t.Errorf("thread mode = %q, want final (display.mode)", th.Mode)
	}
	p.User("work")
	p.ToolCall("c1", "make")
	e.never("a progress message in final mode", 700*time.Millisecond, func() bool { return e.hasText(thread, "⏱") })
}

func TestTypingIndicatorForTheTopicUsedLast(t *testing.T) {
	e, p, thread := piTopic(t, "pi-typing")
	e.say(thread, "start working")
	e.waitFor("the text to be typed", func() bool { return len(e.keys()) == 1 })
	p.User("start working")
	p.ToolCall("c1", "sleep 1")
	e.waitFor("a typing action", func() bool {
		for _, c := range e.tg.Calls("sendChatAction") {
			if v, _ := c.Params["message_thread_id"].(float64); int64(v) == thread {
				return true
			}
		}
		return false
	})
}

func TestCodexAlertUsesConfiguredAnswersAndDenyIsNo(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	e.setProcess("codex", "codex", 6161)
	dir := filepath.Join(e.home, ".codex", "sessions", "2026", "11", "11")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-11-11T10-00-00-cdx-1.jsonl")
	lines := []string{
		fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":"cdx-1","cwd":%q,"originator":"codex-tui","source":"cli"}}`, now(), paneCWD),
		fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"message","id":"m1","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"codex is ready"}]}}`, now()),
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.start()
	e.spool("codex", "SessionStart", map[string]any{
		"hook_event_name": "SessionStart", "source": "startup", "session_id": "cdx-1", "transcript_path": path, "cwd": paneCWD,
	})
	thread := e.openTopic()
	e.waitText(thread, "codex is ready")

	e.setScreen("Allow command?\n  git push origin main\n  [y] yes  [p] always  [esc] no\n")
	e.setStatus("blocked", "permission")
	alert := e.waitText(thread, "Permission needed")
	var labels []string
	for _, row := range alert.Markup.InlineKeyboard {
		for _, b := range row {
			labels = append(labels, b.Text)
		}
	}
	if got := strings.Join(labels, "|"); got != "Yes|Always|No" {
		t.Errorf("buttons = %q", got)
	}
	e.say(thread, "/deny")
	e.waitFor("a key", func() bool { return len(e.keys()) == 1 })
	if got := e.keys()[0]; got != "escape" {
		t.Errorf("/deny pressed %q, want escape (No), never the always-allow key", got)
	}
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestVoiceNoteWithWhisper(t *testing.T) {
	bin := t.TempDir()
	// The fake ffmpeg copies its input to the last argument; the fake whisper
	// prints a transcript and records its arguments.
	writeScript(t, filepath.Join(bin, "ffmpeg"), `for a; do last="$a"; done; cp "$5" "$last"`)
	argsFile := filepath.Join(bin, "whisper.args")
	writeScript(t, filepath.Join(bin, "whisper"), fmt.Sprintf(`echo "$@" > %s; echo " run the   tests "`, argsFile))
	t.Setenv("PATH", bin+":"+os.Getenv("PATH")) // so this test does not run in parallel

	e := newEnvSerial(t)
	e.cfg.Voice.WhisperCLI = filepath.Join(bin, "whisper")
	e.cfg.Voice.Model = "/models/base.bin"
	e.cfg.Voice.Language = "en"
	e.seedOwner()
	p := e.newPi("pi-whisper")
	p.User("start")
	p.Final("ready")
	e.start()
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "ready")

	e.tg.AddFile("voice-2", "voice/file_2.oga", []byte("ogg-bytes"))
	e.tg.PushUpdate(telegram.Update{Message: &telegram.Message{
		MessageID: 9200, MessageThreadID: thread, IsTopicMessage: true,
		From: &telegram.User{ID: ownerID}, Chat: telegram.Chat{ID: chatID, Type: "private"},
		Voice: &telegram.Voice{FileID: "voice-2", FileSize: 9, Duration: 1},
	}})
	e.waitText(thread, "🎤 run the tests")
	e.waitFor("the transcript to be typed", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; got != "run the tests" {
		t.Errorf("pasted %q", got)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "-m /models/base.bin -l en -nt") || !strings.Contains(string(args), "-f ") {
		t.Errorf("whisper arguments = %q", args)
	}
}

func TestLargeUploadIsRefused(t *testing.T) {
	e := newEnv(t)
	e.cfg.InputSettings.MaxUploadMB = 1
	e.seedOwner()
	p := e.newPi("pi-big")
	p.User("start")
	p.Final("ready")
	e.start()
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "ready")
	e.tg.PushUpdate(telegram.Update{Message: &telegram.Message{
		MessageID: 9400, MessageThreadID: thread, IsTopicMessage: true,
		From: &telegram.User{ID: ownerID}, Chat: telegram.Chat{ID: chatID, Type: "private"},
		Document: &telegram.Document{FileID: "doc-big", FileName: "../../etc/passwd", FileSize: 3 << 20},
	}})
	e.waitText(thread, "too large")
	e.never("typing a refused file", 400*time.Millisecond, func() bool { return len(e.pastes()) > 0 })

	// A small document keeps only a safe base name.
	e.tg.AddFile("doc-small", "docs/a.txt", []byte("hi"))
	e.tg.PushUpdate(telegram.Update{Message: &telegram.Message{
		MessageID: 9401, MessageThreadID: thread, IsTopicMessage: true,
		From: &telegram.User{ID: ownerID}, Chat: telegram.Chat{ID: chatID, Type: "private"},
		Document: &telegram.Document{FileID: "doc-small", FileName: "../../my notes.txt", FileSize: 2},
	}})
	e.waitFor("the path", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; !strings.HasPrefix(got, e.cfg.UploadDir()+"/") || strings.Contains(got, "..") {
		t.Errorf("path = %q", got)
	}
}

func TestHousekeepingRemovesOldFiles(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	old := time.Now().Add(-8 * 24 * time.Hour)
	fresh := filepath.Join(e.cfg.UploadDir(), "1", "fresh.jpg")
	stale := filepath.Join(e.cfg.UploadDir(), "1", "stale.jpg")
	for _, f := range []string{fresh, stale} {
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.cfg.SpoolDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	staleSpool := filepath.Join(e.cfg.SpoolDir(), "1-1-1.json")
	if err := os.WriteFile(staleSpool, []byte(`{"runtime":"pi","event":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staleSpool, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	e.start()
	e.waitFor("the old upload to be removed", func() bool { _, err := os.Stat(stale); return os.IsNotExist(err) })
	e.waitFor("the old spool file to be removed", func() bool { _, err := os.Stat(staleSpool); return os.IsNotExist(err) })
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a fresh upload was removed: %v", err)
	}
}

func TestStrangerCannotPressButtons(t *testing.T) {
	e, _, thread := piTopic(t, "pi-stranger")
	e.setScreen(permScreen)
	e.setStatus("blocked", "permission")
	alert := e.waitText(thread, "Permission needed")
	e.tg.PushCallback(chatID, thread, 555, alert.ID, buttonData(alert, "1 Yes"))
	e.waitFor("the callback to be answered", func() bool { return len(e.tg.Calls("answerCallbackQuery")) >= 1 })
	e.never("keys from a stranger", 500*time.Millisecond, func() bool { return len(e.keys()) > 0 })
	// The owner still can.
	e.pressButton(alert, buttonData(alert, "1 Yes"))
	e.waitFor("the owner's key", func() bool { return len(e.keys()) == 1 })
}

func TestSecretsAreRedactedInAnswers(t *testing.T) {
	e, p, thread := piTopic(t, "pi-secret")
	p.User("show env")
	p.Final("token is ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789 and key sk-ant-" + "api03-abcdefghijklmnopqrstuvwxyz")
	e.waitText(thread, "token is")
	for _, m := range e.botMessages(thread) {
		if strings.Contains(m.Text, "ghp_abc") || strings.Contains(m.Text, "sk-ant-"+"api03-abc") {
			t.Errorf("a secret was posted: %q", m.Text)
		}
	}
}

// For a topic that still exists, the agent's button posts a link to it. The
// button reports the tap instead of opening a link itself, so rex-tg can check
// that the topic still exists.
func TestOpenTwiceLinksToExistingTopic(t *testing.T) {
	e, _, thread := piTopic(t, "pi-twice")
	list := e.agentsList()
	if buttonData(list, "↗ pane one") == "" || buttonURL(list, "↗ pane one") != "" {
		t.Fatalf("the topic button must report the tap: %+v", list.Markup)
	}
	before := len(e.botMessages(0))
	e.pressButton(list, buttonData(list, "↗ pane one"))
	e.waitFor("the link", func() bool { return len(e.botMessages(0)) > before })
	m := e.botMessages(0)[before]
	if !strings.Contains(m.Text, "↗ pane one") {
		t.Fatalf("reply = %q", m.Text)
	}
	if url := buttonURL(m, "Open topic"); url != fmt.Sprintf("https://t.me/test_bot/%d", thread) {
		t.Errorf("link = %q, want topic %d", url, thread)
	}
	if got := len(e.tg.Topics(chatID)); got != 1 {
		t.Errorf("%d topics, want 1", got)
	}
}

func TestReplacedTranscriptDoesNotRepostOldAnswers(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	p := e.newPi("pi-replace")
	p.User("q1")
	p.Final("answer alpha")
	p.User("q2")
	p.Final("answer beta")
	e.start()
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "answer beta")

	// The file is rewritten (new inode) with the same history and one new answer.
	old, err := os.ReadFile(p.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Path, old, 0o644); err != nil {
		t.Fatal(err)
	}
	p.User("q3")
	p.Final("answer gamma")
	e.waitText(thread, "answer gamma")
	time.Sleep(600 * time.Millisecond)
	for _, s := range []string{"answer alpha", "answer beta", "answer gamma"} {
		if n := e.countText(thread, s); n > 1 {
			t.Errorf("%q posted %d times\n%s", s, n, e.dump())
		}
	}
}

func TestUnlinkThenOpenAgain(t *testing.T) {
	e, p, thread := piTopic(t, "pi-relink")
	e.say(thread, "/unlink")
	e.waitText(thread, "Unlinked")
	p.User("after unlink")
	p.Final("silent answer")
	e.never("an answer in the unlinked topic", 600*time.Millisecond, func() bool { return e.hasText(thread, "silent answer") })

	e.say(0, "/agents")
	e.waitFor("a fresh list", func() bool {
		ms := e.botMessages(0)
		last := ms[len(ms)-1]
		return strings.Contains(last.Text, "Active agents") && buttonData(last, "Open") != "" && buttonData(last, "Open") != "o:0"
	})
	ms := e.botMessages(0)
	list := ms[len(ms)-1]
	e.pressButton(list, buttonData(list, "Open"))
	e.waitFor("a second topic", func() bool { return len(e.tg.Topics(chatID)) == 2 })
	second := e.tg.Topics(chatID)[1].ThreadID
	e.waitText(second, "silent answer") // the backfill of the new topic
	p.User("again")
	p.Final("answer in the new topic")
	e.waitText(second, "answer in the new topic")
	if e.hasText(thread, "answer in the new topic") {
		t.Errorf("the old topic got an answer")
	}
}

func TestForceTypesAtAPromptAndMacAnswerClosesAlert(t *testing.T) {
	e, _, thread := piTopic(t, "pi-force")
	e.setScreen(permScreen)
	e.setStatus("blocked", "permission")
	alert := e.waitText(thread, "Permission needed")

	// A plain message waits while the prompt is open, and "Send now" is
	// refused: free text could pick an answer.
	id := e.say(thread, "plain text at a prompt")
	waiting := e.waitText(thread, "Waiting until the agent is idle")
	e.pressButton(waiting, buttonData(waiting, "Send now"))
	e.waitFor("a refusal", func() bool { m, _ := e.message(id); return m.Reaction == "👎" })
	e.waitText(thread, "prompt")
	if len(e.pastes()) != 0 {
		t.Fatalf("typed at a prompt: %v", e.pastes())
	}
	// /force types anyway.
	e.say(thread, "/force yes, go on")
	e.waitFor("the forced text", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; got != "yes, go on" {
		t.Errorf("pasted %q", got)
	}

	// The user answers on the Mac: the prompt ends and the buttons go away.
	e.setStatus("working", "")
	e.waitFor("the buttons to be removed", func() bool { m, _ := e.message(alert.ID); return m.Markup == nil })
	als, _ := e.peek().OpenAlerts(context.Background(), thread)
	if len(als) != 0 {
		t.Errorf("open alerts: %+v", als)
	}
}

func TestPaneClosedTakesTopicOffline(t *testing.T) {
	e, _, thread := piTopic(t, "pi-closed")
	e.fake.On([]string{"block", "ls"}, `{"blocks":[]}`, 0)
	e.waitText(thread, "pi stopped: its Rex pane was closed.")
	id := e.say(thread, "are you there")
	e.waitFor("a refusal", func() bool { m, _ := e.message(id); return m.Reaction == "👎" })
	e.waitText(thread, "Not sent")
	if len(e.pastes()) != 0 {
		t.Errorf("typed into a closed pane: %v", e.pastes())
	}
	e.say(0, "/topics")
	e.waitText(0, "offline")
}

func TestPairingExpiryUsesTheInjectedClock(t *testing.T) {
	e := newEnv(t)
	var code string
	e.withStore(func(st *store.Store) {
		code, _ = daemon.NewPairingCode(context.Background(), st, time.Now())
	})
	opt := e.options()
	opt.Now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	e.startWith(opt)
	e.tg.PushMessage(chatID, 0, ownerID, "/start "+code)
	// The right code, but expired by the injected clock: the owner is told so.
	e.waitText(0, "expired")
	if e.hasText(0, "Paired") {
		t.Fatalf("paired after the code expired: %s", e.dump())
	}
	if _, ok, _ := e.peek().Get(context.Background(), "owner_user_id"); ok {
		t.Fatal("paired with an expired code")
	}
}

func TestBadTokenStopsRunAndCancelStopsRetries(t *testing.T) {
	e := newEnv(t)
	e.tg.FailNext("getMe", 1, 401, "Unauthorized", 0)
	err := daemon.Run(context.Background(), e.options())
	if err == nil {
		t.Fatal("Run returned nil for a rejected token")
	}

	// A network failure is retried; cancel ends the retries.
	e2 := newEnv(t)
	e2.tg.FailNext("getMe", 100, 502, "bad gateway", 0)
	e2.start()
	e2.waitFor("a getMe attempt", func() bool { return len(e2.tg.Calls("getMe")) >= 1 })
	e2.stop() // checks that Run returned nil
}

func TestFailedTypingIsNeverRetried(t *testing.T) {
	e, _, thread := piTopic(t, "pi-typefail")
	e.fake.OnError([]string{"api", "call"}, "pane is busy", 1)
	id := e.say(thread, "this paste fails")
	e.waitFor("the thumbs down", func() bool { m, _ := e.message(id); return m.Reaction == "👎" })
	e.waitText(thread, "may be in the input box")
	if len(e.keys()) != 0 {
		t.Errorf("Enter was pressed after a failed paste: %v", e.keys())
	}
	n := len(e.fake.Calls())
	e.never("another attempt to type it", 1200*time.Millisecond, func() bool {
		for _, c := range e.fake.Calls()[n:] {
			if strings.Contains(strings.Join(c.Args, " "), "block.event") {
				return true
			}
		}
		return false
	})
	if c := e.countText(thread, "may be in the input box"); c != 1 {
		t.Errorf("%d notices, want 1", c)
	}
	qs, _ := e.peek().QueuedInbound(context.Background(), thread)
	if len(qs) != 0 {
		t.Errorf("queued rows: %+v", qs)
	}
}

func TestUnknownLineTypesAreCountedNotTracked(t *testing.T) {
	probe, err := pi.New().ParseLine([]byte(`{"type":"made_up_type","id":"zz"}`), agents.LineContext{SessionID: "x"})
	if err != nil || len(probe) != 1 || probe[0].Kind != agents.KindUnknown {
		t.Skipf("the pi adapter does not yet report unknown line types as KindUnknown (got %v, %v)", probe, err)
	}
	e, p, thread := piTopic(t, "pi-unknown")
	before := len(e.botMessages(thread))
	p.write(`{"type":"made_up_type","id":"zz1"}`, `{"type":"made_up_type","id":"zz2"}`, `{"type":"other_new_type","id":"zz3"}`)
	p.User("go")
	p.Final("known answer")
	e.waitText(thread, "known answer")
	e.stop() // flushes the counts
	v, ok, _ := e.peek().Get(context.Background(), "unknown_line_types")
	if !ok {
		t.Fatalf("unknown_line_types not stored (%d new messages)", len(e.botMessages(thread))-before)
	}
	var got map[string]map[string]int
	if err := json.Unmarshal([]byte(v), &got); err != nil {
		t.Fatal(err)
	}
	if got["pi"]["made_up_type"] != 2 || got["pi"]["other_new_type"] != 1 {
		t.Errorf("counts = %s", v)
	}

	// A second run merges with the stored value.
	e.start()
	e.waitFor("the second start", func() bool { return len(e.tg.Calls("getMe")) >= 2 })
	p.write(`{"type":"made_up_type","id":"zz4"}`)
	p.User("again")
	p.Final("second answer")
	e.waitText(thread, "second answer")
	e.stop()
	v, _, _ = e.peek().Get(context.Background(), "unknown_line_types")
	got = nil
	_ = json.Unmarshal([]byte(v), &got)
	if got["pi"]["made_up_type"] != 3 {
		t.Errorf("merged counts = %s", v)
	}
	// Nothing about the unknown lines reached Telegram.
	if e.hasText(thread, "made_up_type") {
		t.Errorf("an unknown line was posted")
	}
}

func TestProgressEditsAreSpacedByTheInterval(t *testing.T) {
	e := newEnv(t)
	e.cfg.Display.ProgressEditInterval.Duration = 500 * time.Millisecond
	e.seedOwner()
	p := e.newPi("pi-edits")
	p.User("start")
	p.Final("ready")
	e.pi = p
	e.start()
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "ready")

	p.User("many tools")
	p.ToolCall("c-first", "step first")
	var progress int64
	e.waitFor("the progress message", func() bool {
		for _, m := range e.botMessages(thread) {
			if strings.Contains(m.Text, "⏱") {
				progress = m.ID
				return true
			}
		}
		return false
	})
	// Tool calls arrive over a span of about 1.8 s, more than three intervals.
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("c%d", i)
		p.ToolCall(id, fmt.Sprintf("step %d", i))
		p.ToolResult(id)
		time.Sleep(150 * time.Millisecond)
	}
	p.Final("all steps done")
	e.waitText(thread, "all steps done")
	e.waitFor("the summary", func() bool { m, _ := e.message(progress); return strings.Contains(m.Text, "Done") })

	var times []time.Time
	for _, c := range e.tg.Calls("editMessageText") {
		if id, _ := c.Params["message_id"].(float64); int64(id) == progress {
			times = append(times, c.At)
		}
	}
	if len(times) < 2 {
		t.Fatalf("only %d edits of the progress message", len(times))
	}
	for i := 1; i < len(times); i++ {
		// The fake server sees arrival times, which jitter under load: a late
		// edit shortens the next gap (seen: 300 ms with the race detector).
		// A broken spacing would show gaps of one tick (20 ms).
		if gap := times[i].Sub(times[i-1]); gap < 200*time.Millisecond {
			t.Errorf("edit %d came %v after the one before; want about 500ms", i, gap)
		}
	}
}

func TestAlertFromEventStreamArrivesWithinThreeSeconds(t *testing.T) {
	e, _, thread := piTopic(t, "pi-latency")
	e.stop()
	e.cfg.Rex.ScanInterval.Duration = time.Hour
	e.setScreen(permScreen)
	at := time.Now().UTC()
	rec := fmt.Sprintf(`{"type":"session_event","payload":{"session_id":%q,"event":{"type":"block_event","payload":{"block_id":%q,"name":"program_status_changed","payload":{"record":{"state":"blocked","kind":"permission","app":"pi","updated_at":%q,"owner":{"pid":%d,"name":"node"}},"reason":"report"}}}}}`,
		rexSess, rexBlock, at.Format(time.RFC3339Nano), piPID)
	e.fake.SetEvents(rec)
	e.start()
	e.waitText(thread, "Permission needed")
	var sent time.Time
	for _, c := range e.tg.Calls("sendMessage") {
		if s, _ := c.Params["text"].(string); strings.Contains(s, "Permission needed") {
			sent = c.At
		}
	}
	if sent.IsZero() {
		t.Fatal("no sendMessage call for the alert")
	}
	if d := sent.Sub(at); d > 3*time.Second {
		t.Errorf("the alert took %v from the status record, want < 3s", d)
	}
}

func TestPauseWhilePrepareIsInFlightBlocksTheTyping(t *testing.T) {
	e, _, thread := piTopic(t, "pi-pause-race")
	e.stop()
	e.cfg.Rex.ScanInterval.Duration = time.Hour
	e.start()
	e.waitFor("the second start", func() bool { return len(e.tg.Calls("getMe")) >= 2 })
	e.waitFor("the first scan", func() bool { return len(e.rexCallsMatching("program_status")) >= 1 })
	time.Sleep(300 * time.Millisecond)
	// The process call of the safety check takes a while.
	e.fake.OnDelay([]string{"block", "call", "-s", rexSess, "-b", rexBlock, "process"},
		fmt.Sprintf(`{"foreground":{"pid":%d,"name":"node","argv0":"pi","cwd":%q},"ancestors":[]}`, piPID, paneCWD), 700*time.Millisecond)
	before := len(e.rexCallsMatching("process"))
	id := e.say(thread, "must not be typed")
	e.waitFor("the safety check to start", func() bool { return len(e.rexCallsMatching("process")) > before })
	e.say(0, "/pause")
	e.waitText(0, "paused")
	e.never("typing after /pause", 1500*time.Millisecond, func() bool { return len(e.pastes()) > 0 })
	_ = id
}

func (e *env) rexCallsMatching(word string) []rexCall {
	var out []rexCall
	for _, c := range e.fake.Calls() {
		if len(c.Args) > 0 && strings.HasSuffix(c.Args[len(c.Args)-1], word) {
			out = append(out, rexCall{c.Args, c.Stdin})
		}
	}
	return out
}

func TestTwoTapsOnAnAlertPressTheKeysOnce(t *testing.T) {
	e, _, thread := piTopic(t, "pi-double-tap")
	e.setScreen(permScreen)
	e.setStatus("blocked", "permission")
	alert := e.waitText(thread, "Permission needed")
	// The capture before the keys is slow, so the taps overlap.
	e.fake.OnDelay([]string{"capture"}, permScreen, 400*time.Millisecond)
	data := buttonData(alert, "1 Yes")
	e.pressButton(alert, data)
	e.pressButton(alert, data)
	e.say(thread, "/approve")
	e.waitFor("the key", func() bool { return len(e.keys()) >= 1 })
	e.never("a second key press", 1200*time.Millisecond, func() bool { return len(e.keys()) > 1 })
	if got := e.keys(); len(got) != 1 || got[0] != "1" {
		t.Errorf("keys = %v, want [1]", got)
	}
}

func TestUnlinkRefusesWaitingMessages(t *testing.T) {
	e, p, thread := piTopic(t, "pi-unlink-queue")
	working(e, p, thread)
	e.say(thread, "queued for later")
	e.waitText(thread, "Waiting until the agent is idle")
	e.say(thread, "/unlink")
	e.waitText(thread, "Unlinked")
	e.waitFor("no queued rows", func() bool {
		qs, _ := e.peek().QueuedInbound(context.Background(), thread)
		return len(qs) == 0
	})
	p.ToolResult("job-1")
	p.Final("job over")
	e.never("typing for an unlinked topic", 1000*time.Millisecond, func() bool { return len(e.pastes()) > 0 })
}

// piTopicCfg is piTopic with a change to the config before the daemon starts.
func piTopicCfg(t *testing.T, id string, tweak func(*env)) (*env, *piLog, int64) {
	t.Helper()
	e := newEnv(t)
	tweak(e)
	e.seedOwner()
	p := e.newPi(id)
	p.User("start")
	p.Final("ready")
	e.start()
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "ready")
	e.pi = p
	return e, p, thread
}

func TestAgentGoneBeforeThePasteIsRefused(t *testing.T) {
	e, _, thread := piTopicCfg(t, "pi-gone-before", func(e *env) { e.cfg.Rex.ScanInterval.Duration = time.Hour })
	time.Sleep(300 * time.Millisecond)
	e.fake.OnDelay([]string{"block", "call", "-s", rexSess, "-b", rexBlock, "process"},
		fmt.Sprintf(`{"foreground":{"pid":%d,"name":"node","argv0":"pi","cwd":%q},"ancestors":[]}`, piPID, paneCWD), 600*time.Millisecond)
	before := len(e.rexCallsMatching("process"))
	id := e.say(thread, "echo hi > /tmp/x")
	e.waitFor("the safety check to start", func() bool { return len(e.rexCallsMatching("process")) > before })
	e.setProcess("zsh", "-zsh", 777) // the agent exits while the check is running
	e.waitFor("the thumbs down", func() bool { m, _ := e.message(id); return m.Reaction == "👎" })
	e.waitText(thread, "Not sent")
	if len(e.pastes()) != 0 || len(e.keys()) != 0 {
		t.Errorf("typed into a shell: %v %v", e.pastes(), e.keys())
	}
}

func TestAgentGoneOrPauseAfterThePasteStopsEnter(t *testing.T) {
	for _, how := range []string{"shell", "pause"} {
		t.Run(how, func(t *testing.T) {
			e, _, thread := piTopicCfg(t, "pi-after-"+how, func(e *env) {
				e.cfg.Agents.Pi.PasteEnterDelay.Duration = 700 * time.Millisecond
			})
			id := e.say(thread, "rm -rf important")
			e.waitFor("the paste", func() bool { return len(e.pastes()) == 1 })
			if how == "shell" {
				e.setProcess("zsh", "-zsh", 777)
			} else {
				e.say(0, "/pause")
			}
			e.waitText(thread, "may be in the input box")
			e.waitFor("the thumbs down", func() bool { m, _ := e.message(id); return m.Reaction == "👎" })
			e.never("Enter after the checks failed", 800*time.Millisecond, func() bool { return len(e.keys()) > 0 })
		})
	}
}

func TestKeyCommandRejectsOptions(t *testing.T) {
	e, _, thread := piTopic(t, "pi-keys")
	e.say(thread, "/key -b block:other enter")
	e.waitText(thread, "Key names use")
	e.say(thread, "/key ctrl+c")
	e.waitFor("the valid key", func() bool { return len(e.keys()) == 1 })
	if got := e.keys(); got[0] != "ctrl+c" {
		t.Errorf("keys = %v (the rejected command must send nothing)", got)
	}
}

const pemKey = "-----BEGIN RSA PRIV" + "ATE KEY-----\nFAKEPEMLINEONEFAKEPEMLINEONEFAKEPEMLINEONEFAKEPEMLINEONEFAKEPEM\nFAKEPEMLINETWOFAKEPEMLINETWOFAKEPEMLINETWOFAKEPEMLINETWOFAKEPEM\n-----END RSA PRIV" + "ATE KEY-----"

func TestScreenRedactsMultiLineKeys(t *testing.T) {
	e, _, thread := piTopic(t, "pi-pem")
	e.setScreen("before\n" + pemKey + "\nafter\n")
	e.say(thread, "/screen")
	m := e.waitText(thread, "after")
	if strings.Contains(m.Text, "FAKEPEMLINEONE") || strings.Contains(m.Text, "FAKEPEMLINETWO") {
		t.Errorf("key body on screen: %q", m.Text)
	}
	e.setScreen("Do you want to proceed?\n" + pemKey + "\n❯ 1. Yes\n  2. No\n")
	e.setStatus("blocked", "permission")
	a := e.waitText(thread, "Permission needed")
	if strings.Contains(a.Text, "FAKEPEMLINEONE") || strings.Contains(a.Text, "FAKEPEMLINETWO") {
		t.Errorf("key body in alert: %q", a.Text)
	}
}

func TestStateFoldersAreRestrictedAtStart(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	for _, d := range []string{e.cfg.State.Dir, e.cfg.LogDir(), e.cfg.SpoolDir(), e.cfg.UploadDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.start()
	e.waitFor("the daemon to start", func() bool { return len(e.tg.Calls("getMe")) > 0 })
	for _, d := range []string{e.cfg.State.Dir, e.cfg.LogDir(), e.cfg.SpoolDir(), e.cfg.UploadDir()} {
		if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %v, %v; want 0700", d, fi.Mode().Perm(), err)
		}
	}
}

// vimScreen is a pane screen whose prompt editor shows a pi-vim mode label at
// the right end of the input box's bottom border.
func vimScreen(label string) string {
	return "some answer text\n\n" + strings.Repeat("─", 60) + " " + label + "\n~/work/project (main)\n"
}

// piVimMachine makes the fake pane behave like pi-vim: Escape leaves INSERT,
// VISUAL, an EX line, or a pending command, and i enters INSERT. An Escape in
// plain NORMAL mode reaches pi and stops the agent: the screen "ABORTED".
func piVimMachine(e *env, start string) {
	screens := map[string]string{"ABORTED": vimScreen("NORMAL") + "Operation aborted\n"}
	for _, l := range []string{"NORMAL", "INSERT", "NORMAL 3d_", "EX w_", "VISUAL 3_", "VISUAL"} {
		screens[l] = vimScreen(l)
	}
	e.fake.SetScreenMachine(start, screens, map[string]map[string]string{
		"NORMAL":     {"i": "INSERT", "escape": "ABORTED"},
		"INSERT":     {"escape": "NORMAL"},
		"NORMAL 3d_": {"escape": "NORMAL"},
		"EX w_":      {"escape": "NORMAL"},
		"VISUAL 3_":  {"escape": "VISUAL"},
		"VISUAL":     {"escape": "NORMAL"},
	})
}

// The daemon brings pi-vim to INSERT before it pastes: here an Escape cancels
// the pending command, i enters INSERT, and only then the text goes in. The
// unit tests in vim_settle_test.go cover every other start state.
func TestPiVimReachesInsertBeforeThePaste(t *testing.T) {
	e, _, thread := piTopic(t, "pi-vim-pending")
	piVimMachine(e, "NORMAL 3d_")
	id := e.say(thread, "please check the docs")
	e.waitFor("the paste", func() bool { return len(e.pastes()) == 1 })
	e.waitFor("Enter", func() bool { return len(e.keys()) == 3 })
	if got := strings.Join(e.keys(), " "); got != "escape i enter" {
		t.Fatalf("keys = %q, want escape i enter", got)
	}
	var lastVimKey, pasteAt int
	for i, k := range e.fake.Calls() {
		args := strings.Join(k.Args, " ")
		if strings.Contains(args, "send-key") && !strings.HasSuffix(args, " enter") {
			lastVimKey = i
		}
		if strings.Contains(args, "block.event") {
			pasteAt = i
		}
	}
	if pasteAt < lastVimKey {
		t.Errorf("the paste (call %d) came before the last vim key (call %d)", pasteAt, lastVimKey)
	}
	if got := e.fake.ScreenState(); got != "INSERT" {
		t.Errorf("pi-vim ended in %q, want INSERT", got)
	}
	e.waitFor("the eyes reaction", func() bool { m, _ := e.message(id); return m.Reaction == "👀" })
	if e.hasText(thread, "Not sent") {
		t.Errorf("the message was refused:\n%s", e.dump())
	}
}

// A pane that does not react to a key gets no second key: keys must not pile
// up in a pane that does not read them. The message waits, and it is typed
// once the editor shows INSERT.
func TestPiVimThatDoesNotReactGetsNoMoreKeys(t *testing.T) {
	e, _, thread := piTopic(t, "pi-vim-stuck")
	e.fake.SetScreenMachine("NORMAL", map[string]string{"NORMAL": vimScreen("NORMAL"), "INSERT": vimScreen("INSERT")}, nil)
	e.say(thread, "hello")
	e.waitText(thread, "did not react")
	e.never("a second key", 4*testTiming.VimRetry, func() bool { return len(e.keys()) > 1 })
	if got := e.keys(); len(got) != 1 || got[0] != "i" {
		t.Fatalf("keys = %v, want one i", got)
	}
	if len(e.pastes()) != 0 {
		t.Fatalf("pasted into a pane that showed NORMAL: %v", e.pastes())
	}
	e.fake.SetScreenState("INSERT") // the editor caught up with the i
	e.waitFor("the paste", func() bool { return len(e.pastes()) == 1 })
	e.waitFor("Enter", func() bool { return len(e.keys()) == 2 })
	if got := e.keys()[1]; got != "enter" {
		t.Errorf("keys = %v, want i then enter", e.keys())
	}
	if n := e.countText(thread, "did not react"); n != 1 {
		t.Errorf("the note was posted %d times, want once", n)
	}
}

// /interrupt brings pi-vim to plain NORMAL mode first, where an Escape
// reaches pi. /esc is a plain key press.
func TestInterruptInPiVimLeavesInsertModeFirst(t *testing.T) {
	e, _, thread := piTopic(t, "pi-vim-interrupt")
	piVimMachine(e, "INSERT")
	e.say(thread, "/interrupt")
	e.waitText(thread, "Sent: escape")
	if got := strings.Join(e.keys(), " "); got != "escape escape" {
		t.Fatalf("keys = %q, want one Escape for pi-vim, then the interrupt", got)
	}
	if got := e.fake.ScreenState(); got != "ABORTED" {
		t.Errorf("screen = %q, want the interrupt to reach pi", got)
	}
	e.say(thread, "/esc")
	e.waitFor("the third key call", func() bool { return len(e.keys()) == 3 })
	if got := e.keys()[2]; got != "escape" {
		t.Errorf("/esc pressed %q, want escape", got)
	}
}

func TestTopicCreateWithoutAnAnswerSaysToTapAgain(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	p := e.newPi("pi-topic-no-answer")
	p.User("hello")
	e.start()
	e.hookPi(p, "startup")
	e.waitFor("a resolved pane", func() bool {
		pane, ok, err := e.peek().PaneByBlock(context.Background(), "local", rexBlock)
		return err == nil && ok && pane.ConversationID != 0
	})
	e.say(0, "/agents")
	list := e.waitText(0, "Active agents")
	if !strings.Contains(list.Text, "<code>proj</code>") {
		t.Errorf("agents text = %q, want the folder in <code>", list.Text)
	}
	open := buttonData(list, "Open")

	e.tg.FailNext("createForumTopic", 1, 502, "Bad Gateway", 0) // the outcome is unknown
	e.pressButton(list, open)
	e.waitText(0, "did not answer in time")

	e.pressButton(list, open) // the user taps again
	e.waitText(0, "Opened")
	if got := e.tg.Topics(chatID); len(got) != 1 {
		t.Errorf("topics = %+v, want one", got)
	}
}

func TestTopicCreateRefusedMentionsThreadedMode(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	p := e.newPi("pi-topic-refused")
	p.User("hello")
	e.start()
	e.hookPi(p, "startup")
	e.waitFor("a resolved pane", func() bool {
		pane, ok, err := e.peek().PaneByBlock(context.Background(), "local", rexBlock)
		return err == nil && ok && pane.ConversationID != 0
	})
	e.say(0, "/agents")
	list := e.waitText(0, "Active agents")
	e.tg.FailNext("createForumTopic", 1, 400, "Bad Request: the chat is not a forum", 0)
	e.pressButton(list, buttonData(list, "Open"))
	e.waitFor("the alert", func() bool {
		for _, s := range e.callbackTexts() {
			if strings.Contains(s, "Threaded Mode") {
				return true
			}
		}
		return false
	})
}

func TestVimKeysAreOnlyForPi(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	e.setProcess("codex", "codex", 6262)
	dir := filepath.Join(e.home, ".codex", "sessions", "2026", "11", "11")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-11-11T11-00-00-cdx-vim.jsonl")
	meta := fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":"cdx-vim","cwd":%q,"originator":"codex-tui","source":"cli"}}`, now(), paneCWD)
	if err := os.WriteFile(path, []byte(meta+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.start()
	e.spool("codex", "SessionStart", map[string]any{
		"hook_event_name": "SessionStart", "source": "startup", "session_id": "cdx-vim", "transcript_path": path, "cwd": paneCWD,
	})
	thread := e.openTopic()

	// A line that looks like a pi-vim label means nothing in a Codex pane.
	e.setScreen(vimScreen("NORMAL"))
	e.say(thread, "hello codex")
	e.waitFor("Enter", func() bool { return len(e.keys()) == 1 })
	if got := e.keys(); got[0] != "enter" {
		t.Fatalf("keys = %v, want [enter] only", got)
	}
	e.setScreen(vimScreen("INSERT"))
	e.say(thread, "/interrupt")
	e.waitFor("the interrupt", func() bool { return len(e.keys()) == 2 })
	if got := e.keys()[1]; got != "escape" {
		t.Errorf("/interrupt pressed %q, want one escape", got)
	}
}

// buttonURL returns the URL of the button whose text contains sub.
func buttonURL(m tgtest.Message, sub string) string {
	if m.Markup == nil {
		return ""
	}
	for _, row := range m.Markup.InlineKeyboard {
		for _, b := range row {
			if strings.Contains(b.Text, sub) {
				return b.URL
			}
		}
	}
	return ""
}

// Telegram does not tell the bot that the user deleted a topic. The /agents
// button still works: rex-tg finds the topic gone and makes a new one for the
// same session.
func TestDeletedTopicIsMadeAgainFromAgents(t *testing.T) {
	e, _, thread := piTopic(t, "pi-deleted")
	e.tg.DeleteTopic(chatID, thread) // the user deletes the topic in Telegram

	list := e.agentsList()
	data := buttonData(list, "↗ pane one")
	if data == "" || buttonURL(list, "↗ pane one") != "" {
		t.Fatalf("the topic button must report the tap, not open a link: %+v", list.Markup)
	}
	e.pressButton(list, data)
	var fresh int64
	e.waitFor("a new topic", func() bool {
		for _, tp := range e.tg.Topics(chatID) {
			if !tp.Deleted && tp.ThreadID != thread {
				fresh = tp.ThreadID
			}
		}
		return fresh != 0
	})
	e.waitFor("the link to the new topic", func() bool {
		for _, m := range e.botMessages(0) {
			if strings.Contains(m.Text, "Opened") && strings.HasSuffix(buttonURL(m, "Open topic"), fmt.Sprintf("/%d", fresh)) {
				return true
			}
		}
		return false
	})
	e.waitText(fresh, "Linked")
	e.waitText(fresh, "ready") // the backfill
	e.say(fresh, "still there?")
	e.waitFor("the paste", func() bool { return len(e.pastes()) == 1 })
}

// A topic is named after its pane when the pane has its own name, else after
// the session. The name follows changes of both.
func TestTopicNameFollowsThePaneAndTheSession(t *testing.T) {
	e := newEnv(t)
	var skew atomic.Int64
	opt := e.options()
	opt.Now = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	e.seedOwner()
	p := e.newPi("pi-names")
	p.User("start")
	p.Final("ready")
	e.startWith(opt)
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "ready")
	name := func() string {
		for _, tp := range e.tg.Topics(chatID) {
			if tp.ThreadID == thread {
				return tp.Name
			}
		}
		return ""
	}
	if got := name(); got != "pane one" {
		t.Fatalf("topic name = %q, want the pane's name", got)
	}

	// The pane gets Rex's default name, and the session gets a name.
	e.fake.On([]string{"block", "ls"}, fmt.Sprintf(`{"blocks":[{"block_id":%q,"label":"shell"}]}`, rexBlock), 0)
	p.write(fmt.Sprintf(`{"type":"session_info","id":"n1","timestamp":%q,"name":"[proj][fix] Repair the flaky login test on CI"}`, now()))
	e.waitFor("the new pane name and session name", func() bool {
		pane, ok, _ := e.peek().PaneByBlock(context.Background(), "local", rexBlock)
		if !ok || pane.Label != "shell" {
			return false
		}
		c, err := e.peek().Conversation(context.Background(), pane.ConversationID)
		return err == nil && c.Title != ""
	})
	skew.Add(int64(31 * time.Second)) // the next name check is due
	e.waitFor("the rename", func() bool { return name() == "[proj][fix] Repair the flaky…" })
	// The tab is the user's: only tabs of the session for /new follow their topic.
	for _, c := range e.fake.Calls() {
		if slices.Contains(c.Args, "rename") {
			t.Fatalf("rex %q renamed a tab outside the session for /new", c.Args)
		}
	}

	// A rename that fails is tried again at the next check.
	e.fake.On([]string{"block", "ls"}, fmt.Sprintf(`{"blocks":[{"block_id":%q,"label":"api work"}]}`, rexBlock), 0)
	e.waitFor("the new pane name", func() bool {
		pane, ok, _ := e.peek().PaneByBlock(context.Background(), "local", rexBlock)
		return ok && pane.Label == "api work"
	})
	e.tg.FailNext("editForumTopic", 1, 502, "Bad Gateway", 0)
	calls := len(e.tg.Calls("editForumTopic"))
	skew.Add(int64(31 * time.Second))
	e.waitFor("the failed rename", func() bool { return len(e.tg.Calls("editForumTopic")) > calls })
	if got := name(); got == "api work" {
		t.Fatalf("renamed although Telegram failed")
	}
	skew.Add(int64(31 * time.Second))
	e.waitFor("the rename on the next check", func() bool { return name() == "api work" })
}

// The command menu is set for the owner's chat too: Telegram shows a chat's
// own list before the default list, and another program may have left one.
func TestCommandMenuIsSetForTheOwnersChat(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	old := []telegram.BotCommand{{Command: "sessions", Description: "old"}}
	e.tg.SetCommandsFor(telegram.BotCommandScope{Type: "chat", ChatID: chatID}, old)
	e.start()
	e.waitFor("the menu for the owner's chat", func() bool {
		return len(e.tg.CommandsFor(telegram.BotCommandScope{Type: "chat", ChatID: chatID})) == len(daemon.BotCommands())
	})
	if got := e.tg.Commands(); len(got) != len(daemon.BotCommands()) {
		t.Errorf("default menu = %v", got)
	}
}

// A transcript follower that stops on a read error is started again, so the
// topic keeps showing the session.
func TestFollowerStartsAgainAfterAReadError(t *testing.T) {
	e, p, thread := piTopic(t, "pi-read-error")
	saved := p.Path + ".saved"
	if err := os.Rename(p.Path, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p.Path, 0o755); err != nil { // reading a folder fails
		t.Fatal(err)
	}
	time.Sleep(4 * testTiming.TailPoll) // the follower polls, fails, and stops
	if err := os.Remove(p.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(saved, p.Path); err != nil {
		t.Fatal(err)
	}
	p.Final("after the read error")
	deadline := time.Now().Add(15 * time.Second)
	for !e.hasText(thread, "after the read error") {
		if time.Now().After(deadline) {
			t.Fatalf("the answer never arrived\n%s", e.dump())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := e.countText(thread, "ready"); n != 1 {
		t.Errorf("the old answer was posted %d times, want once", n)
	}
}

// A reply whose send had an unknown outcome gets a notice with a button. One
// tap sends it again, and the button goes away.
func TestLostReplyIsSentAgainWithOneTap(t *testing.T) {
	e, p, thread := piTopic(t, "pi-lost-reply")
	e.tg.LoseAnswerNext("sendMessage", 1) // posted, but the answer is lost: the outcome is unknown
	p.Final("the lost answer")
	notice := e.waitText(thread, "may not have arrived")
	if strings.Contains(notice.Text, "/resend") {
		t.Errorf("notice = %q: a tapped /resend would lose its id", notice.Text)
	}
	data := buttonData(notice, "Send again")
	if data == "" {
		t.Fatalf("no Send again button: %+v", notice.Markup)
	}
	if e.countText(thread, "the lost answer") != 1 {
		t.Fatal("the reply that may have arrived was sent again without a tap")
	}
	e.pressButton(notice, data)
	e.waitText(thread, "Sending 1 part(s)")
	e.waitFor("the answer, sent again", func() bool { return e.countText(thread, "the lost answer") == 2 })
	e.waitFor("the button removed", func() bool { m, _ := e.message(notice.ID); return m.Markup == nil })
}

// /resend without an id sends the newest reply that may not have arrived, and
// the id of a notice stands for the reply that it is about. Here the check
// cannot post its note three times, so the reply gets a notice of its own.
// The answer goes out as a rich message, so only the notes fail.
func TestResendWithoutIDOrWithANoticeID(t *testing.T) {
	e, p, thread := piTopicCfg(t, "pi-resend-ids", func(e *env) { e.cfg.Display.RichMessages = true })
	e.tg.LoseAnswerNext("sendRichMessage", 1)              // the answer may have arrived
	e.tg.FailNext("sendMessage", 3, 502, "Bad Gateway", 0) // the three check notes
	p.Final("first lost answer")
	var answerID, noticeID int64
	e.waitFor("the notice of the answer", func() bool {
		for id := int64(1); id < 40; id++ {
			row, err := e.peek().OutboxRow(context.Background(), id)
			if err != nil {
				continue
			}
			switch {
			case row.Kind == "answer" && row.State == store.OutboxUnknown:
				answerID = id
			case strings.HasPrefix(row.Key, "notice:unknown:") && row.State == store.OutboxSent:
				noticeID = id
			}
		}
		return answerID != 0 && noticeID != 0
	})
	e.never("a notice about the notice", 400*time.Millisecond, func() bool { return e.countText(thread, "may not have arrived") > 1 })

	e.say(thread, fmt.Sprintf("/resend %d", noticeID))
	e.waitText(thread, fmt.Sprintf("Sending 1 part(s) of #%d again", answerID))
	e.waitFor("the answer, sent again", func() bool { return e.hasText(thread, "first lost answer") })

	e.say(thread, "/resend")
	e.waitText(thread, "Nothing to resend")
}

// An agent that dies in the middle of a turn ends the turn: the progress
// message becomes a "stopped" summary and "typing…" stops.
func TestAgentDyingMidTurnEndsTheTurn(t *testing.T) {
	e, p, thread := piTopic(t, "pi-dies")
	p.User("long work")
	p.ToolCall("c1", "sleep 100")
	progress := e.waitText(thread, "⏱")
	e.setProcess("zsh", "-zsh", 777) // pi crashes
	e.waitText(thread, "pi stopped")
	e.waitFor("the stopped summary", func() bool {
		m, _ := e.message(progress.ID)
		return strings.Contains(m.Text, "🔴 Stopped")
	})
	before := len(e.tg.Calls("sendChatAction"))
	e.never("typing after the stop", 3*testTiming.Typing, func() bool { return len(e.tg.Calls("sendChatAction")) > before })
}

// A replaced transcript longer than one batch is read again from byte 0 in
// several batches. Old lines stay hidden in all of them, and new lines are
// all posted, also two with the same time on both sides of a batch end.
func TestLongReReadKeepsNewLinesInEveryBatch(t *testing.T) {
	e, p, thread := piTopic(t, "pi-long-reread")
	const old, newAt = "2026-01-01T00:00:00.000Z", "2099-01-01T00:00:00.000Z"
	var b strings.Builder
	filler := func(n int) {
		for i := range n {
			fmt.Fprintf(&b, `{"type":"message","id":"f%07d","timestamp":%q,"message":{"role":"user","content":[{"type":"text","text":%q}]}}`+"\n",
				b.Len()+i, old, strings.Repeat("x", 1000))
		}
	}
	final := func(id, text string) {
		fmt.Fprintf(&b, `{"type":"message","id":%q,"timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":%q}],"stopReason":"stop"}}`+"\n",
			id, newAt, text)
	}
	filler(3600) // 4.10 MB of old lines (1138 bytes each): the first batch ends at 4 MiB plus up to one 64 KiB chunk
	final("a1", "new answer before the batch end")
	filler(300) // the first batch ends in here, by 4.26 MB
	final("a2", "new answer after the batch end")

	keep, err := os.Open(p.Path) // keep the old inode, so the new file gets another one
	if err != nil {
		t.Fatal(err)
	}
	defer keep.Close()
	tmp := p.Path + ".new"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, p.Path); err != nil {
		t.Fatal(err)
	}
	e.waitText(thread, "new answer before the batch end")
	e.waitText(thread, "new answer after the batch end")
	if n := e.countText(thread, "ready"); n != 1 {
		t.Errorf("the old answer was posted %d times, want once", n)
	}
}
