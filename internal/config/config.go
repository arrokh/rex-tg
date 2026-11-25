// Package config loads rex-tg's TOML configuration (design section 12).
//
// Default fills every setting, so a missing or partial file works. Unknown
// keys are an error, which catches typos early.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"

	"rex-tg/internal/agents"
)

// Duration is a time.Duration that reads from TOML text such as "2s".
type Duration struct{ time.Duration }

// UnmarshalText parses a Go duration string.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(strings.TrimSpace(string(b)))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", b, err)
	}
	if v < 0 {
		return fmt.Errorf("negative duration %q", b)
	}
	d.Duration = v
	return nil
}

// MarshalText writes the duration as text such as "2s".
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.Duration.String()), nil }

// Answer is an explicitly configured fallback permission choice for a screen
// whose controls the prompt reader does not recognize.
type Answer struct {
	Label string   `toml:"label"`
	Keys  []string `toml:"keys"`
}

// RuntimeInput holds the settings of one runtime: how /new starts it and
// how to type into it.
type RuntimeInput struct {
	// Launch is the command that /new runs in a new Rex tab: the program and
	// its arguments, one word each. It runs through the login shell with its
	// interactive startup files (~/.zshrc), as when you type it in a
	// terminal.
	Launch          []string `toml:"launch"`
	PasteEnterDelay Duration `toml:"paste_enter_delay"`
	InterruptKeys   []string `toml:"interrupt_keys"`
	WhileWorking    string   `toml:"while_working"` // steer, queue, unknown
	// AllowUnknownPermissionAnswers is an explicit opt-in to use Answers when
	// an explicitly typed permission screen cannot be parsed.
	AllowUnknownPermissionAnswers bool `toml:"allow_unknown_permission_answers"`
	// Answers are fallback permission choices for an unknown screen. Empty by
	// default, and ignored unless AllowUnknownPermissionAnswers is true.
	Answers []Answer `toml:"answers"`
	// Choices replaces the buttons that a setting command offers, by control
	// name: [agents.claude.choices] model = ["opus", "sonnet"]. A control
	// that is not listed keeps the agent's own choices.
	Choices map[string][]string `toml:"choices"`
}

