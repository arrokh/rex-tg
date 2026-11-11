package turn_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/turn"
)

var update = flag.Bool("update", false, "rewrite golden files")

var t0 = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

func at(sec float64) time.Time { return t0.Add(time.Duration(sec * float64(time.Second))) }

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(p, []byte(got+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSuffix(string(want), "\n") != got {
		t.Errorf("golden %s mismatch\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func prompt(key, text string) agents.Event {
	return agents.Event{Kind: agents.UserPrompt, Key: key, Text: text}
}
func text(key string, p agents.Phase, s string) agents.Event {
	return agents.Event{Kind: agents.AssistantText, Key: key, Phase: p, Text: s}
}
func call(key, id, name string, k agents.ToolKind, label string) agents.Event {
	return agents.Event{Kind: agents.ToolCall, Key: key, Tool: &agents.Tool{CallID: id, Name: name, Kind: k, Label: label}}
}
func result(key, id string, ok bool, out string) agents.Event {
	return agents.Event{Kind: agents.ToolResult, Key: key, Tool: &agents.Tool{CallID: id, OK: ok, Output: out}}
}

type h struct {
	t *testing.T
	k *turn.Tracker
}

func newH(t *testing.T, cfg turn.Config, s turn.Settings) *h {
	return &h{t, turn.New(cfg, s)}
}
func (x *h) ev(e agents.Event, sec float64) []turn.Action { return x.k.Event(e, false, at(sec)) }
func (x *h) tick(sec float64) []turn.Action               { return x.k.Tick(at(sec)) }

func types(as []turn.Action) string {
	var p []string
	for _, a := range as {
		p = append(p, strings.TrimPrefix(fmt.Sprintf("%T", a), "turn."))
	}
	return strings.Join(p, ",")
}

// find returns the first action of type T.
func find[T turn.Action](t *testing.T, as []turn.Action) T {
	t.Helper()
	for _, a := range as {
		if v, ok := a.(T); ok {
			return v
		}
	}
	var z T
	t.Fatalf("no %T in [%s]", z, types(as))
	return z
}

func without(as []turn.Action) []turn.Action {
	var out []turn.Action
	for _, a := range as {
		if _, ok := a.(turn.Typing); !ok {
			out = append(out, a)
		}
	}
	return out
}

var progress = turn.Settings{Mode: "progress"}

func TestQuickAnswerNoProgress(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p1", "hi"), 0)
	as := x.ev(text("a1", agents.Final, "Hello"), 1.5)
	if types(as) != "Post" {
		t.Fatalf("got %s", types(as))
	}
	p := as[0].(turn.Post)
	if p.Key != "a1" || p.Kind != "answer" || p.Markdown != "Hello" || p.Silent {
		t.Errorf("post = %+v", p)
	}
	if got := x.tick(1.6); types(without(got)) != "" {
		t.Errorf("tick after answer: %s", types(got))
	}
	if got := x.tick(30); got != nil {
		t.Errorf("late tick: %s", types(got))
	}
}

func TestLongTurnProgressAndSummary(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p1", "fix it"), 0)
	x.ev(call("c1", "t1", "Read", agents.ToolRead, "src/parser.go"), 0.5)
	x.ev(result("r1", "t1", true, "x"), 0.6)
	if got := without(x.tick(1.9)); len(got) != 0 {
		t.Fatalf("before delay: %s", types(got))
	}
	as := x.tick(2)
	show := find[turn.ShowProgress](t, as)
	golden(t, "show_first", show.HTML)

	// Changes within the interval are coalesced.
	x.ev(call("c2", "t2", "Grep", agents.ToolSearch, `"func parseLine"`), 2.2)
	x.ev(result("r2", "t2", true, ""), 2.3)
	x.ev(text("m1", agents.Commentary, "Running the parser tests to find the failure\nsecond line"), 2.4)
	x.ev(call("c3", "t3", "Edit", agents.ToolEdit, "src/parser.go"), 2.5)
	x.ev(result("r3", "t3", true, ""), 2.6)
	x.ev(call("c4", "t4", "Bash", agents.ToolShell, "Run the parser tests"), 3.0)
	if got := without(x.tick(3.9)); len(got) != 0 {
		t.Fatalf("throttle: %s", types(got))
	}
	as = x.tick(4)
	ed := find[turn.EditProgress](t, as)
	golden(t, "edit_running", ed.HTML)
	if got := without(x.tick(8)); len(got) != 0 {
		t.Errorf("no change, no edit: %s", types(got))
	}

	x.ev(result("r4", "t4", false, "boom"), 70)
	as = x.ev(text("a1", agents.Final, "Done."), 72)
	if types(as) != "Post,FinishProgress" {
		t.Fatalf("got %s", types(as))
	}
	golden(t, "summary_failed", as[1].(turn.FinishProgress).HTML)
}

func TestCollapseRowCapAndSummaryMore(t *testing.T) {
	cfg := turn.Config{MaxRows: 3, MaxSummaryRows: 4}
	x := newH(t, cfg, progress)
	x.ev(prompt("p", "go"), 0)
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("r%d", i)
		x.ev(call("k"+id, id, "Read", agents.ToolRead, fmt.Sprintf("f%d.go", i)), 0.1)
	}
	x.ev(result("o0", "r0", true, ""), 0.2)
	x.ev(result("o1", "r1", true, ""), 0.2)
	as := x.tick(2)
	html := find[turn.ShowProgress](t, as).HTML
	if !strings.Contains(html, "▸ 📖 Read ×3 · f2.go") {
		t.Errorf("collapse running: %s", html)
	}
	x.ev(result("o2", "r2", false, ""), 2.1)
	x.ev(call("kb", "b", "Bash", agents.ToolShell, ""), 2.2)
	x.ev(call("kg", "g", "Grep", agents.ToolSearch, "x"), 2.3)
	x.ev(call("kw", "w", "WebFetch", agents.ToolWeb, "u"), 2.3)
	x.ev(call("ka", "a", "Task", agents.ToolSubagent, "sub"), 2.3)
	x.ev(call("kp", "p", "Todo", agents.ToolPlan, "plan"), 2.3)
	x.ev(call("kx", "x", "Mystery", agents.ToolOther, "?"), 2.3)
	html = find[turn.EditProgress](t, x.tick(4.5)).HTML
	golden(t, "row_cap", html)
	as = x.ev(text("a", agents.Final, "ok"), 5)
	golden(t, "summary_more", find[turn.FinishProgress](t, as).HTML)
}

func TestRepostAfterPost(t *testing.T) {
	x := newH(t, turn.Config{}, turn.Settings{Mode: "progress", Verbose: "on"})
	x.ev(prompt("p", "go"), 0)
	x.tick(2)
	as := x.ev(call("c1", "t1", "Read", agents.ToolRead, "a.go"), 2.5)
	if types(as) != "Post" {
		t.Fatalf("got %s", types(as))
	}
	if got := without(x.tick(3)); len(got) != 0 {
		t.Fatalf("throttled: %s", types(got))
	}
	as = x.tick(4)
	find[turn.RepostProgress](t, as)
	if got := without(x.tick(6.5)); len(got) != 0 {
		t.Errorf("repost once: %s", types(got))
	}
	// the answer ends the turn: finish, no repost
	as = x.ev(text("a", agents.Final, "x"), 7)
	if types(as) != "Post,FinishProgress" {
		t.Errorf("got %s", types(as))
	}
}

func TestIdleGraceCommentaryBecomesAnswer(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p", "go"), 0)
	x.ev(call("c1", "t1", "Read", agents.ToolRead, "a.go"), 0.5)
	x.ev(result("r1", "t1", true, ""), 0.6)
	x.ev(text("m1", agents.Commentary, "The answer is 42"), 1)
	x.tick(2)
	if as := x.k.State(turn.StateIdle, at(10)); len(as) != 0 {
		t.Fatalf("state idle: %s", types(as))
	}
	if got := x.tick(14.9); len(got) != 0 {
		t.Fatalf("within grace: %s", types(got))
	}
	as := x.tick(15)
	if types(as) != "Post,FinishProgress" {
		t.Fatalf("got %s", types(as))
	}
	p := as[0].(turn.Post)
	if p.Key != "m1" || p.Kind != "answer" || p.Markdown != "The answer is 42" || p.Silent {
		t.Errorf("post = %+v", p)
	}
	fin := as[1].(turn.FinishProgress).HTML
	if !strings.HasPrefix(fin, "✅ Done · 10s · 1 tool\n") {
		t.Errorf("summary: %q", fin)
	}
	golden(t, "summary_idle_commentary", fin)
}

func TestCommentaryBeforeToolIsNotAnswer(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p", "go"), 0)
	x.ev(text("m1", agents.Commentary, "Looking"), 0.5)
	x.ev(call("c1", "t1", "Read", agents.ToolRead, "a.go"), 1)
	x.tick(2)
	x.k.State(turn.StateDone, at(3))
	as := x.tick(8)
	if types(as) != "FinishProgress" {
		t.Fatalf("got %s", types(as))
	}
	if !strings.HasPrefix(as[0].(turn.FinishProgress).HTML, "✅ Done · 3s · 1 tool") {
		t.Errorf("%q", as[0])
	}
}

func TestIdleNothingIsInterrupted(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.k.State(turn.StateWorking, at(0))
	x.ev(call("c1", "t1", "Bash", agents.ToolShell, "sleep 100"), 0.1)
	x.tick(2)
	x.k.State(turn.StateIdle, at(72))
	as := x.tick(77)
	if types(as) != "FinishProgress" {
		t.Fatalf("got %s", types(as))
	}
	golden(t, "summary_interrupted", as[0].(turn.FinishProgress).HTML)
}

func TestNeverShownNothingFinished(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.k.State(turn.StateWorking, at(0))
	x.k.State(turn.StateIdle, at(1))
	if as := x.tick(7); len(as) != 0 {
		t.Errorf("got %s", types(as))
	}
}

func TestStateErrorAndErrorAnswer(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p", "go"), 0)
	x.tick(2)
	x.k.State(turn.StateError, at(5))
	as := x.tick(10)
	if types(as) != "FinishProgress" || as[0].(turn.FinishProgress).HTML != "❌ Error" {
		t.Errorf("state error: %v", as)
	}

	y := newH(t, turn.Config{}, progress)
	y.ev(prompt("p", "go"), 0)
	y.ev(call("c", "t", "Read", agents.ToolRead, "a.go"), 0.5)
	y.tick(2)
	as = y.ev(text("e1", agents.Error, "rate limit <429>\nmore"), 3)
	if types(as) != "Post,FinishProgress" {
		t.Fatalf("got %s", types(as))
	}
	if p := as[0].(turn.Post); p.Kind != "answer" || p.Key != "e1" || p.Markdown != "rate limit <429>\nmore" {
		t.Errorf("post %+v", p)
	}
	golden(t, "summary_error", as[1].(turn.FinishProgress).HTML)
}

func TestLateFinalCorrectsSummary(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p", "go"), 0)
	x.tick(2)
	x.k.State(turn.StateIdle, at(10))
	as := x.tick(15)
	if got := as[0].(turn.FinishProgress).HTML; got != "⏹ Interrupted · 10s · 0 tools" {
		t.Fatalf("first summary %q", got)
	}
	as = x.ev(text("a", agents.Final, "Late answer"), 20)
	if types(as) != "Post,FinishProgress" {
		t.Fatalf("got %s", types(as))
	}
	if got := as[1].(turn.FinishProgress).HTML; got != "✅ Done · 10s · 0 tools" {
		t.Errorf("corrected %q", got)
	}
	// A new prompt starts a fresh turn; its final does not finish the old message.
	x.ev(prompt("p2", "again"), 30)
	as = x.ev(text("a2", agents.Final, "quick"), 31)
	if types(as) != "Post" {
		t.Errorf("got %s", types(as))
	}
}

func TestFinalDuringGrace(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p", "go"), 0)
	x.tick(2)
	x.k.State(turn.StateDone, at(5))
	as := x.ev(text("a", agents.Final, "ans"), 6)
	if types(as) != "Post,FinishProgress" {
		t.Fatalf("got %s", types(as))
	}
	if got := x.tick(20); len(got) != 0 {
		t.Errorf("after: %s", types(got))
	}
}

func TestWorkingDuringGraceResumes(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p", "go"), 0)
	x.tick(2)
	x.k.State(turn.StateIdle, at(5))
	x.k.State(turn.StateWorking, at(6))
	if got := without(x.tick(12)); len(got) != 0 {
		t.Errorf("got %s", types(got))
	}
}

func TestNewPromptAfterEndedTurnFinishesIt(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p", "go"), 0)
	x.ev(text("m", agents.Commentary, "partial"), 1)
	x.tick(2)
	x.k.State(turn.StateIdle, at(3))
	as := x.ev(prompt("p2", "next"), 4)
	if types(as) != "Post,FinishProgress" {
		t.Fatalf("got %s", types(as))
	}
	if as[0].(turn.Post).Markdown != "partial" {
		t.Errorf("%+v", as[0])
	}
}

func TestBlockAndFinalModes(t *testing.T) {
	for _, mode := range []string{"block", "final"} {
		t.Run(mode, func(t *testing.T) {
			x := newH(t, turn.Config{}, turn.Settings{Mode: mode})
			x.ev(prompt("p", "go"), 0)
			as := x.ev(text("m1", agents.Commentary, "thinking out loud"), 1)
			if mode == "block" {
				if len(as) != 1 || as[0].(turn.Post).Kind != "block" || as[0].(turn.Post).Key != "m1" {
					t.Errorf("block post: %v", as)
				}
			} else if len(as) != 0 {
				t.Errorf("final mode commentary: %v", as)
			}
			x.ev(call("c", "t", "Read", agents.ToolRead, "a"), 2)
			for _, s := range []float64{3, 10, 30} {
				for _, a := range x.tick(s) {
					if _, ok := a.(turn.Typing); !ok {
						t.Errorf("unexpected %T", a)
					}
				}
			}
			x.k.State(turn.StateIdle, at(40))
			if as := x.tick(50); len(as) != 0 {
				t.Errorf("idle in %s mode: %s", mode, types(as))
			}
			as = x.ev(text("a", agents.Final, "ans"), 60)
			if types(as) != "Post" {
				t.Errorf("answer: %s", types(as))
			}
		})
	}
}

func TestVerbose(t *testing.T) {
	for _, v := range []string{"off", "on", "full"} {
		t.Run(v, func(t *testing.T) {
			x := newH(t, turn.Config{}, turn.Settings{Mode: "final", Verbose: v})
			as := x.ev(call("c1", "t1", "Bash", agents.ToolShell, "ls"), 0)
			as2 := x.ev(result("r1", "t1", true, "a & b <c>"), 1)
			switch v {
			case "off":
				if len(as)+len(as2) != 0 {
					t.Errorf("off posted")
				}
			case "on":
				p := as[0].(turn.Post)
				if p.Kind != "tool" || p.Markdown != "🔧 Bash · ls" || !p.Silent || p.Key != "c1" {
					t.Errorf("%+v", p)
				}
				if len(as2) != 0 {
					t.Errorf("on posted output")
				}
			case "full":
				if as[0].(turn.Post).Kind != "tool" {
					t.Errorf("%+v", as[0])
				}
				p := as2[0].(turn.Post)
				want := "<b>Bash</b> output:\n<blockquote expandable>a &amp; b &lt;c&gt;</blockquote>"
				if p.Kind != "output" || p.HTML != want || !p.Silent || p.Key != "r1" {
					t.Errorf("%+v", p)
				}
			}
		})
	}
	// No label: no separator.
	x := newH(t, turn.Config{}, turn.Settings{Mode: "final", Verbose: "on"})
	if p := x.ev(call("c", "t", "Task", agents.ToolSubagent, ""), 0)[0].(turn.Post); p.Markdown != "🔧 Task" {
		t.Errorf("%q", p.Markdown)
	}
}

func TestReasoning(t *testing.T) {
	th := agents.Event{Kind: agents.Thinking, Key: "k", Text: "pondering"}
	on := newH(t, turn.Config{}, turn.Settings{Mode: "final", Reasoning: true})
	as := on.ev(th, 0)
	if p := as[0].(turn.Post); p.Kind != "reasoning" || p.Markdown != "_pondering_" || !p.Silent || p.Key != "k" {
		t.Errorf("%+v", p)
	}
	off := newH(t, turn.Config{}, turn.Settings{Mode: "final"})
	if as := off.ev(th, 0); len(as) != 0 {
		t.Errorf("off posted")
	}
	// thinking still starts a turn: typing shows
	if types(off.tick(0)) != "Typing" {
		t.Errorf("no typing")
	}
}

func TestMacPromptVersusEcho(t *testing.T) {
	x := newH(t, turn.Config{}, turn.Settings{Mode: "progress", ShowMacPrompts: true})
	as := x.k.Event(prompt("p1", "from the mac"), false, at(0))
	p := as[0].(turn.Post)
	if p.Kind != "prompt" || p.Markdown != "💻 You (on Mac): from the mac" || p.Silent || p.Key != "p1" {
		t.Errorf("%+v", p)
	}
	if as := x.k.Event(prompt("p2", "from telegram"), true, at(1)); len(as) != 0 {
		t.Errorf("echo posted")
	}
	off := newH(t, turn.Config{}, progress)
	if as := off.k.Event(prompt("p", "x"), false, at(0)); len(as) != 0 {
		t.Errorf("ShowMacPrompts off posted")
	}
}

func TestTypingCadence(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	if got := x.tick(0); got != nil {
		t.Fatalf("no turn: %s", types(got))
	}
	x.k.State(turn.StateWorking, at(0))
	var times []float64
	for s := 0.0; s <= 16; s += 1 {
		for _, a := range x.tick(s) {
			if _, ok := a.(turn.Typing); ok {
				times = append(times, s)
			}
		}
	}
	if fmt.Sprint(times) != "[0 5 10 15]" {
		t.Errorf("typing at %v", times)
	}
	x.k.State(turn.StateIdle, at(16))
	if as := x.tick(18); types(as) != "" {
		t.Errorf("typing must stop at idle: %s", types(as))
	}
}

func TestBlockedHeadline(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p", "go"), 0)
	x.ev(text("m", agents.Commentary, "Running things"), 0.5)
	x.tick(2)
	x.k.State(turn.StateBlocked, at(3))
	as := x.tick(5)
	if types(as) != "EditProgress" {
		t.Fatalf("got %s (typing must stop)", types(as))
	}
	html := as[0].(turn.EditProgress).HTML
	golden(t, "blocked", html)
	if got := x.tick(10); len(got) != 0 {
		t.Errorf("blocked tick: %s", types(got))
	}
	x.ev(call("c", "t", "Read", agents.ToolRead, "a"), 11)
	as = x.tick(13)
	if types(as) != "Typing,EditProgress" {
		t.Fatalf("after unblock: %s", types(as))
	}
	if strings.Contains(as[1].(turn.EditProgress).HTML, "Waiting") || !strings.HasPrefix(as[1].(turn.EditProgress).HTML, "⚙️ Running things") {
		t.Errorf("%s", as[1])
	}
}

func TestProgressGone(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p", "go"), 0)
	x.tick(2)
	x.k.ProgressGone()
	as := x.tick(3)
	find[turn.ShowProgress](t, as)
	if got := without(x.tick(10)); len(got) != 0 {
		t.Errorf("got %s", types(got))
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	cfg := turn.Config{}
	s := turn.Settings{Mode: "progress", Verbose: "on"}
	x := newH(t, cfg, s)
	x.ev(prompt("p", "go"), 0)
	x.ev(text("m", agents.Commentary, "Headline here"), 0.2)
	x.ev(call("c1", "t1", "Read", agents.ToolRead, "a.go"), 0.5)
	x.tick(2)
	x.ev(call("c2", "t2", "Bash", agents.ToolShell, "go test"), 2.5) // posts, pending repost + dirty

	raw, err := json.Marshal(x.k.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var snap turn.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	y := turn.Restore(cfg, s, snap)

	if got := without(y.Tick(at(3))); len(got) != 0 {
		t.Errorf("throttle must survive: %s", types(got))
	}
	a := x.tick(4.5)
	b := y.Tick(at(4.5))
	if fmt.Sprint(a) != fmt.Sprint(b) || types(b) != "RepostProgress" {
		t.Errorf("restored differs:\n%v\n%v", a, b)
	}
	y.Event(result("r2", "t2", true, ""), false, at(5))
	as := y.Event(text("a", agents.Final, "ok"), false, at(6))
	fin := find[turn.FinishProgress](t, as).HTML
	if !strings.HasPrefix(fin, "✅ Done · 6s · 2 tools") {
		t.Errorf("%q", fin)
	}
}

func TestEscapingAndRedact(t *testing.T) {
	redact := func(s string) string { return strings.ReplaceAll(s, "sk-SECRET", "[redacted]") }
	x := newH(t, turn.Config{Redact: redact}, turn.Settings{Mode: "progress", Verbose: "full", Reasoning: true, ShowMacPrompts: true})
	x.ev(prompt("p", "use sk-SECRET"), 0)
	x.ev(text("m", agents.Commentary, "Checking <b> & sk-SECRET"), 0.1)
	x.ev(call("c", "t", "Bash", agents.ToolShell, "echo '<x>' & sk-SECRET"), 0.2)
	html := find[turn.ShowProgress](t, x.tick(2)).HTML
	golden(t, "escape_redact", html)

	if p := x.ev(result("r", "t", true, "<out> sk-SECRET"), 3)[0].(turn.Post); !strings.Contains(p.HTML, "&lt;out&gt; [redacted]") {
		t.Errorf("%s", p.HTML)
	}
	if p := x.ev(agents.Event{Kind: agents.ToolCall, Key: "c9", Tool: &agents.Tool{Name: "N", Label: "sk-SECRET"}}, 3.1)[0].(turn.Post); p.Markdown != "🔧 N · [redacted]" {
		t.Errorf("%s", p.Markdown)
	}
	as := x.ev(text("e", agents.Error, "bad <sk-SECRET>"), 4)
	fin := find[turn.FinishProgress](t, as).HTML
	if !strings.HasPrefix(fin, "❌ Error: bad &lt;[redacted]&gt;") {
		t.Errorf("%s", fin)
	}
}

func TestTruncationAndElapsed(t *testing.T) {
	x := newH(t, turn.Config{MaxRowRunes: 20}, progress)
	x.ev(prompt("p", "go"), 0)
	x.ev(text("m", agents.Commentary, strings.Repeat("é", 50)), 0)
	x.ev(call("c", "t", "Read", agents.ToolRead, strings.Repeat("x", 100)), 0)
	html := find[turn.ShowProgress](t, x.tick(2)).HTML
	for _, l := range strings.Split(html, "\n")[:2] {
		if n := len([]rune(l)); n > 20+len([]rune("⚙️ ")) {
			t.Errorf("line too long (%d): %s", n, l)
		}
	}
	if !strings.Contains(html, "…") {
		t.Errorf("no ellipsis: %s", html)
	}
	y := newH(t, turn.Config{}, progress)
	y.ev(prompt("p", "go"), 0)
	y.ev(call("c", "t", "Read", agents.ToolRead, "q"), 0)
	for _, c := range []struct {
		sec  float64
		want string
	}{{8, "⏱ 8s · 1 tool"}, {72, "⏱ 1m 12s · 1 tool"}, {3780, "⏱ 1h 3m · 1 tool"}} {
		y.k.ProgressGone()
		html := find[turn.ShowProgress](t, y.tick(c.sec)).HTML
		if !strings.HasSuffix(html, c.want) {
			t.Errorf("%s: want suffix %q", html, c.want)
		}
	}
}
