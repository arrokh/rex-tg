package daemon

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"rex-tg/internal/rex"
	"rex-tg/internal/store"
)

// memPane is an in-memory Rex pane for the vim steps. Its screen is a small
// state machine: a key moves it from one named screen to the next, as
// moves[screen][key] says. A key without a move leaves the screen as it is,
// like an editor that does not react. No process is started per call, so
// these tests run in milliseconds.
type memPane struct {
	rexClient // the methods the vim steps do not use; calling one panics

	mu      sync.Mutex
	state   string
	screens map[string]string
	moves   map[string]map[string]string
	gone    map[string]bool // screens on which a shell is in front instead of pi
	pid     int             // pi's process id; 0 means memPID
	keys    []string

	captureErr error // every capture fails with it
	keyErr     error // every key fails with it
	procErr    error // every process check fails with it
	captures   int   // successful captures, the first one included
	failAfter  int   // when > 0, captures after this many fail
}

const (
	memPID  = 4242
	memName = "pi"
)

var memPaneRow = store.Pane{ID: 7, Session: "S1", Block: "B1", PID: memPID, ProcessName: memName}

func (p *memPane) Capture(context.Context, string, string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.captureErr != nil {
		return "", p.captureErr
	}
	if p.failAfter > 0 && p.captures >= p.failAfter {
		return "", errors.New("capture failed")
	}
	p.captures++
	return p.screens[p.state], nil
}

func (p *memPane) SendKeys(_ context.Context, _, _ string, keys ...string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.keyErr != nil {
		return p.keyErr
	}
	for _, k := range keys {
		p.keys = append(p.keys, k)
		if next, ok := p.moves[p.state][k]; ok {
			p.state = next
		}
	}
	return nil
}

func (p *memPane) Process(context.Context, string, string) (rex.Process, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.procErr != nil {
		return rex.Process{}, p.procErr
	}
	if p.gone[p.state] {
		return rex.Process{Foreground: rex.ProcInfo{PID: 777, Name: "zsh"}}, nil
	}
	pid := p.pid
	if pid == 0 {
		pid = memPID
	}
	return rex.Process{Foreground: rex.ProcInfo{PID: pid, Name: memName, Argv0: memName}}, nil
}

func (p *memPane) setState(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = s
}

func (p *memPane) pressed() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.keys, " ")
}

// vimDaemon is a daemon with only what the vim steps use. A key's effect is
// looked for on 15 reads, 1 ms apart.
func vimDaemon(t *testing.T, p *memPane) *daemon {
	return &daemon{ctx: t.Context(), rx: p, timing: Timing{VimPoll: time.Millisecond}.withDefaults()}
}

// pi-vim screens: the label sits on the input box's bottom border.
func piScreen(label string) string { return piVim(label + " ") }

// piMachine is pi-vim as it behaves (handleEscape and the i key in pi-vim's
// index.ts): an Escape goes one step back toward plain NORMAL, i enters
// INSERT from plain NORMAL, and a count in VISUAL takes one Escape of its own.
func piMachine(start string) *memPane {
	labels := []string{"NORMAL", "INSERT", "NORMAL 3d_", "EX w_", "VISUAL", "VISUAL 3_", "V-LINE"}
	screens := map[string]string{}
	for _, l := range labels {
		screens[l] = piScreen(l)
	}
	return &memPane{state: start, screens: screens, moves: map[string]map[string]string{
		"NORMAL":     {"i": "INSERT"},
		"INSERT":     {"escape": "NORMAL"},
		"NORMAL 3d_": {"escape": "NORMAL"},
		"EX w_":      {"escape": "NORMAL"},
		"VISUAL":     {"escape": "NORMAL"},
		"VISUAL 3_":  {"escape": "VISUAL"},
		"V-LINE":     {"escape": "NORMAL"},
	}}
}