// Config is the whole configuration.
type Config struct {
	Telegram struct {
		APIBase string `toml:"api_base"`
		Proxy   string `toml:"proxy"`
		// Fallback lets rex-tg switch between the proxy and the direct
		// route to Telegram when one of them stops working. It is used
		// only when Proxy is set.
		Fallback      bool    `toml:"fallback"`
		RatePerSecond float64 `toml:"rate_per_second"`
		RateBurst     int     `toml:"rate_burst"`
	} `toml:"telegram"`
	Rex struct {
		CLI          string   `toml:"cli"`
		ScanInterval Duration `toml:"scan_interval"`
		// NewSession is the Rex session where /new opens its tabs. rex-tg
		// makes it when it does not exist.
		NewSession string `toml:"new_session"`
		// NewFolders are the folders that /new offers first, in this order.
		NewFolders []string `toml:"new_folders"`
	} `toml:"rex"`
	Agents struct {
		PiArgv0 []string `toml:"pi_argv0"`
		// ClaudeArgv0 are the programs that count as Claude Code: claude
		// itself, and cux, which runs claude as its child.
		ClaudeArgv0    []string     `toml:"claude_argv0"`
		ClaudeExecGlob []string     `toml:"claude_exec_glob"`
		CodexNames     []string     `toml:"codex_names"`
		Pi             RuntimeInput `toml:"pi"`
		Claude         RuntimeInput `toml:"claude"`
		Codex          RuntimeInput `toml:"codex"`
	} `toml:"agents"`
	Display struct {
		Mode           string `toml:"mode"` // progress, block, final
		Backfill       int    `toml:"backfill"`
		ShowMacPrompts bool   `toml:"show_mac_prompts"`
		// MacNotice holds the turns that start on the Mac: the topic gets one
		// notice instead of their prompts, progress, and answers, until a
		// message from Telegram. On by default.
		MacNotice            bool     `toml:"mac_notice"`
		ProgressDelay        Duration `toml:"progress_delay"`
		ProgressEditInterval Duration `toml:"progress_edit_interval"`
		// ProgressAnimation edits the progress message this often while the
		// agent works, so its hourglass turns and its timer moves. Off ("0s")
		// by default: then the timer moves every 10 s at first and less often
		// later. Edits every few seconds in several topics made Telegram hold
		// the chat for about 95 minutes (2026-11-14).
		ProgressAnimation Duration `toml:"progress_animation"`
		// SummaryAfter keeps the "✅ Done" summary of a turn that ended with
		// its answer only when the turn took at least this long; a shorter
		// turn leaves just its answer. "0s" keeps every summary. Default 2m.
		SummaryAfter Duration `toml:"summary_after"`
		// RichMessages is retained so older config files still load. All
		// Markdown now uses the same HTML renderer; this setting is ignored.
		RichMessages bool `toml:"rich_messages"`
		// SendImages sends the image files that a final answer names to the
		// topic, right after the answer. On by default.
		SendImages bool `toml:"send_images"`
		// TurnImages is how many of the images that the agent looked at in
		// a turn (in tool results) come after its answer, when the answer
		// names no image file. The last ones count; 0 turns it off.
		TurnImages int `toml:"turn_images"`
		// SendFiles are the other file types (extensions, without the dot)
		// that a final answer offers: a button per file sends it, as a
		// document. An empty list offers none.
		SendFiles []string `toml:"send_files"`
		// QuickReplies are buttons under each answer that send their text
		// to the agent with one tap. Empty (the default): none.
		QuickReplies []string `toml:"quick_replies"`
		// ShowChanges adds a button to an answer that shows what the turn
		// changed in its git repository, in the Mini App ([miniapp] url).
		ShowChanges bool `toml:"show_changes"`
		// Stream shows the agent's text while it writes, as a Telegram draft
		// (sendMessageDraft): word by word for pi, block by block for Claude
		// Code and Codex. Off by default.
		Stream bool `toml:"stream"`
		// MessageLength is about how long one message of a reply is: a
		// longer reply comes as several messages, split at paragraphs and
		// headings like a person would split it. At most MaxMessageLength.
		MessageLength int `toml:"message_length"`
		// StreamInterval is the least time between two drafts of a topic.
		// MinStreamInterval is the fastest: Telegram allows about one
		// message a second in a chat, and drafts share that budget.
		StreamInterval Duration `toml:"stream_interval"`
	} `toml:"display"`
	// Alerts holds the [alerts] table: reminders of permission prompts and
	// questions that wait for an answer.
	// MiniApp is rex-tg's Telegram Mini App: pages too big for a message
	// (a long answer, the changes of a turn), only for the owner.
	MiniApp struct {
		// URL is the HTTPS address that reaches Listen, such as
		// "tailscale serve" on the Mac's tailnet name. Empty turns the Mini
		// App off.
		URL string `toml:"url"`
		// TailnetOnly answers only requests that came through "tailscale
		// serve" from a device of your tailnet (Tailscale's identity
		// header). On by default: the keyboard's buttons carry a key in
		// their address, and the Mini App must not be public (Funnel).
		TailnetOnly bool `toml:"tailnet_only"`
		// TailnetUsers are the Tailscale logins (as "tailscale status"
		// shows them, such as you@example.com) whose devices may open
		// pages with the buttons' key. Empty: rex-tg learns your login
		// from your first visit (MiniApp server). Used with TailnetOnly.
		TailnetUsers []string `toml:"tailnet_users"`
		// Listen is the local address of the Mini App's server.
		Listen string `toml:"listen"`
		// AnswerPreview: an answer longer than this many characters shows
		// its start, with a button that opens the whole answer.
		AnswerPreview int `toml:"answer_preview"`
		// Files lets /files open a browser of the agent's folder, read-only.
		Files bool `toml:"files"`
		// Changes lets /changes show what changed in the agent's git
		// repository, read-only.
		Changes bool `toml:"changes"`
	} `toml:"miniapp"`
	Alerts struct {
		RemindAfter Duration `toml:"remind_after"` // the first reminder, and the gap between two; 0 turns them off
		Reminders   int      `toml:"reminders"`    // the most reminders for one prompt
	} `toml:"alerts"`
	// InputSettings holds the [input] table. It is not called Input because
	// Config.Input(rt) is a method.
	InputSettings struct {
		QueueExpiry     Duration `toml:"queue_expiry"`
		JoinWindow      Duration `toml:"join_window"`
		JoinWindowLong  Duration `toml:"join_window_long"`
		BusyAfterTyping Duration `toml:"busy_after_typing"`
		MaxUploadMB     int      `toml:"max_upload_mb"`
		// WhileBusy is what a message sent while the agent works does:
		// "steer" types it into the current run at once (when the agent's
		// while_working is steer or queue), "wait" waits until the agent is
		// idle, with ⚡ Send now and ✖ Cancel.
		WhileBusy string `toml:"while_busy"`
	} `toml:"input"`
	// Voice is optional. An empty WhisperCLI turns voice notes off.
	Voice struct {
		WhisperCLI string `toml:"whisper_cli"`
		Model      string `toml:"model"`
		Language   string `toml:"language"`
	} `toml:"voice"`
	State struct {
		Dir string `toml:"dir"`
	} `toml:"state"`
}

