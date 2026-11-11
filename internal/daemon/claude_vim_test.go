package daemon

import (
	"strings"
	"testing"
)

const claudeBox = "╭──────────────────────────────╮\n│ > hello                      │\n╰──────────────────────────────╯\n"

func TestClaudeVisual(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   bool
	}{
		{"visual below the box", "answer\n" + claudeBox + "  -- VISUAL --\n", true},
		{"visual line", "answer\n" + claudeBox + "  -- VISUAL LINE --\n", true},
		{"visual with other hints", "answer\n" + claudeBox + "  -- VISUAL --  ? for shortcuts\n", true},
		{"insert", "answer\n" + claudeBox + "  -- INSERT --\n", false},
		{"normal has no label", "answer\n" + claudeBox + "  ? for shortcuts\n", false},
		{"the same text in output above the box", "the footer says -- VISUAL --\n-- VISUAL --\n" + claudeBox + "  ? for shortcuts\n", false},
		{"an older box does not count", "╭──╮\n╰──╯\n  -- VISUAL --\n" + claudeBox + "  -- INSERT --\n", false},
		{"no box at all", "-- VISUAL --\n", false},
		{"empty", "", false},
		{"text that only contains the words", "see " + strings.Repeat("x", 3) + " -- VISUAL --x\n" + claudeBox, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := claudeVisual(c.screen); got != c.want {
				t.Errorf("claudeVisual = %v, want %v", got, c.want)
			}
		})
	}
}
