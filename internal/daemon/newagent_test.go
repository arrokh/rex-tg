package daemon

import (
	"strings"
	"testing"

	"rex-tg/internal/agents"
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
	cases := []struct {
		in          string
		rt          agents.Runtime
		folder, bad string
	}{
		{in: ""},
		{in: "pi", rt: agents.Pi},
		{in: "Claude ~/work/app", rt: agents.Claude, folder: "~/work/app"},
		{in: "codex   ~/My Projects/x ", rt: agents.Codex, folder: "~/My Projects/x"},
		{in: "claude\n~/work/app", rt: agents.Claude, folder: "~/work/app"},
		{in: "pi\t~/x", rt: agents.Pi, folder: "~/x"},
		{in: "vim ~/x", bad: "vim"},
	}
	for _, c := range cases {
		rt, folder, bad := parseNewArgs(c.in)
		if rt != c.rt || folder != c.folder || bad != c.bad {
			t.Errorf("parseNewArgs(%q) = %q, %q, %q; want %q, %q, %q", c.in, rt, folder, bad, c.rt, c.folder, c.bad)
		}
	}
}

// The session of a folder is the one named after it. A name that rex would
// read as an option loses its dashes.
func TestSessionForFolder(t *testing.T) {
	for _, c := range []struct {
		dir  string
		want string
	}{
		{"/Users/me/work/rex-tg", "rex-tg"},
		{"/Users/me/My App", "My App"},
		{"/Users/me/--x", "x"},
		{"/Users/me/- x", "x"},
		{"/", "pi"},
		{"/Users/me/a\tb\x7f", "ab"}, // Rex refuses control characters
		{"/Users/me/" + strings.Repeat("é", 130), strings.Repeat("é", 128)}, // and more than 128 characters
	} {
		if got := sessionLabel(c.dir, agents.Pi); got != c.want {
			t.Errorf("sessionLabel(%q) = %q, want %q", c.dir, got, c.want)
		}
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
	text, kb := newQuestion("tok", newFlow{step: newPickAgent, linked: true}, func(s string) string { return s })
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
	if text, _ := newQuestion("tok", newFlow{step: newPickAgent}, nil); strings.Contains(text, "//new") {
		t.Errorf("agent question outside a linked topic: %q", text)
	}

	long := "/Users/me/" + strings.Repeat("deep/", 60) + "app"
	text, kb = newQuestion("tok", newFlow{step: newPickFolder, rt: agents.Claude, folders: []string{"/Users/me/a", long}},
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
	if text, kb := newQuestion("tok", newFlow{step: newPickFolder, rt: agents.Pi}, nil); strings.Contains(text, "Tap") || len(kb.InlineKeyboard) != 1 {
		t.Errorf("folder question without folders: %q %+v", text, kb.InlineKeyboard)
	}

	// The longest callback data, with a real token.
	_, kb = newQuestion(newToken(), newFlow{step: newPickAgent}, nil)
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
