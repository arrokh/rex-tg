package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/agents"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaults(t *testing.T) {
	c := Default("/h")
	if c.Telegram.RatePerSecond != 1 || c.Telegram.RateBurst != 5 {
		t.Errorf("telegram = %+v", c.Telegram)
	}
	if c.Rex.ScanInterval.Duration != 30*time.Second || c.Rex.NewSession != "rex-tg" {
		t.Errorf("rex = %+v", c.Rex)
	}
	if c.Display.MessageLength != 1500 {
		t.Errorf("message_length = %d, want 1500", c.Display.MessageLength)
	}
	if c.Display.TurnImages != 4 {
		t.Errorf("turn_images = %d, want 4", c.Display.TurnImages)
	}
	// Drafts are off; turned on, they go at the fastest speed.
	if c.Display.Stream || c.Display.StreamInterval.Duration != MinStreamInterval {
		t.Errorf("stream %v every %v, want off, every %v", c.Display.Stream, c.Display.StreamInterval.Duration, MinStreamInterval)
	}
	// cux runs claude as its child, so a pane with cux in front is Claude Code.
	if got := strings.Join(c.Agents.ClaudeArgv0, " "); got != "claude cux" {
		t.Errorf("claude_argv0 = %q", got)
	}
	if got := c.Agents.ClaudeExecGlob[0]; got != "/h/.local/share/claude/versions/*" {
		t.Errorf("glob = %q", got)
	}
	if c.Display.Mode != "progress" || c.Display.Backfill != 3 || !c.Display.ShowMacPrompts {
		t.Errorf("display = %+v", c.Display)
	}
	if c.InputSettings.QueueExpiry.Duration != 30*time.Minute || c.InputSettings.BusyAfterTyping.Duration != 15*time.Second ||
		c.InputSettings.JoinWindow.Duration != 300*time.Millisecond || c.InputSettings.JoinWindowLong.Duration != 1500*time.Millisecond ||
		c.InputSettings.WhileBusy != "steer" || c.Display.ProgressAnimation.Duration != 3*time.Second || !c.Display.RichMessages ||
		c.Display.SummaryAfter.Duration != 2*time.Minute {
		t.Errorf("input = %+v", c.InputSettings)
	}
	if c.DBPath() != "/h/.local/state/rex-tg/rex-tg.db" || c.SpoolDir() != "/h/.local/state/rex-tg/spool" ||
		c.LogDir() != "/h/.local/state/rex-tg/logs" || c.LockPath() != "/h/.local/state/rex-tg/daemon.lock" ||
		!strings.HasPrefix(c.UploadDir(), c.State.Dir) {
		t.Errorf("paths: %s %s %s %s %s", c.DBPath(), c.SpoolDir(), c.LogDir(), c.LockPath(), c.UploadDir())
	}
	if c.Voice.WhisperCLI != "" {
		t.Error("voice should be off by default")
	}
	cl := c.Input(agents.Claude)
	if cl.PasteEnterDelay.Duration != 300*time.Millisecond || cl.WhileWorking != "steer" ||
		len(cl.InterruptKeys) != 1 || cl.InterruptKeys[0] != "escape" {
		t.Errorf("claude = %+v", cl)
	}
	want := []Answer{{"1 Yes", []string{"1"}}, {"2 Always", []string{"2"}}, {"4 No", []string{"4"}}, {"Esc", []string{"escape"}}}
	if len(cl.Answers) != len(want) {
		t.Fatalf("claude answers = %+v", cl.Answers)
	}
	for i, a := range want {
		if cl.Answers[i].Label != a.Label || cl.Answers[i].Keys[0] != a.Keys[0] {
			t.Errorf("answer %d = %+v", i, cl.Answers[i])
		}
	}
	if len(c.Input(agents.Pi).Answers) != 0 {
		t.Error("pi has no answers")
	}
	cx := c.Input(agents.Codex).Answers
	// No "always" answer: a guessed lasting rule must not be one tap away.
	if len(cx) != 2 || cx[0].Keys[0] != "y" || cx[1].Keys[0] != "escape" {
		t.Errorf("codex answers = %+v", cx)
	}
	if got := c.Input("nope"); got.WhileWorking != "" {
		t.Errorf("unknown runtime = %+v", got)
	}
	for rt, want := range map[agents.Runtime]string{agents.Pi: "pi", agents.Claude: "claude", agents.Codex: "codex"} {
		if l := c.Input(rt).Launch; len(l) != 1 || l[0] != want {
			t.Errorf("%s launch = %q", rt, l)
		}
	}
}

func TestLoadMissingGivesDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "none.toml"), "/h")
	if err != nil {
		t.Fatal(err)
	}
	if c.Display.Mode != "progress" {
		t.Errorf("mode = %q", c.Display.Mode)
	}
}

