// Package turn tracks one topic's agent turn and renders the live progress
// message and the end-of-turn summary (design sections 4.2, 4.4, 8.1-8.5).
//
// The package is pure. Every method takes the current time, nothing starts a
// goroutine or timer, and Tick drives everything that depends on time. The
// caller carries out the returned Actions in order.
package turn

import (
	"strings"
	"time"

	"rex-tg/internal/agents"
)

// State is the state of an agent pane, as the registry merges it (design 4.4).
type State string

// Pane states.
const (
	StateIdle    State = "idle"
	StateWorking State = "working"
	StateDone    State = "done"
	StateBlocked State = "blocked"
	StateError   State = "error"
	StateStopped State = "stopped" // the agent process went away
)

// Settings are the per-topic display settings.
type Settings struct {
	Mode           string // progress (default), block, or final
	Verbose        string // off (default), on, or full
	Reasoning      bool
	ShowMacPrompts bool
	// MacNotice holds turns that start on the Mac: instead of the prompt,
	// progress, and answer, the topic gets one notice that is edited as the
	// turns go on. A message from Telegram ends the hold. ShowMacPrompts
	// counts only when MacNotice is off.
	MacNotice bool
}

// Config holds timing and size limits. Zero fields take their defaults.
type Config struct {
	ProgressDelay time.Duration // default 2s
	EditInterval  time.Duration // default 2s
	// TimerInterval is the least time between two edits that only move the
	// elapsed time on, while nothing else changes (the agent thinks or a long
	// tool runs). The step grows with the turn, up to one minute. Default 10s.
	TimerInterval time.Duration
	AnswerGrace   time.Duration // default 5s
	// StoppedGrace is the wait for the last lines of an agent that stopped.
	// Its lines were written before it exited, so they are read by the time
	// the stop is seen; a short wait is enough. Default 2s.
	StoppedGrace   time.Duration
	TypingInterval time.Duration // default 5s
	MaxRows        int           // default 8
	MaxSummaryRows int           // default 30
	MaxRowRunes    int           // default 120
	Redact         func(string) string
}

func (c Config) withDefaults() Config {
	if c.ProgressDelay <= 0 {
		c.ProgressDelay = 2 * time.Second
	}
	if c.EditInterval <= 0 {
		c.EditInterval = 2 * time.Second
	}
	if c.TimerInterval <= 0 {
		c.TimerInterval = 10 * time.Second
	}
	if c.AnswerGrace <= 0 {
		c.AnswerGrace = 5 * time.Second
	}
	if c.StoppedGrace <= 0 {
		c.StoppedGrace = 2 * time.Second
	}
	if c.TypingInterval <= 0 {
		c.TypingInterval = 5 * time.Second
	}
	if c.MaxRows <= 0 {
		c.MaxRows = 8
	}
	if c.MaxSummaryRows <= 0 {
		c.MaxSummaryRows = 30
	}
	if c.MaxRowRunes <= 0 {
		c.MaxRowRunes = 120
	}
	if c.Redact == nil {
		c.Redact = func(s string) string { return s }
	}
	return c
}

// Action is an instruction for the caller. The concrete types are Post,
// ShowProgress, EditProgress, RepostProgress, FinishProgress, Typing,
// MacNotice, MacBack, and HeldAnswer.
type Action interface{ isAction() }

// Post asks the caller to send a new message. Key is the key of the event that
// caused it, so the outbox can drop duplicates. Kind is answer, block, tool,
// output, prompt, or reasoning. HTML overrides Markdown when set.
type Post struct {
	Key      string
	Kind     string
	Markdown string
	HTML     string
	Silent   bool
}

// ShowProgress asks for a new, silent progress message.
type ShowProgress struct{ HTML string }

// EditProgress asks to edit the progress message in place.
type EditProgress struct{ HTML string }

// RepostProgress asks to send a new progress message and delete the old one.
type RepostProgress struct{ HTML string }

// FinishProgress asks to edit the progress message into the summary. It can
// come twice for one turn when a late answer corrects the summary.
type FinishProgress struct{ HTML string }

// Typing asks for a "typing" chat action.
type Typing struct{}

// MacNotice asks to show that the session is in use on the Mac. The caller
// posts one notice, silently, and edits it with each later MacNotice until a
// MacBack.
type MacNotice struct{ HTML string }

// MacBack says that a message from Telegram ended the hold. The caller stops
// editing the notice; the next MacNotice posts a new one.
type MacBack struct{}