// From every pi-vim state the editor reaches INSERT before a paste, and plain
// NORMAL before an interrupt. Each key is pressed only after the screen showed
// the effect of the one before. An Escape is never pressed in plain NORMAL,
// where it would reach pi and stop the agent.
func TestSettlePiVimReachesTheWantedMode(t *testing.T) {
	cases := []struct {
		start string
		want  vimMode
		keys  string
		end   string
	}{
		{"NORMAL", vimInsert, "i", "INSERT"},
		{"INSERT", vimInsert, "", "INSERT"},
		{"NORMAL 3d_", vimInsert, "escape i", "INSERT"},
		{"EX w_", vimInsert, "escape i", "INSERT"},
		{"VISUAL", vimInsert, "escape i", "INSERT"},
		{"VISUAL 3_", vimInsert, "escape escape i", "INSERT"},
		{"V-LINE", vimInsert, "escape i", "INSERT"},
		{"INSERT", vimNormal, "escape", "NORMAL"},
		{"VISUAL 3_", vimNormal, "escape escape", "NORMAL"},
		{"NORMAL", vimNormal, "", "NORMAL"},
	}
	for _, c := range cases {
		t.Run(c.start+" to "+map[vimMode]string{vimInsert: "INSERT", vimNormal: "NORMAL"}[c.want], func(t *testing.T) {
			p := piMachine(c.start)
			d := vimDaemon(t, p)
			pressed, err := d.settlePiVim(t.Context(), memPaneRow, c.want)
			if err != nil {
				t.Fatalf("settlePiVim: %v", err)
			}
			if got := p.pressed(); got != c.keys {
				t.Errorf("keys = %q, want %q", got, c.keys)
			}
			if pressed != (c.keys != "") {
				t.Errorf("pressed = %v with keys %q", pressed, c.keys)
			}
			if p.state != c.end {
				t.Errorf("editor ends in %q, want %q", p.state, c.end)
			}
		})
	}
}

// Without a pi-vim label, or when the first screen cannot be read, typing goes
// on as without pi-vim: no key is pressed.
func TestSettlePiVimLeavesOtherEditorsAlone(t *testing.T) {
	for name, p := range map[string]*memPane{
		"plain editor":       {state: "x", screens: map[string]string{"x": "╭──╮\n│ > │\n╰──╯\n"}},
		"Claude Code footer": {state: "x", screens: map[string]string{"x": claudeBox + "  -- INSERT --\n"}},
		"unreadable screen":  {state: "x", captureErr: errors.New("capture failed")},
	} {
		t.Run(name, func(t *testing.T) {
			pressed, err := vimDaemon(t, p).settlePiVim(t.Context(), memPaneRow, vimInsert)
			if err != nil || pressed || p.pressed() != "" {
				t.Fatalf("pressed=%v err=%v keys=%q, want nothing", pressed, err, p.pressed())
			}
		})
	}
}

// An editor that does not react gets one key and no more: keys must not pile
// up in a pane that does not read them. While the screen stays the same, the
// next try presses nothing. Once the screen changes, the mark is gone.
func TestSettlePiVimStopsWhenTheEditorDoesNotReact(t *testing.T) {
	p := piMachine("NORMAL")
	p.moves["NORMAL"] = nil // the i has no effect
	d := vimDaemon(t, p)

	pressed, err := d.settlePiVim(t.Context(), memPaneRow, vimInsert)
	if !errors.Is(err, errVimStuck) || !pressed || p.pressed() != "i" {
		t.Fatalf("first try: pressed=%v err=%v keys=%q, want one i and errVimStuck", pressed, err, p.pressed())
	}
	pressed, err = d.settlePiVim(t.Context(), memPaneRow, vimInsert)
	if !errors.Is(err, errVimStuck) || pressed || p.pressed() != "i" {
		t.Fatalf("second try: pressed=%v err=%v keys=%q, want no key and errVimStuck", pressed, err, p.pressed())
	}

	// The same label with another pi process is a new editor: it gets a key.
	other := memPaneRow
	other.PID = memPID + 1
	p.pid = other.PID
	if _, err := d.settlePiVim(t.Context(), other, vimInsert); !errors.Is(err, errVimStuck) || p.pressed() != "i i" {
		t.Fatalf("new process: err=%v keys=%q, want a second i", err, p.pressed())
	}

	p.setState("INSERT") // the editor caught up
	if pressed, err := d.settlePiVim(t.Context(), other, vimInsert); err != nil || pressed {
		t.Fatalf("after the change: pressed=%v err=%v, want ready", pressed, err)
	}
	if _, marked := d.vimStuck.Load(memPaneRow.ID); marked {
		t.Error("the stuck mark stayed after the screen changed")
	}
}

// Every pi-vim mode has a label. A screen without one after a key is a redraw
// caught halfway or something covering the editor, so it is not taken as a
// change, and certainly not as INSERT.
func TestSettlePiVimMissingLabelIsNotAChange(t *testing.T) {
	p := piMachine("NORMAL")
	p.screens["BLANK"] = "some answer text\n"
	p.moves["NORMAL"] = map[string]string{"i": "BLANK"}
	pressed, err := vimDaemon(t, p).settlePiVim(t.Context(), memPaneRow, vimInsert)
	if !errors.Is(err, errVimStuck) || !pressed || p.pressed() != "i" {
		t.Fatalf("pressed=%v err=%v keys=%q, want one i and errVimStuck", pressed, err, p.pressed())
	}
}

