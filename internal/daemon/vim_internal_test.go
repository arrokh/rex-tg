package daemon

import (
	"strings"
	"testing"
)

// The pi-vim screen below is shaped like a real capture of a pi pane: the
// input box border carries the mode, and status lines follow it.
const piVimScreen = ` Operation aborted                                                     ┃

────────────────────────────────────────────────────────────────────────

──────────────────────────────────────────────────────────────── %s
~/work/project (main) • [project][fix] Update the plan
↑131k ↓20k R726k $0.030 (sub) 31.1%%/272k (auto)         model • max
herdr 0.9.5`

func piVim(label string) string { return strings.Replace(piVimScreen, "%s", label, 1) }

func TestPiVimMode(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   vimMode
	}{
		{"pi-vim normal", piVim("NORMAL"), vimNormal},
		{"pi-vim insert", piVim("INSERT"), vimInsert},
		{"pi-vim pending command", piVim("NORMAL 3d_"), vimBusy},
		{"pi-vim ex", piVim("EX :w"), vimBusy},
		{"pi-vim visual", piVim("VISUAL"), vimVisual},
		{"pi-vim visual line", piVim("V-LINE"), vimVisual},
		{"no vim editor", "╭─────╮\n│ > hello │\n╰─────╯\n  ? for shortcuts\n", vimNone},
		{"a Claude Code label is not read", "╭─────╮\n│ >   │\n╰─────╯\n  -- INSERT --\n", vimNone},
		{"the word in output is not a label", "The NORMAL mode is described here.\n> \n", vimNone},
		{"a border without a label", "────────────────\n> \n", vimNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := piVimMode(c.screen); got != c.want {
				t.Fatalf("piVimMode = %d, want %d", got, c.want)
			}
		})
	}
}

func TestVimLabelFarUpIsIgnored(t *testing.T) {
	screen := piVim("NORMAL") + strings.Repeat("\nlater output", vimLines)
	if got := piVimMode(screen); got != vimNone {
		t.Fatalf("piVimMode = %d, want vimNone for a label above the last %d lines", got, vimLines)
	}
}

func TestEnterInsertKeys(t *testing.T) {
	cases := []struct {
		mode vimMode
		keys string
		ok   bool
	}{
		{vimNone, "", true},
		{vimInsert, "", true},
		{vimNormal, "i", true},
		{vimVisual, "escape i", true},
		{vimBusy, "", false},
	}
	for _, c := range cases {
		keys, ok := enterInsertKeys(c.mode)
		if strings.Join(keys, " ") != c.keys || ok != c.ok {
			t.Errorf("enterInsertKeys(%d) = %q, %v; want %q, %v", c.mode, keys, ok, c.keys, c.ok)
		}
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