func secs(s float64) Duration { return Duration{time.Duration(s * float64(time.Second))} }

func rexAppBundleCLI(app string) string {
	return filepath.Join("/Applications", app, "Contents", "Helpers", "Rex Server.rex", "Contents", "MacOS", "rex")
}

func executableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// defaultRexCLI prefers installed Rex paths, including the version-managed
// app-support install, then falls back to the current app bundle location.
func defaultRexCLI(home string) string {
	candidates := make([]string, 0, 3)
	if home != "" {
		candidates = append(candidates, filepath.Join(home, "Library", "Application Support", "com.superlogical.rex", "RexServer", "Versions", "Current", "Rex Server.app", "Contents", "MacOS", "rex"))
	}
	candidates = append(candidates, rexAppBundleCLI("Rex.app"), rexAppBundleCLI("Rex Beta.app"))
	for _, candidate := range candidates {
		if executableFile(candidate) {
			return candidate
		}
	}
	return rexAppBundleCLI("Rex.app")
}

// migrateMissingRexCLI updates only a missing known bundle path; custom paths
// stay authoritative so a typo or a remote setup is not silently rewritten.
func migrateMissingRexCLI(cli, home string) string {
	if executableFile(cli) {
		return cli
	}
	if cli == rexAppBundleCLI("Rex.app") || cli == rexAppBundleCLI("Rex Beta.app") {
		return defaultRexCLI(home)
	}
	return cli
}

// Default returns the default configuration for the given home folder.
func Default(home string) Config {
	var c Config
	c.Telegram.APIBase = "https://api.telegram.org"
	c.Telegram.Fallback = true
	c.Telegram.RatePerSecond = 1
	c.Telegram.RateBurst = 5

	c.Rex.CLI = defaultRexCLI(home)
	c.Rex.ScanInterval = Duration{30 * time.Second}
	c.Rex.NewSession = "rex-tg"

	c.Agents.PiArgv0 = []string{"pi"}
	c.Agents.ClaudeArgv0 = []string{"claude", "cux"}
	c.Agents.ClaudeExecGlob = []string{filepath.Join(home, ".local/share/claude/versions/*")}
	c.Agents.CodexNames = []string{"codex"}
	base := func(answers ...Answer) RuntimeInput {
		return RuntimeInput{
			PasteEnterDelay: Duration{300 * time.Millisecond},
			InterruptKeys:   []string{"escape"},
			WhileWorking:    "steer",
			Answers:         answers,
		}
	}
	c.Agents.Pi = base()
	c.Agents.Pi.Answers = []Answer{}
	c.Agents.Pi.Launch = []string{"pi"}
	c.Agents.Claude = base()
	c.Agents.Claude.Launch = []string{"claude"}
	// Fallback answers stay empty. A Codex prompt with an unknown layout
	// must not gain guessed keys such as y, p, or a.
	c.Agents.Codex = base()
	c.Agents.Codex.Launch = []string{"codex"}

	c.Display.Mode = "progress"
	c.Display.Backfill = 3
	c.Display.ShowMacPrompts = true
	c.Display.MacNotice = true
	c.Alerts.RemindAfter = Duration{10 * time.Minute}
	c.Alerts.Reminders = 3
	c.Display.SendImages = true
	c.Display.TurnImages = 4
	c.Display.SendFiles = []string{"pdf", "csv", "tsv", "html", "htm", "xlsx", "docx", "pptx"}
	c.Display.ShowChanges = true
	c.MiniApp.Listen = "127.0.0.1:8770"
	c.MiniApp.AnswerPreview = 3000
	c.MiniApp.Files = true
	c.MiniApp.Changes = true
	c.MiniApp.TailnetOnly = true
	c.Display.Stream = false // off: at its fastest it changes once a second
	c.Display.StreamInterval = Duration{MinStreamInterval}
	c.Display.MessageLength = 1500
	c.Display.ProgressDelay = secs(2)
	c.Display.ProgressEditInterval = secs(5)
	c.Display.ProgressAnimation = secs(0)
	c.Display.SummaryAfter = secs(120)
	c.Display.RichMessages = false

	c.InputSettings.QueueExpiry = Duration{30 * time.Minute}
	c.InputSettings.JoinWindow = Duration{300 * time.Millisecond}
	c.InputSettings.JoinWindowLong = Duration{1500 * time.Millisecond}
	c.InputSettings.BusyAfterTyping = Duration{15 * time.Second}
	c.InputSettings.WhileBusy = "steer"
	c.InputSettings.MaxUploadMB = 20

	c.State.Dir = filepath.Join(home, ".local/state/rex-tg")
	return c
}