// Before each key the steps check again that typing is not paused and that pi
// is in front, because bringing pi-vim to INSERT takes a few keys.
func TestSettlePiVimChecksSafetyBeforeEachKey(t *testing.T) {
	t.Run("pi leaves after the first key", func(t *testing.T) {
		p := piMachine("NORMAL 3d_")
		p.gone = map[string]bool{"NORMAL": true} // after the Escape, a shell is in front
		_, err := vimDaemon(t, p).settlePiVim(t.Context(), memPaneRow, vimInsert)
		if !errors.Is(err, errGoneBefore) || p.pressed() != "escape" {
			t.Fatalf("err=%v keys=%q, want errGoneBefore after the one Escape", err, p.pressed())
		}
	})
	t.Run("paused", func(t *testing.T) {
		p := piMachine("NORMAL")
		d := vimDaemon(t, p)
		d.paused.Store(true)
		if _, err := d.settlePiVim(t.Context(), memPaneRow, vimInsert); !errors.Is(err, errPausedBefore) || p.pressed() != "" {
			t.Fatalf("err=%v keys=%q, want errPausedBefore and no key", err, p.pressed())
		}
	})
	t.Run("the check fails", func(t *testing.T) {
		p := piMachine("NORMAL")
		p.procErr = errors.New("process failed")
		if _, err := vimDaemon(t, p).settlePiVim(t.Context(), memPaneRow, vimInsert); !errors.Is(err, errRetryBefore) || p.pressed() != "" {
			t.Fatalf("err=%v keys=%q, want errRetryBefore and no key", err, p.pressed())
		}
	})
}

func TestSettlePiVimCallFailures(t *testing.T) {
	t.Run("the key fails", func(t *testing.T) {
		p := piMachine("NORMAL")
		p.keyErr = errors.New("send-key failed")
		if _, err := vimDaemon(t, p).settlePiVim(t.Context(), memPaneRow, vimInsert); !errors.Is(err, errRetryBefore) {
			t.Fatalf("err=%v, want errRetryBefore", err)
		}
	})
	t.Run("the screen cannot be read after a key", func(t *testing.T) {
		p := piMachine("NORMAL")
		p.failAfter = 1
		if _, err := vimDaemon(t, p).settlePiVim(t.Context(), memPaneRow, vimInsert); !errors.Is(err, errRetryBefore) {
			t.Fatalf("err=%v, want errRetryBefore", err)
		}
	})
	t.Run("the daemon stops while it waits", func(t *testing.T) {
		p := piMachine("NORMAL")
		p.moves["NORMAL"] = nil
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := vimDaemon(t, p).settlePiVim(ctx, memPaneRow, vimInsert); !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v, want context.Canceled", err)
		}
	})
}

// A pane whose label keeps changing without reaching INSERT gets at most
// vimMaxKeys keys in one try.
func TestSettlePiVimKeyLimit(t *testing.T) {
	p := &memPane{state: "VISUAL 1_", screens: map[string]string{}, moves: map[string]map[string]string{}}
	for i := 1; i <= 2*vimMaxKeys; i++ {
		cur, next := "VISUAL "+string(rune('0'+i))+"_", "VISUAL "+string(rune('0'+i+1))+"_"
		p.screens[cur] = piScreen(cur)
		p.moves[cur] = map[string]string{"escape": next}
	}
	p.screens["VISUAL "+string(rune('0'+2*vimMaxKeys+1))+"_"] = piScreen("VISUAL ?_")
	_, err := vimDaemon(t, p).settlePiVim(t.Context(), memPaneRow, vimInsert)
	if !errors.Is(err, errVimStuck) {
		t.Fatalf("err=%v, want errVimStuck", err)
	}
	if n := len(strings.Fields(p.pressed())); n != vimMaxKeys {
		t.Fatalf("%d keys, want %d", n, vimMaxKeys)
	}
}

// Claude Code screens: the prompt box, and the footer below it.
func claudeMachine(start string) *memPane {
	return &memPane{state: start, screens: map[string]string{
		"VISUAL":      "answer\n" + claudeBox + "  -- VISUAL --\n",
		"VISUAL LINE": "answer\n" + claudeBox + "  -- VISUAL LINE --\n",
		"NORMAL":      "answer\n" + claudeBox + "  ? for shortcuts\n",
		"INSERT":      "answer\n" + claudeBox + "  -- INSERT --\n",
		"REDRAW":      "answer\n",
	}, moves: map[string]map[string]string{
		"VISUAL":      {"escape": "NORMAL"},
		"VISUAL LINE": {"escape": "NORMAL"},
	}}
}

