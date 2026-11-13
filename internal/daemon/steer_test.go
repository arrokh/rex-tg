package daemon

import (
	"testing"

	"rex-tg/internal/agents"
	"rex-tg/internal/config"
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

// Every agent steers by default: pi, Claude Code, and Codex all take Enter
// while they work (spike S2). "wait", or an agent whose Enter is unknown
// while it works, keeps the message until the agent is idle.
func TestSteersEveryAgent(t *testing.T) {
	d := &daemon{cfg: config.Default(t.TempDir())}
	for _, rt := range []agents.Runtime{agents.Pi, agents.Claude, agents.Codex} {
		if !d.steers(rt) {
			t.Errorf("%s does not steer by default", rt)
		}
	}
	if d.steers("") {
		t.Error("an unknown agent steers")
	}
	d.cfg.Agents.Codex.WhileWorking = "unknown"
	if d.steers(agents.Codex) || !d.steers(agents.Claude) {
		t.Error("while_working = unknown must wait, for that agent only")
	}
	d.cfg.InputSettings.WhileBusy = "wait"
	for _, rt := range []agents.Runtime{agents.Pi, agents.Claude, agents.Codex} {
		if d.steers(rt) {
			t.Errorf("%s steers with while_busy = wait", rt)
		}
	}
}

// The screen check before a steered paste sees the prompts of Claude Code
// and Codex, and not an agent's plain working screen.
func TestPromptGuardSeesEachAgentsPrompt(t *testing.T) {
	for name, screen := range map[string]string{
		"claude permission": claudePermScreen,
		"claude question":   singleScreen,
		"claude questions":  multiScreen,
		"codex permission":  codexScreen,
	} {
		if !parsePromptView(lastLines(screen, alertLines), true).ok {
			t.Errorf("%s: the prompt is not seen", name)
		}
	}
	for name, screen := range map[string]string{
		"claude working": "⏺ Reading the files\n  1. first step\n  2. second step\n\n✻ Thinking… (esc to interrupt)\n╭────╮\n│ >  │\n╰────╯\n",
		"codex working":  "• Working (12s • esc to interrupt)\n\n› Ask Codex to do anything\n",
		"pi working":     "Working...\n\n> \n",
	} {
		if parsePromptView(lastLines(screen, alertLines), true).ok {
			t.Errorf("%s: a prompt is seen on a plain screen", name)
		}
	}
}
