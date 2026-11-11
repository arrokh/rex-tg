// Package registry owns panes, sessions, and agent state (design sections
// 4.4, 6, and 7).
//
// It reads Rex through the RexAPI interface and writes pane, conversation,
// and topic-status rows through *store.Store. It never talks to Telegram.
// Every method returns Change values that the daemon acts on: post dividers
// and notices, start or stop tailers.
package registry

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/agents/claude"
	"rex-tg/internal/agents/codex"
	"rex-tg/internal/agents/pi"
	"rex-tg/internal/rex"
	"rex-tg/internal/store"
)

// RexAPI is the part of the Rex CLI wrapper that the registry uses.
type RexAPI interface {
	Sessions(ctx context.Context) ([]rex.Session, error)
	Blocks(ctx context.Context, session string) ([]rex.Block, error)
	Process(ctx context.Context, session, block string) (rex.Process, error)
	ProgramStatus(ctx context.Context, session, block string) ([]rex.StatusRecord, error)
}

// Signal is a state signal from a transcript tailer. State is working, idle,
// done, blocked, or error. Kind is the blocked kind (permission, question,
// auth), or "prompt" with State working for a prompt line: a prompt always
// means working, even after a stale done.
type Signal struct {
	State, Kind string
	At          time.Time
}

// AgentView is one row of /agents.
type AgentView struct {
	Pane         store.Pane
	Runtime      agents.Runtime
	Conversation *store.Conversation
	Resolution   string // hook, scan, guess, unresolved
	Thread       *store.Thread
}

// ChangeKind names what happened.
type ChangeKind string

// Change kinds.
const (
	SessionStarted ChangeKind = "session_started" // a resolved session now runs in a pane; no topic action
	SessionStopped ChangeKind = "session_stopped" // the session left the pane (stop its tailer)
	TakeOver       ChangeKind = "take_over"       // a new session took over a topic
	Resume         ChangeKind = "resume"          // a session of a topic runs again
	ResumeBusy     ChangeKind = "resume_busy"     // resumed while its topic is live elsewhere
	TopicOffline   ChangeKind = "topic_offline"
	TopicLive      ChangeKind = "topic_live"
	StateChanged   ChangeKind = "state_changed" // State "blocked" means a prompt is open
	PaneGone       ChangeKind = "pane_gone"     // ConversationID is the session that ran there, if any
)

// TailStart tells where the tailer of a session starts. All false means the
// saved cursor.
type TailStart struct{ FromStart, AtEnd, Backfill bool }

// Change is one thing the daemon should act on.
type Change struct {
	Kind                             ChangeKind
	PaneID, ConversationID, ThreadID int64
	State, StateKind, Msg            string // StateChanged
	Divider                          string // TakeOver, Resume
	Start                            TailStart
}

// Options configure a Registry.
type Options struct {
	Host     string // Rex host ID stored with panes; default "local"
	Home     string // the user's home folder, for transcript lookups
	Rules    Rules
	Adapters map[agents.Runtime]agents.Adapter // default: the three real adapters
	Now      func() time.Time                  // default time.Now
	Logger   *slog.Logger                      // default: discard
	// ProcStart returns the start time of a process. Default: `ps -o lstart= -p <pid>`.
	// When it fails, the time the registry first saw the process is used.
	ProcStart func(pid int) (time.Time, error)
	// BusyAfterTyping is how long MarkTyped keeps a pane busy. Default 15 s.
	BusyAfterTyping time.Duration
}

type procIdent struct {
	pid   int
	name  string
	rt    agents.Runtime
	cwd   string
	start time.Time
}

type paneMem struct {
	hasProc     bool
	proc        procIdent
	hookAt      time.Time // newest session-bearing hook applied
	state, kind string
	stateAt     time.Time
	lastWorking time.Time
	lastIdle    time.Time
}

// Registry is the pane and session registry. It is safe for concurrent use;
// calls run one at a time.
type Registry struct {
	st  *store.Store
	rx  RexAPI
	opt Options

	mu    sync.Mutex
	panes map[int64]*paneMem
}

// New returns a Registry.
func New(st *store.Store, rx RexAPI, opt Options) *Registry {
	if opt.Host == "" {
		opt.Host = "local"
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Logger == nil {
		opt.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opt.ProcStart == nil {
		opt.ProcStart = psStart
	}
	if opt.BusyAfterTyping <= 0 {
		opt.BusyAfterTyping = 15 * time.Second
	}
	if opt.Adapters == nil {
		opt.Adapters = map[agents.Runtime]agents.Adapter{
			agents.Pi: pi.New(), agents.Claude: claude.New(), agents.Codex: codex.New(),
		}
	}
	return &Registry{st: st, rx: rx, opt: opt, panes: map[int64]*paneMem{}}
}

// psStart runs `ps -o lstart= -p <pid>` and parses the answer.
func psStart(pid int) (time.Time, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return time.Time{}, fmt.Errorf("registry: ps for pid %d: %w", pid, err)
	}
	return parsePSStart(string(out))
}

func parsePSStart(s string) (time.Time, error) {
	f := strings.Fields(s)
	if len(f) != 5 {
		return time.Time{}, fmt.Errorf("registry: unexpected ps lstart %q", strings.TrimSpace(s))
	}
	t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(f, " "), time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("registry: parse ps lstart %q: %w", strings.TrimSpace(s), err)
	}
	return t, nil
}

// memFor returns the in-memory record of a pane, filling it from the row the
// first time (after a restart).
func (r *Registry) memFor(ctx context.Context, p store.Pane) *paneMem {
	if m, ok := r.panes[p.ID]; ok {
		return m
	}
	m := &paneMem{state: p.State, kind: p.StateKind, stateAt: p.StateAt}
	switch p.State {
	case "working":
		m.lastWorking = p.StateAt
	case "idle", "done":
		m.lastIdle = p.StateAt
	}
	if p.ConversationID != 0 && p.PID != 0 {
		m.hasProc = true
		m.proc = procIdent{pid: p.PID, name: p.ProcessName}
		if c, err := r.st.Conversation(ctx, p.ConversationID); err == nil {
			m.proc.rt, m.proc.cwd = c.Runtime, c.CWD
		}
		m.proc.start = r.startOf(p.PID, p.SeenAt)
	}
	r.panes[p.ID] = m
	return m
}

func (r *Registry) startOf(pid int, fallback time.Time) time.Time {
	if t, err := r.opt.ProcStart(pid); err == nil && !t.IsZero() {
		return t
	}
	return fallback
}

func (r *Registry) adapter(rt agents.Runtime) (agents.Adapter, bool) {
	a, ok := r.opt.Adapters[rt]
	return a, ok && a != nil
}