// Claude Code: one Escape leaves VISUAL, and nothing is pressed in any other
// mode. Enter does not submit in VISUAL, and an Escape in NORMAL reaches
// Claude Code.
func TestSettleClaudeVimLeavesVisualOnly(t *testing.T) {
	cases := []struct {
		start, keys string
	}{
		{"VISUAL", "escape"},
		{"VISUAL LINE", "escape"},
		{"NORMAL", ""},
		{"INSERT", ""},
		{"REDRAW", ""},
	}
	for _, c := range cases {
		t.Run(c.start, func(t *testing.T) {
			p := claudeMachine(c.start)
			pressed, err := vimDaemon(t, p).settleClaudeVim(t.Context(), memPaneRow)
			if err != nil || pressed != (c.keys != "") || p.pressed() != c.keys {
				t.Fatalf("pressed=%v err=%v keys=%q, want keys %q", pressed, err, p.pressed(), c.keys)
			}
		})
	}
}

// VISUAL that stays after the Escape gets no second Escape while the screen
// is the same: two Escapes in a row can open Claude Code's rewind dialog.
func TestSettleClaudeVimStopsWhenVisualStays(t *testing.T) {
	p := claudeMachine("VISUAL")
	p.moves = nil
	d := vimDaemon(t, p)
	if pressed, err := d.settleClaudeVim(t.Context(), memPaneRow); !errors.Is(err, errVimStuck) || !pressed {
		t.Fatalf("first try: pressed=%v err=%v, want errVimStuck after one Escape", pressed, err)
	}
	if pressed, err := d.settleClaudeVim(t.Context(), memPaneRow); !errors.Is(err, errVimStuck) || pressed {
		t.Fatalf("second try: pressed=%v err=%v, want errVimStuck and no key", pressed, err)
	}
	if got := p.pressed(); got != "escape" {
		t.Fatalf("keys = %q, want one escape", got)
	}
	p.setState("NORMAL")
	if pressed, err := d.settleClaudeVim(t.Context(), memPaneRow); err != nil || pressed {
		t.Fatalf("after the change: pressed=%v err=%v, want ready", pressed, err)
	}
	if _, marked := d.vimStuck.Load(memPaneRow.ID); marked {
		t.Error("the stuck mark stayed after VISUAL was gone")
	}
}

// After the Escape, a screen without the prompt box (a redraw caught halfway)
// does not count as "VISUAL is gone".
func TestSettleClaudeVimNeedsTheBoxBack(t *testing.T) {
	p := claudeMachine("VISUAL")
	p.moves["VISUAL"] = map[string]string{"escape": "REDRAW"}
	if _, err := vimDaemon(t, p).settleClaudeVim(t.Context(), memPaneRow); !errors.Is(err, errVimStuck) {
		t.Fatalf("err=%v, want errVimStuck", err)
	}
}

func TestSettleClaudeVimChecksSafetyFirst(t *testing.T) {
	cases := map[string]struct {
		setup func(*daemon, *memPane)
		want  error
	}{
		"paused":         {func(d *daemon, _ *memPane) { d.paused.Store(true) }, errPausedBefore},
		"a shell":        {func(_ *daemon, p *memPane) { p.gone = map[string]bool{"VISUAL": true} }, errGoneBefore},
		"check fails":    {func(_ *daemon, p *memPane) { p.procErr = errors.New("process failed") }, errRetryBefore},
		"the key fails":  {func(_ *daemon, p *memPane) { p.keyErr = errors.New("send-key failed") }, errRetryBefore},
		"read after key": {func(_ *daemon, p *memPane) { p.failAfter = 1 }, errRetryBefore},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := claudeMachine("VISUAL")
			d := vimDaemon(t, p)
			c.setup(d, p)
			_, err := d.settleClaudeVim(t.Context(), memPaneRow)
			if !errors.Is(err, c.want) {
				t.Fatalf("err=%v, want %v", err, c.want)
			}
			if c.want != errRetryBefore && p.pressed() != "" {
				t.Errorf("keys = %q, want none", p.pressed())
			}
		})
	}
}

// The steps never press a key other than the ones named here: i to enter
// INSERT, and Escape to go back toward NORMAL.
func TestVimStepsPressOnlyKnownKeys(t *testing.T) {
	allowed := []string{"i", "escape"}
	for _, start := range []string{"NORMAL", "INSERT", "NORMAL 3d_", "EX w_", "VISUAL", "VISUAL 3_", "V-LINE"} {
		for _, want := range []vimMode{vimInsert, vimNormal} {
			p := piMachine(start)
			_, _ = vimDaemon(t, p).settlePiVim(t.Context(), memPaneRow, want)
			for _, k := range strings.Fields(p.pressed()) {
				if !slices.Contains(allowed, k) {
					t.Errorf("from %s: pressed %q", start, k)
				}
			}
		}
	}
}
