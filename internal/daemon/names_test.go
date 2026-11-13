package daemon

import (
	"strings"
	"testing"

	"rex-tg/internal/agents"
	"rex-tg/internal/rex"
)

func TestUniqueTabName(t *testing.T) {
	cases := []struct {
		name   string
		others []string
		want   string
	}{
		{"api", nil, "api"},
		{"api", []string{"web"}, "api"},
		{"api", []string{"api"}, "api #2"},
		{"api", []string{"api", "api #2"}, "api #3"},
		{"api", []string{"api #2"}, "api"},
	}
	for _, c := range cases {
		if got := uniqueTabName(c.name, c.others); got != c.want {
			t.Errorf("uniqueTabName(%q, %q) = %q, want %q", c.name, c.others, got, c.want)
		}
	}
	long := strings.Repeat("x", maxLabel)
	if got := uniqueTabName(long, []string{long}); len([]rune(got)) > maxLabel || !strings.HasSuffix(got, " #2") {
		t.Errorf("a long name got %q", got)
	}
}

func TestTabName(t *testing.T) {
	cwd := "/work/app"
	cases := []struct {
		desc, pane, title, current, want string
	}{
		{"pane name wins", "api work", "", "app · pi", "api work"},
		{"session name", "shell", "Fix login", "app · pi", "Fix login"},
		{"folder fallback sets the agent form", "shell", "", "cux", "app · pi"},
		{"folder fallback keeps another agent form", "shell", "", "app · cux", "app · cux"},
		{"folder fallback of another folder", "shell", "", "web · pi", "app · pi"},
	}
	for _, c := range cases {
		if got := tabName(c.pane, agents.Pi, cwd, c.title, c.current); got != c.want {
			t.Errorf("%s: tabName = %q, want %q", c.desc, got, c.want)
		}
	}
	if got := tabName("shell", agents.Pi, "", "", "x"); got != "pi" {
		t.Errorf("no folder: %q, want the agent alone", got)
	}
}

func TestOfflineTabName(t *testing.T) {
	if got := offlineTabName("app · pi"); got != "🔴 app · pi" {
		t.Errorf("got %q", got)
	}
	if got := offlineTabName("🔴 app · pi"); got != "🔴 app · pi" {
		t.Errorf("the mark was added twice: %q", got)
	}
}

func TestTabNameDropsAnOldSuffix(t *testing.T) {
	// The twin tab is gone: the " #2" goes, and uniqueTabName adds one back
	// only when another tab still has the name.
	got := tabName("", agents.Pi, "/work/app", "", "app · pi #2")
	if got != "app · pi" {
		t.Errorf("tabName = %q, want the suffix dropped", got)
	}
	if u := uniqueTabName(got, []string{"app · pi"}); u != "app · pi #2" {
		t.Errorf("uniqueTabName = %q, want #2 while the twin is there", u)
	}
}

func TestConfigChoicesKeepOnlyValuesTheAdapterAccepts(t *testing.T) {
	fixed := agents.Control{Name: "effort", Title: "Effort", Choices: []string{"low", "high"}}
	if got := configChoices(fixed, []string{"HIGH", "ultra"}); len(got) != 1 || got[0] != "high" {
		t.Errorf("configChoices = %v, want [high]", got)
	}
	if got := configChoices(fixed, []string{"ultra"}); len(got) != 2 {
		t.Errorf("configChoices = %v, want the adapter's list when none is valid", got)
	}
	free := agents.Control{Name: "model", Title: "Model", Free: true}
	if got := configChoices(free, []string{"any-model"}); len(got) != 1 {
		t.Errorf("configChoices = %v for a free control", got)
	}
}

func TestTabOwnerIsTheFirstAgentOfTheTab(t *testing.T) {
	blocks := []rex.Block{{ID: "a", WindowID: "W1"}, {ID: "b", WindowID: "W1"}, {ID: "c", WindowID: "W2"}}
	agent := map[string]bool{"a": true, "b": true, "c": true}
	has := func(b string) bool { return agent[b] }
	if got := tabOwner(blocks, "W1", "b", has); got != "a" {
		t.Errorf("owner = %q, want a: two agents in one tab must not both rename it", got)
	}
	if got := tabOwner(blocks, "W2", "c", has); got != "c" {
		t.Errorf("owner = %q, want c", got)
	}
	agent["a"] = false // a stopped: b names the tab now
	if got := tabOwner(blocks, "W1", "a", has); got != "b" {
		t.Errorf("owner = %q, want b: a stopped agent must not mark a tab another agent uses", got)
	}
	agent["b"] = false // nobody runs: the stopped agent marks its own tab
	if got := tabOwner(blocks, "W1", "a", has); got != "a" {
		t.Errorf("owner = %q, want a", got)
	}
}

// A topic's icon is the first character of its name, so a name starts with
// a letter or digit: pi's "[folder]" tag goes when it repeats the folder and
// moves to the end otherwise; Markdown marks and leading symbols go.
func TestTopicNameStartsWithALetter(t *testing.T) {
	cases := []struct{ cwd, title, want string }{
		{"/work/resume.md", "[resume.md] Resume md Practice", "Resume md Practice"},
		{"/work/app", "[WIP] Fix login", "Fix login [WIP]"},
		{"/work/app", "**Fix** the `login` page", "Fix the login page"},
		{"/work/app", "— 🚀 Launch plan", "Launch plan"},
		{"/work/app", "2nd try", "2nd try"},
		{"/work/.dotfiles", "", "dotfiles"},
		{"/work/app", "[app]", "app"},
		{"/work/app", "***", "app"},
	}
	for _, c := range cases {
		if got := topicName("shell", agents.Pi, c.cwd, c.title); got != c.want {
			t.Errorf("topicName(%q, %q) = %q, want %q", c.cwd, c.title, got, c.want)
		}
	}
}
