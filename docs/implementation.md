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
- `Input()` values (confirmed by spike S2 for all three): `{PasteEnterDelay: 300ms, SubmitKey: "enter", InterruptKeys: ["escape"], WhileWorking: "steer"}`.

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
func (c *Client) OnMessage(fn func(chatID, messageID int64)) // gets the number of each sent message and of each message in an update; nil removes it
func Classify(err error) Class
```

- `SendMessageParams`: `ChatID`, `MessageThreadID`, `Text`, `ParseMode` (`"HTML"` or `""`), `DisableNotification`, `ReplyMarkup *InlineKeyboardMarkup`, `ReplyToMessageID`; link previews are always off (`link_preview_options.is_disabled`).
- Errors: an `*APIError{Method, Code, Description, RetryAfter}` when Telegram answers `ok: false`. Network errors, timeouts, and 5xx answers come back as `*UnknownOutcomeError`, because the request may have taken effect.
- `Class` values: `ClassNone`, `ClassRateLimited` (429), `ClassParseEntities`, `ClassNotModified`, `ClassEditTargetGone`, `ClassDeleteTargetGone`, `ClassThreadNotFound`, `ClassBlocked` (403), `ClassConflict` (409), `ClassBadRequest` (other 4xx), `ClassUnknownOutcome`.
- Default timeouts: 15 s per call; `GetUpdates` uses its long-poll timeout plus 10 s. The token never appears in errors or logs.
- `WithProxy(*url.URL)` sets a proxy (nil keeps `HTTP(S)_PROXY` and `ALL_PROXY` from the environment). `config.ParseProxy` accepts `http`, `https`, `socks5`, and `socks5h` URLs with a host, and its errors never hold the URL.
- Dial and TLS handshake time out after 5 s. TCP keepalive is 5 s idle, 3 s interval, 3 probes. The daemon polls with a 25 s long poll and retries network failures after 0.5 s to 2 s (API errors: 1 s to 30 s, and `retry_after`). The sender pauses at most 3 s after a request that never left the machine.
- The default transport speaks HTTP/1.1 only, and its connections drop unsent data when they close (`SO_LINGER` 0), so a request that timed out cannot reach Telegram later (design 9.1).
- `tgtest.New(t)` starts an `httptest` server that implements the methods above, keeps chats, topics, and messages in memory (one message number counter per chat, shared by both sides, as Telegram does), records every call with its time, can fail or delay the next calls of a method, can handle a call and then lose its answer (`LoseAnswerNext`), sends the bot its own "topic created" message as an update, and lets a test push updates that a pending `getUpdates` returns at once.

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
func (c *Client) NewSession(ctx, label string, tab Tab) (Started, error)   // `rex new --cwd=… --window=… --interactive --json LABEL -- CMD…`
func (c *Client) NewWindow(ctx, session string, tab Tab) (Started, error)  // `rex window new --session=… --cwd=… --interactive --focus=false --json TAB -- CMD…`
func (c *Client) RenameWindow(ctx, session, window, label string) error   // `rex window rename -s SESSION WINDOW LABEL`
func (c *Client) Events(ctx) (<-chan Event, <-chan error)           // `rex events --all --json`
```

- `Process`: `Foreground ProcInfo`, `Ancestors []ProcInfo`, `Child *ProcInfo`, `LastExit *Exit`; `ProcInfo{PID int, Name, Path, InvokedPath, Argv0, CWD string}`; `Exit{Code int, Error string}` from `{"exit_code":127,"error":"exit status 127"}`, set while a pane stays open after its program ended.
- `Tab{Label, CWD string; Command []string}` and `Started{SessionID, WindowID, BlockID string}`. `NewSession` and `NewWindow` refuse a name that starts with `-` (rex would read it as an option), a relative folder, and an empty command, before running rex. They are never retried: after a failure the tab may exist anyway. `RenameWindow` refuses a tab or a name that starts with `-`, and an empty session.
- `StatusRecord{ID, State, Kind, App, EffectiveApp, Title, Msg string; UpdatedAt time.Time; Owner *ProcOwner}`.
- `Event{Type EventType; SessionID, BlockID string; Status *StatusRecord; Received time.Time; Raw json.RawMessage}` with types `SessionCreated`, `SessionRemoved`, `BlockCreated`, `BlockClosed`, `ProgramStatusChanged`, `ProcessChanged`, `PwdChanged`, `TitleChanged`, `OtherEvent`. `progress_report` and client join/leave events are dropped.
- `rextest.New(t)` builds or provides a fake `rex` binary that answers commands from a scenario, logs every call (arguments and stdin), and can stream scripted events.

