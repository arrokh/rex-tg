package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/rex"
	"rex-tg/internal/store"
)

// fakeRex implements RexAPI. It never touches a real Rex server.
type fakeRex struct {
	mu       sync.Mutex
	sessions []rex.Session
	blocks   map[string][]rex.Block
	procs    map[string]rex.Process // by block ID
	status   map[string][]rex.StatusRecord
	procErr  error
}

func newFakeRex() *fakeRex {
	return &fakeRex{blocks: map[string][]rex.Block{}, procs: map[string]rex.Process{}, status: map[string][]rex.StatusRecord{}}
}

func (f *fakeRex) Sessions(context.Context) ([]rex.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rex.Session(nil), f.sessions...), nil
}

func (f *fakeRex) Blocks(_ context.Context, s string) ([]rex.Block, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rex.Block(nil), f.blocks[s]...), nil
}

func (f *fakeRex) Process(_ context.Context, _, b string) (rex.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.procErr != nil {
		return rex.Process{}, f.procErr
	}
	p, ok := f.procs[b]
	if !ok {
		return rex.Process{}, errors.New("no such block")
	}
	return p, nil
}

func (f *fakeRex) ProgramStatus(_ context.Context, _, b string) ([]rex.StatusRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status[b], nil
}

// setBlock makes a block exist in session s with the given foreground process.
func (f *fakeRex) setBlock(s, b string, fg rex.ProcInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	found := false
	for _, x := range f.sessions {
		found = found || x.ID == s
	}
	if !found {
		f.sessions = append(f.sessions, rex.Session{ID: s})
	}
	have := false
	for _, x := range f.blocks[s] {
		have = have || x.ID == b
	}
	if !have {
		f.blocks[s] = append(f.blocks[s], rex.Block{ID: b})
	}
	f.procs[b] = rex.Process{Foreground: fg}
}

func (f *fakeRex) removeBlock(s, b string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keep []rex.Block
	for _, x := range f.blocks[s] {
		if x.ID != b {
			keep = append(keep, x)
		}
	}
	f.blocks[s] = keep
	delete(f.procs, b)
}

// rules are the default rules (config.Default) with test paths.
var rules = Rules{
	PiArgv0:        []string{"pi"},
	ClaudeArgv0:    []string{"claude", "cux"},
	ClaudeExecGlob: []string{"/h/.local/share/claude/versions/*"},
	CodexNames:     []string{"codex"},
}

func claudeProc(pid int, cwd string) rex.ProcInfo {
	return rex.ProcInfo{PID: pid, Name: "2.1.322", Argv0: "claude", Path: "/h/.local/share/claude/versions/2.1.322",
		InvokedPath: "/h/.local/bin/claude", CWD: cwd}
}

// cuxProc is cux in front of a pane: it runs claude as its child, in the
// same process group, so Rex reports cux as the foreground.
func cuxProc(pid int, cwd string) rex.ProcInfo {
	return rex.ProcInfo{PID: pid, Name: "cux", Argv0: "cux", Path: "/h/.local/bin/cux", InvokedPath: "/h/.local/bin/cux", CWD: cwd}
}
func piProc(pid int, cwd string) rex.ProcInfo {
	return rex.ProcInfo{PID: pid, Name: "node", Argv0: "pi", Path: "/usr/local/bin/node", CWD: cwd}
}
func codexProc(pid int, cwd string) rex.ProcInfo {
	return rex.ProcInfo{PID: pid, Name: "codex", Argv0: "codex", Path: "/opt/codex/bin/codex", CWD: cwd}
}
func shellProc(pid int) rex.ProcInfo {
	return rex.ProcInfo{PID: pid, Name: "zsh", Argv0: "-zsh", Path: "/bin/zsh", CWD: "/w"}
}

const (
	sess1 = "session:one"
	cwdA  = "/w/proj"
)

type env struct {
	t     *testing.T
	ctx   context.Context
	st    *store.Store
	rx    *fakeRex
	reg   *Registry
	home  string
	base  time.Time
	now   time.Time
	start map[int]time.Time
	nChat int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "rex-tg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{t: t, ctx: context.Background(), st: st, rx: newFakeRex(), home: t.TempDir(),
		base: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC), start: map[int]time.Time{}}
	e.now = e.base.Add(time.Hour)
	e.reg = New(st, e.rx, Options{
		Host: "h", Home: e.home, Rules: rules,
		Now: func() time.Time { return e.now },
		ProcStart: func(pid int) (time.Time, error) {
			if s, ok := e.start[pid]; ok {
				return s, nil
			}
			return e.base, nil
		},
	})
	return e
}

// at returns a time sec seconds after the base time (which is the start time
// of every process unless a test says otherwise).
func (e *env) at(sec float64) time.Time {
	return e.base.Add(time.Duration(sec * float64(time.Second)))
}

func (e *env) hookRaw(rt agents.Runtime, event, block string, at time.Time, payload map[string]any) ([]Change, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		e.t.Fatal(err)
	}
	if event == "" {
		event, _ = payload["hook_event_name"].(string)
	}
	return e.reg.Hook(e.ctx, HookReport{Runtime: rt, Event: event, At: at, RexSession: sess1, RexBlock: block, Payload: b})
}