func TestLoad(t *testing.T) {
	p := write(t, `
[telegram]
rate_per_second = 2.5
rate_burst = 9

[rex]
cli = "~/bin/rex"
scan_interval = "10s"
new_session = "agents"
new_folders = ["~/work/app/", " /srv/site "]

[agents]
pi_argv0 = ["pi", "pi2"]
claude_exec_glob = ["~/vers/*"]

[agents.claude]
launch = ["claude", "--model", "opus"]
paste_enter_delay = "500ms"
while_working = "queue"
answers = [{label="1 Yes", keys=["1"]}]

[display]
mode = "final"
backfill = 5
show_mac_prompts = false
progress_delay = "3s"
send_files = [".PDF", " csv "]
stream = true
stream_interval = "2s"
message_length = 2000

[input]
queue_expiry = "1h"
max_upload_mb = 7

[voice]
whisper_cli = "~/w"
language = "en"

[state]
dir = "~/st"
`)
	c, err := Load(p, "/h")
	if err != nil {
		t.Fatal(err)
	}
	if c.Telegram.RatePerSecond != 2.5 || c.Telegram.RateBurst != 9 {
		t.Errorf("telegram = %+v", c.Telegram)
	}
	if c.Rex.CLI != "/h/bin/rex" || c.Rex.ScanInterval.Duration != 10*time.Second || c.Rex.NewSession != "agents" {
		t.Errorf("rex = %+v", c.Rex)
	}
	if strings.Join(c.Rex.NewFolders, ",") != "/h/work/app,/srv/site" {
		t.Errorf("new folders = %q", c.Rex.NewFolders)
	}
	if len(c.Agents.PiArgv0) != 2 || c.Agents.ClaudeExecGlob[0] != "/h/vers/*" {
		t.Errorf("agents = %+v", c.Agents)
	}
	if c.Agents.ClaudeArgv0[0] != "claude" {
		t.Error("untouched default lost")
	}
	cl := c.Input(agents.Claude)
	if cl.PasteEnterDelay.Duration != 500*time.Millisecond || cl.WhileWorking != "queue" || len(cl.Answers) != 1 {
		t.Errorf("claude = %+v", cl)
	}
	if cl.InterruptKeys[0] != "escape" {
		t.Error("claude interrupt default lost")
	}
	if strings.Join(cl.Launch, " ") != "claude --model opus" || c.Input(agents.Pi).Launch[0] != "pi" {
		t.Errorf("launch: claude %q, pi %q", cl.Launch, c.Input(agents.Pi).Launch)
	}
	if c.Input(agents.Codex).Answers[0].Keys[0] != "y" {
		t.Error("codex default lost")
	}
	if strings.Join(c.Display.SendFiles, ",") != "pdf,csv" || !c.Display.Stream || c.Display.StreamInterval.Duration != 2*time.Second || c.Display.MessageLength != 2000 {
		t.Errorf("send files %q, stream %v", c.Display.SendFiles, c.Display.Stream)
	}
	if c.Display.Mode != "final" || c.Display.Backfill != 5 || c.Display.ShowMacPrompts || c.Display.ProgressDelay.Duration != 3*time.Second {
		t.Errorf("display = %+v", c.Display)
	}
	if c.InputSettings.QueueExpiry.Duration != time.Hour || c.InputSettings.MaxUploadMB != 7 {
		t.Errorf("input = %+v", c.InputSettings)
	}
	if c.Voice.WhisperCLI != "/h/w" || c.Voice.Language != "en" {
		t.Errorf("voice = %+v", c.Voice)
	}
	if c.State.Dir != "/h/st" || c.DBPath() != "/h/st/rex-tg.db" {
		t.Errorf("state = %+v", c.State)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"unknown key", "[telegram]\nrate_per_sec = 1\n", "rate_per_sec"},
		{"unknown table", "[nope]\nx = 1\n", "nope"},
		{"bad duration", "[rex]\nscan_interval = \"soon\"\n", "duration"},
		{"negative duration", "[rex]\nscan_interval = \"-1s\"\n", "negative"},
		{"bad mode", "[display]\nmode = \"loud\"\n", "display.mode"},
		{"bad while working", "[agents.pi]\nwhile_working = \"x\"\n", "while_working"},
		{"bad while busy", "[input]\nwhile_busy = \"later\"\n", "input.while_busy"},
		{"animation too fast", "[display]\nprogress_animation = \"500ms\"\n", "display.progress_animation"},
		{"empty launch", "[agents.codex]\nlaunch = []\n", "agents.codex.launch"},
		{"blank launch", "[agents.pi]\nlaunch = [\" \"]\n", "agents.pi.launch"},
		{"syntax", "[rex\n", "parse"},
		{"relative state dir", "[state]\ndir = \"state\"\n", "absolute"},
		{"proxy scheme", "[telegram]\nproxy = \"ftp://p.example:21\"\n", "socks5h"},
		{"proxy no host", "[telegram]\nproxy = \"socks5://\"\n", "host"},
		{"fallback not a bool", "[telegram]\nfallback = \"yes\"\n", "fallback"},
		{"proxy not a url", "[telegram]\nproxy = \"nonsense\"\n", "telegram.proxy"},
		{"plain http api", "[telegram]\napi_base = \"http://api.telegram.org\"\n", "https"},
		{"empty new session", "[rex]\nnew_session = \" \"\n", "rex.new_session"},
		{"new session read as an option", "[rex]\nnew_session = \"-x\"\n", "rex.new_session"},
		{"new session with a newline", "[rex]\nnew_session = \"a\\nb\"\n", "rex.new_session"},
		{"relative new folder", "[rex]\nnew_folders = [\"work/app\"]\n", "rex.new_folders"},
		{"empty new folder", "[rex]\nnew_folders = [\"\"]\n", "rex.new_folders"},
		{"a path as a file type", "[display]\nsend_files = [\"a/pdf\"]\n", "display.send_files"},
		{"an empty file type", "[display]\nsend_files = [\"\"]\n", "display.send_files"},
		{"drafts too fast", "[display]\nstream_interval = \"500ms\"\n", "display.stream_interval"},
		{"messages too long", "[display]\nmessage_length = 5000\n", "display.message_length"},
		{"messages too short", "[display]\nmessage_length = 10\n", "display.message_length"},
		{"too many turn images", "[display]\nturn_images = 11\n", "display.turn_images"},
		{"negative turn images", "[display]\nturn_images = -1\n", "display.turn_images"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(write(t, tc.body), "/h")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestDurationText(t *testing.T) {
	var d Duration
	if err := d.UnmarshalText([]byte("1m30s")); err != nil || d.Duration != 90*time.Second {
		t.Fatalf("d = %v, err = %v", d, err)
	}
	b, _ := d.MarshalText()
	if string(b) != "1m30s" {
		t.Errorf("marshal = %s", b)
	}
	if err := d.UnmarshalText([]byte("x")); err == nil {
		t.Error("want error")
	}
}

func TestProxyConfig(t *testing.T) {
	c, err := Load(write(t, "[telegram]\n"), "/h")
	if err != nil || c.TelegramProxy() != nil {
		t.Fatalf("default: %v, %v", c.TelegramProxy(), err)
	}
	for _, p := range []string{"http://h:8080", "https://h", "socks5://u:" + "pw@127.0.0.1:1080", "socks5h://h:1080"} {
		c, err := Load(write(t, "[telegram]\nproxy = \""+p+"\"\n"), "/h")
		if err != nil || c.TelegramProxy() == nil || c.TelegramProxy().Hostname() == "" {
			t.Errorf("%s: %v", p, err)
		}
	}
	// The fake password is split, so secret scanners do not read a whole
	// URL with a password (AGENTS.md).
	_, err = Load(write(t, "[telegram]\nproxy = \"ftp://u:"+"secretpw@h\"\n"), "/h")
	if err == nil || strings.Contains(err.Error(), "secretpw") {
		t.Errorf("err = %v, must not hold the password", err)
	}
}

// Plain http is allowed only for a local test server.
func TestLocalHTTPAPIBaseIsAllowed(t *testing.T) {
	for _, base := range []string{"http://127.0.0.1:8080", "http://localhost:9000", "https://api.telegram.org"} {
		if _, err := Load(write(t, "[telegram]\napi_base = \""+base+"\"\n"), "/h"); err != nil {
			t.Errorf("%s: %v", base, err)
		}
	}
}

func TestChoices(t *testing.T) {
	c, err := Load(write(t, "[agents.claude.choices]\nmodel = [\"opus\", \"claude-x\"]\n"), "/h")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Input(agents.Claude).Choices["model"]; len(got) != 2 || got[1] != "claude-x" {
		t.Errorf("choices = %v", got)
	}
	if got := c.Input(agents.Pi).Choices["model"]; len(got) != 0 {
		t.Errorf("default choices = %v", got)
	}
	if _, err := Load(write(t, "[agents.codex.choices]\nmodel = [\"a b\"]\n"), "/h"); err == nil || !strings.Contains(err.Error(), "agents.codex.choices.model") {
		t.Errorf("a bad value gave %v", err)
	}
}

func TestFallbackConfig(t *testing.T) {
	c, err := Load(write(t, ""), "/h")
	if err != nil || !c.Telegram.Fallback {
		t.Fatalf("default: %v, %v", c.Telegram.Fallback, err)
	}
	c, err = Load(write(t, "[telegram]\nfallback = false\n"), "/h")
	if err != nil || c.Telegram.Fallback {
		t.Fatalf("off: %v, %v", c.Telegram.Fallback, err)
	}
}