// Load reads the file at path over the defaults. A missing file gives the
// defaults. Unknown keys and invalid values are errors. A leading "~" in a
// path setting means home.
func Load(path, home string) (Config, error) {
	c := Default(home)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return c, nil
		}
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	md, err := toml.Decode(string(data), &c)
	if err != nil {
		return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	c.Rex.CLI = migrateMissingRexCLI(c.Rex.CLI, home)
	if un := md.Undecoded(); len(un) > 0 {
		keys := make([]string, len(un))
		for i, k := range un {
			keys[i] = k.String()
		}
		return Config{}, fmt.Errorf("config: %s: unknown keys: %s", path, strings.Join(keys, ", "))
	}
	c.expand(home)
	if err := c.validate(); err != nil {
		return Config{}, fmt.Errorf("config: %s: %w", path, err)
	}
	return c, nil
}

// MaxTurnImages bounds display.turn_images.
const MaxTurnImages = 10

// MinMessageLength and MaxMessageLength bound display.message_length. 4000
// keeps to Telegram's limit of 4096 characters, so a reply is split only
// when it must be.
const (
	MinMessageLength = 300
	MaxMessageLength = 4000
)

// MinProgressAnimation is the fastest display.progress_animation: every
// working agent edits its progress message this often, within the chat's
// budget of about one message a second.
const MinProgressAnimation = 2 * time.Second

// MinStreamInterval is the fastest display.stream_interval.
const MinStreamInterval = time.Second

// fileExtRe is a file extension in display.send_files, without the dot.
var fileExtRe = regexp.MustCompile(`^[a-z0-9]{1,10}$`)

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

func (c *Config) expand(home string) {
	c.Rex.CLI = expandHome(c.Rex.CLI, home)
	c.State.Dir = expandHome(c.State.Dir, home)
	c.Voice.WhisperCLI = expandHome(c.Voice.WhisperCLI, home)
	for i, g := range c.Agents.ClaudeExecGlob {
		c.Agents.ClaudeExecGlob[i] = expandHome(g, home)
	}
	for i, x := range c.Display.SendFiles {
		c.Display.SendFiles[i] = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(x), "."))
	}
	for i, f := range c.Rex.NewFolders {
		if f = expandHome(strings.TrimSpace(f), home); filepath.IsAbs(f) {
			f = filepath.Clean(f)
		}
		c.Rex.NewFolders[i] = f
	}
}

