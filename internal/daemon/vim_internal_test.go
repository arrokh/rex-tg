package daemon

import (
	"strings"
	"testing"
)

// The pi-vim screen below is shaped like a real capture of a pi pane: the
// input box's bottom border carries the mode, and status lines follow it.
const piVimScreen = ` Operation aborted                                                     ┃

────────────────────────────────────────────────────────────────────────

──────────────────────────────────────────────────────────────── %s
~/work/project (main) • [project][fix] Update the plan
↑131k ↓20k R726k $0.030 (sub) 31.1%%/272k (auto)         model • max
herdr 0.9.5`

func piVim(label string) string { return strings.Replace(piVimScreen, "%s", label, 1) }

func TestPiVimState(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   vimState
	}{
		{"normal", piVim("NORMAL "), vimState{vimNormal, "NORMAL"}},
		{"insert", piVim("INSERT "), vimState{vimInsert, "INSERT"}},
		{"pending command", piVim("NORMAL 3d_ "), vimState{vimBusy, "NORMAL 3d_"}},
		{"pending replace", piVim("NORMAL r_ "), vimState{vimBusy, "NORMAL r_"}},
		{"ex line", piVim("EX w_ "), vimState{vimBusy, "EX w_"}},
		{"visual", piVim("VISUAL "), vimState{vimVisual, "VISUAL"}},
		{"visual with a count", piVim("VISUAL 3_ "), vimState{vimVisual, "VISUAL 3_"}},
		{"visual line", piVim("V-LINE "), vimState{vimVisual, "V-LINE"}},
		{"a label cut to fit", piVim("NORMAL …d2w_"), vimState{vimBusy, "NORMAL …d2w_"}},
		{"no vim editor", "╭─────╮\n│ > hello │\n╰─────╯\n  ? for shortcuts\n", vimState{}},
		{"a Claude Code label is not read", "╭─────╮\n│ >   │\n╰─────╯\n  -- INSERT --\n", vimState{}},
		{"the word in output is not a label", "The NORMAL mode is described here.\n> \n", vimState{}},
		{"a border without a label", "────────────────\n> \n", vimState{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := piVimState(c.screen); got != c.want {
				t.Fatalf("piVimState = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestVimLabelFarUpIsIgnored(t *testing.T) {
	screen := piVim("NORMAL") + strings.Repeat("\nlater output", vimLines)
	if got := piVimState(screen); got != (vimState{}) {
		t.Fatalf("piVimState = %+v, want none for a label above the last %d lines", got, vimLines)
	}
}

// Each mode has one next key, and an Escape is never chosen in plain NORMAL
// mode, where it would reach pi and stop the agent.
func TestNextVimKey(t *testing.T) {
	cases := []struct {
		mode, want vimMode
		key        string
	}{
		{vimNone, vimInsert, ""},
		{vimInsert, vimInsert, ""},
		{vimNormal, vimInsert, "i"},
		{vimVisual, vimInsert, "escape"},
		{vimBusy, vimInsert, "escape"},
		{vimNone, vimNormal, ""},
		{vimNormal, vimNormal, ""},
		{vimInsert, vimNormal, "escape"},
		{vimVisual, vimNormal, "escape"},
		{vimBusy, vimNormal, "escape"},
	}
	for _, c := range cases {
		if got := nextVimKey(c.mode, c.want); got != c.key {
			t.Errorf("nextVimKey(%d, %d) = %q, want %q", c.mode, c.want, got, c.key)
		}
	}
}

func TestTopicName(t *testing.T) {
	const title = "[resume.md][fix] Update the likely follow ups in the remote people study plan"
	cases := []struct {
		name, pane, cwd, title, want string
	}{
		{"the pane's own name first", "rhasia-scret migrate api to go", "/w/infra", title, "rhasia-scret migrate api to go"},
		{"a default pane name is skipped", "shell", "/w/resume.md", title, "[resume.md][fix] Update the…"},
		{"then the folder", "shell", "/w/resume.md", "", "resume.md"},
		{"then the runtime", "", "", "", "pi"},
		{"spaces are folded", "  my   pane ", "", "", "my pane"},
		{"a long pane name is cut", "a very long pane name that keeps going and going", "", "", "a very long pane name that…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := topicName(c.pane, "pi", c.cwd, c.title)
			if got != c.want {
				t.Fatalf("topicName = %q, want %q", got, c.want)
			}
			if n := len([]rune(got)); n > maxTopicName {
				t.Fatalf("topicName has %d characters, more than %d", n, maxTopicName)
			}
		})
	}
}

func TestShortNameCutsAWordlessName(t *testing.T) {
	s := strings.Repeat("x", 40)
	if got := shortName(s, 10); got != strings.Repeat("x", 9)+"…" {
		t.Fatalf("shortName = %q", got)
	}
}

func TestNoAutoLink(t *testing.T) {
	cases := map[string]string{
		"resume.md":                  "resume.\u200bmd",
		"[resume.md][fix] Update it": "[resume.\u200bmd][fix] Update it",
		"version 1.2 and a.b":        "version 1.2 and a.\u200bb",
		"end of sentence. Next":      "end of sentence. Next",
		"no dots":                    "no dots",
		"…/rhasia-scret/.piwt/infra": "…/rhasia-scret/.piwt/infra",
	}
	for in, want := range cases {
		if got := noAutoLink(in); got != want {
			t.Errorf("noAutoLink(%q) = %q, want %q", in, got, want)
		}
	}
}
