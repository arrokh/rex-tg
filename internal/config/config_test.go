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
	if c.Rex.ScanInterval.Duration != 30*time.Second {
		t.Errorf("scan = %v", c.Rex.ScanInterval)
	}
	if got := c.Agents.ClaudeExecGlob[0]; got != "/h/.local/share/claude/versions/*" {
		t.Errorf("glob = %q", got)
	}
	if c.Display.Mode != "progress" || c.Display.Backfill != 3 || !c.Display.ShowMacPrompts {
		t.Errorf("display = %+v", c.Display)
	}
	if c.InputSettings.QueueExpiry.Duration != 30*time.Minute || c.InputSettings.BusyAfterTyping.Duration != 15*time.Second ||
		c.InputSettings.JoinWindow.Duration != 300*time.Millisecond || c.InputSettings.JoinWindowLong.Duration != 1500*time.Millisecond {
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
	if len(cx) != 3 || cx[0].Keys[0] != "y" || cx[1].Keys[0] != "p" || cx[2].Keys[0] != "escape" {
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
	if c.Rex.CLI != "/h/bin/rex" || c.Rex.ScanInterval.Duration != 10*time.Second {
		t.Errorf("rex = %+v", c.Rex)
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
		{"empty launch", "[agents.codex]\nlaunch = []\n", "agents.codex.launch"},
		{"blank launch", "[agents.pi]\nlaunch = [\" \"]\n", "agents.pi.launch"},
		{"syntax", "[rex\n", "parse"},
		{"relative state dir", "[state]\ndir = \"state\"\n", "absolute"},
		{"plain http api", "[telegram]\napi_base = \"http://api.telegram.org\"\n", "https"},
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

// Plain http is allowed only for a local test server.
func TestLocalHTTPAPIBaseIsAllowed(t *testing.T) {
	for _, base := range []string{"http://127.0.0.1:8080", "http://localhost:9000", "https://api.telegram.org"} {
		if _, err := Load(write(t, "[telegram]\napi_base = \""+base+"\"\n"), "/h"); err != nil {
			t.Errorf("%s: %v", base, err)
		}
	}
}
