package daemon_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rex-tg/internal/config"
	"rex-tg/internal/daemon"
	"rex-tg/internal/rex"
	"rex-tg/internal/rex/rextest"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram/tgtest"
)

const (
	ownerID  int64 = 1000
	chatID   int64 = 1000
	rexSess        = "S1"
	rexBlock       = "B1"
	paneCWD        = "/work/proj"
	piPID          = 4242
)

// env is one daemon under test: a fake Telegram, a fake Rex, a temporary
// home folder, and the state folder.
type env struct {
	t    *testing.T
	home string
	cfg  config.Config
	tg   *tgtest.Server
	fake *rextest.Fake

	seq atomic.Int64
	pi  *piLog

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan error
}

// newEnv makes a test environment and runs the test in parallel with the
// others: each one has its own folders, fake Rex, and fake Telegram server.
func newEnv(t *testing.T) *env {
	t.Helper()
	if _, again := parallelTests.LoadOrStore(t, true); !again {
		t.Parallel()
	}
	return newEnvSerial(t)
}

// parallelTests holds the tests that called t.Parallel, which may be called
// only once per test.
var parallelTests sync.Map

// newEnvSerial makes a test environment for a test that must not run in
// parallel, such as one that sets an environment variable.
func newEnvSerial(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	cfg := config.Default(home)
	cfg.State.Dir = filepath.Join(t.TempDir(), "state")
	cfg.Rex.ScanInterval = config.Duration{Duration: 200 * time.Millisecond}
	cfg.Display.ProgressDelay = config.Duration{Duration: 150 * time.Millisecond}
	cfg.Display.ProgressEditInterval = config.Duration{Duration: 100 * time.Millisecond}
	cfg.InputSettings.JoinWindow = config.Duration{Duration: 50 * time.Millisecond}
	cfg.InputSettings.JoinWindowLong = config.Duration{Duration: 100 * time.Millisecond}
	cfg.InputSettings.BusyAfterTyping = config.Duration{Duration: 400 * time.Millisecond}
	cfg.Telegram.RatePerSecond = 200
	cfg.Telegram.RateBurst = 200
	// Most tests drive turns with prompts written to the transcript, which
	// count as typed on the Mac. They test how turns are shown, so the hold
	// of Mac turns is off; macHoldTopic turns it on.
	cfg.Display.MacNotice = false
	for _, in := range []*config.RuntimeInput{&cfg.Agents.Pi, &cfg.Agents.Claude, &cfg.Agents.Codex} {
		in.PasteEnterDelay = config.Duration{Duration: 20 * time.Millisecond}
	}
	e := &env{t: t, home: home, cfg: cfg, tg: tgtest.New(t), fake: rextest.New(t)}
	e.fake.On([]string{"api", "call"}, "{}", 0)
	e.fake.On([]string{"send-key"}, "", 0)
	e.fake.On([]string{"capture"}, "", 0)
	e.fake.On([]string{"whoami"}, `{"rex_version":"test"}`, 0)
	e.setRex(rexSess, rexBlock)
	e.setProcess("node", "pi", piPID)
	e.setStatus("idle", "")
	t.Cleanup(e.stop)
	return e
}

// setRex scripts ls and block ls with one session and one block.
func (e *env) setRex(sess, block string) {
	e.fake.On([]string{"ls"}, fmt.Sprintf(`{"sessions":[{"session_id":%q,"label":"main"}]}`, sess), 0)
	e.fake.On([]string{"block", "ls"}, fmt.Sprintf(`{"blocks":[{"block_id":%q,"label":"pane one"}]}`, block), 0)
}

func (e *env) setProcess(name, argv0 string, pid int) {
	e.fake.On([]string{"block", "call", "-s", rexSess, "-b", rexBlock, "process"},
		fmt.Sprintf(`{"foreground":{"pid":%d,"name":%q,"argv0":%q,"cwd":%q},"ancestors":[]}`, pid, name, argv0, paneCWD), 0)
}

// setStatus scripts the program status. Its time is now, so it is the newest
// signal the registry has seen.
func (e *env) setStatus(state, kind string) {
	rec := map[string]any{"state": state, "kind": kind, "app": "pi", "updated_at": time.Now().UTC().Format(time.RFC3339Nano)}
	b, _ := json.Marshal(map[string]any{"records": []any{rec}})
	e.fake.On([]string{"block", "call", "-s", rexSess, "-b", rexBlock, "program_status"}, string(b), 0)
}

func (e *env) setScreen(text string) { e.fake.On([]string{"capture"}, text, 0) }

