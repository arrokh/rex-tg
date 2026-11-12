package pi_test

import (
	"reflect"
	"testing"

	"rex-tg/internal/agents"
	"rex-tg/internal/agents/pi"
)

func ctl(t *testing.T) agents.Controller {
	t.Helper()
	c, ok := pi.New().(agents.Controller)
	if !ok {
		t.Fatal("the pi adapter is not a Controller")
	}
	return c
}

func TestControls(t *testing.T) {
	m, ok := agents.FindControl(ctl(t).Controls(), "model")
	if !ok || !m.Free || len(m.Choices) != 0 {
		t.Errorf("model = %+v", m)
	}
	e, ok := agents.FindControl(ctl(t).Controls(), "effort")
	if !ok || e.Free || e.Choices[0] != "off" || e.Choices[len(e.Choices)-1] != "max" {
		t.Errorf("effort = %+v", e)
	}
}

func TestCommand(t *testing.T) {
	for _, tc := range []struct{ control, value, want string }{
		{"model", "anthropic/claude-x", "/model anthropic/claude-x"},
		{"model", "gpt-test", "/model gpt-test"},
		{"effort", "Minimal", "/thinking minimal"},
		{"effort", "off", "/thinking off"},
		{"effort", "extreme", ""},
		{"model", "a b", ""},
		{"model", "x\ny", ""},
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
		{"model_change", `{"type":"model_change","id":"a","provider":"anthropic","modelId":"claude-x"}`,
			map[string]string{"model": "anthropic/claude-x"}},
		{"model_change without provider", `{"type":"model_change","modelId":"claude-x"}`, map[string]string{"model": "claude-x"}},
		{"thinking_level_change", `{"type":"thinking_level_change","thinkingLevel":"high"}`, map[string]string{"effort": "high"}},
		{"assistant message", `{"type":"message","message":{"role":"assistant","provider":"p","model":"m","thinkingLevel":"low"}}`,
			map[string]string{"model": "p/m", "effort": "low"}},
		{"assistant level on the line", `{"type":"message","thinkingLevel":"max","message":{"role":"assistant","model":"m"}}`,
			map[string]string{"model": "m", "effort": "max"}},
		{"user message", `{"type":"message","message":{"role":"user","model":"m"}}`, nil},
		{"other line", `{"type":"compaction","model":"m"}`, nil},
		{"bad json", `{"modelId":`, nil},
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
