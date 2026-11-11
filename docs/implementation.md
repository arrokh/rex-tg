# rex-tg implementation guide

This guide is the contract between the packages of rex-tg. [design.md](design.md) says what the system does; this file says how the code is split and what each package exports. Keep the two in sync.

## Toolchain and rules

- Go 1.26.1, pinned in `.tool-versions` (mise). Module path: `rex-tg`. Run Go commands from the repository root. If `go version` is not 1.26.x, run `eval "$(mise env -s bash)"` first.
- `make check` runs gofmt, `go vet`, staticcheck v0.8.2, and `go test -race ./...`.
- **Do not change `go.mod` or `go.sum`.** Do not run `go get` or `go mod tidy`. The allowed third-party modules are already pinned (see `internal/deps/deps.go`): `github.com/BurntSushi/toml`, `github.com/fsnotify/fsnotify`, `github.com/tidwall/gjson`, `github.com/tidwall/sjson`, `github.com/yuin/goldmark`, `gopkg.in/natefinch/lumberjack.v2`, `modernc.org/sqlite`. Everything else comes from the standard library.
- **Do not change `internal/agents/agents.go`.** It is the shared event contract. If a change is needed, report it instead.
- Each package has a package doc comment. Exported identifiers have doc comments.
- No panics on bad input. Errors carry context (`fmt.Errorf("…: %w", err)`). I/O takes a `context.Context`.
- No global mutable state. Time comes from parameters (`now time.Time`) or an injected clock, so tests are deterministic.
- Tests are table-driven where it fits, test behavior through the package's exported API (the seams in [plan.md](plan.md#tests)), and use synthetic data only. Never copy real transcripts, tokens, or user data into tests.
- Test data lives in the package's own `testdata/` folder.
- Never write to the user's real config or state: `~/.claude`, `~/.codex`, `~/.pi`, `~/Library/LaunchAgents`, `~/.local/state/rex-tg`. Tests use `t.TempDir()`.

## Package map

| Package | Purpose | Depends on |
| --- | --- | --- |
| `internal/agents` | Shared event types and the `Adapter` interface (fixed) | — |
| `internal/agents/pi`, `claude`, `codex` | Transcript parsers, one per runtime | `agents` |
| `internal/format` | Markdown to Telegram HTML, splitting into parts, UTF-16 length, secret removal | goldmark |
| `internal/telegram` | Small Bot API client and error classes | stdlib |
| `internal/telegram/tgtest` | Fake Bot API server for tests | `telegram` |
| `internal/rex` | Wrapper around the `rex` CLI, including the event stream | stdlib |
| `internal/rex/rextest` | Fake `rex` CLI for tests | `rex` |
| `internal/store` | SQLite schema, migrations, and all queries | sqlite, `agents` |
| `internal/tail` | Offset-based JSONL follower | fsnotify |
| `internal/turn` | Turn tracker and progress message renderer (pure) | `agents` |
| `internal/install` | Hook, extension, and launchd installers | gjson, sjson, `extensions` |
| `extensions` | Embedded pi extension source (`extensions/pi/rex-tg.ts`) | — |
| `internal/config` | TOML config with defaults | toml |
| `internal/registry` | Panes, sessions, state merge, take-over and resume rules (pure) | `agents` |
| `internal/inbound` | Safety checks, waiting queue policy, echo matching | `agents` |
| `internal/daemon` | Wiring: core loop, sender, router, alerts, housekeeping | everything |
| `cmd/rex-tg` | CLI: `daemon`, `hook`, `login`, `install`, `uninstall`, `status`, `doctor` | everything |

## internal/agents/{pi,claude,codex}

Each package exports `func New() agents.Adapter`. Formats are in [agent-transcripts.md](agent-transcripts.md).

