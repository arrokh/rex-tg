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
	// FormatRich is a legacy Markdown part sent with sendRichMessage.
	// Schema version 2 adds it to the CHECK constraint.
	FormatRich = "rich"
	// FormatImage is an image file sent from the Mac. Content is the absolute
	// path; the sender reads the file when it sends. Schema version 4 adds it
	// to the CHECK constraint.
	FormatImage = "image"
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

// Inbound reasons.
const (
	// InboundReasonSteered records that delivery joined an already-running turn.
	InboundReasonSteered = "steered while working"
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
// without parts (for echoes). FullText is the complete redacted answer Markdown
// when available; Parts remain the sendable Telegram output.
type OutboxItem struct {
	ThreadID int64
	Key      string
	Kind     string
	Parts    []PartContent
	FullText string
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
	FullText  string
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

// Bookmark text formats.
const (
	BookmarkTextPlain    = "plain"
	BookmarkTextMarkdown = "markdown"
)

// Bookmark is a saved Telegram message. Its source identifiers and snapshot
// fields are independent of thread and conversation lifetimes.
type Bookmark struct {
	ID              int64
	ChatID          int64
	MessageID       int64
	MessageThreadID int64 // 0 is the main chat
	SourceTopicName string
	MessageDate     time.Time
	SavedAt         time.Time
	Title           string
	FullText        string
	TextFormat      string // BookmarkTextPlain or BookmarkTextMarkdown
	ContentKind     string
	ContentLabel    string
}

// BookmarkTopic is a source topic represented in a chat's saved bookmarks.
type BookmarkTopic struct {
	MessageThreadID int64 // 0 is the main chat
	Name            string
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
