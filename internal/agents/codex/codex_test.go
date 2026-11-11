package codex_test

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"rex-tg/internal/agents"
	"rex-tg/internal/agents/codex"
)

func parseFixture(t *testing.T) map[int][]agents.Event {
	t.Helper()
	f, err := os.Open("testdata/rollout.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a := codex.New()
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
	f, err := os.Open("testdata/rollout.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h, err := codex.New().ReadHeader(f)
	if err != nil {
		t.Fatal(err)
	}
	if h.SessionID != "sess-cx-1" || h.CWD != "/Users/me/work/project" || h.Title != "" ||
		!h.StartedAt.Equal(time.Date(2026, 11, 10, 5, 59, 59, 5e8, time.UTC)) {
		t.Errorf("header = %+v", h)
	}
	for name, in := range map[string]string{
		"empty": "", "bad": "{x\n", "wrong type": `{"type":"event_msg","payload":{"id":"x"}}` + "\n",
		"no id": `{"type":"session_meta","payload":{"cwd":"/x"}}` + "\n",
	} {
		if _, err := codex.New().ReadHeader(strings.NewReader(in)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	// A very long session_meta line is fine.
	long := `{"type":"session_meta","payload":{"id":"s","cwd":"/x","base_instructions":"` + strings.Repeat("a", 3<<20) + `"}}`
	if h, err := codex.New().ReadHeader(strings.NewReader(long)); err != nil || h.SessionID != "s" {
		t.Errorf("%+v %v", h, err)
	}
}

func TestParseFixture(t *testing.T) {
	evs := parseFixture(t)

	// Skipped: session_meta, Codex-added user text, empty reasoning, AgentMessage
	// duplicates, token lines, turn_context, and tool outputs handled below.
	for _, n := range []int{1, 2, 5, 7, 17, 19, 20, 21} {
		if len(evs[n]) != 0 {
			t.Errorf("line %d: want no events, got %+v", n, evs[n])
		}
	}
	if e := evs[3]; len(e) != 1 || e[0].Kind != agents.UserPrompt || e[0].Text != "Fix the failing test" || e[0].Key != "codex:S:um-1" {
		t.Errorf("prompt: %+v", e)
	}
	if e := evs[4]; len(e) != 1 || e[0].Kind != agents.Thinking || e[0].Text != "Plan the fix" {
		t.Errorf("thinking: %+v", e)
	}
	if e := evs[6]; len(e) != 1 || e[0].Phase != agents.Commentary || e[0].Key != "codex:S:msg-1" {
		t.Errorf("commentary: %+v", e)
	}
	if e := evs[8]; len(e) != 1 || e[0].Tool.Kind != agents.ToolShell || e[0].Tool.Label != "go test ./..." || e[0].Tool.CallID != "call-1" || e[0].Tool.Name != "exec_command" {
		t.Errorf("exec_command: %+v", e)
	}
	if e := evs[9]; len(e) != 1 || e[0].Kind != agents.ToolResult || e[0].Tool.OK || e[0].Tool.CallID != "call-1" || !strings.Contains(e[0].Tool.Output, "FAIL") {
		t.Errorf("failed result: %+v", e)
	}
	if e := evs[10]; len(e) != 1 || e[0].Tool.Label != "ls -la" || e[0].Tool.Kind != agents.ToolShell {
		t.Errorf("shell array: %+v", e)
	}
	if e := evs[11]; len(e) != 1 || !e[0].Tool.OK || e[0].Tool.Output != "a\nb" || e[0].Tool.Took != 500*time.Millisecond {
		t.Errorf("ok result: %+v", e)
	}
	if e := evs[12]; len(e) != 1 || e[0].Tool.Kind != agents.ToolEdit || e[0].Tool.Label != "pkg/a.go, pkg/b.go, old.go" {
		t.Errorf("apply_patch: %+v", e)
	}
	if e := evs[13]; len(e) != 1 || e[0].Kind != agents.ToolResult || e[0].Tool.OK {
		t.Errorf("custom output with exit_code 1: %+v", e)
	}
	if e := evs[14]; len(e) != 1 || e[0].Tool.Kind != agents.ToolPlan {
		t.Errorf("plan: %+v", e)
	}
	if e := evs[15]; len(e) != 1 || e[0].Tool.Kind != agents.ToolWeb || e[0].Tool.Label != "go generics" {
		t.Errorf("web: %+v", e)
	}
	if e := evs[16]; len(e) != 1 || e[0].Phase != agents.Final || e[0].Text != "The test passes now." {
		t.Errorf("final: %+v", e)
	}
	if e := evs[22]; len(e) != 1 || e[0].Kind != agents.TurnEnd || e[0].Key != "codex:S:turn-end:turn-2" {
		t.Errorf("turn_aborted: %+v", e)
	}
	if e := evs[18]; len(e) != 1 || e[0].Kind != agents.TurnEnd || e[0].Text != "" {
		t.Errorf("turn end: %+v", e)
	}

	// Duplicate suppression: across the whole file exactly two AssistantText events.
	var texts int
	keys := map[string]bool{}
	for _, es := range evs {
		for _, e := range es {
			if e.Kind == agents.AssistantText {
				texts++
			}
			if keys[e.Key] {
				t.Errorf("duplicate key %s", e.Key)
			}
			keys[e.Key] = true
		}
	}
	if texts != 2 {
		t.Errorf("AssistantText events = %d, want 2", texts)
	}
}

func TestExitCodeDetection(t *testing.T) {
	cases := []struct {
		output string
		ok     bool
	}{
		{`"plain output"`, true},
		{`"Exit code: 0\nOutput:\nfine"`, true},
		{`"Exit code: 2\nOutput:\nbad"`, false},
		{`"exit code: 127"`, false},
		{`"{\"output\":\"x\",\"metadata\":{\"exit_code\":1}}"`, false},
		{`"{\"output\":\"x\",\"metadata\":{\"exit_code\":0}}"`, true},
		{`{"content":"fine","success":true}`, true},
		{`{"content":"nope","success":false}`, false},
		{`null`, true},
	}
	for _, c := range cases {
		line := `{"type":"response_item","payload":{"type":"function_call_output","call_id":"c","output":` + c.output + `}}`
		evs, err := codex.New().ParseLine([]byte(line), agents.LineContext{SessionID: "S", Offset: 1})
		if err != nil || len(evs) != 1 {
			t.Fatalf("%s: %v %v", c.output, evs, err)
		}
		if evs[0].Tool.OK != c.ok {
			t.Errorf("%s: OK = %v", c.output, evs[0].Tool.OK)
		}
	}
}

func TestToolLabels(t *testing.T) {
	cases := []struct {
		typ, name, field, args string
		kind                   agents.ToolKind
		label                  string
	}{
		{"function_call", "exec_command", "arguments", `{"cmd":"ls  -la"}`, agents.ToolShell, "ls -la"},
		{"function_call", "shell", "arguments", `{"command":"echo hi"}`, agents.ToolShell, "echo hi"},
		{"function_call", "shell", "arguments", `{"command":["git","status"]}`, agents.ToolShell, "git status"},
		{"function_call", "shell", "arguments", `{"command":["bash","-lc","make test"]}`, agents.ToolShell, "make test"},
		{"function_call", "exec_command", "arguments", `{}`, agents.ToolShell, "exec_command"},
		{"function_call", "exec_command", "arguments", `not json`, agents.ToolShell, "exec_command"},
		{"function_call", "apply_patch", "arguments", `{"input":"*** Begin Patch\n*** Add File: n.txt\n+x\n*** End Patch"}`, agents.ToolEdit, "n.txt"},
		{"function_call", "view_image", "arguments", `{"path":"x.png"}`, agents.ToolOther, "view_image"},
		{"custom_tool_call", "exec", "input", `"stuff"`, agents.ToolOther, "exec"},
	}
	for _, c := range cases {
		line := fmt.Sprintf(`{"type":"response_item","payload":{"type":%q,"name":%q,"call_id":"c",%q:%s}}`, c.typ, c.name, c.field, quoteIfNeeded(c.field, c.args))
		evs, err := codex.New().ParseLine([]byte(line), agents.LineContext{SessionID: "S", Offset: 3})
		if err != nil || len(evs) != 1 {
			t.Fatalf("%s: %v %v", c.name, evs, err)
		}
		if tl := evs[0].Tool; tl.Kind != c.kind || tl.Label != c.label {
			t.Errorf("%s %s: got %+v", c.name, c.args, tl)
		}
		if evs[0].Key != agents.FallbackKey(agents.Codex, "S", 3) {
			t.Errorf("key %q", evs[0].Key)
		}
	}
}

// quoteIfNeeded wraps arguments in a JSON string, as Codex does for
// function_call; raw JSON strings (already quoted) pass through.
func quoteIfNeeded(field, args string) string {
	if strings.HasPrefix(args, `"`) {
		return args
	}
	return fmt.Sprintf("%q", args)
}

func TestLineContextCWD(t *testing.T) {
	line := `{"type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"c","input":"*** Begin Patch\n*** Update File: /w/p/a.go\n*** Add File: /elsewhere/b.go\n*** End Patch"}}`
	evs, err := codex.New().ParseLine([]byte(line), agents.LineContext{SessionID: "S", CWD: "/w/p"})
	if err != nil || len(evs) != 1 || evs[0].Tool.Label != "a.go, /elsewhere/b.go" {
		t.Errorf("%+v %v", evs, err)
	}
}

func TestParseLineEdgeCases(t *testing.T) {
	a := codex.New()
	if _, err := a.ParseLine([]byte(`{"type":"response_item",`), agents.LineContext{}); err == nil {
		t.Error("malformed: want error")
	}
	for _, l := range []string{
		`{}`, `{"type":"future"}`, `{"type":"response_item"}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"x"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[]}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","content":[{"type":"text","text":"dup"}]}}}`,
		`{"type":"event_msg","payload":{"type":"task_started"}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[]}}}`,
	} {
		evs, err := a.ParseLine([]byte(l), agents.LineContext{})
		if err != nil || len(evs) != 0 {
			t.Errorf("%s: %v %v", l, evs, err)
		}
	}
	// A bare task_complete: no turn id means a fallback key.
	evs, err := a.ParseLine([]byte(`{"type":"event_msg","payload":{"type":"task_complete"}}`), agents.LineContext{SessionID: "S", Offset: 9})
	if err != nil || len(evs) != 1 || evs[0].Key != agents.FallbackKey(agents.Codex, "S", 9) {
		t.Errorf("%+v %v", evs, err)
	}
	// Missing phase defaults to commentary.
	evs, _ = a.ParseLine([]byte(`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a"},{"type":"output_text","text":"b"}]}}`), agents.LineContext{})
	if len(evs) != 1 || evs[0].Phase != agents.Commentary || evs[0].Text != "a\n\nb" {
		t.Errorf("%+v", evs)
	}
}

func TestVeryLongLine(t *testing.T) {
	huge := strings.Repeat("語", 3<<20)
	line := `{"type":"response_item","payload":{"type":"function_call_output","call_id":"c","output":"` + huge + `"}}`
	evs, err := codex.New().ParseLine([]byte(line), agents.LineContext{SessionID: "S"})
	if err != nil || len(evs) != 1 {
		t.Fatal(evs, err)
	}
	out := evs[0].Tool.Output
	if len(out) == 0 || len(out) > agents.MaxToolOutput || !utf8.ValidString(out) {
		t.Errorf("output len %d", len(out))
	}
}

func TestIsSubagentAndInput(t *testing.T) {
	a := codex.New()
	if a.IsSubagentTranscript("/h/.codex/sessions/2026/11/10/rollout-x/subagents/y.jsonl") {
		t.Error("codex has no subagent transcripts")
	}
	if in := a.Input(); in.WhileWorking != "steer" || in.SubmitKey != "enter" || in.PasteEnterDelay != 300*time.Millisecond {
		t.Errorf("%+v", in)
	}
	if a.Runtime() != agents.Codex {
		t.Error("runtime")
	}
}

func TestTranscriptCandidates(t *testing.T) {
	home := t.TempDir()
	cwd := "/Users/me/work/project"
	now := time.Now()
	write := func(day time.Time, name, meta string, mod time.Time) string {
		t.Helper()
		dir := filepath.Join(home, ".codex", "sessions", day.Format("2006"), day.Format("01"), day.Format("02"))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(meta+"\n{\"type\":\"turn_context\"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
		return p
	}
	metaFor := func(id, dir string) string {
		return fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":%q}}`, id, dir)
	}
	yesterday := now.AddDate(0, 0, -1)
	a1 := write(yesterday, "rollout-a.jsonl", metaFor("a", cwd), now.Add(-2*time.Hour))
	b1 := write(now, "rollout-b.jsonl", metaFor("b", cwd), now.Add(-1*time.Hour))
	write(now, "rollout-c.jsonl", metaFor("c", "/other"), now)
	write(now, "rollout-old.jsonl", metaFor("old", cwd), now.Add(-30*time.Hour))
	write(now, "notes.jsonl", metaFor("n", cwd), now)
	write(now, "rollout-bad.jsonl", "{broken", now)
	write(now.AddDate(0, 0, -10), "rollout-ancient.jsonl", metaFor("z", cwd), now.AddDate(0, 0, -10))

	a := codex.New()
	got, err := a.TranscriptCandidates(home, cwd, now.Add(-3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != b1 || got[1] != a1 {
		t.Errorf("got %v", got)
	}
	all, err := a.TranscriptCandidates(home, cwd, time.Time{})
	if err != nil || len(all) != 4 {
		t.Errorf("all: %v %v", all, err)
	}
	if none, err := a.TranscriptCandidates(t.TempDir(), cwd, time.Time{}); err != nil || len(none) != 0 {
		t.Errorf("empty home: %v %v", none, err)
	}
}