// HeldAnswer is the answer of a held turn. It is not posted; the caller keeps
// the newest one, so that the notice can offer it.
type HeldAnswer struct {
	Key      string
	Markdown string
}

func (Post) isAction()           {}
func (ShowProgress) isAction()   {}
func (EditProgress) isAction()   {}
func (RepostProgress) isAction() {}
func (FinishProgress) isAction() {}
func (Typing) isAction()         {}
func (MacNotice) isAction()      {}
func (MacBack) isAction()        {}
func (HeldAnswer) isAction()     {}

type phase string

const (
	phNone   phase = ""
	phActive phase = "active"
	phEnding phase = "ending" // idle/done/error seen, waiting out AnswerGrace
	phEnded  phase = "ended"  // finished; kept so a late answer can correct the summary
)

const (
	callRunning = 0
	callOK      = 1
	callFailed  = 2
)

type call struct {
	id, name string
	kind     agents.ToolKind
	label    string
	status   int
}

// Tracker follows one topic's turns. It is not safe for concurrent use.
type Tracker struct {
	cfg Config
	set Settings

	ph      phase
	start   time.Time
	endedAt time.Time
	graceAt time.Time
	endKind string // idle, done, error (State), final, error (answer)

	calls []call

	commentary        string // latest commentary text
	commentaryKey     string
	commentaryPending bool // commentary came after the last tool call and is not posted
	commentarySeen    bool
	answered          bool
	errText           string

	blocked bool

	shown         bool
	lastEdit      time.Time
	dirty         bool
	pendingRepost bool
	finishHeld    bool   // the finish waits for EditInterval
	lastHTML      string // last progress or summary HTML sent

	lastTyping time.Time

	// The hold of turns that start on the Mac (Settings.MacNotice). onMac,
	// macPrompt, and macNote last across turns; quiet and fromTG belong to
	// one turn.
	onMac     bool   // the last prompt that a person typed came from the Mac
	macPrompt string // that prompt, redacted and cut
	macNote   string // the last notice HTML asked for
	quiet     bool   // this turn is held: nothing of it is posted
	fromTG    bool   // a message from Telegram is part of this turn
}

// New returns a Tracker with no turn in progress.
func New(cfg Config, s Settings) *Tracker {
	return &Tracker{cfg: cfg.withDefaults(), set: s}
}

// OnMac reports whether the session is in use on the Mac: a hold of Mac
// turns runs (Settings.MacNotice).
func (t *Tracker) OnMac() bool { return t.onMac && t.set.MacNotice }

// Streaming tells whether a draft of the agent's text belongs to the topic
// now (design section 8.1a): a turn runs, it is shown on Telegram (not held
// for the Mac), its answer is not out yet, and the mode is not final (answers
// only). text is the agent's latest commentary, redacted; it is "" in block
// mode, where each block is posted as a message anyway.
func (t *Tracker) Streaming() (text string, ok bool) {
	if (t.ph != phActive && t.ph != phEnding) || t.quiet || t.answered || t.set.Mode == "final" {
		return "", false
	}
	if t.set.Mode == "block" {
		return "", true
	}
	return t.cfg.Redact(t.commentary), true
}

// MacNoticeHTML is the last notice of the Mac hold that the tracker asked
// for, or "" (it is part of the Snapshot, so it survives a restart).
func (t *Tracker) MacNoticeHTML() string { return t.macNote }

// SetSettings changes the display settings. They apply from the next event.
func (t *Tracker) SetSettings(s Settings) {
	t.set = s
	t.dropHoldIfOff()
}

// dropHoldIfOff ends a Mac hold when MacNotice is off, so a turn held before
// the setting changed (a restart with a new config) still shows its answer.
func (t *Tracker) dropHoldIfOff() {
	if !t.set.MacNotice {
		t.onMac, t.quiet, t.macNote = false, false, ""
	}
}

func (t *Tracker) progressMode() bool {
	return t.set.Mode == "" || t.set.Mode == "progress"
}

func (t *Tracker) startTurn(now time.Time) {
	*t = Tracker{cfg: t.cfg, set: t.set, ph: phActive, start: now,
		onMac: t.onMac, macPrompt: t.macPrompt, macNote: t.macNote,
		quiet: t.onMac && t.set.MacNotice}
}

// post appends a Post and, when the progress message is above it, schedules a
// repost. A held turn posts nothing.
func (t *Tracker) post(out []Action, p Post) []Action {
	if t.quiet {
		return out
	}
	if t.shown && t.ph == phActive {
		t.pendingRepost = true
	}
	return append(out, p)
}

