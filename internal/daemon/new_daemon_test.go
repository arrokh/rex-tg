package daemon_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/store"
)

// rexCommand returns the arguments, without the global flags, of the first
// rex call that starts with prefix, or nil.
func (e *env) rexCommand(prefix ...string) []string {
	for _, c := range e.fake.Calls() {
		args := c.Args
		for len(args) > 0 && strings.HasPrefix(args[0], "--autostart") {
			args = args[1:]
		}
		if len(args) >= len(prefix) && slices.Equal(args[:len(prefix)], prefix) {
			return args
		}
	}
	return nil
}

func (e *env) countRex(prefix ...string) int {
	n := 0
	for _, c := range e.fake.Calls() {
		if strings.Contains(strings.Join(c.Args, " "), strings.Join(prefix, " ")) {
			n++
		}
	}
	return n
}

// /new with the buttons: the folder has no Rex session yet, so rex-tg makes
// one named after the folder, and opens the topic once the agent reports
// its session. A second tap on a button starts nothing.
func TestNewStartsAnAgentAndOpensItsTopic(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	dir := filepath.Join(e.home, "work", "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A session that ran in the folder before puts it on a button.
	e.withStore(func(st *store.Store) {
		_, err := st.UpsertConversation(context.Background(), store.Conversation{
			Runtime: agents.Claude, NativeID: "earlier", Transcript: filepath.Join(dir, "earlier.jsonl"), CWD: dir, FirstSeenAt: time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	e.fake.On([]string{"new"}, `{"session_id":"S2","initial_windows":[{"window_id":"W2","block_ids":["B2"]}]}`, 0)
	e.start()

	e.say(0, "/new")
	q := e.waitText(0, "Which coding agent?")
	e.pressButton(q, buttonData(q, "pi"))
	e.waitFor("the folder question", func() bool {
		m, _ := e.message(q.ID)
		q = m
		return strings.Contains(m.Text, "Which folder?")
	})
	folder := buttonData(q, "~/work/app")
	if folder == "" {
		t.Fatalf("no button for the folder: %+v", q.Markup)
	}
	e.pressButton(q, folder)

	var args []string
	e.waitFor("rex new", func() bool { args = e.rexCommand("new"); return args != nil })
	if want := []string{"new", "--cwd=" + dir, "--window=pi", "--interactive", "--json", "app", "--", "pi"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("rex %q,\nwant %q", args, want)
	}
	e.waitFor("the started note", func() bool {
		m, _ := e.message(q.ID)
		return strings.Contains(m.Text, "Started pi in") && strings.Contains(m.Text, "new Rex session") && m.Markup == nil
	})

	// The agent starts in the new pane and reports its session.
	e.fake.On([]string{"ls"}, `{"sessions":[{"session_id":"S1","label":"main"},{"session_id":"S2","label":"app"}]}`, 0)
	e.fake.On([]string{"block", "ls", "-s", "S2"}, `{"blocks":[{"block_id":"B2","label":"shell"}]}`, 0)
	e.fake.On([]string{"block", "call", "-s", "S2", "-b", "B2", "process"},
		fmt.Sprintf(`{"foreground":{"pid":5151,"name":"node","argv0":"pi","cwd":%q},"ancestors":[]}`, dir), 0)
	e.fake.On([]string{"block", "call", "-s", "S2", "-b", "B2", "program_status"}, `{"records":[]}`, 0)
	transcript := filepath.Join(e.home, "new-session.jsonl")
	header := fmt.Sprintf(`{"type":"session","version":3,"id":"new-1","timestamp":%q,"cwd":%q}`+"\n", now(), dir)
	if err := os.WriteFile(transcript, []byte(header), 0o644); err != nil {
		t.Fatal(err)
	}
	e.spoolFrom("S2", "B2", "pi", "session_start", map[string]any{
		"hook_event_name": "session_start", "reason": "startup", "session_file": transcript, "cwd": dir, "session_id": "new-1",
	})

	var thread int64
	e.waitFor("the agent's topic", func() bool {
		for _, tp := range e.tg.Topics(chatID) {
			if tp.Name == "app" {
				thread = tp.ThreadID
				return true
			}
		}
		return false
	})
	e.waitText(thread, "Linked")
	e.waitText(0, "Opened app")

	// The old folder button only says that the request is over.
	e.pressButton(q, folder)
	e.waitFor("the answer to the old button", func() bool {
		return slices.Contains(e.callbackTexts(), "This request expired. Send /new again.")
	})
	if n := e.countRex("new", "--cwd"); n != 1 {
		t.Fatalf("rex new ran %d times, want 1", n)
	}
}

// The question's message arrived, but Telegram's answer to the send was
// lost. Its buttons still work: the request exists before the send, and a
// button tells which message the question is.
func TestNewQuestionWorksWhenTheSendAnswerIsLost(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	e.start()
	e.tg.LoseAnswerNext("sendMessage", 1)
	e.say(0, "/new")
	q := e.waitText(0, "Which coding agent?")
	e.pressButton(q, buttonData(q, "Codex"))
	e.waitFor("the folder question in the same message", func() bool {
		m, _ := e.message(q.ID)
		q = m
		return strings.Contains(m.Text, "New Codex")
	})
	e.pressButton(q, buttonData(q, "Cancel"))
	e.waitFor("the cancel note", func() bool {
		m, _ := e.message(q.ID)
		return strings.Contains(m.Text, "Cancelled") && m.Markup == nil
	})
	if n := e.countRex("new", "--cwd") + e.countRex("window", "new"); n != 0 {
		t.Errorf("%d rex calls that start an agent, want none", n)
	}
}

// In a topic that is linked to an agent, /new answers the bot, and a path
// answers the folder question while other text still goes to the agent.
// The folder's name matches the Rex session, so the agent gets a new tab
// there. This agent then exits at once, and the topic shows why.
func TestNewInALinkedTopicTakesOnlyAPath(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	p := e.newPi("s-1")
	e.start()
	e.hookPi(p, "startup")
	thread := e.openTopic()

	dir := filepath.Join(e.home, "main") // the test's Rex session is named "main"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	e.fake.On([]string{"window", "new"}, `{"window_id":"W2","block_ids":["B2"]}`, 0)
	e.fake.On([]string{"block", "call", "-s", rexSess, "-b", "B2", "process"},
		`{"foreground":null,"child":null,"last_exit":{"exit_code":127,"error":"exit status 127"}}`, 0)
	e.fake.On([]string{"capture", "-s", rexSess, "-b", "B2"}, "zsh:1: command not found: pi\n", 0)

	e.say(thread, "/pause")
	e.say(thread, "/new")
	e.waitText(thread, "starts no agent")
	e.say(thread, "/unpause")
	e.waitText(thread, "Typing is on again")

	e.say(thread, "/new pi")
	e.waitText(thread, "Which folder?")
	e.say(thread, "hello agent")
	e.say(thread, "/compact")
	e.say(thread, "//new")
	e.waitFor("the messages for the agent", func() bool { // messages close together become one prompt
		all := strings.Join(e.pastes(), "\n")
		return strings.Contains(all, "hello agent") && strings.Contains(all, "/compact") && strings.Contains(all, "/new")
	})
	e.say(thread, "~/nowhere")
	e.waitText(thread, "There is no folder")
	e.say(thread, "~/main")

	var args []string
	e.waitFor("rex window new", func() bool { args = e.rexCommand("window", "new"); return args != nil })
	want := []string{"window", "new", "--session=" + rexSess, "--cwd=" + dir, "--interactive", "--focus=false", "--json", "pi", "--", "pi"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("rex %q,\nwant %q", args, want)
	}
	e.waitText(thread, "a new tab of the Rex session")
	m := e.waitText(thread, "exited with code 127")
	if !strings.Contains(m.Text, "command not found") {
		t.Errorf("the exit note does not show the screen: %q", m.Text)
	}
	if n := e.countRex("new", "--cwd"); n != 0 {
		t.Errorf("rex new ran %d times; the session exists", n)
	}
	for _, text := range e.pastes() {
		if strings.Contains(text, "nowhere") || strings.Contains(text, "~/main") {
			t.Errorf("typed into the agent: %q", text)
		}
	}
}

// trustScreen is Claude Code's trust question with the cursor on answer i:
// no numbers, and "No, exit" first.
func trustScreen(i int) string {
	answers := []string{"No, exit", "Yes, I trust this folder"}
	s := " Quick safety check: Is this a project you created or one you trust?\n\n"
	for k, a := range answers {
		if k == i {
			s += " ❯ " + a + "\n"
		} else {
			s += "   " + a + "\n"
		}
	}
	return s + "\n Enter to confirm · Esc to cancel\n"
}

// trustEnv is a daemon whose next /new starts Claude Code in pane S2/B2,
// which shows the trust question. wait is the start wait (0: the test
// default).
func trustEnv(t *testing.T, wait time.Duration) (*env, string) {
	e := newEnv(t)
	e.seedOwner()
	dir := filepath.Join(e.home, "work", "fresh")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	e.fake.On([]string{"new"}, `{"session_id":"S2","initial_windows":[{"window_id":"W2","block_ids":["B2"]}]}`, 0)
	e.fake.On([]string{"block", "call", "-s", "S2", "-b", "B2", "process"},
		fmt.Sprintf(`{"foreground":{"pid":6161,"name":"2.1.322","argv0":"claude","cwd":%q}}`, dir), 0)
	e.fake.On([]string{"block", "call", "-s", "S2", "-b", "B2", "program_status"}, `{"records":[]}`, 0)
	e.fake.SetScreenMachine("no", map[string]string{
		"no": trustScreen(0), "yes": trustScreen(1), "started": "╭───╮\n│ > │\n╰───╯\n",
	}, map[string]map[string]string{
		"no":  {"down": "yes"},
		"yes": {"up": "no", "enter": "started"},
	})
	opt := e.options()
	if wait > 0 {
		opt.Timing.NewAgentWait = wait
	}
	e.startWith(opt)
	return e, dir
}

// A new Claude Code asks whether you trust the folder before its session
// starts, so it has no topic yet. The question comes to the chat with its
// answers as buttons. The button moves the cursor with Down and presses
// Enter, and the agent's topic opens once it reports its session.
func TestNewAnswersTheTrustQuestion(t *testing.T) {
	e, dir := trustEnv(t, 0)
	e.say(0, "/new claude ~/work/fresh")
	q := e.waitText(0, "asks this before it starts")
	if !strings.Contains(q.Text, "Quick safety check") || buttonData(q, "No, exit") == "" {
		t.Fatalf("question: %q %+v", q.Text, q.Markup)
	}
	e.pressButton(q, buttonData(q, "Yes, I trust this folder"))
	e.waitFor("the answer note", func() bool {
		m, _ := e.message(q.ID)
		return strings.Contains(m.Text, "Answered: Yes, I trust this folder") && m.Markup == nil
	})
	if got := strings.Join(e.keys(), ","); got != "down,enter" {
		t.Fatalf("keys %q, want down,enter", got)
	}

	// Now the agent starts its session.
	e.fake.On([]string{"ls"}, `{"sessions":[{"session_id":"S1","label":"main"},{"session_id":"S2","label":"fresh"}]}`, 0)
	e.fake.On([]string{"block", "ls", "-s", "S2"}, `{"blocks":[{"block_id":"B2","label":"shell"}]}`, 0)
	transcript := filepath.Join(e.home, "claude-new.jsonl")
	if err := os.WriteFile(transcript, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	e.spoolFrom("S2", "B2", "claude", "SessionStart", map[string]any{
		"hook_event_name": "SessionStart", "session_id": "c-new-1", "transcript_path": transcript, "cwd": dir, "source": "startup",
	})
	e.waitText(0, "Opened fresh")
	if got := strings.Join(e.keys(), ","); got != "down,enter" {
		t.Errorf("keys after the start %q, want down,enter", got)
	}
}

// A question that waits for you keeps rex-tg waiting too, past the start
// wait. Then you answer it on the Mac instead: its buttons go, and the
// message says why. No key is pressed.
func TestNewTrustQuestionAnsweredOnTheMac(t *testing.T) {
	e, _ := trustEnv(t, 300*time.Millisecond)
	e.say(0, "/new claude ~/work/fresh")
	q := e.waitText(0, "asks this before it starts")
	time.Sleep(time.Second) // longer than the start wait
	if e.hasText(0, "has not reported its session") {
		t.Fatal("rex-tg stopped waiting while the question was open")
	}
	e.fake.SetScreenState("started")
	e.waitFor("the closed question", func() bool {
		m, _ := e.message(q.ID)
		return strings.Contains(m.Text, "no longer on the screen") && m.Markup == nil
	})
	e.pressButton(q, buttonData(q, "Yes, I trust this folder")) // a late tap
	e.waitFor("the answer to the late tap", func() bool {       // before or after the wait ended
		cb := e.callbackTexts()
		return slices.Contains(cb, "This question is no longer open.") || slices.Contains(cb, "This request expired. Send /new again.")
	})
	if keys := e.keys(); len(keys) != 0 {
		t.Errorf("keys %q, want none", keys)
	}
}
