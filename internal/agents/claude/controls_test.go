package claude_test

import (
	"reflect"
	"testing"

	"rex-tg/internal/agents"
	"rex-tg/internal/agents/claude"
)

func ctl(t *testing.T) agents.Controller {
	t.Helper()
	c, ok := claude.New().(agents.Controller)
	if !ok {
		t.Fatal("the Claude adapter is not a Controller")
	}
	return c
}

func TestControls(t *testing.T) {
	m, ok := agents.FindControl(ctl(t).Controls(), "model")
	if !ok || !m.Free || len(m.Choices) == 0 {
		t.Errorf("model = %+v", m)
	}
	e, ok := agents.FindControl(ctl(t).Controls(), "effort")
	if !ok || e.Free || !reflect.DeepEqual(e.Choices, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Errorf("effort = %+v", e)
	}
}

func TestCommand(t *testing.T) {
	for _, tc := range []struct{ control, value, want string }{
		{"model", "opus", "/model opus"},
		{"model", "Sonnet", "/model sonnet"},
		{"model", "claude-opus-5-5", "/model claude-opus-5-5"},
		{"effort", "HIGH", "/effort high"},
		{"effort", "ultra", ""},
		{"model", "a b", ""},
		{"fast", "on", ""},
	} {
		got, err := ctl(t).Command(tc.control, tc.value, nil)
		if (err != nil) != (tc.want == "") || got != tc.want {
			t.Errorf("Command(%q, %q) = %q, %v; want %q", tc.control, tc.value, got, err, tc.want)
		}
	}
}

func TestSeen(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		want       map[string]string
	}{
		{"assistant", `{"type":"assistant","effort":"high","message":{"model":"claude-opus-5-5","role":"assistant"}}`,
			map[string]string{"model": "claude-opus-5-5", "effort": "high"}},
		{"model only", `{"type":"assistant","message":{"model":"claude-x"}}`, map[string]string{"model": "claude-x"}},
		{"synthetic", `{"type":"assistant","message":{"model":"<synthetic>"}}`, map[string]string{}},
		{"sidechain", `{"type":"assistant","isSidechain":true,"effort":"low","message":{"model":"m"}}`, nil},
		{"user line", `{"type":"user","message":{"model":"m"}}`, nil},
		{"effort is not text", `{"type":"assistant","effort":3,"message":{"model":"m"}}`, map[string]string{"model": "m"}},
		{"no field", `{"type":"assistant","message":{"role":"assistant"}}`, nil},
		{"bad json", `{"model":`, nil},
	} {
		got := ctl(t).Seen([]byte(tc.line))
		if len(got) == 0 && len(tc.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: Seen = %v, want %v", tc.name, got, tc.want)
		}
	}
}
