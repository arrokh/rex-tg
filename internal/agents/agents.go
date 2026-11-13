// Package agents defines the normalized events that runtime adapters produce
// from coding-agent transcripts (session JSONL files), and the Adapter
// interface that each runtime (pi, Claude Code, Codex) implements.
//
// Adapters are pure: ParseLine and ReadHeader do no I/O beyond the reader they
// are given, so fixtures can test them. See docs/design.md section 5 and
// docs/agent-transcripts.md.
package agents

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Runtime names a supported coding agent.
type Runtime string

// The supported runtimes.
const (
	Pi     Runtime = "pi"
	Claude Runtime = "claude"
	Codex  Runtime = "codex"
)

// Runtimes lists every supported runtime, in a stable order.
var Runtimes = []Runtime{Pi, Claude, Codex}

// Valid reports whether r is a supported runtime.
func (r Runtime) Valid() bool {
	switch r {
	case Pi, Claude, Codex:
		return true
	}
	return false
}

// Kind is the kind of a normalized event.
type Kind int

// Event kinds. The fields of Event that each kind uses are listed per kind.
const (
	KindUnknown   Kind = iota
	AssistantText      // Text, Phase
	Thinking           // Text; shown only with /reasoning on
	UserPrompt         // Text: a prompt the user sent to the agent
	ToolCall           // Tool: CallID, Name, Kind, Label
	ToolResult         // Tool: CallID, OK, Took, Output
	Title              // Text: the session's new title
	Boundary           // Text: why, for example "compaction"
	TurnEnd            // the transcript itself marks the end of a turn
)

// String returns the kind's name, for logs and test failures.
func (k Kind) String() string {
	switch k {
	case AssistantText:
		return "AssistantText"
	case Thinking:
		return "Thinking"
	case UserPrompt:
		return "UserPrompt"
	case ToolCall:
		return "ToolCall"
	case ToolResult:
		return "ToolResult"
	case Title:
		return "Title"
	case Boundary:
		return "Boundary"
	case TurnEnd:
		return "TurnEnd"
	}
	return "KindUnknown"
}

// Phase tells whether assistant text is commentary, the final answer of a
// turn, or an error.
type Phase int

// Phases of assistant text.
const (
	PhaseUnknown Phase = iota
	Commentary
	Final
	Error
)

// String returns the phase's name.
func (p Phase) String() string {
	switch p {
	case Commentary:
		return "Commentary"
	case Final:
		return "Final"
	case Error:
		return "Error"
	}
	return "PhaseUnknown"
}

// ToolKind groups tools for the progress message's emoji (design 8.1).
type ToolKind string

// Tool kinds.
const (
	ToolRead     ToolKind = "read"
	ToolEdit     ToolKind = "edit"
	ToolShell    ToolKind = "shell"
	ToolSearch   ToolKind = "search"
	ToolWeb      ToolKind = "web"
	ToolSubagent ToolKind = "subagent"
	ToolPlan     ToolKind = "plan"
	ToolOther    ToolKind = "other"
)

// Tool describes one tool call or its result.
type Tool struct {
	CallID string        // links a ToolResult to its ToolCall
	Name   string        // the tool's name as the agent reports it
	Kind   ToolKind      // ToolCall only
	Label  string        // ToolCall only: short, single line, not yet redacted (design D14)
	OK     bool          // ToolResult only
	Took   time.Duration // ToolResult only; 0 when unknown
	Output string        // ToolResult only; at most MaxToolOutput bytes, for /verbose full
}

// MaxToolOutput caps Tool.Output.
const MaxToolOutput = 2000

// MaxLabel caps Tool.Label, in runes.
const MaxLabel = 120

// Event is one normalized event from a transcript line.
type Event struct {
	Kind  Kind
	Key   string    // stable deduplication key, for example "pi:<session id>:<entry id>"
	At    time.Time // the line's own timestamp; zero when the line has none
	Text  string
	Phase Phase
	Tool  *Tool
	// Auto marks a UserPrompt that no person typed: the agent's own program
	// wrote it, such as a background task's notification. It starts a turn
	// like a prompt, but it is never posted as typed on the Mac and never
	// matched with a message from Telegram.
	Auto bool
}

// Header is the identity of a session, read from the start of its transcript.
type Header struct {
	SessionID string
	CWD       string
	Title     string
	StartedAt time.Time
}

// LineContext is what ParseLine needs besides the line itself.
type LineContext struct {
	SessionID string // the session the line belongs to
	Offset    int64  // byte offset of the line's first byte, for fallback keys
	CWD       string // the session's working folder, for relative file labels; may be empty
}

// InputProfile describes how rex-tg types into the agent's terminal UI.
// Spike S2 fills in the values (docs/spikes).
type InputProfile struct {
	PasteEnterDelay time.Duration // wait between the paste event and Enter
	SubmitKey       string        // rex send-key name, normally "enter"
	InterruptKeys   []string      // keys that stop the current turn, for /interrupt
	WhileWorking    string        // what Enter does while the agent works: "steer", "queue", or "unknown"
}

// Adapter turns one runtime's transcript lines into events.
type Adapter interface {
	// Runtime returns the runtime this adapter handles.
	Runtime() Runtime
	// ReadHeader reads the session identity from the start of a transcript.
	ReadHeader(r io.Reader) (Header, error)
	// ParseLine turns one complete JSONL line (without the newline) into zero
	// or more events. It must not panic on any input. For a line type it does
	// not know (neither handled nor deliberately skipped), it returns one
	// event with Kind KindUnknown, Text set to the type name, and no Key, so
	// callers can count format changes after an agent update.
	ParseLine(line []byte, ctx LineContext) ([]Event, error)
	// TranscriptCandidates lists this runtime's transcripts for the working
	// folder cwd that changed at or after since, newest first. home is the
	// user's home folder.
	TranscriptCandidates(home, cwd string, since time.Time) ([]string, error)
	// IsSubagentTranscript reports transcripts of nested agents, which
	// rex-tg ignores.
	IsSubagentTranscript(path string) bool
	// Input describes how to type into this agent.
	Input() InputProfile
}

// FallbackKey is the deduplication key for a line that has no ID of its own.
func FallbackKey(r Runtime, sessionID string, offset int64) string {
	return fmt.Sprintf("%s:%s@%d", r, sessionID, offset)
}

// Key builds a deduplication key from a line's own ID.
func Key(r Runtime, sessionID, lineID string) string {
	return fmt.Sprintf("%s:%s:%s", r, sessionID, lineID)
}

// KnowsType reports whether an adapter handles or deliberately skips a line
// whose type field is typ: a counted unknown type that a newer adapter knows
// is no longer a format change to report.
func KnowsType(a Adapter, typ string) bool {
	b, err := json.Marshal(map[string]string{"type": typ})
	if err != nil {
		return false
	}
	evs, err := a.ParseLine(b, LineContext{})
	if err != nil {
		return false
	}
	for _, ev := range evs {
		if ev.Kind == KindUnknown && ev.Text == typ {
			return false
		}
	}
	return true
}
