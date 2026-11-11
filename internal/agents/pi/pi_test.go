package pi_test

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"rex-tg/internal/agents"
	"rex-tg/internal/agents/pi"
)

// parseFixture parses every line of testdata/session.jsonl and returns the
// events per line number (1-based).
func parseFixture(t *testing.T) map[int][]agents.Event {
	t.Helper()
	f, err := os.Open("testdata/session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a := pi.New()
	out := map[int][]agents.Event{}
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	var off int64
	for n := 1; sc.Scan(); n++ {
		evs, err := a.ParseLine(sc.Bytes(), agents.LineContext{SessionID: "S", Offset: off})
		if err != nil {
			t.Fatalf("line %d: %v", n, err)
		}
		out[n] = evs
		off += int64(len(sc.Bytes())) + 1
	}
	return out
}

func TestReadHeader(t *testing.T) {
	f, err := os.Open("testdata/session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h, err := pi.New().ReadHeader(f)
	if err != nil {
		t.Fatal(err)
	}
	if h.SessionID != "sess-pi-1" || h.CWD != "/Users/me/work/project" || h.Title != "tests: fix parser" {
		t.Errorf("header = %+v", h)
	}
	if !h.StartedAt.Equal(time.Date(2026, 11, 10, 6, 3, 11, 852e6, time.UTC)) {
		t.Errorf("StartedAt = %v", h.StartedAt)
	}
}

func TestReadHeaderErrors(t *testing.T) {
	for name, in := range map[string]string{
		"empty":     "",
		"malformed": "{nope\n",
		"not hdr":   `{"type":"message","id":"x"}` + "\n",
		"no id":     `{"type":"session","cwd":"/x"}` + "\n",
	} {
		if _, err := pi.New().ReadHeader(strings.NewReader(in)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	h, err := pi.New().ReadHeader(strings.NewReader(`{"type":"session","id":"s","cwd":"/x"}`))
	if err != nil || h.SessionID != "s" || h.Title != "" {
		t.Errorf("no newline: %+v %v", h, err)
	}
}

func TestParseFixture(t *testing.T) {
	evs := parseFixture(t)

	for _, n := range []int{1, 9, 10, 11} {
		if len(evs[n]) != 0 {
			t.Errorf("line %d: want no events, got %v", n, evs[n])
		}
	}

	if e := evs[2]; len(e) != 1 || e[0].Kind != agents.Title || e[0].Text != "tests: fix parser" || e[0].Key != "pi:S:i0000001" {
		t.Errorf("title: %+v", e)
	}
	if e := evs[3]; len(e) != 1 || e[0].Kind != agents.UserPrompt || e[0].Text != "Fix the failing test" {
		t.Errorf("prompt: %+v", e)
	}

	e := evs[4]
	if len(e) != 4 {
		t.Fatalf("assistant toolUse: want 4 events, got %+v", e)
	}
	if e[0].Kind != agents.Thinking || e[0].Text != "Need to run tests." {
		t.Errorf("thinking: %+v", e[0])
	}
	if e[1].Kind != agents.AssistantText || e[1].Phase != agents.Commentary || e[1].Text != "I will run the tests first." {
		t.Errorf("commentary: %+v", e[1])
	}
	if tl := e[2].Tool; e[2].Kind != agents.ToolCall || tl.CallID != "call-1" || tl.Kind != agents.ToolShell || tl.Label != "go test ./... -v" {
		t.Errorf("bash call: %+v", e[2])
	}
	if tl := e[3].Tool; tl.CallID != "call-2" || tl.Kind != agents.ToolRead || tl.Label != "/Users/me/work/project/main.go" {
		t.Errorf("read call: %+v", e[3])
	}
	keys := map[string]bool{}
	for _, ev := range e {
		if keys[ev.Key] {
			t.Errorf("duplicate key %s", ev.Key)
		}
		keys[ev.Key] = true
		if !strings.HasPrefix(ev.Key, "pi:S:a0000001") {
			t.Errorf("key %q", ev.Key)
		}
	}
	if !e[0].At.Equal(time.Date(2026, 11, 10, 6, 3, 14, 0, time.UTC)) {
		t.Errorf("At = %v", e[0].At)
	}

	if r := evs[5]; len(r) != 1 || r[0].Kind != agents.ToolResult ||
		r[0].Tool.CallID != "call-1" || r[0].Tool.OK || r[0].Tool.Took != 1500*time.Millisecond || r[0].Tool.Output != "FAIL\nexit 1" {
		t.Errorf("tool result: %+v", r)
	}
	if f := evs[6]; len(f) != 1 || f[0].Phase != agents.Final || f[0].Text != "The test passes now." {
		t.Errorf("final: %+v", f)
	}
	if er := evs[7]; len(er) != 1 || er[0].Phase != agents.Error || er[0].Text != "provider overloaded" {
		t.Errorf("error: %+v", er)
	}
	if b := evs[8]; len(b) != 1 || b[0].Kind != agents.Boundary || b[0].Text != "compaction" {
		t.Errorf("boundary: %+v", b)
	}
}

func TestToolLabels(t *testing.T) {
	cases := []struct {
		args, name string
		kind       agents.ToolKind
		label      string
	}{
		{`{"command":"ls -la"}`, "bash", agents.ToolShell, "ls -la"},
		{`{"path":"a.go","oldText":"x"}`, "edit", agents.ToolEdit, "a.go"},
		{`{"path":"a.go","content":"x"}`, "write", agents.ToolEdit, "a.go"},
		{`{"pattern":"TODO","path":"src"}`, "grep", agents.ToolSearch, "TODO"},
		{`{"pattern":"*.go"}`, "find", agents.ToolSearch, "*.go"},
		{`{"path":"src"}`, "ls", agents.ToolRead, "src"},
		{`{}`, "bash", agents.ToolShell, "bash"},
		{`{"x":1}`, "mystery", agents.ToolOther, "mystery"},
		{`{"command":"` + strings.Repeat("é", 500) + `"}`, "bash", agents.ToolShell, strings.Repeat("é", 119) + "…"},
	}
	for _, c := range cases {
		line := `{"type":"message","id":"m1","message":{"role":"assistant","content":[{"type":"toolCall","id":"c","name":"` + c.name + `","arguments":` + c.args + `}],"stopReason":"toolUse"}}`
		evs, err := pi.New().ParseLine([]byte(line), agents.LineContext{SessionID: "S"})
		if err != nil || len(evs) != 1 {
			t.Fatalf("%s: %v %v", c.name, evs, err)
		}
		if tl := evs[0].Tool; tl.Kind != c.kind || tl.Label != c.label || tl.Name != c.name {
			t.Errorf("%s %s: got %+v", c.name, c.args, tl)
		}
	}
}

func TestLineContextCWD(t *testing.T) {
	line := `{"type":"message","id":"m","message":{"role":"assistant","content":[{"type":"toolCall","id":"c","name":"edit","arguments":{"path":"/w/p/src/a.go"}},{"type":"toolCall","id":"d","name":"read","arguments":{"path":"/other/a.go"}}],"stopReason":"toolUse"}}`
	evs, err := pi.New().ParseLine([]byte(line), agents.LineContext{SessionID: "S", CWD: "/w/p"})
	if err != nil || len(evs) != 2 || evs[0].Tool.Label != "src/a.go" || evs[1].Tool.Label != "/other/a.go" {
		t.Errorf("%+v %v", evs, err)
	}
}

func TestParseLineEdgeCases(t *testing.T) {
	a := pi.New()
	if _, err := a.ParseLine([]byte(`{"type":"message",`), agents.LineContext{}); err == nil {
		t.Error("malformed JSON: want error")
	}
	for _, l := range []string{`{}`, `{"type":"message","id":"1"}`, `{"type":"message","id":"1","message":{"role":"user","content":[{"type":"image"}]}}`} {
		evs, err := a.ParseLine([]byte(l), agents.LineContext{})
		if err != nil || len(evs) != 0 {
			t.Errorf("%s: %v %v", l, evs, err)
		}
	}
	// A line without an id gets a fallback key.
	evs, err := a.ParseLine([]byte(`{"type":"message","message":{"role":"user","content":"hello"}}`), agents.LineContext{SessionID: "S", Offset: 42})
	if err != nil || len(evs) != 1 || evs[0].Key != agents.FallbackKey(agents.Pi, "S", 42) || evs[0].Text != "hello" {
		t.Errorf("fallback: %+v %v", evs, err)
	}
	// Invalid shapes must not panic.
	for _, l := range []string{`null`, `"s"`, `{"type":"message","message":"x"}`, `{"type":"message","id":"1","message":{"role":"assistant","content":5}}`} {
		_, _ = a.ParseLine([]byte(l), agents.LineContext{})
	}
}

func TestVeryLongLine(t *testing.T) {
	huge := strings.Repeat("日本", 2<<20) // about 12 MB
	line := `{"type":"message","id":"r1","message":{"role":"toolResult","toolCallId":"c","isError":false,"content":[{"type":"text","text":"` + huge + `"}]}}`
	evs, err := pi.New().ParseLine([]byte(line), agents.LineContext{SessionID: "S"})
	if err != nil || len(evs) != 1 {
		t.Fatal(evs, err)
	}
	out := evs[0].Tool.Output
	if len(out) == 0 || len(out) > agents.MaxToolOutput || !utf8.ValidString(out) || !evs[0].Tool.OK {
		t.Errorf("output len %d valid=%v", len(out), utf8.ValidString(out))
	}
	// ReadHeader copes with huge lines after the header.
	in := `{"type":"session","id":"s","cwd":"/x"}` + "\n" + line + "\n" + `{"type":"session_info","id":"i","name":"T"}` + "\n"
	h, err := pi.New().ReadHeader(strings.NewReader(in))
	if err != nil || h.Title != "T" {
		t.Errorf("%+v %v", h, err)
	}
}

func TestIsSubagentTranscript(t *testing.T) {
	a := pi.New()
	for p, want := range map[string]bool{
		"/h/.pi/agent/sessions/--x--/2026_abc.jsonl":                               false,
		"/h/.pi/agent/sessions/--x--/2026_abc/run-0/session.jsonl":                 true,
		"/h/.pi/agent/sessions/--x--/2026_abc/run-12/session.jsonl":                true,
		"/h/.pi/agent/sessions/--x--/subagent-artifacts/a_worker_transcript.jsonl": true,
		"/h/.pi/agent/sessions/--x--/run-0/other.jsonl":                            false,
	} {
		if got := a.IsSubagentTranscript(p); got != want {
			t.Errorf("%s: got %v", p, got)
		}
	}
}

func TestInput(t *testing.T) {
	in := pi.New().Input()
	if in.WhileWorking != "steer" || in.SubmitKey != "enter" || in.PasteEnterDelay != 300*time.Millisecond || len(in.InterruptKeys) != 1 || in.InterruptKeys[0] != "escape" {
		t.Errorf("%+v", in)
	}
	if pi.New().Runtime() != agents.Pi {
		t.Error("runtime")
	}
}

func TestTranscriptCandidates(t *testing.T) {
	home := t.TempDir()
	cwd := "/Users/me/work/.x"
	dir := filepath.Join(home, ".pi", "agent", "sessions", "--Users-me-work-.x--")
	other := filepath.Join(home, ".pi", "agent", "sessions", "--Users-me-other--")
	now := time.Now()
	write := func(p string, mod time.Time) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	old := filepath.Join(dir, "old.jsonl")
	mid := filepath.Join(dir, "mid.jsonl")
	newest := filepath.Join(dir, "new.jsonl")
	write(old, now.Add(-3*time.Hour))
	write(mid, now.Add(-2*time.Hour))
	write(newest, now.Add(-1*time.Hour))
	write(filepath.Join(dir, "new", "run-0", "session.jsonl"), now)
	write(filepath.Join(dir, "subagent-artifacts", "a_worker_transcript.jsonl"), now)
	write(filepath.Join(dir, "notes.txt"), now)
	write(filepath.Join(other, "x.jsonl"), now)

	a := pi.New()
	got, err := a.TranscriptCandidates(home, cwd, now.Add(-150*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != newest || got[1] != mid {
		t.Errorf("got %v", got)
	}
	got, _ = a.TranscriptCandidates(home, cwd, time.Time{})
	if len(got) != 3 || got[2] != old {
		t.Errorf("all: %v", got)
	}
	got, err = a.TranscriptCandidates(home, "/no/such/dir", time.Time{})
	if err != nil || len(got) != 0 {
		t.Errorf("missing dir: %v %v", got, err)
	}
}

func TestUnknownTypes(t *testing.T) {
	cases := []struct{ line, want string }{
		{`{"type":"future_thing","id":"1"}`, "future_thing"},
	}
	for _, c := range cases {
		evs, err := pi.New().ParseLine([]byte(c.line), agents.LineContext{SessionID: "S"})
		if err != nil || len(evs) != 1 || evs[0].Kind != agents.KindUnknown || evs[0].Text != c.want || evs[0].Key != "" {
			t.Errorf("%s: %+v %v", c.line, evs, err)
		}
	}
}