// Event feeds one transcript event. echo is true for a prompt that came from
// Telegram.
func (t *Tracker) Event(ev agents.Event, echo bool, now time.Time) []Action {
	var out []Action
	r := t.cfg.Redact
	switch ev.Kind {
	case agents.UserPrompt:
		if t.ph == phEnding {
			out = t.finalize(out, now)
		}
		mac := !echo && !ev.Auto // typed on the Mac
		hold := mac && t.set.MacNotice
		if echo && t.onMac {
			t.onMac, t.macNote = false, ""
			out = append(out, MacBack{})
		}
		if hold {
			t.onMac, t.macPrompt = true, truncate(strings.TrimSpace(r(ev.Text)), maxMacPrompt)
		}
		if t.ph != phActive {
			out = t.flushHeld(out, now)
			t.startTurn(now)
		}
		if echo {
			t.fromTG, t.quiet = true, false // the rest of this turn is shown
		}
		if hold && !t.shown && !t.fromTG {
			t.quiet = true // a turn that Telegram already sees stays shown
		}
		t.unblock()
		switch {
		case hold:
			out = t.macNotice(out, macWorking)
		case mac && t.set.ShowMacPrompts:
			out = t.post(out, Post{Key: ev.Key, Kind: "prompt", Markdown: macPrompt(r(ev.Text))})
		}

	case agents.ToolCall:
		if ev.Tool == nil {
			return nil
		}
		out = t.beginIfNeeded(out, now)
		t.unblock()
		t.calls = append(t.calls, call{id: ev.Tool.CallID, name: ev.Tool.Name, kind: ev.Tool.Kind, label: ev.Tool.Label})
		t.commentaryPending = false
		t.markDirty()
		if t.set.Verbose == "on" || t.set.Verbose == "full" {
			out = t.post(out, Post{Key: ev.Key, Kind: "tool", Markdown: "🔧 " + toolText(r, ev.Tool.Name, ev.Tool.Label), Silent: true})
		}

	case agents.ToolResult:
		if ev.Tool == nil {
			return nil
		}
		t.unblock()
		name := ev.Tool.Name
		for i := len(t.calls) - 1; i >= 0; i-- {
			if t.calls[i].id != "" && t.calls[i].id == ev.Tool.CallID {
				if t.ph == phActive || t.ph == phEnding {
					t.calls[i].status = callOK
					if !ev.Tool.OK {
						t.calls[i].status = callFailed
					}
					t.markDirty()
				}
				if name == "" {
					name = t.calls[i].name
				}
				break
			}
		}
		if t.set.Verbose == "full" {
			if o := strings.TrimSpace(r(ev.Tool.Output)); o != "" {
				if name == "" {
					name = "Tool"
				}
				out = t.post(out, Post{Key: ev.Key, Kind: "output", Silent: true,
					HTML: "<b>" + escape(r(name)) + "</b> output:\n<blockquote expandable>" + escape(o) + "</blockquote>"})
			}
		}

	case agents.Thinking:
		out = t.beginIfNeeded(out, now)
		t.unblock()
		if t.set.Reasoning {
			if md := italic(r(ev.Text)); md != "" {
				out = t.post(out, Post{Key: ev.Key, Kind: "reasoning", Markdown: md, Silent: true})
			}
		}

	case agents.AssistantText:
		switch ev.Phase {
		case agents.Final, agents.Error:
			out = t.answer(out, ev, now)
		default:
			out = t.beginIfNeeded(out, now)
			t.unblock()
			t.commentary = ev.Text
			t.commentaryKey = ev.Key
			t.commentaryPending = true
			t.commentarySeen = true
			t.markDirty()
			if t.set.Mode == "block" {
				out = t.post(out, Post{Key: ev.Key, Kind: "block", Markdown: r(ev.Text)})
			}
		}

	case agents.TurnEnd:
		if t.ph == phActive {
			t.unblock()
			t.toEnding(string(StateDone), now)
		}
	}
	return out
}

// beginIfNeeded starts a new turn when none is running. During the grace
// period the event only adds to the ended turn.
func (t *Tracker) beginIfNeeded(out []Action, now time.Time) []Action {
	if t.ph == phNone || t.ph == phEnded {
		out = t.flushHeld(out, now)
		t.startTurn(now)
	}
	return out
}