func (e *env) options() daemon.Options {
	return daemon.Options{
		Logger:    slog.New(slog.NewTextHandler(testWriter{e.t}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Config:    e.cfg,
		Token:     e.tg.Token(),
		Home:      e.home,
		Rex:       rex.New(e.fake.Bin()),
		Telegram:  e.tg.Client(),
		ProcStart: func(int) (time.Time, error) { return time.Now().Add(-time.Hour), nil },
		Timing:    testTiming,
		// The programs of the machine that runs the tests do not count.
		HasProgram: func(string) bool { return false },
	}
}

// testTiming shortens the daemon's fixed waits, so tests do not sleep through
// production-length pauses. A test that checks one of these waits uses the
// value from here.
var testTiming = daemon.Timing{
	Tick:         20 * time.Millisecond,
	Prep:         50 * time.Millisecond,
	TailPoll:     50 * time.Millisecond,
	TailerGrace:  300 * time.Millisecond,
	TailRestart:  300 * time.Millisecond,
	VimPoll:      10 * time.Millisecond,
	VimRetry:     300 * time.Millisecond,
	Spool:        100 * time.Millisecond,
	AnswerGrace:  600 * time.Millisecond,
	StoppedGrace: 200 * time.Millisecond,
	Typing:       500 * time.Millisecond,
	NewAgentWait: 3 * time.Second,
	NewAgentPoll: 50 * time.Millisecond,
	CheckDelay:   100 * time.Millisecond,
	UpdateCheck:  50 * time.Millisecond,
	BoardEdit:    50 * time.Millisecond,
	BoardRefresh: time.Hour, // tests trigger the board by state changes
	RemindCheck:  50 * time.Millisecond,
	Draft:        50 * time.Millisecond,
	DraftKeep:    time.Hour, // a test of the refresh sets its own
	LiveCheck:    30 * time.Millisecond,
}

// start runs the daemon in a goroutine.
func (e *env) start() {
	e.t.Helper()
	e.startWith(e.options())
}

func (e *env) startWith(opt daemon.Options) {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	e.mu.Lock()
	e.cancel, e.done = cancel, done
	e.mu.Unlock()
	go func() { done <- daemon.Run(ctx, opt) }()
}

// stop cancels the daemon and checks that Run returns.
func (e *env) stop() {
	e.mu.Lock()
	cancel, done := e.cancel, e.done
	e.cancel, e.done = nil, nil
	e.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			e.t.Errorf("Run returned %v after cancel", err)
		}
	case <-time.After(10 * time.Second):
		e.t.Error("Run did not return within 10s after cancel")
	}
}

// seedOwner pairs the owner directly in the store (the pairing tests use the
// real flow).
func (e *env) seedOwner() {
	e.t.Helper()
	e.withStore(func(st *store.Store) {
		ctx := context.Background()
		for k, v := range map[string]string{"owner_user_id": fmt.Sprint(ownerID), "owner_chat_id": fmt.Sprint(chatID)} {
			if err := st.Set(ctx, k, v); err != nil {
				e.t.Fatal(err)
			}
		}
	})
}

// withStore opens the database while the daemon is not running.
func (e *env) withStore(f func(st *store.Store)) {
	e.t.Helper()
	if err := os.MkdirAll(e.cfg.State.Dir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	st, err := store.Open(e.cfg.DBPath())
	if err != nil {
		e.t.Fatal(err)
	}
	defer st.Close()
	f(st)
}

// peek reads the database through a second, read-only connection.
func (e *env) peek() *store.Store {
	e.t.Helper()
	st, err := store.OpenReadOnly(e.cfg.DBPath())
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { st.Close() })
	return st
}

// waitFor polls cond until it holds.
func (e *env) waitFor(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for %s\n%s", what, e.dump())
}

