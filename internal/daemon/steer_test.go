package daemon

import (
	"testing"

	"rex-tg/internal/store"
)

// Only the user's own words join a running turn. An agent's setting
// command (/model, /effort), an agent command (//compact), a shell command
// (!ls), and a prompt's note wait until the agent is idle.
func TestSteerable(t *testing.T) {
	for _, c := range []struct {
		kind, body string
		want       bool
	}{
		{"text", "also check the docs", true},
		{"file", "[file] /tmp/x.png\nlook at this", true},
		{"voice", "do not open a PR yet", true},
		{"text", "/compact", false},
		{"text", "  /model opus", false},
		{"text", "!ls", false},
		{kindControl, "/model opus", false},
		{kindControl, "/effort high", false},
		{kindNote, "use the other folder", false},
	} {
		if got := steerable(store.Inbound{Kind: c.kind, Body: c.body}); got != c.want {
			t.Errorf("steerable(%s %q) = %v, want %v", c.kind, c.body, got, c.want)
		}
	}
}
