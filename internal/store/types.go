package store

import (
	"time"

	"rex-tg/internal/agents"
)

// Thread statuses.
const (
	ThreadLive    = "live"
	ThreadOffline = "offline"
	ThreadClosed  = "closed"
)

// Thread modes and verbosity levels.
const (
	ModeProgress = "progress"
	ModeBlock    = "block"
	ModeFinal    = "final"

	VerboseOff  = "off"
	VerboseOn   = "on"
	VerboseFull = "full"
)

// Pane occupant sources.
const (
	SourceHook  = "hook"
	SourceScan  = "scan"
	SourceGuess = "guess"
)

// Outbox and outbox-part states. Parts use all but OutboxSkipped.
const (
	OutboxPending = "pending"
	OutboxSending = "sending"
	OutboxSent    = "sent"
	OutboxUnknown = "unknown"
	OutboxFailed  = "failed"
	OutboxSkipped = "skipped"
)

// Part formats.
const (
	FormatHTML     = "html"
	FormatPlain    = "plain"
	FormatDocument = "document"
	// FormatRich is Markdown sent with sendRichMessage (Phase 6, opt-in).
	// Schema version 2 adds it to the CHECK constraint.
	FormatRich = "rich"
)

// Progress message states.
const (
	ProgressWaiting = "waiting"
	ProgressSending = "sending"
	ProgressShown   = "shown"
	ProgressUnknown = "unknown"
)

// Alert kinds and states.
const (
	AlertPermission = "permission"
	AlertQuestion   = "question"

	AlertOpen     = "open"
	AlertAnswered = "answered"
	AlertGone     = "gone"
)

// Inbound states.
const (
	InboundQueued    = "queued"
	InboundTyping    = "typing"
	InboundDelivered = "delivered"
	InboundAcked     = "acked"
	InboundRefused   = "refused"
	InboundExpired   = "expired"
	InboundUnknown   = "unknown"
	InboundDone      = "done"
)

// Conversation is one agent session. ThreadID is 0 when it has no topic.
type Conversation struct {
	ID          int64
	Runtime     agents.Runtime
	NativeID    string
	Transcript  string
	CWD         string
	Title       string
	ThreadID    int64
	FirstSeenAt time.Time
}

// Pane is one Rex pane. ConversationID and LastConversationID are 0 when unset.
type Pane struct {
	ID                 int64
	Host               string
	Block              string
	Session            string
	Label              string
	ConversationID     int64
	LastConversationID int64
	PID                int
	ProcessName        string
	Source             string
	State              string
	StateKind          string
	StateAt            time.Time
	BusyUntil          time.Time
	SeenAt             time.Time
	GoneAt             time.Time
}

// Thread is a Telegram topic. ClosedAt is set while Status is closed.
type Thread struct {
	ID                    int64
	ChatID                int64
	MessageThreadID       int64
	CurrentConversationID int64
	Name                  string
	Mode                  string
	Verbose               string
	Reasoning             bool
	Muted                 bool
	Status                string
	CreatedAt             time.Time
	ClosedAt              time.Time
}

// Cursor is the read position in a transcript.
type Cursor struct {
	Transcript  string
	DevIno      string
	Offset      int64
	LastEventAt time.Time
	UpdatedAt   time.Time
}

// PartContent is the text of one part of an outgoing message.
type PartContent struct {
	Content  string
	Format   string // html, plain, or document
	Filename string // documents only
}

// OutboxItem is an outgoing message to enqueue. Skip stores it as skipped,
// without parts (for echoes).
type OutboxItem struct {
	ThreadID int64
	Key      string
	Kind     string
	Parts    []PartContent
	Silent   bool
	Skip     bool
}

// Part is one sendable part, with the place to send it to.
type Part struct {
	OutboxID        int64
	Part            int
	ThreadID        int64
	ChatID          int64
	MessageThreadID int64
	Kind            string
	Key             string // the outbox item's event key
	Content         string
	Format          string
	Filename        string
	Silent          bool
	State           string
	TGMessageID     int64
	// Floor is the newest message number of the chat that rex-tg knew when
	// the part was last sent; HasFloor is false when that is not known.
	Floor    int64
	HasFloor bool
	Checked  bool // an unknown part whose arrival was checked, so it waits for /resend
}

// OutboxRow is an outgoing message with its parts.
type OutboxRow struct {
	ID        int64
	ThreadID  int64
	Key       string
	Kind      string
	Body      string
	State     string
	Silent    bool
	Attempts  int
	LastError string
	CreatedAt time.Time
	SentAt    time.Time
	Parts     []Part
}

// Progress is the live progress message of a topic. Rows is a JSON array.
type Progress struct {
	ThreadID    int64
	TurnKey     string
	State       string
	TGMessageID int64
	Headline    string
	Rows        string
	StartedAt   time.Time
	EditedAt    time.Time
}

// AlertOption is one answer button of an alert.
type AlertOption struct {
	Label string   `json:"label"`
	Keys  []string `json:"keys"`
}

// Alert is a permission prompt or question shown with buttons.
type Alert struct {
	ID          int64
	ThreadID    int64
	Kind        string
	ScreenHash  string
	Options     []AlertOption
	TGMessageID int64
	State       string
	CreatedAt   time.Time
}

// Inbound is an incoming Telegram update.
type Inbound struct {
	UpdateID        int64
	ChatID          int64
	MessageThreadID int64
	TGMessageID     int64
	FromID          int64
	Kind            string
	Body            string
	BodyHash        string
	ThreadID        int64
	State           string // empty on insert means queued
	Reason          string
	CreatedAt       time.Time
	DeliveredAt     time.Time
	AckedEventKey   string
}