// never checks that cond stays false for d.
func (e *env) never(what string, d time.Duration, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			e.t.Fatalf("unexpected: %s\n%s", what, e.dump())
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func (e *env) dump() string {
	var b strings.Builder
	b.WriteString("telegram messages:\n")
	for _, m := range e.tg.Messages(chatID) {
		fmt.Fprintf(&b, "  #%d thread=%d bot=%v react=%q del=%v edits=%d markup=%v: %q\n", m.ID, m.ThreadID, m.FromBot, m.Reaction, m.Deleted, m.Edits, m.Markup != nil, m.Text)
	}
	b.WriteString("rex calls:\n")
	for _, c := range e.fake.Calls() {
		if len(c.Args) > 1 && (strings.Contains(strings.Join(c.Args, " "), "send-key") || strings.Contains(strings.Join(c.Args, " "), "block.event")) {
			fmt.Fprintf(&b, "  %v %s\n", c.Args, c.Stdin)
		}
	}
	return b.String()
}

// --- Telegram helpers ---

// botMessages returns the bot's messages in a topic (0 = main chat).
func (e *env) botMessages(thread int64) []tgtest.Message {
	var out []tgtest.Message
	for _, m := range e.tg.Messages(chatID) {
		if m.FromBot && m.ThreadID == thread && !m.Deleted {
			out = append(out, m)
		}
	}
	return out
}

func (e *env) countText(thread int64, sub string) int {
	n := 0
	for _, m := range e.botMessages(thread) {
		if strings.Contains(m.Text, sub) {
			n++
		}
	}
	return n
}

func (e *env) hasText(thread int64, sub string) bool { return e.countText(thread, sub) > 0 }

func (e *env) waitText(thread int64, sub string) tgtest.Message {
	e.t.Helper()
	var found tgtest.Message
	e.waitFor(fmt.Sprintf("a bot message with %q in thread %d", sub, thread), func() bool {
		for _, m := range e.botMessages(thread) {
			if strings.Contains(m.Text, sub) {
				found = m
				return true
			}
		}
		return false
	})
	return found
}

func (e *env) message(id int64) (tgtest.Message, bool) {
	for _, m := range e.tg.Messages(chatID) {
		if m.ID == id {
			return m, true
		}
	}
	return tgtest.Message{}, false
}

func (e *env) say(thread int64, text string) int64 {
	u := e.tg.PushMessage(chatID, thread, ownerID, text)
	return u.Message.MessageID
}

func (e *env) pressButton(m tgtest.Message, data string) {
	e.tg.PushCallback(chatID, m.ThreadID, ownerID, m.ID, data)
}

func (e *env) callbackTexts() []string {
	var out []string
	for _, c := range e.tg.Calls("answerCallbackQuery") {
		s, _ := c.Params["text"].(string)
		out = append(out, s)
	}
	return out
}

// buttonData returns the callback data of the button whose text contains sub.
func buttonData(m tgtest.Message, sub string) string {
	if m.Markup == nil {
		return ""
	}
	for _, row := range m.Markup.InlineKeyboard {
		for _, b := range row {
			if strings.Contains(b.Text, sub) {
				return b.CallbackData
			}
		}
	}
	return ""
}

// --- Rex helpers ---

type rexCall struct {
	args  []string
	stdin string
}

func (e *env) rexCalls(kind string) []rexCall {
	var out []rexCall
	for _, c := range e.fake.Calls() {
		var args []string
		for _, a := range c.Args {
			if !strings.HasPrefix(a, "--autostart") {
				args = append(args, a)
			}
		}
		if len(args) == 0 {
			continue
		}
		switch kind {
		case "send-key":
			if args[0] == "send-key" {
				out = append(out, rexCall{args[5:], c.Stdin})
			}
		case "paste":
			if args[0] == "api" && len(args) > 4 && args[4] == "block.event" {
				out = append(out, rexCall{args, c.Stdin})
			}
		}
	}
	return out
}

// pastes returns the pasted texts.
func (e *env) pastes() []string {
	var out []string
	for _, c := range e.rexCalls("paste") {
		var body struct {
			Event struct {
				Payload struct {
					Text string `json:"text"`
				} `json:"payload"`
			} `json:"event"`
		}
		if json.Unmarshal([]byte(c.stdin), &body) == nil {
			out = append(out, body.Event.Payload.Text)
		}
	}
	return out
}

// keys returns the pressed keys, one string per send-key call.
func (e *env) keys() []string {
	var out []string
	for _, c := range e.rexCalls("send-key") {
		out = append(out, strings.Join(c.args, " "))
	}
	return out
}

// --- registry helpers ---

// spool writes a hook report of the test's pane in the spool folder.
func (e *env) spool(runtime, event string, payload map[string]any) {
	e.t.Helper()
	e.spoolFrom(rexSess, rexBlock, runtime, event, payload)
}

// spoolFrom writes a hook report from the agent in a Rex session and block.
func (e *env) spoolFrom(sess, block, runtime, event string, payload map[string]any) {
	e.t.Helper()
	dir := e.cfg.SpoolDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	rep := map[string]any{
		"runtime": runtime, "event": event, "at": time.Now().UTC().Format(time.RFC3339Nano),
		"rex_session": sess, "rex_block": block, "payload": payload,
	}
	b, _ := json.Marshal(rep)
	name := fmt.Sprintf("%d-%d-%d.json", time.Now().UnixNano(), os.Getpid(), e.seq.Add(1))
	tmp := filepath.Join(dir, "."+name)
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		e.t.Fatal(err)
	}
}

// paneID waits for the pane row and returns its ID.
func (e *env) paneID() int64 {
	e.t.Helper()
	var id int64
	e.waitFor("the pane row", func() bool {
		p, ok, err := e.peek().PaneByBlock(context.Background(), "local", rexBlock)
		if err == nil && ok {
			id = p.ID
			return true
		}
		return false
	})
	return id
}

