package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rex-tg/internal/agents"
	"rex-tg/internal/config"
	"rex-tg/internal/rex"
)

func TestFolderPath(t *testing.T) {
	const home = "/Users/me"
	ok := []struct{ in, want string }{
		{"~", "/Users/me"},
		{"~/work/app", "/Users/me/work/app"},
		{"  ~/work/app/  ", "/Users/me/work/app"},
		{"/opt/src/x", "/opt/src/x"},
		{"work/app", "/Users/me/work/app"}, // relative: from the home folder
		{"\"~/My Projects/x\"", "/Users/me/My Projects/x"},
		{"“~/work/app”", "/Users/me/work/app"}, // a phone's curly quotes
		{"'/opt/a b'", "/opt/a b"},
		{"~/work/../work/app", "/Users/me/work/app"},
	}
	for _, c := range ok {
		got, err := folderPath(c.in, home)
		if err != nil || got != c.want {
			t.Errorf("folderPath(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"", "  ", `""`, "~other/x", "/a\n/b"} {
		if got, err := folderPath(in, home); err == nil {
			t.Errorf("folderPath(%q) = %q, want an error", in, got)
		}
	}
}

// In a linked topic, only a path answers the folder question. Agent
// commands still reach the agent.
func TestLinkedFolderText(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	for text, want := range map[string]bool{
		"~/work/app": true, // even when it does not exist: the answer says so
		"~":          true,
		dir:          true,
		"/compact":   false,
		"/nowhere/x": false,
		"//new":      false,
		"hello":      false,
		"work/app":   false,
	} {
		if got := linkedFolderText(text, home); got != want {
			t.Errorf("linkedFolderText(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestParseNewArgs(t *testing.T) {
	list := testAgents(true)
	cases := []struct {
		in               string
		key, folder, bad string
	}{
		{in: ""},
		{in: "pi", key: "pi"},
		{in: "Claude ~/work/app", key: "claude", folder: "~/work/app"},
		{in: "codex   ~/My Projects/x ", key: "codex", folder: "~/My Projects/x"},
		{in: "claude\n~/work/app", key: "claude", folder: "~/work/app"},
		{in: "pi\t~/x", key: "pi", folder: "~/x"},
		{in: "CUX ~/x", key: "cux", folder: "~/x"},
		{in: "vim ~/x", bad: "vim"},
	}
	for _, c := range cases {
		a, ok, folder, bad := parseNewArgs(c.in, list)
		if a.key != c.key || ok != (c.key != "") || folder != c.folder || bad != c.bad {
			t.Errorf("parseNewArgs(%q) = %q, %v, %q, %q; want %q, %q, %q", c.in, a.key, ok, folder, bad, c.key, c.folder, c.bad)
		}
	}
	// Without cux installed, /new cux names no agent.
	if _, ok, _, bad := parseNewArgs("cux ~/x", testAgents(false)); ok || bad != "cux" {
		t.Errorf("cux without cux: %v, %q", ok, bad)
	}
}

// testAgents is the list of /new agents, with or without cux.
func testAgents(cux bool) []newAgent {
	d := &daemon{cfg: config.Default("/Users/me"), hasProgram: func(name string) bool { return cux && name == "cux" }}
	return d.newAgents()
}

// cux runs Claude Code, so /new offers it only when it is installed, and
// its pane is a Claude Code pane.
func TestNewAgentsOfferCuxWhenInstalled(t *testing.T) {
	if got := agentKeys(testAgents(false)); got != "pi|claude|codex" {
		t.Errorf("without cux: %s", got)
	}
	list := testAgents(true)
	if got := agentKeys(list); got != "pi|claude|cux|codex" {
		t.Errorf("with cux: %s", got)
	}
	cux, _ := findAgent(list, "cux")
	if cux.rt != agents.Claude || strings.Join(cux.command, " ") != "cux" || cux.name() != "cux" {
		t.Errorf("cux: %+v, named %q", cux, cux.name())
	}
	if claude, _ := findAgent(list, "claude"); claude.name() != "Claude Code" || strings.Join(claude.command, " ") != "claude" {
		t.Errorf("claude: %+v", claude)
	}

	// installed looks on the PATH and in the usual folders of user programs,
	// such as ~/.local/bin, where launchd's short PATH does not look.
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	const prog = "rex-tg-test-no-such-program"
	if installed(home, prog) {
		t.Fatal("found a program that does not exist")
	}
	if err := os.WriteFile(filepath.Join(bin, prog), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if installed(home, prog) {
		t.Error("a file that cannot run counts as a program")
	}
	if err := os.Chmod(filepath.Join(bin, prog), 0o755); err != nil {
		t.Fatal(err)
	}
	if !installed(home, prog) {
		t.Error("a program in ~/.local/bin is not found")
	}
}

// A new agent's tab is named after its folder and the agent. A name that
// rex would read as an option loses its dashes.
func TestTabLabel(t *testing.T) {
	for _, c := range []struct {
		dir, agent string
		want       string
	}{
		{"/Users/me/work/rex-tg", "pi", "rex-tg · pi"},
		{"/Users/me/My App", "cux", "My App · cux"},
		{"/Users/me/--x", "claude", "x · claude"},
		{"/Users/me/- x", "pi", "x · pi"},
		{"/", "pi", "pi"},
		{"/Users/me/a\tb\x7f", "pi", "ab · pi"}, // Rex refuses control characters
		{"/Users/me/" + strings.Repeat("é", 130), "codex", strings.Repeat("é", 120) + " · codex"}, // and more than 128 characters
	} {
		got := tabLabel(c.dir, c.agent)
		if got != c.want || len([]rune(got)) > maxLabel {
			t.Errorf("tabLabel(%q) = %q, want %q", c.dir, got, c.want)
		}
	}
	// A topic name becomes a tab name the same way.
	if got := cleanLabel(" -- [app] fix\nthe bug "); got != "[app] fixthe bug" {
		t.Errorf("cleanLabel = %q", got)
	}
	sessions := []rex.Session{{ID: "s1", Label: "T3 Code"}, {ID: "s2", Label: " Rex-TG "}, {ID: "s3", Label: "rex-tg"}}
	if s, ok := findSession(sessions, "rex-tg"); !ok || s.ID != "s2" {
		t.Errorf("findSession = %+v, %v; want the first match, s2", s, ok)
	}
	if _, ok := findSession(sessions, "rex"); ok {
		t.Error("findSession matched a part of a name")
	}
}

// A linked topic sends /new to the bot, so the question says how to reach
// the agent's own /new. Buttons carry the request's token and an index,
// never a path: callback data holds at most 64 bytes.
func TestNewQuestion(t *testing.T) {
	three := testAgents(false)
	text, kb := newQuestion("tok", newFlow{step: newPickAgent, linked: true, offered: three}, func(s string) string { return s })
	if !strings.Contains(text, "//new") {
		t.Errorf("agent question in a linked topic: %q", text)
	}
	var data []string
	for _, b := range kb.InlineKeyboard[0] {
		data = append(data, b.CallbackData)
	}
	if strings.Join(data, " ") != "nw:tok:a:pi nw:tok:a:claude nw:tok:a:codex" || kb.InlineKeyboard[1][0].CallbackData != "nw:tok:x" {
		t.Errorf("agent buttons: %+v", kb.InlineKeyboard)
	}
	if text, _ := newQuestion("tok", newFlow{step: newPickAgent, offered: three}, nil); strings.Contains(text, "//new") {
		t.Errorf("agent question outside a linked topic: %q", text)
	}
	// With cux, four buttons go two by two.
	_, kb = newQuestion("tok", newFlow{step: newPickAgent, offered: testAgents(true)}, nil)
	if len(kb.InlineKeyboard) != 3 || kb.InlineKeyboard[1][0].Text != "cux" || kb.InlineKeyboard[1][0].CallbackData != "nw:tok:a:cux" ||
		kb.InlineKeyboard[1][1].Text != "Codex" || kb.InlineKeyboard[2][0].CallbackData != "nw:tok:x" {
		t.Errorf("agent buttons with cux: %+v", kb.InlineKeyboard)
	}

	long := "/Users/me/" + strings.Repeat("deep/", 60) + "app"
	claude, _ := findAgent(three, "claude")
	text, kb = newQuestion("tok", newFlow{step: newPickFolder, agent: claude, folders: []string{"/Users/me/a", long}},
		func(s string) string { return strings.Replace(s, "/Users/me", "~", 1) })
	if !strings.Contains(text, "New Claude Code") || !strings.Contains(text, "Tap one") {
		t.Errorf("folder question: %q", text)
	}
	if len(kb.InlineKeyboard) != 3 || kb.InlineKeyboard[0][0].Text != "~/a" || kb.InlineKeyboard[1][0].CallbackData != "nw:tok:d:1" {
		t.Errorf("folder buttons: %+v", kb.InlineKeyboard)
	}
	if n := len([]rune(kb.InlineKeyboard[1][0].Text)); n > maxShownPath {
		t.Errorf("a long folder's button has %d characters", n)
	}
	pi, _ := findAgent(three, "pi")
	if text, kb := newQuestion("tok", newFlow{step: newPickFolder, agent: pi}, nil); strings.Contains(text, "Tap") || len(kb.InlineKeyboard) != 1 {
		t.Errorf("folder question without folders: %q %+v", text, kb.InlineKeyboard)
	}

	// The longest callback data, with a real token.
	_, kb = newQuestion(newToken(), newFlow{step: newPickAgent, offered: testAgents(true)}, nil)
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if len(b.CallbackData) > 64 {
				t.Errorf("callback data %q has %d bytes", b.CallbackData, len(b.CallbackData))
			}
		}
	}
	if a, b := newToken(), newToken(); a == b || len(a) != 12 {
		t.Errorf("tokens %q and %q", a, b)
	}
}
