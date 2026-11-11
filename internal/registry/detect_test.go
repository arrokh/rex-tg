package registry

import (
	"testing"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/rex"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		p    rex.ProcInfo
		want agents.Runtime
		ok   bool
	}{
		{"pi by argv0", rex.ProcInfo{Name: "node", Argv0: "pi"}, agents.Pi, true},
		{"pi with path argv0", rex.ProcInfo{Name: "node", Argv0: "/usr/local/bin/pi"}, agents.Pi, true},
		{"node alone", rex.ProcInfo{Name: "node", Argv0: "node"}, "", false},
		{"claude by argv0", rex.ProcInfo{Name: "2.1.322", Argv0: "claude"}, agents.Claude, true},
		{"claude by invoked path", rex.ProcInfo{Name: "2.1.322", Argv0: "2.1.322", InvokedPath: "/h/.local/bin/claude"}, agents.Claude, true},
		{"claude by glob", rex.ProcInfo{Name: "2.1.400", Argv0: "2.1.400", Path: "/h/.local/share/claude/versions/2.1.400"}, agents.Claude, true},
		{"outside glob", rex.ProcInfo{Name: "2.1.400", Argv0: "2.1.400", Path: "/opt/other/2.1.400"}, "", false},
		{"cux runs claude", cuxProc(7, "/w"), agents.Claude, true},
		{"codex by name", rex.ProcInfo{Name: "codex", Argv0: "codex"}, agents.Codex, true},
		{"tr3x is not supported", rex.ProcInfo{Name: "tr3x", Argv0: "claude"}, "", false},
		{"tr3x by argv0", rex.ProcInfo{Name: "x", Argv0: "tr3x", InvokedPath: "/h/.local/bin/claude"}, "", false},
		{"zsh", rex.ProcInfo{Name: "zsh", Argv0: "-zsh"}, "", false},
		{"bash", rex.ProcInfo{Name: "bash", Argv0: "bash"}, "", false},
		{"empty", rex.ProcInfo{}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Detect(rules, tc.p)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("Detect = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestAgentInFront(t *testing.T) {
	cl := rex.ProcInfo{PID: 10, Name: "2.1.322"}
	tool := rex.ProcInfo{PID: 11, Name: "sleep"}
	tests := []struct {
		name string
		p    rex.Process
		pid  int
		nm   string
		want bool
	}{
		{"foreground", rex.Process{Foreground: cl}, 10, "2.1.322", true},
		{"ancestor while a tool runs", rex.Process{Foreground: tool, Ancestors: []rex.ProcInfo{{PID: 5, Name: "zsh"}, cl}}, 10, "2.1.322", true},
		{"child", rex.Process{Foreground: tool, Child: &cl}, 10, "2.1.322", true},
		{"pid reused by another program", rex.Process{Foreground: rex.ProcInfo{PID: 10, Name: "zsh"}}, 10, "2.1.322", false},
		{"plain shell", rex.Process{Foreground: rex.ProcInfo{PID: 4, Name: "zsh"}}, 10, "2.1.322", false},
		{"no pid", rex.Process{}, 0, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := AgentInFront(tc.p, tc.pid, tc.nm); got != tc.want {
				t.Fatalf("AgentInFront = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseSpool(t *testing.T) {
	rep, err := ParseSpool([]byte(`{"runtime":"claude","event":"SessionStart","at":"2026-11-11T03:12:54.486Z","rex_session":"session:a","rex_block":"block:b","payload":{"session_id":"s1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Runtime != agents.Claude || rep.Event != "SessionStart" || rep.RexSession != "session:a" || rep.RexBlock != "block:b" {
		t.Errorf("rep = %+v", rep)
	}
	if !rep.At.Equal(time.Date(2026, 11, 11, 3, 12, 54, 486_000_000, time.UTC)) {
		t.Errorf("at = %v", rep.At)
	}
	if string(rep.Payload) != `{"session_id":"s1"}` {
		t.Errorf("payload = %s", rep.Payload)
	}
	for name, in := range map[string]string{
		"not json":       `nope`,
		"unknown rt":     `{"runtime":"vim","event":"x"}`,
		"no event":       `{"runtime":"pi"}`,
		"bad time":       `{"runtime":"pi","event":"x","at":"yesterday"}`,
		"empty document": ``,
	} {
		if _, err := ParseSpool([]byte(in)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestParsePSStart(t *testing.T) {
	for _, in := range []string{"Tue Nov 11 10:25:38 2026\n", "Tue Nov  4 09:01:02 2026\n"} {
		tm, err := parsePSStart(in)
		if err != nil || tm.Year() != 2026 {
			t.Errorf("parsePSStart(%q) = %v, %v", in, tm, err)
		}
	}
	if _, err := parsePSStart("garbage"); err == nil {
		t.Error("want error")
	}
}
