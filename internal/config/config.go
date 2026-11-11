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
	"strings"
	"time"

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

// Answer is a fallback permission answer: a button label and the keys it types.
type Answer struct {
	Label string   `toml:"label"`
	Keys  []string `toml:"keys"`
}

// RuntimeInput describes how to type into one runtime.
type RuntimeInput struct {
	PasteEnterDelay Duration `toml:"paste_enter_delay"`
	InterruptKeys   []string `toml:"interrupt_keys"`
	WhileWorking    string   `toml:"while_working"` // steer, queue, unknown
	// Answers are the fallback permission answers when the screen shows no
	// numbered options.
	Answers []Answer `toml:"answers"`
}

// Config is the whole configuration.
type Config struct {
	Telegram struct {
		APIBase       string  `toml:"api_base"`
		RatePerSecond float64 `toml:"rate_per_second"`
		RateBurst     int     `toml:"rate_burst"`
	} `toml:"telegram"`
	Rex struct {
		CLI          string   `toml:"cli"`
		ScanInterval Duration `toml:"scan_interval"`
	} `toml:"rex"`
	Agents struct {
		PiArgv0        []string     `toml:"pi_argv0"`
		ClaudeArgv0    []string     `toml:"claude_argv0"`
		ClaudeExecGlob []string     `toml:"claude_exec_glob"`
		CodexNames     []string     `toml:"codex_names"`
		Pi             RuntimeInput `toml:"pi"`
		Claude         RuntimeInput `toml:"claude"`
		Codex          RuntimeInput `toml:"codex"`
	} `toml:"agents"`
	Display struct {
		Mode                 string   `toml:"mode"` // progress, block, final
		Backfill             int      `toml:"backfill"`
		ShowMacPrompts       bool     `toml:"show_mac_prompts"`
		ProgressDelay        Duration `toml:"progress_delay"`
		ProgressEditInterval Duration `toml:"progress_edit_interval"`
		// RichMessages sends final answers with Telegram's sendRichMessage
		// (Bot API 10.1) instead of HTML parts. Off by default.
		RichMessages bool `toml:"rich_messages"`
	} `toml:"display"`
	// InputSettings holds the [input] table. It is not called Input because
	// Config.Input(rt) is a method.
	InputSettings struct {
		QueueExpiry     Duration `toml:"queue_expiry"`
		JoinWindow      Duration `toml:"join_window"`
		JoinWindowLong  Duration `toml:"join_window_long"`
		BusyAfterTyping Duration `toml:"busy_after_typing"`
		MaxUploadMB     int      `toml:"max_upload_mb"`
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

// Default returns the default configuration for the given home folder.
func Default(home string) Config {
	var c Config
	c.Telegram.APIBase = "https://api.telegram.org"
	c.Telegram.RatePerSecond = 1
	c.Telegram.RateBurst = 5

	c.Rex.CLI = "/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex"
	c.Rex.ScanInterval = Duration{30 * time.Second}

	c.Agents.PiArgv0 = []string{"pi"}
	c.Agents.ClaudeArgv0 = []string{"claude"}
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
	c.Agents.Claude = base(
		Answer{"1 Yes", []string{"1"}},
		Answer{"2 Always", []string{"2"}},
		Answer{"4 No", []string{"4"}},
		Answer{"Esc", []string{"escape"}},
	)
	c.Agents.Codex = base(
		Answer{"Yes", []string{"y"}},
		Answer{"Always", []string{"p"}},
		Answer{"No", []string{"escape"}},
	)

	c.Display.Mode = "progress"
	c.Display.Backfill = 3
	c.Display.ShowMacPrompts = true
	c.Display.ProgressDelay = secs(2)
	c.Display.ProgressEditInterval = secs(2)

	c.InputSettings.QueueExpiry = Duration{30 * time.Minute}
	c.InputSettings.JoinWindow = Duration{300 * time.Millisecond}
	c.InputSettings.JoinWindowLong = Duration{1500 * time.Millisecond}
	c.InputSettings.BusyAfterTyping = Duration{15 * time.Second}
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
}

func (c Config) validate() error {
	switch c.Display.Mode {
	case "progress", "block", "final":
	default:
		return fmt.Errorf("display.mode %q: want progress, block, or final", c.Display.Mode)
	}
	for name, in := range map[string]RuntimeInput{"pi": c.Agents.Pi, "claude": c.Agents.Claude, "codex": c.Agents.Codex} {
		switch in.WhileWorking {
		case "steer", "queue", "unknown":
		default:
			return fmt.Errorf("agents.%s.while_working %q: want steer, queue, or unknown", name, in.WhileWorking)
		}
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
	// The hooks run in the agents' folders and the daemon under launchd: a
	// relative folder would mean a different place for each of them.
	if !filepath.IsAbs(c.State.Dir) {
		return fmt.Errorf("state.dir %q must be an absolute path (or start with ~/)", c.State.Dir)
	}
	// The token is part of every request URL, so it must never go out in
	// clear text. Plain http is only for a local test server.
	u, err := url.Parse(c.Telegram.APIBase)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) {
		return fmt.Errorf("telegram.api_base %q: want an https URL (http only for a local test server)", c.Telegram.APIBase)
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// DBPath is the SQLite database file.
func (c Config) DBPath() string { return filepath.Join(c.State.Dir, "rex-tg.db") }

// SpoolDir is the hook spool folder.
func (c Config) SpoolDir() string { return filepath.Join(c.State.Dir, "spool") }

// LogDir is the log folder.
func (c Config) LogDir() string { return filepath.Join(c.State.Dir, "logs") }

// UploadDir is the folder for files received from Telegram.
func (c Config) UploadDir() string { return filepath.Join(c.State.Dir, "uploads") }

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