func (c Config) validate() error {
	switch c.Display.Mode {
	case "progress", "block", "final":
	default:
		return fmt.Errorf("display.mode %q: want progress, block, or final", c.Display.Mode)
	}
	switch c.InputSettings.WhileBusy {
	case "steer", "wait":
	default:
		return fmt.Errorf("input.while_busy %q: want steer or wait", c.InputSettings.WhileBusy)
	}
	for name, in := range map[string]RuntimeInput{"pi": c.Agents.Pi, "claude": c.Agents.Claude, "codex": c.Agents.Codex} {
		switch in.WhileWorking {
		case "steer", "queue", "unknown":
		default:
			return fmt.Errorf("agents.%s.while_working %q: want steer, queue, or unknown", name, in.WhileWorking)
		}
		for control, vals := range in.Choices {
			for _, v := range vals {
				if !agents.ValidValue(v) {
					return fmt.Errorf("agents.%s.choices.%s: %q is not a valid value (letters, digits, and . _ : / [ ] - only)", name, control, v)
				}
			}
		}
		if len(in.Launch) == 0 || strings.TrimSpace(in.Launch[0]) == "" {
			return fmt.Errorf("agents.%s.launch: want a program, such as [%q]", name, name)
		}
	}
	if c.Alerts.RemindAfter.Duration < 0 || c.Alerts.Reminders < 0 {
		return errors.New("alerts.remind_after and alerts.reminders must not be negative")
	}
	if c.Telegram.RatePerSecond <= 0 {
		return errors.New("telegram.rate_per_second must be positive")
	}
	if c.Telegram.RateBurst < 1 {
		return errors.New("telegram.rate_burst must be at least 1")
	}
	if c.State.Dir == "" {
		return errors.New("state.dir must not be empty")
	}
	if err := checkLabel(c.Rex.NewSession); err != nil {
		return fmt.Errorf("rex.new_session %q: %w", c.Rex.NewSession, err)
	}
	// The hooks run in the agents' folders and the daemon under launchd: a
	// relative folder would mean a different place for each of them.
	if !filepath.IsAbs(c.State.Dir) {
		return fmt.Errorf("state.dir %q must be an absolute path (or start with ~/)", c.State.Dir)
	}
	for _, f := range c.Rex.NewFolders {
		if !filepath.IsAbs(f) {
			return fmt.Errorf("rex.new_folders %q must be an absolute path (or start with ~/)", f)
		}
	}
	if c.Display.MessageLength < MinMessageLength || c.Display.MessageLength > MaxMessageLength {
		return fmt.Errorf("display.message_length %d: want %d to %d characters", c.Display.MessageLength, MinMessageLength, MaxMessageLength)
	}
	if c.Display.TurnImages < 0 || c.Display.TurnImages > MaxTurnImages {
		return fmt.Errorf("display.turn_images %d: want 0 to %d", c.Display.TurnImages, MaxTurnImages)
	}
	for _, u := range c.MiniApp.TailnetUsers {
		if strings.TrimSpace(u) == "" || len(u) > 320 {
			return fmt.Errorf("miniapp.tailnet_users %q: a Tailscale login, such as you@example.com", u)
		}
	}
	if u := c.MiniApp.URL; u != "" && !strings.HasPrefix(u, "https://") {
		return fmt.Errorf("miniapp.url %q: Telegram opens a Mini App only from an https:// address", u)
	}
	if c.MiniApp.AnswerPreview < 2000 {
		return fmt.Errorf("miniapp.answer_preview %d: at least 2000 (the chat shows about 1200 characters of a longer answer)", c.MiniApp.AnswerPreview)
	}
	if len(c.Display.QuickReplies) > 8 {
		return fmt.Errorf("display.quick_replies: at most 8 buttons")
	}
	for _, q := range c.Display.QuickReplies {
		if n := len([]rune(strings.TrimSpace(q))); n == 0 || n > 64 {
			return fmt.Errorf("display.quick_replies %q: 1 to 64 characters", q)
		}
	}
	if c.Display.SummaryAfter.Duration < 0 {
		return fmt.Errorf("display.summary_after %v: \"0s\" or more", c.Display.SummaryAfter.Duration)
	}
	if a := c.Display.ProgressAnimation.Duration; a != 0 && a < MinProgressAnimation {
		return fmt.Errorf("display.progress_animation %v: \"0s\" (off) or at least %v", a, MinProgressAnimation)
	}
	if c.Display.StreamInterval.Duration < MinStreamInterval {
		return fmt.Errorf("display.stream_interval %v: at least %v (Telegram allows about one message a second in a chat)", c.Display.StreamInterval.Duration, MinStreamInterval)
	}
	for _, x := range c.Display.SendFiles {
		if !fileExtRe.MatchString(x) {
			return fmt.Errorf("display.send_files %q: want a file extension such as \"pdf\"", x)
		}
	}
	// The token is part of every request URL, so it must never go out in
	// clear text. Plain http is only for a local test server.
	u, err := url.Parse(c.Telegram.APIBase)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) {
		return fmt.Errorf("telegram.api_base %q: want an https URL (http only for a local test server)", c.Telegram.APIBase)
	}
	if c.Telegram.Proxy != "" {
		if _, err := ParseProxy(c.Telegram.Proxy); err != nil {
			return err
		}
	}
	return nil
}