## internal/store

SQLite at a path given by the caller. Opens with WAL, `foreign_keys=ON`, `busy_timeout=5000`, and one open connection. Applies migrations from an embedded `schema.sql` and records the version in `kv`. Before migrating an existing database, copies it to `<path>.bak-<version>`.

The schema is design section 10, with these additions:

- `outbox.silent INTEGER NOT NULL DEFAULT 0`.
- `outbox_part.content TEXT NOT NULL` (the text to send), `outbox_part.format TEXT NOT NULL` (`html`, `plain`, `document`, `rich`, or `image`), `outbox_part.filename TEXT` (documents only).
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
RecentFolders(limit int) ([]string, error)                       // session folders, most recently used first (session start or pane last seen)

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
ClaimPart(outboxID int64, part int, floor int64) (bool, error) // floor: the newest message number known before the send; < 0 unknown
MarkPartSent(outboxID int64, part int, tgMessageID int64) error
ReleasePart(outboxID int64, part int) error           // back to pending (429 or a clear error)
MarkPartUnknown(outboxID int64, part int) error
MarkPartFailed(outboxID int64, part int, reason string) error
ResendUnknown(outboxID int64) (int, error)
ResendPart(outboxID int64, part int) (bool, error)      // one unknown part back to pending (the check found it missing)
MarkPartChecked(outboxID int64, part int) error
PartsToCheck() ([]Part, error)                          // unknown, not checked, with a floor
Part(outboxID int64, part int) (Part, error)
RecoverSending() ([]Part, error)                      // at start: sending -> unknown
OutboxRow(id int64) (OutboxRow, error)

// Message numbers (table seen_message, schema version 3)
NoteMessage(chatID, messageID int64, at time.Time) error
MaxMessage(chatID int64) (int64, bool, error)
UnseenBetween(chatID, lo, hi int64) (int64, error)      // numbers strictly between lo and hi not seen

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

## Wave 2 packages

### internal/config

```go
type Duration struct{ time.Duration }            // TOML text such as "2s"
type Answer struct{ Label string; Keys []string }
type RuntimeInput struct {
	Launch          []string // what /new runs: pi ["pi"], claude ["claude"], codex ["codex"]; must not be empty
	PasteEnterDelay Duration
	InterruptKeys   []string
	WhileWorking    string   // steer, queue, unknown
	Answers         []Answer // fallback permission answers when the screen shows no numbered options
}
type Config struct {
	Telegram struct{ APIBase string; RatePerSecond float64; RateBurst int }
	Rex      struct{ CLI string; ScanInterval Duration; NewSession string } // NewSession: the session for /new, default "rex-tg"
	Agents   struct {
		PiArgv0, ClaudeArgv0, ClaudeExecGlob, CodexNames []string
		Pi, Claude, Codex                                RuntimeInput
	}
	Display struct{ Mode string; Backfill int; ShowMacPrompts bool; ProgressDelay, ProgressEditInterval Duration }
	Input   struct{ QueueExpiry, JoinWindow, JoinWindowLong, BusyAfterTyping Duration; MaxUploadMB int }
	Voice   struct{ WhisperCLI, Model, Language string } // optional; empty WhisperCLI turns voice notes off
	State   struct{ Dir string }                        // default ~/.local/state/rex-tg
}
func Default(home string) Config
func Load(path, home string) (Config, error) // a missing file gives the defaults; unknown keys are an error
func (c Config) DBPath() string; func (c Config) SpoolDir() string; func (c Config) LogDir() string; func (c Config) UploadDir() string; func (c Config) LockPath() string
func (c Config) Input(rt agents.Runtime) RuntimeInput
```