// flushHeld emits a held FinishProgress, so the old progress message is closed
// before a new turn starts.
func (t *Tracker) flushHeld(out []Action, now time.Time) []Action {
	if t.finishHeld {
		return t.emitFinish(out, now)
	}
	return out
}

func (t *Tracker) unblock() {
	if t.blocked {
		t.blocked = false
		t.markDirty()
	}
}

func (t *Tracker) markDirty() {
	if t.shown {
		t.dirty = true
	}
}

// answer handles a Final or Error-phase text.
func (t *Tracker) answer(out []Action, ev agents.Event, now time.Time) []Action {
	isErr := ev.Phase == agents.Error
	text := t.cfg.Redact(ev.Text)
	held := t.held()
	if strings.TrimSpace(text) != "" {
		if held {
			out = append(out, HeldAnswer{Key: ev.Key, Markdown: text})
		} else {
			out = append(out, Post{Key: ev.Key, Kind: "answer", Markdown: text})
		}
	}
	kind := "final"
	if isErr {
		kind = "error"
		t.errText = text
	}
	switch t.ph {
	case phNone:
		// A lone answer: no turn to finish.
		return out
	case phActive, phEnding:
		t.ph = phEnded
		t.endedAt = now
		t.endKind = kind
		t.answered = true
		t.blocked = false
		if t.shown {
			out = t.finish(out, now)
		}
	case phEnded:
		t.answered = true
		if kind == "final" && t.endKind == "error" && t.errText != "" {
			kind = "error" // keep the error summary
		}
		t.endKind = kind
		if t.shown {
			out = t.finishIfChanged(out, now)
		}
	}
	if t.quiet || t.OnMac() {
		out = t.macNotice(out, macEnd(t.endKind)) // also a shown turn that a Mac prompt joined
	}
	return out
}

// held reports whether an answer now belongs to a held turn: the turn is
// held, or no turn runs while the session is in use on the Mac.
func (t *Tracker) held() bool {
	return t.quiet || (t.ph == phNone && t.onMac && t.set.MacNotice)
}

// macNotice asks for the notice of the Mac hold in the given state, unless
// it would not change.
func (t *Tracker) macNotice(out []Action, state string) []Action {
	h := macNoticeHTML(t.macPrompt, state)
	if h == t.macNote {
		return out
	}
	t.macNote = h
	return append(out, MacNotice{HTML: h})
}

func (t *Tracker) toEnding(kind string, now time.Time) {
	t.ph = phEnding
	t.endKind = kind
	t.endedAt = now
	t.graceAt = now.Add(t.cfg.AnswerGrace)
	t.blocked = false
}

// finalize ends a turn that waited out its grace period.
func (t *Tracker) finalize(out []Action, now time.Time) []Action {
	if t.ph != phEnding {
		return out
	}
	if t.progressMode() && !t.answered && t.commentaryPending && strings.TrimSpace(t.commentary) != "" {
		md := t.cfg.Redact(t.commentary)
		if t.quiet {
			out = append(out, HeldAnswer{Key: t.commentaryKey, Markdown: md})
		} else {
			out = append(out, Post{Key: t.commentaryKey, Kind: "answer", Markdown: md})
		}
		t.answered = true
		t.commentaryPending = false
	}
	t.ph = phEnded
	if t.shown {
		out = t.finish(out, now)
	}
	if t.quiet || t.OnMac() {
		out = t.macNotice(out, macEnd(t.endKind)) // also a shown turn that a Mac prompt joined
	}
	return out
}

// finish emits the summary, or holds it until EditInterval has passed since
// the last progress edit.
func (t *Tracker) finish(out []Action, now time.Time) []Action {
	t.dirty, t.pendingRepost = false, false
	if now.Sub(t.lastEdit) < t.cfg.EditInterval {
		t.finishHeld = true
		return out
	}
	return t.emitFinish(out, now)
}

func (t *Tracker) emitFinish(out []Action, now time.Time) []Action {
	h := t.summaryHTML()
	t.lastHTML, t.lastEdit, t.finishHeld = h, now, false
	return append(out, FinishProgress{HTML: h})
}

func (t *Tracker) finishIfChanged(out []Action, now time.Time) []Action {
	if t.finishHeld {
		return out // the held finish is rendered when it is emitted
	}
	if h := t.summaryHTML(); h != t.lastHTML {
		return t.finish(out, now)
	}
	return out
}

