package daemon

import (
	"errors"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/registry"
	"rex-tg/internal/rex"
)

// claudeTrust is Claude Code 2.1.322's trust question as its code draws it:
// no numbers, "No, exit" first, and the cursor on it at the start.
func claudeTrust(cursor int) string {
	answers := []string{"No, exit", "Yes, I trust this folder"}
	var b strings.Builder
	b.WriteString(" Accessing workspace:\n\n /Users/me/work/new-app\n\n")
	b.WriteString(" Quick safety check: Is this a project you created or one you trust? (Like your own code, a\n")
	b.WriteString(" well-known open source project, or work from your team).\n\n")
	b.WriteString(" Claude Code'll be able to read, edit, and execute files here.\n\n Security guide\n\n")
	for i, a := range answers {
		if i == cursor {
			b.WriteString(" ❯ " + a + "\n")
		} else {
			b.WriteString("   " + a + "\n")
		}
	}
	b.WriteString("\n Enter to confirm · Esc to cancel\n")
	return b.String()
}

func TestParseStartupMenu(t *testing.T) {
	cases := []struct {
		name    string
		screen  string
		labels  []string
		cursor  int
		inQuest string // a line the question must hold
	}{
		{name: "Claude trust", screen: claudeTrust(0), labels: []string{"No, exit", "Yes, I trust this folder"}, inQuest: "Quick safety check"},
		{name: "Claude trust, cursor moved", screen: claudeTrust(1), labels: []string{"No, exit", "Yes, I trust this folder"}, cursor: 1},
		{name: "numbered, in a box",
			screen: "╭────────────────────────────╮\n│ Trust this folder?         │\n│                            │\n│ › 1. Trust and continue    │\n│   2. Open restricted       │\n│                            │\n│ Press Enter to continue.   │\n╰────────────────────────────╯\n",
			labels: []string{"1. Trust and continue", "2. Open restricted"}, inQuest: "Trust this folder?"},
		{name: "cursor below", screen: "Pick one\n\n  1. Yes\n› 2. No\n", labels: []string{"1. Yes", "2. No"}, cursor: 1},
		{name: "descriptions are not answers",
			screen: "Model?\n\n❯ Fast\n    for quick edits\n  Deep\n    for hard tasks\n",
			labels: []string{"Fast", "Deep"}},
	}
	for _, c := range cases {
		m, ok := parseStartupMenu(c.screen)
		if !ok {
			t.Errorf("%s: no list found", c.name)
			continue
		}
		if strings.Join(m.labels, "|") != strings.Join(c.labels, "|") || m.cursor != c.cursor {
			t.Errorf("%s: labels %q cursor %d, want %q cursor %d", c.name, m.labels, m.cursor, c.labels, c.cursor)
		}
		if c.inQuest != "" && !strings.Contains(strings.Join(m.question, "\n"), c.inQuest) {
			t.Errorf("%s: question %q does not hold %q", c.name, m.question, c.inQuest)
		}
	}
	// An input box, a single line with a prompt sign, two cursors, or plain
	// text is not a list of answers.
	for name, screen := range map[string]string{
		"Claude input box": "╭──────────────────────╮\n│ > Try \"write a test\" │\n╰──────────────────────╯\n  ? for shortcuts\n",
		"Codex input":      "› Ask Codex to do anything\n\n  ⏎ send   ⇧⏎ newline\n",
		"two cursors":      "❯ One\n  Two\n\n❯ Three\n  Four\n",
		"plain text":       "Starting…\nloading plugins\n",
		"one answer":       "Continue?\n\n❯ OK\n\nEnter to confirm\n",
	} {
		if m, ok := parseStartupMenu(screen); ok {
			t.Errorf("%s: found a list %q", name, m.labels)
		}
	}
	// The cursor does not change which question it is; the answers do.
	a, _ := parseStartupMenu(claudeTrust(0))
	b, _ := parseStartupMenu(claudeTrust(1))
	c, _ := parseStartupMenu(strings.Replace(claudeTrust(0), "No, exit", "No, quit", 1))
	if a.id() != b.id() || a.id() == c.id() {
		t.Errorf("ids: cursor moved %v, other answer %v", a.id() == b.id(), a.id() != c.id())
	}
}

// trustPane is a pane with Claude Code's trust question: Down and Up move
// the cursor, and Enter on an answer leaves the question.
func trustPane() *memPane {
	return &memPane{state: "no", screens: map[string]string{
		"no": claudeTrust(0), "yes": claudeTrust(1), "started": "╭───╮\n│ > │\n╰───╯\n", "exited": "$ ",
	}, moves: map[string]map[string]string{
		"no":  {"down": "yes", "enter": "exited"},
		"yes": {"up": "no", "enter": "started"},
	}}
}

func startupDaemon(t *testing.T, p *memPane) *daemon {
	d := vimDaemon(t, p)
	d.rules = registry.Rules{PiArgv0: []string{memName}}
	return d
}

var trustStarted = rex.Started{SessionID: "S1", BlockID: "B1"}

func TestAnswerStartupMovesTheCursorThenPressesEnter(t *testing.T) {
	id := func() string { m, _ := parseStartupMenu(claudeTrust(0)); return m.id() }()
	for _, c := range []struct {
		target int
		keys   string
		end    string
	}{
		{target: 1, keys: "down enter", end: "started"},
		{target: 0, keys: "enter", end: "exited"},
	} {
		p := trustPane()
		if err := startupDaemon(t, p).answerStartup(t.Context(), trustStarted, agents.Pi, id, c.target); err != nil {
			t.Fatalf("target %d: %v", c.target, err)
		}
		if got := p.pressed(); got != c.keys || p.state != c.end {
			t.Errorf("target %d: keys %q, screen %q; want %q, %q", c.target, got, p.state, c.keys, c.end)
		}
	}
}

func TestAnswerStartupNeverPressesOnAGuess(t *testing.T) {
	id := func() string { m, _ := parseStartupMenu(claudeTrust(0)); return m.id() }()
	cases := []struct {
		name  string
		setup func(p *memPane, d *daemon) (id string)
		want  error
		keys  string
	}{
		{"another question", func(p *memPane, d *daemon) string { return "other" }, errStartupGone, ""},
		{"paused", func(p *memPane, d *daemon) string { d.paused.Store(true); return id }, errPausedBefore, ""},
		{"a shell in front", func(p *memPane, d *daemon) string { p.gone = map[string]bool{"no": true}; return id }, errGoneBefore, ""},
		{"the cursor does not move", func(p *memPane, d *daemon) string { delete(p.moves["no"], "down"); return id }, errStartupStuck, "down"},
		{"Enter does not take", func(p *memPane, d *daemon) string { delete(p.moves["yes"], "enter"); return id }, errStartupStuck, "down enter"},
		{"the agent leaves after a key", func(p *memPane, d *daemon) string { p.gone = map[string]bool{"yes": true}; return id }, errGoneBefore, "down"},
		{"the screen cannot be read", func(p *memPane, d *daemon) string { p.captureErr = errors.New("no"); return id }, errRetryBefore, ""},
	}
	for _, c := range cases {
		p := trustPane()
		d := startupDaemon(t, p)
		qid := c.setup(p, d)
		start := time.Now()
		err := d.answerStartup(t.Context(), trustStarted, agents.Pi, qid, 1)
		if !errors.Is(err, c.want) || p.pressed() != c.keys {
			t.Errorf("%s: %v, keys %q; want %v, %q", c.name, err, p.pressed(), c.want, c.keys)
		}
		if time.Since(start) > 2*time.Second {
			t.Errorf("%s took %s", c.name, time.Since(start))
		}
	}
}
