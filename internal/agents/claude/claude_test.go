package claude_test

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"rex-tg/internal/agents"
	"rex-tg/internal/agents/claude"
)

func parseFixture(t *testing.T) map[int][]agents.Event {
	t.Helper()
	f, err := os.Open("testdata/session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a := claude.New()
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
	h, err := claude.New().ReadHeader(f)
	if err != nil {
		t.Fatal(err)
	}
	if h.SessionID != "sess-cl-1" || h.CWD != "/Users/me/work/project" || h.Title != "tests: fix parser" {
		t.Errorf("header = %+v", h)
	}
	if !h.StartedAt.Equal(time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)) {
		t.Errorf("StartedAt = %v", h.StartedAt)
	}
}

func TestReadHeaderErrors(t *testing.T) {
	for name, in := range map[string]string{
		"empty":   "",
		"no cwd":  `{"type":"queue-operation","sessionId":"s"}` + "\n",
		"no id":   `{"type":"user","cwd":"/x"}` + "\n",
		"garbage": "{nope\n",
	} {
		if _, err := claude.New().ReadHeader(strings.NewReader(in)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	// Malformed lines before the identity are skipped; no title is fine.
	h, err := claude.New().ReadHeader(strings.NewReader("{bad\n" + `{"type":"user","sessionId":"s","cwd":"/x"}`))
	if err != nil || h.SessionID != "s" || h.CWD != "/x" || h.Title != "" {
		t.Errorf("%+v %v", h, err)
	}
}

func TestParseFixture(t *testing.T) {
	evs := parseFixture(t)

	for _, n := range []int{1, 13, 15, 16, 17, 19, 20, 21} {
		if len(evs[n]) != 0 {
			t.Errorf("line %d: want no events, got %+v", n, evs[n])
		}
	}
	if e := evs[2]; len(e) != 1 || e[0].Kind != agents.UserPrompt || e[0].Text != "Fix the failing test" || e[0].Key != "claude:S:u-1" {
		t.Errorf("prompt: %+v", e)
	}
	if e := evs[3]; len(e) != 1 || e[0].Kind != agents.Thinking || e[0].Text != "Let me look." {
		t.Errorf("thinking: %+v", e)
	}
	if e := evs[4]; len(e) != 1 || e[0].Kind != agents.AssistantText || e[0].Phase != agents.Commentary {
		t.Errorf("commentary: %+v", e)
	}
	if e := evs[5]; len(e) != 1 || e[0].Kind != agents.ToolCall ||
		e[0].Tool.CallID != "toolu_1" || e[0].Tool.Name != "Bash" || e[0].Tool.Kind != agents.ToolShell || e[0].Tool.Label != "Run the tests" {
		t.Errorf("bash: %+v", e)
	}
	if e := evs[6]; len(e) != 1 || e[0].Kind != agents.ToolResult || e[0].Tool.CallID != "toolu_1" || e[0].Tool.OK || e[0].Tool.Output != "FAIL: TestX" {
		t.Errorf("result 1: %+v", e)
	}
	if e := evs[7]; len(e) != 1 || e[0].Tool.Kind != agents.ToolRead || e[0].Tool.Label != "pkg/a.go" {
		t.Errorf("read: %+v", e)
	}
	if e := evs[8]; len(e) != 1 || e[0].Kind != agents.ToolResult || !e[0].Tool.OK || e[0].Tool.Output != "line one\nline two" {
		t.Errorf("result 2: %+v", e)
	}
	if e := evs[9]; len(e) != 1 || e[0].Phase != agents.Final || e[0].Text != "The test passes now." {
		t.Errorf("final: %+v", e)
	}
	if e := evs[10]; len(e) != 1 || e[0].Phase != agents.Error {
		t.Errorf("api error: %+v", e)
	}
	if e := evs[11]; len(e) != 1 || e[0].Kind != agents.Title || e[0].Text != "tests: fix parser" || e[0].Key != agents.FallbackKey(agents.Claude, "S", offsetOfLine(t, 11)) {
		t.Errorf("title: %+v", e)
	}
	if e := evs[12]; len(e) != 1 || e[0].Kind != agents.Boundary || e[0].Key != "claude:S:s-1" {
		t.Errorf("boundary: %+v", e)
	}
	if e := evs[22]; len(e) != 0 {
		t.Errorf("queue-operation: %+v", e)
	}
	if e := evs[23]; len(e) != 1 || e[0].Kind != agents.UserPrompt || e[0].Text != "also do X" || e[0].Key != "claude:S:q-1" {
		t.Errorf("queued_command: %+v", e)
	}
	if e := evs[24]; len(e) != 0 {
		t.Errorf("other attachment: %+v", e)
	}
	if e := evs[25]; len(e) != 1 || e[0].Kind != agents.TurnEnd || e[0].Key != "claude:S:i-1" {
		t.Errorf("interrupt: %+v", e)
	}
	if e := evs[18]; len(e) != 1 || e[0].Kind != agents.UserPrompt || e[0].Text != "Now add docs\n\nand a test" {
		t.Errorf("multi-block prompt: %+v", e)
	}
}

func offsetOfLine(t *testing.T, n int) int64 {
	t.Helper()
	b, err := os.ReadFile("testdata/session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var off int64
	for i, l := range strings.SplitAfter(string(b), "\n") {
		if i == n-1 {
			return off
		}
		off += int64(len(l))
	}
	return off
}

func TestMixedLineKeysDistinct(t *testing.T) {
	line := `{"type":"user","uuid":"u","message":{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"t1","content":"a"},{"type":"tool_result","tool_use_id":"t2","content":"b"},{"type":"text","text":"hello"}]}}`
	evs, err := claude.New().ParseLine([]byte(line), agents.LineContext{SessionID: "S"})
	if err != nil || len(evs) != 3 {
		t.Fatal(evs, err)
	}
	seen := map[string]bool{}
	for _, e := range evs {
		if seen[e.Key] || !strings.HasPrefix(e.Key, "claude:S:u") {
			t.Errorf("bad key %q", e.Key)
		}
		seen[e.Key] = true
	}
	if evs[2].Kind != agents.UserPrompt {
		t.Errorf("%+v", evs[2])
	}
}

func TestToolLabels(t *testing.T) {
	cases := []struct {
		name, input string
		kind        agents.ToolKind
		label       string
	}{
		{"Bash", `{"command":"ls -la\nwc"}`, agents.ToolShell, "ls -la wc"},
		{"Bash", `{"command":"ls","description":"List files"}`, agents.ToolShell, "List files"},
		{"Read", `{"file_path":"/elsewhere/x.go"}`, agents.ToolRead, "/elsewhere/x.go"},
		{"Edit", `{"file_path":"/w/p/a.go"}`, agents.ToolEdit, "a.go"},
		{"Write", `{"file_path":"/w/p/b.go"}`, agents.ToolEdit, "b.go"},
		{"MultiEdit", `{"file_path":"/w/p/c.go"}`, agents.ToolEdit, "c.go"},
		{"NotebookEdit", `{"notebook_path":"/w/p/n.ipynb"}`, agents.ToolEdit, "n.ipynb"},
		{"Grep", `{"pattern":"TODO"}`, agents.ToolSearch, "TODO"},
		{"Glob", `{"pattern":"**/*.go"}`, agents.ToolSearch, "**/*.go"},
		{"WebFetch", `{"url":"https://example.com"}`, agents.ToolWeb, "https://example.com"},
		{"WebSearch", `{"query":"go generics"}`, agents.ToolWeb, "go generics"},
		{"Task", `{"description":"Explore code"}`, agents.ToolSubagent, "Explore code"},
		{"Agent", `{"description":"Review"}`, agents.ToolSubagent, "Review"},
		{"TodoWrite", `{"todos":[]}`, agents.ToolPlan, "update plan"},
		{"mcp__srv__do", `{"a":1}`, agents.ToolOther, "mcp__srv__do"},
		{"Bash", `{}`, agents.ToolShell, "Bash"},
		{"Bash", `{"command":"` + strings.Repeat("x", 500) + `"}`, agents.ToolShell, strings.Repeat("x", 119) + "…"},
	}
	for _, c := range cases {
		line := `{"type":"assistant","uuid":"a","cwd":"/w/p","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t","name":"` + c.name + `","input":` + c.input + `}]}}`
		evs, err := claude.New().ParseLine([]byte(line), agents.LineContext{SessionID: "S"})
		if err != nil || len(evs) != 1 {
			t.Fatalf("%s: %v %v", c.name, evs, err)
		}
		if tl := evs[0].Tool; tl.Kind != c.kind || tl.Label != c.label {
			t.Errorf("%s %s: got %+v", c.name, c.input, tl)
		}
	}
}

func TestLineContextCWD(t *testing.T) {
	line := `{"type":"assistant","uuid":"a","message":{"content":[{"type":"tool_use","id":"t","name":"Read","input":{"file_path":"/ctx/dir/x.go"}}]}}`
	evs, _ := claude.New().ParseLine([]byte(line), agents.LineContext{SessionID: "S", CWD: "/ctx/dir"})
	if len(evs) != 1 || evs[0].Tool.Label != "x.go" {
		t.Errorf("%+v", evs)
	}
}

func TestParseLineEdgeCases(t *testing.T) {
	a := claude.New()
	if _, err := a.ParseLine([]byte(`{"type":"user",`), agents.LineContext{}); err == nil {
		t.Error("malformed: want error")
	}
	for _, l := range []string{
		`{}`, `{"type":"user","uuid":"1"}`,
		`{"type":"user","uuid":"1","message":"string"}`,
		`{"type":"assistant","uuid":"1","message":{"content":[{"type":"text","text":"  "}]}}`,
	} {
		evs, err := a.ParseLine([]byte(l), agents.LineContext{})
		if err != nil || len(evs) != 0 {
			t.Errorf("%s: %v %v", l, evs, err)
		}
	}
	evs, err := a.ParseLine([]byte(`{"type":"user","message":{"content":"hi"}}`), agents.LineContext{SessionID: "S", Offset: 7})
	if err != nil || len(evs) != 1 || evs[0].Key != agents.FallbackKey(agents.Claude, "S", 7) {
		t.Errorf("fallback: %+v %v", evs, err)
	}
}

func TestVeryLongLine(t *testing.T) {
	huge := strings.Repeat("é", 4<<20)
	line := `{"type":"user","uuid":"u","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"` + huge + `"}]}}`
	evs, err := claude.New().ParseLine([]byte(line), agents.LineContext{SessionID: "S"})
	if err != nil || len(evs) != 1 {
		t.Fatal(evs, err)
	}
	out := evs[0].Tool.Output
	if len(out) == 0 || len(out) > agents.MaxToolOutput || !utf8.ValidString(out) {
		t.Errorf("output len %d", len(out))
	}
	// An image block next to text does not matter.
	img := `{"type":"user","uuid":"u","message":{"content":[{"type":"image","source":{"data":"` + strings.Repeat("A", 3<<20) + `"}},{"type":"text","text":"look"}]}}`
	evs, err = claude.New().ParseLine([]byte(img), agents.LineContext{SessionID: "S"})
	if err != nil || len(evs) != 1 || evs[0].Text != "look" {
		t.Errorf("image: %+v %v", evs, err)
	}
	in := `{"type":"queue-operation"}` + "\n" + line + "\n" + `{"type":"user","sessionId":"s","cwd":"/x"}` + "\n"
	h, err := claude.New().ReadHeader(strings.NewReader(in))
	if err != nil || h.SessionID != "s" {
		t.Errorf("%+v %v", h, err)
	}
}

func TestIsSubagentTranscript(t *testing.T) {
	a := claude.New()
	for p, want := range map[string]bool{
		"/h/.claude/projects/-x/abc.jsonl":                   false,
		"/h/.claude/projects/-x/abc/subagents/agent-1.jsonl": true,
		"/h/.claude/projects/-x/subagentsfoo.jsonl":          false,
	} {
		if got := a.IsSubagentTranscript(p); got != want {
			t.Errorf("%s: got %v", p, got)
		}
	}
}

func TestInput(t *testing.T) {
	in := claude.New().Input()
	if in.WhileWorking != "steer" || in.SubmitKey != "enter" || in.PasteEnterDelay != 300*time.Millisecond || len(in.InterruptKeys) != 1 {
		t.Errorf("%+v", in)
	}
	if claude.New().Runtime() != agents.Claude {
		t.Error("runtime")
	}
}

func TestTranscriptCandidates(t *testing.T) {
	home := t.TempDir()
	cwd := "/Users/me/work/.x"
	dir := filepath.Join(home, ".claude", "projects", "-Users-me-work--x")
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
	write(filepath.Join(dir, "new", "subagents", "agent-1.jsonl"), now)
	write(filepath.Join(home, ".claude", "projects", "-Users-me-work-x", "other.jsonl"), now)

	a := claude.New()
	got, err := a.TranscriptCandidates(home, cwd, now.Add(-150*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != newest || got[1] != mid {
		t.Errorf("got %v", got)
	}
	if all, _ := a.TranscriptCandidates(home, cwd, time.Time{}); len(all) != 3 || all[2] != old {
		t.Errorf("all: %v", all)
	}
	if none, err := a.TranscriptCandidates(home, "/no/such", time.Time{}); err != nil || len(none) != 0 {
		t.Errorf("missing: %v %v", none, err)
	}
	// Folder naming: every non-alphanumeric character becomes "-".
	w2 := filepath.Join(home, ".claude", "projects", "-Users-me-my-proj-v1-2")
	write(filepath.Join(w2, "s.jsonl"), now)
	if got, _ := a.TranscriptCandidates(home, "/Users/me/my proj_v1.2", time.Time{}); len(got) != 1 {
		t.Errorf("name encoding: %v", got)
	}
}

func TestUnknownTypes(t *testing.T) {
	cases := []struct{ line, want string }{
		{`{"type":"future","uuid":"1"}`, "future"},
	}
	for _, c := range cases {
		evs, err := claude.New().ParseLine([]byte(c.line), agents.LineContext{SessionID: "S"})
		if err != nil || len(evs) != 1 || evs[0].Kind != agents.KindUnknown || evs[0].Text != c.want || evs[0].Key != "" {
			t.Errorf("%s: %+v %v", c.line, evs, err)
		}
	}
}

func TestUnknownBlockIgnored(t *testing.T) {
	line := `{"type":"assistant","uuid":"a","message":{"content":[{"type":"new_block","x":1}]}}`
	evs, err := claude.New().ParseLine([]byte(line), agents.LineContext{SessionID: "S"})
	if err != nil || len(evs) != 0 {
		t.Errorf("%+v %v", evs, err)
	}
}

// Claude Code saves a long paste wrapped in <pasted_content id="…"> tags (seen
// live). The prompt text is what was pasted, without the tags: rex-tg pastes
// long Telegram messages, and the echo check compares the prompt with the
// message it typed.
func TestPastedTextIsUnwrapped(t *testing.T) {
	const pre = `{"type":"user","uuid":"u1","sessionId":"S","timestamp":"2026-11-11T10:00:00.000Z","message":{"role":"user","content":`
	cases := []struct {
		name, content, want string
	}{
		{"only a paste", `"\n\n<pasted_content id=\"b2ba\">\nline one\n\nline two\n</pasted_content id=\"b2ba\">\n"`, "line one\n\nline two"},
		{"text around it", `"see this:\n\n<pasted_content id=\"a1\">\nX\n</pasted_content id=\"a1\">\nthanks"`, "see this:\n\nX\nthanks"},
		{"two pastes", `"<pasted_content id=\"a1\">\nA\n</pasted_content id=\"a1\"> and <pasted_content id=\"b2\">\nB\n</pasted_content id=\"b2\">"`, "A and B"},
		{"a tag inside the paste stays", `"<pasted_content id=\"a1\">\nkeep <pasted_content id=\"zz\">q</pasted_content id=\"zz\"> as text\n</pasted_content id=\"a1\">"`, `keep <pasted_content id="zz">q</pasted_content id="zz"> as text`},
		{"an unclosed tag stays", `"show <pasted_content id=\"a1\"> as text"`, `show <pasted_content id="a1"> as text`},
		{"a block list", `[{"type":"text","text":"<pasted_content id=\"c3\">\nhi\n</pasted_content id=\"c3\">"}]`, "hi"},
		{"an empty id stays", `"<pasted_content id=\"\">x</pasted_content id=\"\">"`, `<pasted_content id="">x</pasted_content id="">`},
		{"with a reminder", `"<pasted_content id=\"e5\">\nhi\n</pasted_content id=\"e5\">\n\n<system-reminder>ctx</system-reminder>"`, "hi"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			evs, err := claude.New().ParseLine([]byte(pre+c.content+`}}`), agents.LineContext{SessionID: "S"})
			if err != nil {
				t.Fatal(err)
			}
			var got string
			for _, e := range evs {
				if e.Kind == agents.UserPrompt {
					got = e.Text
				}
			}
			if got != c.want {
				t.Fatalf("prompt = %q, want %q", got, c.want)
			}
		})
	}
	t.Run("a queued prompt", func(t *testing.T) {
		line := `{"type":"attachment","uuid":"q1","sessionId":"S","timestamp":"2026-11-11T10:00:00.000Z","attachment":{"type":"queued_command","prompt":"<pasted_content id=\"d4\">\nqueued\n</pasted_content id=\"d4\">","humanTurn":true}}`
		evs, err := claude.New().ParseLine([]byte(line), agents.LineContext{SessionID: "S"})
		if err != nil || len(evs) != 1 || evs[0].Text != "queued" {
			t.Fatalf("events = %+v, err = %v; want one prompt %q", evs, err, "queued")
		}
	})
}

// Claude Code writes a background task's notification, and a message from
// another session, as a user line (seen live). Its "origin" (or, for a queued
// line, "commandMode") tells it apart from what a person typed: such a prompt
// is Auto. Older lines without these fields are Auto when they start with
// <task-notification>.
func TestAutomaticPrompts(t *testing.T) {
	const ts = `"uuid":"u1","sessionId":"S","timestamp":"2026-11-11T10:00:00.000Z"`
	cases := []struct {
		name, line string
		auto       bool
	}{
		{"typed", `{"type":"user",` + ts + `,"origin":{"kind":"human"},"message":{"role":"user","content":"hello"}}`, false},
		{"no origin", `{"type":"user",` + ts + `,"message":{"role":"user","content":"hello"}}`, false},
		{"task notification", `{"type":"user",` + ts + `,"origin":{"kind":"task-notification","producer":"session-task"},"message":{"role":"user","content":"<task-notification>\n<task-id>b1</task-id>\n</task-notification>"}}`, true},
		{"peer", `{"type":"user",` + ts + `,"origin":{"kind":"peer"},"message":{"role":"user","content":"from another session"}}`, true},
		{"old notification", `{"type":"user",` + ts + `,"message":{"role":"user","content":"<task-notification>\n<task-id>b1</task-id>\n</task-notification>"}}`, true},
		{"queued typed", `{"type":"attachment",` + ts + `,"attachment":{"type":"queued_command","prompt":"also do X","commandMode":"prompt","origin":{"kind":"human"}}}`, false},
		{"queued notification", `{"type":"attachment",` + ts + `,"attachment":{"type":"queued_command","prompt":"<task-notification>\n<task-id>b2</task-id>\n</task-notification>","commandMode":"task-notification","origin":{"kind":"task-notification"}}}`, true},
		{"queued notification without origin", `{"type":"attachment",` + ts + `,"attachment":{"type":"queued_command","prompt":"<task-notification>x</task-notification>","commandMode":"task-notification"}}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			evs, err := claude.New().ParseLine([]byte(c.line), agents.LineContext{SessionID: "S"})
			if err != nil || len(evs) != 1 || evs[0].Kind != agents.UserPrompt {
				t.Fatalf("events = %+v, err = %v; want one prompt", evs, err)
			}
			if evs[0].Auto != c.auto {
				t.Fatalf("Auto = %v, want %v", evs[0].Auto, c.auto)
			}
		})
	}
}

// <system-reminder> blocks in a user line are context for the model, not part
// of what the user typed: they never reach the prompt text.
func TestRemindersAreNotPartOfThePrompt(t *testing.T) {
	const pre = `{"type":"user","uuid":"u1","sessionId":"S","timestamp":"2026-11-11T10:00:00.000Z","message":{"role":"user","content":`
	cases := []struct {
		name, content, want string
	}{
		{"separate blocks", `[{"type":"text","text":"<system-reminder>secret ctx</system-reminder>"},{"type":"text","text":"hello"}]`, "hello"},
		{"after the text", `[{"type":"text","text":"hello\n\n<system-reminder>note</system-reminder>"}]`, "hello"},
		{"only reminders", `[{"type":"text","text":"<system-reminder>a</system-reminder>"},{"type":"text","text":"<system-reminder>b</system-reminder>"}]`, ""},
		{"an unclosed tag stays", `[{"type":"text","text":"show <system-reminder> as text"}]`, "show <system-reminder> as text"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			evs, err := claude.New().ParseLine([]byte(pre+c.content+`}}`), agents.LineContext{SessionID: "S"})
			if err != nil {
				t.Fatal(err)
			}
			var got string
			for _, e := range evs {
				if e.Kind == agents.UserPrompt {
					got = e.Text
				}
			}
			if got != c.want {
				t.Fatalf("prompt = %q, want %q", got, c.want)
			}
		})
	}
}