// State feeds a pane state from the registry.
func (t *Tracker) State(st State, now time.Time) []Action {
	var out []Action
	switch st {
	case StateWorking:
		switch t.ph {
		case phEnding:
			t.ph = phActive
			t.endKind = ""
		case phActive:
		default:
			out = t.flushHeld(out, now)
			t.startTurn(now)
		}
		t.unblock()
	case StateBlocked:
		switch t.ph {
		case phEnding:
			t.ph = phActive
			t.endKind = ""
		case phActive:
		default:
			out = t.flushHeld(out, now)
			t.startTurn(now)
		}
		if !t.blocked {
			t.blocked = true
			t.markDirty()
		}
	case StateIdle, StateDone, StateError, StateStopped:
		if t.ph != phActive {
			return nil
		}
		t.unblock()
		t.toEnding(string(st), now)
		if st == StateStopped && t.cfg.StoppedGrace < t.cfg.AnswerGrace {
			t.graceAt = now.Add(t.cfg.StoppedGrace)
		}
	}
	return out
}

// Tick advances time-driven work: typing, the progress message, and the end of
// a grace period.
func (t *Tracker) Tick(now time.Time) []Action {
	var out []Action
	switch t.ph {
	case phEnding:
		if !now.Before(t.graceAt) {
			out = t.finalize(out, now)
		}
		return out
	case phEnded:
		if t.finishHeld && now.Sub(t.lastEdit) >= t.cfg.EditInterval {
			out = t.emitFinish(out, now)
		}
		return out
	case phActive:
	default:
		return nil
	}
	if t.quiet {
		return nil // a held turn shows no typing and no progress
	}

	if !t.blocked && (t.lastTyping.IsZero() || now.Sub(t.lastTyping) >= t.cfg.TypingInterval) {
		t.lastTyping = now
		out = append(out, Typing{})
	}
	if !t.progressMode() {
		return out
	}
	if !t.shown {
		if now.Sub(t.start) >= t.cfg.ProgressDelay {
			h := t.progressHTML(now)
			t.shown, t.lastHTML, t.lastEdit = true, h, now
			t.dirty, t.pendingRepost = false, false
			out = append(out, ShowProgress{HTML: h})
		}
		return out
	}
	if (t.dirty || t.pendingRepost) && now.Sub(t.lastEdit) >= t.cfg.EditInterval {
		h := t.progressHTML(now)
		switch {
		case t.pendingRepost:
			out = append(out, RepostProgress{HTML: h})
			t.lastEdit = now
		case h != t.lastHTML:
			out = append(out, EditProgress{HTML: h})
			t.lastEdit = now
		}
		t.lastHTML = h
		t.dirty, t.pendingRepost = false, false
		return out
	}
	// Nothing changed, but the elapsed time moves on: without this the
	// message would show "⏱ 2s" until the turn ends while the agent thinks.
	// A wait for the user's answer keeps its time; the agent is not working.
	if !t.blocked && now.Sub(t.lastEdit) >= t.timerStep(now.Sub(t.start)) {
		if h := t.progressHTML(now); h != t.lastHTML {
			out = append(out, EditProgress{HTML: h})
			t.lastHTML = h
		}
		t.lastEdit = now
	}
	return out
}

// timerStep is the wait between two edits that only move the timer on. It
// grows with the turn (a sixth of the elapsed time), from TimerInterval up to
// one minute, so a long turn spends little of the chat's rate budget.
func (t *Tracker) timerStep(elapsed time.Duration) time.Duration {
	step := min(elapsed/6, time.Minute)
	return max(step, t.cfg.TimerInterval, t.cfg.EditInterval)
}

// ProgressGone tells the tracker that the progress message was deleted outside
// rex-tg. The next Tick of a running turn shows a new one; a finished turn is
// not finished again.
func (t *Tracker) ProgressGone() {
	t.shown = false
	t.dirty, t.pendingRepost, t.finishHeld = false, false, false
}

// SnapshotCall is one tool call in a Snapshot.
type SnapshotCall struct {
	ID     string          `json:"id,omitempty"`
	Name   string          `json:"name"`
	Kind   agents.ToolKind `json:"kind,omitempty"`
	Label  string          `json:"label,omitempty"`
	Status int             `json:"status"` // 0 running, 1 ok, 2 failed
}