// --- pi transcript ---

type piLog struct {
	t    *testing.T
	Path string
	ID   string
	n    int
}

// now formats the time with milliseconds, as the agents write it.
func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// newPi creates a pi transcript with a header, in the folder pi uses.
func (e *env) newPi(id string) *piLog {
	e.t.Helper()
	dir := filepath.Join(e.home, ".pi", "agent", "sessions", "--"+strings.ReplaceAll(strings.TrimPrefix(paneCWD, "/"), "/", "-")+"--")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	p := &piLog{t: e.t, ID: id, Path: filepath.Join(dir, "2026-01-01T00-00-00-000Z_"+id+".jsonl")}
	p.write(fmt.Sprintf(`{"type":"session","version":3,"id":%q,"timestamp":%q,"cwd":%q}`, id, now(), paneCWD))
	return p
}

func (p *piLog) write(lines ...string) {
	p.t.Helper()
	f, err := os.OpenFile(p.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		p.t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			p.t.Fatal(err)
		}
	}
}

func (p *piLog) nextID() string {
	p.n++
	return fmt.Sprintf("%08x", p.n)
}

func jstr(s string) string { b, _ := json.Marshal(s); return string(b) }

func (p *piLog) User(text string) {
	p.write(fmt.Sprintf(`{"type":"message","id":%q,"timestamp":%q,"message":{"role":"user","content":[{"type":"text","text":%s}]}}`,
		p.nextID(), now(), jstr(text)))
}

func (p *piLog) Final(text string) {
	p.write(fmt.Sprintf(`{"type":"message","id":%q,"timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":%s}],"stopReason":"stop"}}`,
		p.nextID(), now(), jstr(text)))
}

// ToolCall writes an assistant message that calls the bash tool.
func (p *piLog) ToolCall(callID, cmd string) {
	p.write(fmt.Sprintf(`{"type":"message","id":%q,"timestamp":%q,"message":{"role":"assistant","content":[{"type":"toolCall","id":%q,"name":"bash","arguments":{"command":%s}}],"stopReason":"toolUse"}}`,
		p.nextID(), now(), callID, jstr(cmd)))
}

func (p *piLog) ToolResult(callID string) {
	p.write(fmt.Sprintf(`{"type":"message","id":%q,"timestamp":%q,"message":{"role":"toolResult","toolCallId":%q,"content":[{"type":"text","text":"ok"}],"durationMs":120}}`,
		p.nextID(), now(), callID))
}

// hookPi reports a pi session_start for the transcript.
func (e *env) hookPi(p *piLog, reason string) {
	e.spool("pi", "session_start", map[string]any{
		"hook_event_name": "session_start", "reason": reason, "session_file": p.Path, "cwd": paneCWD, "session_id": p.ID,
	})
}

// openTopic lists the agents, presses the open button, and returns the new
// topic's thread ID. The pane must have a session already.
// agentsList sends /agents in the main chat and returns the new list.
func (e *env) agentsList() tgtest.Message {
	e.t.Helper()
	before := len(e.botMessages(0))
	e.say(0, "/agents")
	var list tgtest.Message
	e.waitFor("the agents list", func() bool {
		ms := e.botMessages(0)
		if len(ms) > before && strings.Contains(ms[len(ms)-1].Text, "Active agents") {
			list = ms[len(ms)-1]
			return true
		}
		return false
	})
	return list
}

func (e *env) openTopic() int64 {
	e.t.Helper()
	e.waitFor("a resolved pane", func() bool {
		p, ok, err := e.peek().PaneByBlock(context.Background(), "local", rexBlock)
		return err == nil && ok && p.ConversationID != 0
	})
	list := e.agentsList()
	data := buttonData(list, "Open")
	if data == "" {
		e.t.Fatalf("no open button in the agents list: %+v", list.Markup)
	}
	e.pressButton(list, data)
	var thread int64
	e.waitFor("the topic", func() bool {
		ts := e.tg.Topics(chatID)
		if len(ts) == 0 {
			return false
		}
		thread = ts[0].ThreadID
		return true
	})
	e.waitText(thread, "Linked")
	return thread
}

// testWriter sends daemon logs to the test log.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(b []byte) (int, error) {
	defer func() { _ = recover() }() // the test may have ended
	w.t.Log(strings.TrimRight(string(b), "\n"))
	return len(b), nil
}

func (e *env) waitPaneState(state string) {
	e.t.Helper()
	e.waitFor("pane state "+state, func() bool {
		p, ok, err := e.peek().PaneByBlock(context.Background(), "local", rexBlock)
		return err == nil && ok && p.State == state
	})
}