func (e *env) hook(rt agents.Runtime, event, block string, at time.Time, payload map[string]any) []Change {
	e.t.Helper()
	ch, err := e.hookRaw(rt, event, block, at, payload)
	if err != nil {
		e.t.Fatalf("hook %s: %v", event, err)
	}
	return ch
}

func (e *env) claudePath(id string) string {
	return filepath.Join(e.home, ".claude", "projects", projectDir(cwdA), id+".jsonl")
}

func (e *env) claudeHook(block, event, id, source string, at time.Time) []Change {
	e.t.Helper()
	p := map[string]any{"hook_event_name": event, "session_id": id, "transcript_path": e.claudePath(id), "cwd": cwdA}
	if source != "" {
		p["source"] = source
	}
	return e.hook(agents.Claude, event, block, at, p)
}

func (e *env) piHook(block, event, id, reason string, at time.Time) []Change {
	e.t.Helper()
	p := map[string]any{"hook_event_name": event, "session_id": id, "cwd": cwdA,
		"session_file": filepath.Join(e.home, ".pi", "agent", "sessions", "x", id+".jsonl")}
	if reason != "" {
		p["reason"] = reason
	}
	return e.hook(agents.Pi, event, block, at, p)
}

func (e *env) event(typ rex.EventType, block string) []Change {
	e.t.Helper()
	ch, err := e.reg.RexEvent(e.ctx, rex.Event{Type: typ, SessionID: sess1, BlockID: block})
	if err != nil {
		e.t.Fatalf("event %s: %v", typ, err)
	}
	return ch
}

func (e *env) status(block, state, kind string, at time.Time) []Change {
	e.t.Helper()
	ch, err := e.reg.RexEvent(e.ctx, rex.Event{Type: rex.ProgramStatusChanged, SessionID: sess1, BlockID: block,
		Status: &rex.StatusRecord{State: state, Kind: kind, UpdatedAt: at}})
	if err != nil {
		e.t.Fatalf("status %s: %v", state, err)
	}
	return ch
}

func (e *env) conv(rt agents.Runtime, id string) store.Conversation {
	e.t.Helper()
	c, ok, err := e.st.ConversationByNative(e.ctx, rt, id)
	if err != nil || !ok {
		e.t.Fatalf("conversation %s: ok=%v err=%v", id, ok, err)
	}
	return c
}

func (e *env) pane(block string) store.Pane {
	e.t.Helper()
	p, ok, err := e.st.PaneByBlock(e.ctx, "h", block)
	if err != nil || !ok {
		e.t.Fatalf("pane %s: ok=%v err=%v", block, ok, err)
	}
	return p
}

// link gives a conversation a topic.
func (e *env) link(c store.Conversation) store.Thread {
	e.t.Helper()
	e.nChat++
	th, err := e.st.LinkTopic(e.ctx, 1, 100+e.nChat, c.ID, "topic", e.now)
	if err != nil {
		e.t.Fatal(err)
	}
	return th
}

func (e *env) thread(id int64) store.Thread {
	e.t.Helper()
	th, err := e.st.Thread(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return th
}

func (e *env) invariants() {
	e.t.Helper()
	if err := e.st.CheckInvariants(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

func kinds(ch []Change) []ChangeKind {
	out := make([]ChangeKind, len(ch))
	for i, c := range ch {
		out[i] = c.Kind
	}
	return out
}

func find(t *testing.T, ch []Change, k ChangeKind) Change {
	t.Helper()
	for _, c := range ch {
		if c.Kind == k {
			return c
		}
	}
	t.Fatalf("no %s change in %v", k, kinds(ch))
	return Change{}
}

func has(ch []Change, k ChangeKind) bool {
	for _, c := range ch {
		if c.Kind == k {
			return true
		}
	}
	return false
}

func wantKinds(t *testing.T, ch []Change, want ...ChangeKind) {
	t.Helper()
	got := kinds(ch)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("changes = %v, want %v", got, want)
	}
}

// --- fake transcript homes -------------------------------------------------

var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]`)

func projectDir(cwd string) string { return nonAlnum.ReplaceAllString(cwd, "-") }

func writeFile(t *testing.T, path, body string, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func (e *env) writeClaude(id, cwd string, started, mod time.Time) string {
	p := filepath.Join(e.home, ".claude", "projects", projectDir(cwd), id+".jsonl")
	writeFile(e.t, p, fmt.Sprintf(`{"type":"user","sessionId":%q,"cwd":%q,"timestamp":%q}`+"\n",
		id, cwd, started.Format(time.RFC3339Nano)), mod)
	return p
}

func (e *env) writePi(id, cwd string, started, mod time.Time) string {
	dir := "--" + strings.ReplaceAll(strings.TrimPrefix(cwd, "/"), "/", "-") + "--"
	p := filepath.Join(e.home, ".pi", "agent", "sessions", dir, id+".jsonl")
	writeFile(e.t, p, fmt.Sprintf(`{"type":"session","version":3,"id":%q,"timestamp":%q,"cwd":%q}`+"\n",
		id, started.Format(time.RFC3339Nano), cwd), mod)
	return p
}