- Keys: `agents.Key(runtime, sessionID, lineID)` with the line's own ID (pi entry `id`, Claude `uuid`, Codex `payload.id`), or `agents.FallbackKey` when the line has none. A line that yields several events gives each a distinct key by appending `#<n>` (for example one Claude line never has two blocks, but a pi assistant message can hold text plus several tool calls).
- Event order within a line follows the content blocks.
- `Tool.Label` rules (design D14): Claude Bash uses `input.description` when set, else `input.command`; file tools use the path (relative to the session's cwd when it is inside it); search tools use the pattern; web tools use the URL or query; subagents use the description. Labels are single-line and at most `agents.MaxLabel` runes. Labels are not redacted here.
- `Tool.Kind` maps tool names to kinds (read, edit, shell, search, web, subagent, plan, other).
- Final vs commentary: pi `stopReason` `stop` = Final, `toolUse` = Commentary, `error` = Error; Claude `message.stop_reason` `end_turn` = Final, anything else = Commentary, and lines with `isApiErrorMessage` = Error; Codex `payload.phase` `final_answer` = Final, `commentary` = Commentary.
- Prompts: pi `role=user` text; Claude `type=user` with string content or text blocks, skipping `isMeta`, tool results, and command wrappers (`<command-name>`, `<local-command-stdout>`, `<command-message>`, `<system-reminder>`-only content); Codex `event_msg` `item_completed` with `item.type=UserMessage`.
- Codex reads replies only from `response_item` (`message`, `role=assistant`) and emits `TurnEnd` for `event_msg` `task_complete`; it ignores `item_completed` `AgentMessage`.
- `Input()` defaults (spike S2 may change them): pi `{PasteEnterDelay: 300ms, SubmitKey: "enter", InterruptKeys: ["escape"], WhileWorking: "steer"}` (confirmed); Claude and Codex `{300ms, "enter", ["escape"], "unknown"}` until their spikes report.

## internal/format

```go
func Escape(s string) string                 // escapes &, <, > for Telegram HTML
func RenderHTML(markdown string) string      // Telegram HTML subset: b, i, s, u, code, pre, a, blockquote
func Plain(markdown string) string           // readable plain text, for the parse-error fallback
func UTF16Len(s string) int
func VisibleLen(html string) int             // UTF-16 length after Telegram parses the HTML
func Redact(s string) string                 // replaces secrets with "[redacted]"
func Truncate(s string, maxRunes int) string // one line, ends with "…" when cut
type Part struct{ HTML, Plain string }
func Parts(markdown string, limit int) []Part
```

- `RenderHTML`: headings become bold lines; lists become `• ` or `1. ` lines; fenced code becomes `<pre><code class="language-x">`; tables become a `<pre>` block; images become their alt text and URL; raw HTML in the Markdown is escaped, never passed through; links become `<a href>` only for http, https, and mailto URLs.
- `Parts`: splits at blank lines first, then at line ends, then hard; never inside a fenced code block unless the block alone is too long, in which case the block is split and each piece is fenced again; every part's `VisibleLen` is at most `limit`, including the ` (i/n)` suffix that is added when there is more than one part.
- `Redact` patterns: `sk-` and `sk-ant-` keys, `ghp_`/`gho_`/`ghu_`/`ghs_`/`ghr_`/`github_pat_` tokens, `AKIA…` AWS keys, `xox[abprs]-` Slack tokens, `-----BEGIN … PRIVATE KEY-----` blocks, JWTs (`eyJ….eyJ….…`), Telegram bot tokens (`\d{8,10}:[A-Za-z0-9_-]{35}`), and `Bearer <token>` headers.

## internal/telegram and internal/telegram/tgtest

```go
func New(token string, opts ...Option) *Client // WithBaseURL(string), WithHTTPClient(*http.Client)
func (c *Client) GetMe(ctx) (User, error)
func (c *Client) GetUpdates(ctx, offset int64, timeoutSec int, allowed []string) ([]Update, error)
func (c *Client) SendMessage(ctx, SendMessageParams) (Message, error)
func (c *Client) EditMessageText(ctx, EditMessageTextParams) (Message, error)
func (c *Client) EditMessageReplyMarkup(ctx, chatID, messageID int64, markup *InlineKeyboardMarkup) error
func (c *Client) DeleteMessage(ctx, chatID, messageID int64) error
func (c *Client) SetMessageReaction(ctx, chatID, messageID int64, emoji string) error // "" clears
func (c *Client) SendChatAction(ctx, chatID, threadID int64, action string) error
func (c *Client) CreateForumTopic(ctx, chatID int64, name string) (ForumTopic, error)
func (c *Client) EditForumTopic(ctx, chatID, threadID int64, name string) error
func (c *Client) CloseForumTopic(ctx, chatID, threadID int64) error
func (c *Client) AnswerCallbackQuery(ctx, id, text string, showAlert bool) error
func (c *Client) SetMyCommands(ctx, cmds []BotCommand) error
func (c *Client) SendDocument(ctx, chatID, threadID int64, filename string, data []byte, caption string, silent bool) (Message, error)
func (c *Client) GetFile(ctx, fileID string) (File, error)
func (c *Client) DownloadFile(ctx, filePath string, w io.Writer, maxBytes int64) error
func Classify(err error) Class
```

- `SendMessageParams`: `ChatID`, `MessageThreadID`, `Text`, `ParseMode` (`"HTML"` or `""`), `DisableNotification`, `ReplyMarkup *InlineKeyboardMarkup`, `ReplyToMessageID`; link previews are always off (`link_preview_options.is_disabled`).
- Errors: an `*APIError{Method, Code, Description, RetryAfter}` when Telegram answers `ok: false`. Network errors, timeouts, and 5xx answers come back as `*UnknownOutcomeError`, because the request may have taken effect.
- `Class` values: `ClassNone`, `ClassRateLimited` (429), `ClassParseEntities`, `ClassNotModified`, `ClassEditTargetGone`, `ClassDeleteTargetGone`, `ClassThreadNotFound`, `ClassBlocked` (403), `ClassConflict` (409), `ClassBadRequest` (other 4xx), `ClassUnknownOutcome`.
- Default timeouts: 15 s per call; `GetUpdates` uses its long-poll timeout plus 10 s. The token never appears in errors or logs.
- `tgtest.New(t)` starts an `httptest` server that implements the methods above, keeps chats, topics, and messages in memory, records every call with its time, can fail or delay the next calls of a method, and lets a test push updates that a pending `getUpdates` returns at once.

## internal/rex and internal/rex/rextest

```go
func New(bin string) *Client // every call adds --autostart=false; default timeout 10 s
func (c *Client) Whoami(ctx) (Whoami, error)
func (c *Client) Sessions(ctx) ([]Session, error)                  // `rex ls --json` returns {"sessions":[...]}
func (c *Client) Blocks(ctx, session string) ([]Block, error)
func (c *Client) Process(ctx, session, block string) (Process, error)
func (c *Client) ProgramStatus(ctx, session, block string) ([]StatusRecord, error)
func (c *Client) Capture(ctx, session, block string) (string, error) // --trim
func (c *Client) Paste(ctx, session, block, text string) error       // block.event paste, JSON on stdin
func (c *Client) SendKeys(ctx, session, block string, keys ...string) error
func (c *Client) Events(ctx) (<-chan Event, <-chan error)           // `rex events --all --json`
```

- `Process`: `Foreground ProcInfo`, `Ancestors []ProcInfo`, `Child *ProcInfo`, `LastExit *int`; `ProcInfo{PID int, Name, Path, InvokedPath, Argv0, CWD string}`.
- `StatusRecord{ID, State, Kind, App, EffectiveApp, Title, Msg string; UpdatedAt time.Time; Owner *ProcOwner}`.
- `Event{Type EventType; SessionID, BlockID string; Status *StatusRecord; Received time.Time; Raw json.RawMessage}` with types `SessionCreated`, `SessionRemoved`, `BlockCreated`, `BlockClosed`, `ProgramStatusChanged`, `ProcessChanged`, `PwdChanged`, `TitleChanged`, `OtherEvent`. `progress_report` and client join/leave events are dropped.
- `rextest.New(t)` builds or provides a fake `rex` binary that answers commands from a scenario, logs every call (arguments and stdin), and can stream scripted events.

## internal/store

SQLite at a path given by the caller. Opens with WAL, `foreign_keys=ON`, `busy_timeout=5000`, and one open connection. Applies migrations from an embedded `schema.sql` and records the version in `kv`. Before migrating an existing database, copies it to `<path>.bak-<version>`.

The schema is design section 10, with these additions:

- `outbox.silent INTEGER NOT NULL DEFAULT 0`.
- `outbox_part.content TEXT NOT NULL` (the text to send), `outbox_part.format TEXT NOT NULL` (`html`, `plain`, or `document`), `outbox_part.filename TEXT` (documents only).
- `thread.muted INTEGER NOT NULL DEFAULT 0`.
- `pane.state_at INTEGER`.

API (every method takes `ctx` first; times are `time.Time`, stored as Unix milliseconds):

```go
func Open(path string) (*Store, error)
func OpenReadOnly(path string) (*Store, error)
func (s *Store) Close() error

// Sessions
UpsertConversation(c Conversation) (Conversation, error)        // by (runtime, native_id)
Conversation(id int64) (Conversation, error)
ConversationByNative(rt agents.Runtime, nativeID string) (Conversation, bool, error)
SetConversationTitle(id int64, title string) error

// Panes
UpsertPane(host, block, session, label string, now time.Time) (Pane, error)
SetPaneOccupant(paneID, convID int64, pid int, processName, source string) error // convID 0 = none; keeps last_conversation_id; a session runs in one pane only
SetPaneState(paneID int64, state, kind string, at time.Time) error
SetPaneBusyUntil(paneID int64, until time.Time) error
MarkPaneGone(paneID int64, at time.Time) error
Pane(id int64) (Pane, error)
PaneByBlock(host, block string) (Pane, bool, error)
PaneByConversation(convID int64) (Pane, bool, error)
Panes() ([]Pane, error)                                          // panes that are not gone

// Topics
LinkTopic(chatID, messageThreadID, convID int64, name string, now time.Time) (Thread, error)
TakeOver(threadID, convID int64) error                            // also used for resume
Thread(id int64) (Thread, error)
ThreadByTopic(chatID, messageThreadID int64) (Thread, bool, error)
ThreadByConversation(convID int64) (Thread, bool, error)         // the session's topic, if not closed
Threads(statuses ...string) ([]Thread, error)
SetThreadStatus(id int64, status string) error
SetThreadName(id int64, name string) error
SetThreadSettings(id int64, mode, verbose string, reasoning, muted bool) error

// Reading transcripts
Cursor(transcript string) (Cursor, bool, error)
SaveCursor(c Cursor) error
CommitBatch(c Cursor, items []OutboxItem) (inserted int, err error) // one transaction: insert-or-ignore items, save cursor

// Outbox
Enqueue(item OutboxItem) (id int64, inserted bool, err error)
NextParts() ([]Part, error)       // at most one per topic: the oldest pending part, if the topic has no part in flight
ClaimPart(outboxID int64, part int) (bool, error)
MarkPartSent(outboxID int64, part int, tgMessageID int64) error
ReleasePart(outboxID int64, part int) error           // back to pending (429 or a clear error)
MarkPartUnknown(outboxID int64, part int) error
MarkPartFailed(outboxID int64, part int, reason string) error
ResendUnknown(outboxID int64) (int, error)
RecoverSending() ([]Part, error)                      // at start: sending -> unknown
OutboxRow(id int64) (OutboxRow, error)

// Progress messages
Progress(threadID int64) (Progress, bool, error)
SaveProgress(p Progress) error
DeleteProgress(threadID int64) error
AllProgress() ([]Progress, error)

// Alerts
CreateAlert(a Alert) (int64, error)
SetAlertMessage(id, tgMessageID int64) error
SetAlertState(id int64, state string) error
Alert(id int64) (Alert, error)
OpenAlerts(threadID int64) ([]Alert, error)

// Incoming updates
InsertInbound(in Inbound) (bool, error)               // false when update_id exists
SetInboundState(updateID int64, state, reason string, at time.Time) error
QueuedInbound(threadID int64) ([]Inbound, error)     // state queued, oldest first
RecoverTyping(at time.Time) ([]Inbound, error)       // at start: typing -> unknown
DeliveredSince(threadID int64, since time.Time) ([]Inbound, error) // delivered, not yet acked
AckInbound(updateID int64, eventKey string) error

// Small values
Get(key string) (string, bool, error)
Set(key, value string) error
Delete(key string) error

// Housekeeping
Prune(now time.Time) error
CheckInvariants() error
```

- `OutboxItem{ThreadID int64; Key, Kind string; Parts []PartContent; Silent, Skip bool}` with `PartContent{Content, Format, Filename string}`. `Skip` inserts the row as `skipped` (for echoes).
- `Part{OutboxID int64; Part int; ThreadID, ChatID, MessageThreadID int64; Kind string; Content, Format, Filename string; Silent bool; State string; TGMessageID int64}`.
- Row states derive from parts: `sent` when all parts are sent, `unknown` when any is unknown, `failed` when any failed, else `pending`.
- `NextParts` order: within a topic, rows by `id`, parts by index. Rows that are `unknown` or `failed` do not block later rows.

## internal/tail

```go
type Position struct{ Offset int64; DevIno string }
type Line struct{ Data []byte; Offset int64 }
type Batch struct{ Lines []Line; End Position; Reset bool; Skipped int }
func Follow(ctx context.Context, path string, start Position, opts Options) *Follower
func (f *Follower) Batches() <-chan Batch
func (f *Follower) Err() error
func (f *Follower) Close()
func Scan(path string, fn func(Line) error) (Position, error) // reads the whole file once (backfill, end position)
func DevIno(fi os.FileInfo) string
```

- `Options{Poll time.Duration (default 1 s); MaxLine int (default 8 MiB)}`.
- Waits for a missing file. Reads only complete lines and keeps a partial last line until its newline arrives. A replaced file (new `DevIno`) or a shorter file restarts at 0 and sets `Reset`. Lines over `MaxLine` are skipped and counted in `Skipped`. Uses fsnotify plus the poll.

## internal/turn

Pure. One `Tracker` per topic. Times come from the caller.

```go
type State string // "idle", "working", "done", "blocked", "error"
type Settings struct{ Mode, Verbose string; Reasoning, ShowMacPrompts bool } // Mode: progress|block|final; Verbose: off|on|full
type Config struct {
	ProgressDelay, EditInterval, AnswerGrace, TypingInterval time.Duration // 2s, 2s, 5s, 5s
	MaxRows, MaxSummaryRows, MaxRowRunes                     int           // 8, 30, 120
	Redact                                                   func(string) string
}
func New(cfg Config, s Settings) *Tracker
func (t *Tracker) SetSettings(s Settings)
func (t *Tracker) Event(ev agents.Event, echo bool, now time.Time) []Action // echo: a prompt that came from Telegram
func (t *Tracker) State(st State, now time.Time) []Action
func (t *Tracker) Tick(now time.Time) []Action
func (t *Tracker) ProgressGone()                                             // the progress message was deleted outside rex-tg
func (t *Tracker) Snapshot() Snapshot
func Restore(cfg Config, s Settings, snap Snapshot) *Tracker
```

Actions: `Post{Key, Kind, Markdown, HTML string; Silent bool}` (Kind: answer, block, tool, output, prompt, reasoning; HTML overrides Markdown when set), `ShowProgress{HTML}`, `EditProgress{HTML}`, `RepostProgress{HTML}`, `FinishProgress{HTML}`, `Typing{}`.

Behavior is design sections 4.4 and 8.1–8.5: the progress message appears after `ProgressDelay` without an answer, is edited at most every `EditInterval`, moves to the bottom when a `Post` lands below it, and ends as the summary line (D13). A final answer is posted at once. On `idle` or `done` without an answer, the tracker waits `AnswerGrace`; then it posts the last unposted commentary as the answer, if there is one, and finishes the progress message. All dynamic text in HTML is escaped and passed through `Redact`.

## internal/install and extensions

```go
type Paths struct{ Home, Binary, ClaudeSettings, CodexHooks, PiExtension, LaunchAgent, Label, LogDir string }
func DefaultPaths(home, binary string) Paths
func InstallClaude(p Paths) (Change, error); func UninstallClaude(p Paths) (Change, error)
func InstallCodex(p Paths) (Change, error);  func UninstallCodex(p Paths, force bool) (Change, error)
func InstallPi(p Paths) (Change, error);     func UninstallPi(p Paths) (Change, error)
func InstallLaunchd(p Paths, load bool) (Change, error); func UninstallLaunchd(p Paths, unload bool) (Change, error)
func Inspect(p Paths) (Report, error)
var ErrWouldShiftTrust error
type Change struct{ Path string; Changed bool; Backup, Note string }
```

- Hook command string: `<Binary> hook <runtime>`, which identifies rex-tg's entries exactly.
- Claude events: `SessionStart`, `UserPromptSubmit`, `Stop`, `PermissionRequest`, `Notification`, `SessionEnd`. Codex events: `SessionStart`, `UserPromptSubmit`, `Stop`, `PermissionRequest`, appended at the end of each list.
- Edits use gjson/sjson; atomic write through a temporary file in the same folder and rename; re-read before writing and retry when the file changed; backups `<file>.rex-tg-bak-<time>`, keeping the 5 newest. File mode is preserved.
- The pi extension source is `extensions/pi/rex-tg.ts`, embedded by package `extensions` (`extensions.PiExtension`), with the placeholder `__REX_TG_BIN__` replaced by the binary path.

## Hook spool format

`rex-tg hook <runtime>` writes one file per call to `~/.local/state/rex-tg/spool/`, named `<unix-nanos>-<pid>-<random>.json`, through a temporary file and rename, mode 0600:

```json
{"runtime":"claude","event":"SessionStart","at":"2026-11-11T03:12:54.486Z","rex_session":"session:…","rex_block":"block:…","payload":{ "…": "the hook's stdin JSON" }}
```

For pi, the extension sends this stdin payload: `{"hook_event_name":"session_start","reason":"startup","session_file":"…","previous_session_file":"…","target_session_file":"…","cwd":"…","session_id":"…"}`.
