package codex_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"rex-tg/internal/agents"
	"rex-tg/internal/agents/codex"
)

func adapter(t *testing.T) agents.Adapter {
	t.Helper()
	a := codex.New()
	if _, ok := a.(agents.Controller); !ok {
		t.Fatal("the Codex adapter is not a Controller")
	}
	return a
}

func TestCommand(t *testing.T) {
	c := adapter(t).(agents.Controller)
	cur := map[string]string{"model": "gpt-test", "effort": "medium"}
	for _, tc := range []struct {
		name, control, value string
		cur                  map[string]string
		want                 string // "" means an error
		errHas               string
	}{
		{"model keeps the effort", "model", "gpt-other", cur, "/model gpt-other --reasoning=medium", ""},
		{"effort keeps the model", "effort", "HIGH", cur, "/model gpt-test --reasoning=high", ""},
		{"effort needs the model", "effort", "high", map[string]string{"effort": "low"}, "", "send a message first, so rex-tg sees the model"},
		{"model needs the effort", "model", "gpt-other", map[string]string{"model": "gpt-test"}, "", "send a message first"},
		{"no state", "effort", "high", nil, "", "send a message first"},
		{"bad level", "effort", "ultra", cur, "", "effort"},
		{"bad model", "model", "a b", cur, "", "model"},
		{"bad model in the transcript", "effort", "low", map[string]string{"model": "x y", "effort": "low"}, "", "model"},
		{"unknown control", "fast", "on", cur, "", "fast"},
	} {
		got, err := c.Command(tc.control, tc.value, tc.cur)
		if tc.want == "" {
			if err == nil || !strings.Contains(err.Error(), tc.errHas) {
				t.Errorf("%s: Command = %q, %v; want an error with %q", tc.name, got, err, tc.errHas)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: Command = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

func TestSeen(t *testing.T) {
	c := adapter(t).(agents.Controller)
	got := c.Seen([]byte(`{"timestamp":"2026-01-01T00:00:00Z","type":"turn_context","payload":{"turn_id":"t","model":"gpt-test","effort":"high"}}`))
	if want := map[string]string{"model": "gpt-test", "effort": "high"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Seen = %v, want %v", got, want)
	}
	for _, line := range []string{
		`{"type":"event_msg","payload":{"model":"x","effort":"low"}}`,
		`{"type":"turn_context","payload":{}}`,
		`{"type":"turn_context","payload":`,
		``,
	} {
		if got := c.Seen([]byte(line)); len(got) != 0 {
			t.Errorf("Seen(%q) = %v, want nothing", line, got)
		}
	}
}

func TestChoicesFromModelCache(t *testing.T) {
	home := t.TempDir()
	src := adapter(t).(agents.ChoiceSource)
	if got := src.Choices(home, "model"); got != nil {
		t.Errorf("without a cache: %v", got)
	}
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cache := `{"models":[{"slug":"m-one","visibility":"list"},{"slug":"m-hidden","visibility":"hide"},{"slug":"m-two","visibility":"list"},{"slug":"","visibility":"list"}]}`
	if err := os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := src.Choices(home, "model"), []string{"m-one", "m-two"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Choices = %v, want %v", got, want)
	}
	if got := src.Choices(home, "effort"); got != nil {
		t.Errorf("effort choices = %v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := src.Choices(home, "model"); got != nil {
		t.Errorf("bad cache: %v", got)
	}
}

func TestCommandTakesTranscriptValuesOutsideTheList(t *testing.T) {
	a := codex.New().(agents.Controller)
	got, err := a.Command("model", "gpt-b", map[string]string{"model": "gpt-a", "effort": "minimal"})
	if err != nil || got != "/model gpt-b --reasoning=minimal" {
		t.Errorf("Command = %q, %v; want the transcript's effort kept", got, err)
	}
	if _, err := a.Command("model", "gpt-b", map[string]string{"effort": "high; rm -rf"}); err == nil {
		t.Error("a transcript value with a bad form was typed")
	}
}