Defaults come from design section 12 and the spikes: `ClaudeArgv0 ["claude", "cux"]` (cux runs claude as its child, so cux is the pane's foreground process); `Rex.NewSession "rex-tg"`, which must be a name that Rex takes (not empty, no leading `-`, no control characters, at most 128 characters); pi answers `[]`; Claude answers `1 Yes → ["1"]`, `2 Always → ["2"]`, `4 No → ["4"]`, `Esc → ["escape"]`; Codex answers `Yes → ["y"]`, `Always → ["p"]`, `No → ["escape"]`; every runtime `PasteEnterDelay 300ms`, `InterruptKeys ["escape"]`; `WhileWorking` steer for all three.

### internal/registry

Owns panes, sessions, and agent state. It is not pure: it reads Rex through an interface and writes through `*store.Store`, and it returns `Change` values that the daemon acts on.

```go
type Rules struct{ PiArgv0, ClaudeArgv0, ClaudeExecGlob, CodexNames []string }
type RexAPI interface {
	Sessions(ctx context.Context) ([]rex.Session, error)
	Blocks(ctx context.Context, session string) ([]rex.Block, error)
	Process(ctx context.Context, session, block string) (rex.Process, error)
	ProgramStatus(ctx context.Context, session, block string) ([]rex.StatusRecord, error)
}
type HookReport struct{ Runtime agents.Runtime; Event string; At time.Time; RexSession, RexBlock string; Payload json.RawMessage }
func ParseSpool(data []byte) (HookReport, error)
func Detect(rules Rules, p rex.ProcInfo) (agents.Runtime, bool)
func AgentInFront(p rex.Process, pid int, name string) bool

type Options struct{ Host, Home string; Rules Rules; Adapters map[agents.Runtime]agents.Adapter; Now func() time.Time; Logger *slog.Logger }
func New(st *store.Store, rx RexAPI, opt Options) *Registry
func (r *Registry) Hook(ctx context.Context, rep HookReport) ([]Change, error)
func (r *Registry) RexEvent(ctx context.Context, ev rex.Event) ([]Change, error)
func (r *Registry) Scan(ctx context.Context) ([]Change, error)
func (r *Registry) TranscriptSignal(ctx context.Context, convID int64, sig Signal) ([]Change, error) // from the daemon's tailers
func (r *Registry) Agents(ctx context.Context) ([]AgentView, error)                              // for /agents
func (r *Registry) Ready(ctx context.Context, paneID int64, now time.Time) (bool, string, error)  // may a waiting message be typed now? reason when not
func (r *Registry) MarkTyped(ctx context.Context, paneID int64, now time.Time) error              // starts the busy time after typing

type Signal struct{ State, Kind string; At time.Time } // State: working, idle, done, blocked, error
type AgentView struct{ Pane store.Pane; Runtime agents.Runtime; Conversation *store.Conversation; Resolution string /* hook, scan, guess, unresolved */; Thread *store.Thread }
type ChangeKind string // see below
type Change struct {
	Kind           ChangeKind
	PaneID, ConversationID, ThreadID int64
	State, StateKind, Msg string // StateChanged, Blocked
	Divider        string        // TakeOver, Resume: for example "claude · new session (clear)"
	Start          TailStart     // where the tailer for ConversationID starts
}
type TailStart struct{ FromStart, AtEnd, Backfill bool } // FromStart: byte 0; AtEnd: end of file (with Backfill: post the last N final answers)
```

Change kinds: `SessionStarted` (a resolved session now runs in a pane), `SessionStopped` (it left the pane), `TakeOver`, `Resume`, `ResumeBusy` (resumed while its topic is live elsewhere; no topic change), `TopicOffline`, `TopicLive`, `StateChanged`, `PaneGone`.

Rules (design sections 4.4, 6, and 7): detection by foreground process (pi `argv0`, Claude `argv0`/invoked path basename or executable under the glob, Codex name), never by the status `app`; hook reports accepted only when the pane's foreground process is the reporting runtime; nested transcripts ignored (`Adapter.IsSubagentTranscript`); PID plus name identity; one pane per session; the guess resolver with `TranscriptCandidates`; unresolved when ambiguous; take-over, resume, resume-busy, and resume under a new ID; the tail start table; state merge (newest signal wins, a prompt or `UserPromptSubmit` means working even after a stale `done`, delivery needs an idle/done signal newer than the last working signal, silence never counts as idle, blocked blocks delivery); `MarkTyped` sets `busy_until = now + BusyAfterTyping`, cleared by a working signal or a prompt.

Hook payload fields: Claude and Codex `session_id`, `transcript_path`, `cwd`, `hook_event_name`, `source` (SessionStart: startup, resume, clear, compact), `notification_type` (Claude `Notification`: `permission_prompt` means blocked); pi `hook_event_name` (session_start, session_shutdown, agent_start, agent_end), `reason` (startup, reload, new, resume, fork), `session_file`, `previous_session_file`, `target_session_file`, `cwd`, `session_id`.

### internal/inbound

Pure helpers for incoming messages.

```go
type RouteKind int // RouteCommand, RouteAgentText, RouteIgnore
type Route struct{ Kind RouteKind; Command, Args, Text string }
func RouteText(text string, inTopic bool, botUsername string) Route // design D15 and section 8.7

type Action int // ActText, ActSendNow, ActForce, ActKeys, ActPromptAnswer, ActReadOnly
type Facts struct{ FromOwner, OwnerChat, TopicLive, PaneExists, AgentInFront, PromptOpen, AlertMatches, Paused bool }
func Check(a Action, f Facts) (ok bool, reason string) // the action × checks table in design section 9.5; Paused blocks every typing action

type Joiner struct{ /* … */ }
func NewJoiner(window, longWindow time.Duration, longChars int) *Joiner
func (j *Joiner) Add(threadID, updateID int64, text string, at time.Time)
func (j *Joiner) Due(now time.Time) []Joined // Joined{ThreadID int64; UpdateIDs []int64; Text string}

func Normalize(s string) string
func Hash(s string) string
type Delivered struct{ UpdateID int64; BodyHash, Body string; DeliveredAt time.Time }
func MatchEcho(prompt string, at time.Time, delivered []Delivered) (int64, bool) // hash within 10 min, else 40-character prefix within 30 s
```

Topic commands: `info`, `screen`, `approve`, `deny`, `esc`, `interrupt`, `key`, `force`, `mode`, `verbose`, `reasoning`, `resend`, `unlink`, `mute`, `unmute`, `help`. Main-chat commands: `start`, `agents`, `topics`, `status`, `pause`, `unpause`, `help`. `/cmd@<botUsername>` is the same command; a command addressed to another bot is ignored.

### internal/sender

The only component that writes to Telegram. It owns the rate budget and the priorities of design section 9.3.

```go
type Options struct{ RatePerSecond float64; Burst int; Now func() time.Time; Logger *slog.Logger; CheckDelay time.Duration } // CheckDelay default 60 s
func New(st *store.Store, tg *telegram.Client, opt Options) *Sender
func (s *Sender) Run(ctx context.Context) error // until ctx ends
func (s *Sender) Wake()                         // new outbox rows exist
func (s *Sender) Progress(threadID int64, op ProgressOp)  // ProgressOp{Kind: Show|Edit|Repost|Finish; HTML string}; latest pending edit per topic wins
func (s *Sender) React(chatID, messageID int64, emoji string)
func (s *Sender) Typing(chatID, threadID int64)          // dropped when no budget is free
func (s *Sender) Call(prio Priority, fn func(ctx context.Context, tg *telegram.Client) error) error // other calls (topics, alerts, callback answers), waits for the result
func (s *Sender) Events() <-chan Event // Event{Kind: ThreadGone|Blocked|Conflict|PartUnknown; ThreadID, OutboxID int64; Part int}
func (s *Sender) Recover(ctx context.Context) error // at start: sending parts become unknown; unknown parts with a floor are checked
```

Rules: outbox parts per design section 9.1 (claims with the floor, parse-error fallback to plain text, 429 waits, an unknown part is checked against the chat's message numbers and sent again only when they prove it never arrived, else a notice with key `notice:unknown:<outbox id>:<part>` or the check's note edited into it, row state from parts), documents through `SendDocument`, muted topics send answers silently, progress messages through the `progress` table (Show, Edit with "not modified" as success and "edit target gone" as a new Show, Repost as new then delete old, Finish as the last edit then the row is deleted; an unknown Show is never retried), edits and deletes retried with backoff, priorities answers > alerts > new progress > progress edits > typing.

## Wave 3 packages

### internal/keychain

```go
var ErrNotFound error
func Get(ctx context.Context, service, account string) (string, error) // security find-generic-password -s -a -w
func Set(ctx context.Context, service, account, secret string) error    // through `security -i` with the command on stdin, so the secret never appears in argv
func Delete(ctx context.Context, service, account string) error
```

The token lives under service `rex-tg`, account `bot-token`.

### internal/daemon

```go
type Options struct {
	Config    config.Config
	Token     string
	Home      string
	Logger    *slog.Logger
	Rex       *rex.Client                     // nil: rex.New(Config.Rex.CLI)
	Telegram  *telegram.Client                // nil: telegram.New(Token, telegram.WithBaseURL(Config.Telegram.APIBase))
	Now       func() time.Time                // nil: time.Now
	ProcStart func(pid int) (time.Time, error) // nil: the registry default
	Timing    Timing                          // zero values: the production waits
	HasProgram func(name string) bool         // nil: the PATH, ~/.local/bin, /opt/homebrew/bin, /usr/local/bin (cux for /new); tests set it
}
var ErrAlreadyRunning error
func Run(ctx context.Context, opt Options) error                                   // until ctx ends
func NewPairingCode(ctx context.Context, st *store.Store, now time.Time) (string, error) // used by `rex-tg login`
```

KV keys: `owner_user_id`, `owner_chat_id`, `bot_username`, `tg_offset`, `paused`, `pairing_code_hash`, `pairing_expires_at`, `pairing_attempts`, `turn:<thread id>` (the turn tracker snapshot as JSON; the sender owns the `progress` rows).

`Run` does, in order: take the instance lock (`syscall.Flock` on `Config.LockPath()`, fail with `ErrAlreadyRunning`); open the store; `sender.Recover`; `store.RecoverTyping` with one notice per row ("Your message may be in the input box without Enter. Check with /screen."); `getMe` (save `bot_username`; log a warning when `has_topics_enabled` is false); `setMyCommands`; restore tailers and turn trackers for live topics; start the sender, the Rex event stream (restart with backoff), the spool watcher (fsnotify plus a 1 s poll on `Config.SpoolDir()`), the scan ticker, the `getUpdates` loop, and a 250 ms tick; then run one core loop that handles everything below. Only the core loop changes state, so no locks are needed beyond the store's.

Flows (design sections in brackets):

1. **Registry changes** [6, 7]: `SessionStarted`/`TakeOver`/`Resume` start or restart the tailer of that session at its `TailStart`, post the divider (`── <Divider> ──`, kind `divider`, key `divider:<thread>:<conv>:<n>`), and post the backfill (the last `Display.Backfill` final answers from `tail.Scan` through the adapter, kind `backfill`, with their own event keys so nothing repeats). `TopicOffline` posts "🔴 agent stopped" and stops the tailer; `TopicLive` posts "🟢 agent is running again". `StateChanged` goes to the topic's turn tracker; `blocked` with kind `permission` or `question` opens an alert; leaving `blocked` closes the topic's open alerts (buttons removed, state `gone` or `answered`). `PaneGone` makes the topic offline.
2. **Transcript lines** [4.2, 6, 9]: each batch is parsed with the session's adapter (`LineContext{SessionID, Offset, CWD}`); after a `Reset` batch, events whose `At` is at or before `cursor.LastEventAt` are dropped; a `UserPrompt` is checked with `inbound.MatchEcho` against `store.DeliveredSince` (an echo acks the inbound row, reacts 👍, and goes to the tracker with `echo=true`); events go to `turn.Tracker.Event`; prompts and answers also go to `registry.TranscriptSignal`; tracker `Post`s become outbox items (Markdown through `format.Redact` and `format.Parts(…, 4000)`; more than 4 parts become one `.md` document; `HTML` posts are one part; with `Display.RichMessages`, final answers up to 32768 characters become one `rich` part); all items of a batch plus the new cursor go through one `store.CommitBatch`; progress actions go to `sender.Progress`; `Typing` goes to `sender.Typing` only for the topic used last. The tracker snapshot is saved to KV after each batch and tick that changed it.
3. **Telegram updates** [8.7, 8.8, 9.5, 11]: each update is stored with `store.InsertInbound` before `tg_offset` moves past it. Before pairing, only `/start <code>` is handled (hash compare, 10-minute expiry, 5 attempts; success saves the owner and answers "Paired"). After pairing, updates from anyone else are dropped. The main chat handles `start`, `agents` (registry `Agents` with buttons: `o:<pane id>` to open, or a URL button to `https://t.me/<bot>/<message_thread_id>` when the session has a topic), `topics`, `status`, `pause`, `unpause`, `help`. A topic message is looked up with `store.ThreadByTopic`; unknown or closed topics answer "This topic is not linked. Use /agents in the main chat."; text goes through `inbound.RouteText`; agent text goes to the `inbound.Joiner`; commands run as in design 8.7 (`info`, `screen` with redacted capture in `<pre>`, `approve`, `deny`, `esc`, `interrupt`, `key`, `force`, `mode`, `verbose`, `reasoning`, `resend` → `store.ResendUnknown`, `unlink`, `mute`, `unmute`, `help`). Edited messages answer "Edits are not sent to the agent." Every callback query is answered.
4. **Typing** [4.3, 9.5]: joined text becomes a queued inbound message (the first update carries the text; the others are marked done); if the pane is not ready, answer "⏳ Waiting until the agent is idle." with `⚡ Send now` (`s:<update id>`) and `✖ Cancel` (`c:<update id>`). On each tick, for each topic with queued messages, the oldest one is delivered when `registry.Ready` says yes and `inbound.Check` passes: state `typing`, `rex.Paste`, wait `InputProfile.PasteEnterDelay`, `rex.SendKeys(submit)`, state `delivered`, react 👀, `registry.MarkTyped`. A refused message gets 👎 and a reply with the reason. A message older than `InputSettings.QueueExpiry` expires with a reply. The facts for `inbound.Check` come from the store, the registry, `rex.Process` with `registry.AgentInFront`, and the open alerts.
5. **Alerts** [8.6]: capture the pane, take the last 25 lines (redacted), parse numbered options (`^\s*(?:[❯>›]\s*)?(\d)[.)]\s+(.+)$`) into buttons with the digit as the key, else use `Config.Input(rt).Answers`; always add `Esc`. Store the alert with the SHA-256 of the prompt lines, post it through `sender.Call(PriorityAlert, …)` with buttons `a:<alert id>:<index>`. A button press checks the owner, captures again and compares the hash, sends the keys, marks the alert answered, and removes the buttons. `/approve` and `/deny` press the first and the last non-Esc option.
6. **Opening a topic** [4.1]: the `o:<pane id>` button resolves the pane's session (an unresolved pane answers "Waiting for the session file…"); `createForumTopic` through `sender.Call`; `store.LinkTopic`; the intro card (design 8.7); the backfill; the tailer starts at the end of the file; a turn tracker is created with the topic's settings.
7. **Sender events**: `ThreadGone` closes the topic in the store and stops its tailer; `Blocked` and `Conflict` stop the daemon with an error after logging.
8. **Housekeeping**: `store.Prune` once a day; spool files older than 24 h are deleted; uploads older than 7 days are deleted.
9. **Starting an agent** [8.7]: `/new [agent [folder]]` and its buttons (`nw:<token>:a:<agent>`, `nw:<token>:d:<index>`, `nw:<token>:x`). The agents are pi, claude, and codex, with their `launch` commands, and cux (runtime Claude, command `cux`) when `HasProgram("cux")`; the request keeps the list that its buttons show. The core keeps each request in memory under a random 12-character token, from before its question is sent, and ends it after 10 minutes; the first button press names the question's message when Telegram's answer to the send was lost. A worker lists the folder buttons (`store.RecentFolders`, only folders that exist) and hands them to the core before it shows them. A typed folder is taken before routing: a reply to the question, or the newest question in the same chat and topic; in a linked topic only a path that starts with `~`, or the absolute path of an existing folder. The start runs in a worker, one at a time: `rex.Sessions`, then `NewWindow` into the session named `Rex.NewSession` (case and outer spaces ignored), or `NewSession`, with the tab named `<folder> · <agent>`; then it polls `registry.Agents` every `Timing.NewAgentPoll` until the new block has a session and the core opens its topic (flow 6), or `rex.Process` shows that the program exited, or `Timing.NewAgentWait` passed. While it waits, it reads the screen: a list of answers with a cursor, seen on two reads in a row, is posted as a startup question with buttons `nw:<token>:o:<question>:<answer>`, which the core passes to the waiting worker. That worker presses Up or Down until the cursor is on the answer (reading the screen after each key, `Timing.VimPoll` × 15 at most), then Enter, with `registry.Detect` and the pause flag checked before each key (`startup.go`). An open question keeps the wait going, up to 10 minutes. Tab names: after a topic rename succeeds (`rename`, live topics only), `syncTab` looks up the pane's Rex session; when its name is `Rex.NewSession`, it finds the pane's tab (`rex.Blocks` → `WindowID`) and calls `RenameWindow` with the topic name. A failure is logged; the next topic rename tries again.
10. **Phase 6**: photos and documents up to `InputSettings.MaxUploadMB` (default 20) are downloaded to `UploadDir()/<thread id>/` and their path (plus the caption) is delivered like text; voice notes are transcribed when `Voice.WhisperCLI` is set (ffmpeg to 16 kHz WAV, then `<whisper_cli> -m <model> -l <language> -nt -f <wav>`), answered with "🎤 <text>" and delivered like text, else answered "Voice notes need whisper.cpp; see the README."; stickers and others answer "Only text is supported for now."; `/pause` stops all typing (KV `paused`) until `/unpause`; `/mute` and `/unmute` set `thread.muted`.

### cmd/rex-tg

Commands: `daemon [--config F] [--log-stderr]`, `hook <runtime>`, `login [--token-stdin]`, `pair`, `install [--only R] [--no-load]`, `uninstall [--only R] [--force] [--keep-launchd]`, `status`, `doctor`, `version`. The config path defaults to `~/.config/rex-tg/config.toml`.

- `hook`: never prints and always exits 0. Reads at most 1 MiB of stdin, takes `REX_SESSION` and `REX_BLOCK` from the environment, does nothing when `REX_BLOCK` is empty, and writes the spool file described above.
- `login`: reads the token (from the terminal with echo off, or stdin with `--token-stdin`), checks it with `getMe`, stores it with `keychain.Set`, then runs `pair`.
- `pair`: opens the store, calls `daemon.NewPairingCode`, and prints "Send /start <code> to @<bot> within 10 minutes." The running daemon handles the code.
- `daemon`: logs JSON to `LogDir()/rex-tg.log` through lumberjack (10 MB, 3 backups), plus stderr with `--log-stderr`; reads the token with `keychain.Get`; stops cleanly on SIGINT and SIGTERM.
- `doctor` checks and prints one line each: the Rex CLI path and `whoami`, the token, `getMe` with `has_topics_enabled`, the pairing, the hooks and files from `install.Inspect`, the launchd agent, the database, the folder modes, and whether a daemon holds the lock.
- `status` opens the store read-only and prints topics, queued and unknown messages, and open alerts.

### Images in answers

`telegram.(*Client).SendPhoto(ctx, chatID, threadID int64, filename string, data []byte, caption string, silent bool) (Message, error)` uploads with multipart field `photo`, like `SendDocument` (both share one upload helper), and notes the message number. `tgtest` has `sendPhoto` (a message with `Photo` set and a caption, counted like any send).

Schema version 4 rebuilds `outbox_part` so that `format` also allows `image`. It keeps all columns (including `floor_id` and `checked`), all rows, and the index `outbox_part_state`.

`daemon/images.go`: `findImages(markdown, home, cwd) []string` returns the existing image files that an answer names (absolute, no repeats, at most 10, 1 byte to 50 MB). In `work`, a `turn.Post` of kind `answer` also makes an outbox item of kind `image` (key `<answer key>:images`, silent, one `image` part per file, `Content` the path, `Filename` the base name) when `Display.SendImages` is on. It goes into the same `CommitBatch` as the answer.

`sender/image.go`: `sendImage` reads the file at send time. A missing, unreadable, or over 50 MB file fails the part with a short reason. A PNG or JPEG of at most 10 MB whose `image.DecodeConfig` gives width plus height of at most 10000 and a side ratio of at most 20 goes out with `SendPhoto`; all others with `SendDocument`. The caption is the file name. A 400 error on the photo falls back to `SendDocument` in the same attempt. Other outcomes use `judge` and `finishPart` as for any part.

### Rich messages (Phase 6, opt-in)

`telegram.(*Client).SendRichMessage(ctx, chatID, threadID int64, markdown string, silent bool, markup *InlineKeyboardMarkup) (Message, error)` sends `{"chat_id", "message_thread_id", "rich_message": {"markdown": …}, "disable_notification", "reply_markup"}`. Schema version 2 adds `rich` to `outbox_part.format`. The sender sends `rich` parts with `SendRichMessage`; on a 400 error it falls back to plain text, or to a `.md` document when the text is longer than 4000 characters.
