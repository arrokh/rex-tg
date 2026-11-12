package agents_test

import (
	"strings"
	"testing"

	"rex-tg/internal/agents"
)

func TestCheckValue(t *testing.T) {
	levels := agents.Control{Name: "effort", Title: "Thinking effort", Choices: []string{"low", "high"}}
	model := agents.Control{Name: "model", Title: "Model", Choices: []string{"opus"}, Free: true}
	cases := []struct {
		c    agents.Control
		in   string
		want string // "" means an error
	}{
		{levels, "high", "high"},
		{levels, " HIGH ", "high"},
		{levels, "max", ""},
		{levels, "", ""},
		{model, "Opus", "opus"},
		{model, "claude-opus-5-5", "claude-opus-5-5"},
		{model, "anthropic/claude-x:1", "anthropic/claude-x:1"},
		{model, "opus[1m]", "opus[1m]"},
		{model, "two words", ""},
		{model, "a\nb", ""},
		{model, "-x", ""},
		{model, "x;rm", ""},
		{model, "$(id)", ""},
		{model, strings.Repeat("a", 81), ""},
		{model, strings.Repeat("a", 80), strings.Repeat("a", 80)},
	}
	for _, tc := range cases {
		got, err := tc.c.CheckValue(tc.in)
		if tc.want == "" {
			if err == nil {
				t.Errorf("CheckValue(%q) = %q, want an error", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("CheckValue(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestMergeSeen(t *testing.T) {
	cur := map[string]string{"model": "a"}
	if !agents.MergeSeen(cur, map[string]string{"model": "b", "effort": ""}) || cur["model"] != "b" {
		t.Errorf("cur = %v", cur)
	}
	if _, ok := cur["effort"]; ok {
		t.Error("an empty value was stored")
	}
	if agents.MergeSeen(cur, map[string]string{"model": "b"}) {
		t.Error("the same value counted as a change")
	}
}