// TelegramProxy returns the parsed telegram.proxy, or nil when it is empty.
func (c Config) TelegramProxy() *url.URL {
	if c.Telegram.Proxy == "" {
		return nil
	}
	u, _ := ParseProxy(c.Telegram.Proxy) // validated when the config loads
	return u
}

// ParseProxy parses the value of telegram.proxy. The error never contains
// the URL, because it may hold a password.
func ParseProxy(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return nil, errors.New("telegram.proxy: want a URL with a host, such as socks5://127.0.0.1:1080")
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
		return u, nil
	}
	return nil, fmt.Errorf("telegram.proxy: scheme %q is not supported; use http, https, socks5, or socks5h", u.Scheme)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// MiniAppProblem says why the Mini App must stay off although its url is
// set, or "" when it may run. With tailnet_only, rex-tg trusts Tailscale's
// identity header, which only "tailscale serve" on this Mac may set: a
// listen address that other devices reach would let them set it too.
func (c Config) MiniAppProblem() string {
	if strings.TrimSpace(c.MiniApp.URL) == "" || !c.MiniApp.TailnetOnly {
		return ""
	}
	host, _, err := net.SplitHostPort(c.MiniApp.Listen)
	if err != nil {
		return fmt.Sprintf("miniapp.listen %q is not host:port", c.MiniApp.Listen)
	}
	if host == "localhost" {
		return ""
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Sprintf("miniapp.listen %q is reachable from other devices; with tailnet_only it must be 127.0.0.1 or [::1], so only tailscale serve on this Mac reaches it", c.MiniApp.Listen)
	}
	return ""
}

// DBPath is the SQLite database file.
func (c Config) DBPath() string { return filepath.Join(c.State.Dir, "rex-tg.db") }

// SpoolDir is the hook spool folder.
func (c Config) SpoolDir() string { return filepath.Join(c.State.Dir, "spool") }

// LogDir is the log folder.
func (c Config) LogDir() string { return filepath.Join(c.State.Dir, "logs") }

// UploadDir is the folder for files received from Telegram.
func (c Config) UploadDir() string { return filepath.Join(c.State.Dir, "uploads") }

// ShownDir is the folder for the images that an agent looked at, until they
// go to Telegram (display.turn_images).
func (c Config) ShownDir() string { return filepath.Join(c.State.Dir, "shown") }

// LiveDir is the folder where the pi extension writes the text that pi is
// writing, for the drafts (display.stream).
func (c Config) LiveDir() string { return filepath.Join(c.State.Dir, "live") }

// LockPath is the single-instance lock file.
func (c Config) LockPath() string { return filepath.Join(c.State.Dir, "daemon.lock") }

// Input returns the typing settings of a runtime. An unknown runtime gives
// the zero value.
func (c Config) Input(rt agents.Runtime) RuntimeInput {
	switch rt {
	case agents.Pi:
		return c.Agents.Pi
	case agents.Claude:
		return c.Agents.Claude
	case agents.Codex:
		return c.Agents.Codex
	}
	return RuntimeInput{}
}

// checkLabel checks a Rex session name: Rex takes at most 128 characters
// and no control characters, and rex would read a name that starts with "-"
// as an option.
func checkLabel(l string) error {
	switch {
	case strings.TrimSpace(l) == "":
		return errors.New("want a name")
	case strings.HasPrefix(strings.TrimSpace(l), "-"):
		return errors.New("must not start with -")
	case len([]rune(l)) > 128:
		return errors.New("longer than 128 characters")
	case strings.ContainsFunc(l, unicode.IsControl):
		return errors.New("has a control character")
	}
	return nil
}