// Snapshot is everything needed to continue a turn after a restart. It is
// JSON-serializable.
type Snapshot struct {
	Phase             string         `json:"phase"` // "", active, ending, ended
	Start             time.Time      `json:"start"`
	EndedAt           time.Time      `json:"ended_at"`
	GraceAt           time.Time      `json:"grace_at"`
	EndKind           string         `json:"end_kind,omitempty"`
	Calls             []SnapshotCall `json:"calls,omitempty"`
	Commentary        string         `json:"commentary,omitempty"`
	CommentaryKey     string         `json:"commentary_key,omitempty"`
	CommentaryPending bool           `json:"commentary_pending,omitempty"`
	CommentarySeen    bool           `json:"commentary_seen,omitempty"`
	Answered          bool           `json:"answered,omitempty"`
	ErrText           string         `json:"err_text,omitempty"`
	Blocked           bool           `json:"blocked,omitempty"`
	Shown             bool           `json:"shown,omitempty"`
	LastEdit          time.Time      `json:"last_edit"`
	Dirty             bool           `json:"dirty,omitempty"`
	PendingRepost     bool           `json:"pending_repost,omitempty"`
	FinishHeld        bool           `json:"finish_held,omitempty"`
	LastHTML          string         `json:"last_html,omitempty"`
	LastTyping        time.Time      `json:"last_typing"`
	OnMac             bool           `json:"on_mac,omitempty"`
	MacPrompt         string         `json:"mac_prompt,omitempty"`
	MacNote           string         `json:"mac_note,omitempty"`
	Quiet             bool           `json:"quiet,omitempty"`
	FromTG            bool           `json:"from_tg,omitempty"`
}

// Active reports whether the snapshot holds a turn that has not ended.
func (s Snapshot) Active() bool { return s.Phase == string(phActive) || s.Phase == string(phEnding) }

// Snapshot captures the tracker's state.
func (t *Tracker) Snapshot() Snapshot {
	s := Snapshot{
		Phase: string(t.ph), Start: t.start, EndedAt: t.endedAt, GraceAt: t.graceAt, EndKind: t.endKind,
		Commentary: t.commentary, CommentaryKey: t.commentaryKey,
		CommentaryPending: t.commentaryPending, CommentarySeen: t.commentarySeen,
		Answered: t.answered, ErrText: t.errText, Blocked: t.blocked,
		Shown: t.shown, LastEdit: t.lastEdit, Dirty: t.dirty, PendingRepost: t.pendingRepost, FinishHeld: t.finishHeld,
		LastHTML: t.lastHTML, LastTyping: t.lastTyping,
		OnMac: t.onMac, MacPrompt: t.macPrompt, MacNote: t.macNote, Quiet: t.quiet, FromTG: t.fromTG,
	}
	for _, c := range t.calls {
		s.Calls = append(s.Calls, SnapshotCall{ID: c.id, Name: c.name, Kind: c.kind, Label: c.label, Status: c.status})
	}
	return s
}

// Restore rebuilds a Tracker from a Snapshot.
func Restore(cfg Config, s Settings, snap Snapshot) *Tracker {
	t := New(cfg, s)
	t.ph = phase(snap.Phase)
	t.start, t.endedAt, t.graceAt, t.endKind = snap.Start, snap.EndedAt, snap.GraceAt, snap.EndKind
	t.commentary, t.commentaryKey = snap.Commentary, snap.CommentaryKey
	t.commentaryPending, t.commentarySeen = snap.CommentaryPending, snap.CommentarySeen
	t.answered, t.errText, t.blocked = snap.Answered, snap.ErrText, snap.Blocked
	t.shown, t.lastEdit, t.dirty, t.pendingRepost, t.finishHeld = snap.Shown, snap.LastEdit, snap.Dirty, snap.PendingRepost, snap.FinishHeld
	t.lastHTML, t.lastTyping = snap.LastHTML, snap.LastTyping
	t.onMac, t.macPrompt, t.macNote, t.quiet, t.fromTG = snap.OnMac, snap.MacPrompt, snap.MacNote, snap.Quiet, snap.FromTG
	t.dropHoldIfOff()
	for _, c := range snap.Calls {
		t.calls = append(t.calls, call{id: c.ID, name: c.Name, kind: c.Kind, label: c.Label, status: c.Status})
	}
	return t
}

// macPrompt shows a prompt typed on the Mac. A prompt of several lines starts
// on its own line, so a code fence at its start still opens a code block.
func macPrompt(text string) string {
	if strings.Contains(text, "\n") {
		return "💻 You (on Mac):\n\n" + text
	}
	return "💻 You (on Mac): " + text
}
