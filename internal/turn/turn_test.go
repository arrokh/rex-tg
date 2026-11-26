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
	if types(as) != "Post" {
		t.Fatalf("finish must be held: %s", types(as))
	}
	golden(t, "summary_more", find[turn.FinishProgress](t, x.tick(6.5)).HTML)
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

func TestSteeredPromptCommentaryIsPostedOutsideProgress(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.k.Event(prompt("p1", "start work"), true, at(0))
	x.ev(call("c1", "t1", "Read", agents.ToolRead, "a.go"), 0.5)
	x.ev(text("m1", agents.Commentary, "Working through the task"), 0.6)
	x.ev(call("c2", "t2", "Bash", agents.ToolShell, "run tests"), 1)
	show := find[turn.ShowProgress](t, x.tick(2))
	if !strings.Contains(show.HTML, "Working through the task") {
		t.Fatalf("initial commentary should remain in progress: %q", show.HTML)
	}

	// This prompt joins the running Telegram turn. Its first commentary is a
	// reply to the new prompt, not another progress headline.
	x.k.Event(prompt("p2", "quick question"), true, at(2.2))
	x.k.SteeredPrompt()
	x.k = turn.Restore(turn.Config{}, progress, x.k.Snapshot())
	if _, ok := x.k.Streaming(); ok {
		t.Fatal("stale draft remained open while waiting for the steered prompt reply")
	}
	actions := x.k.Event(text("m2", agents.Commentary, "Here is the answer"), false, at(2.5))
	if types(actions) != "Post" {
		t.Fatalf("commentary after a steered prompt: got %s, want a separate Post", types(actions))
	}
	answer := find[turn.Post](t, actions)
	if answer.Key != "m2" || answer.Kind != "answer" || answer.Markdown != "Here is the answer" || answer.Silent {
		t.Fatalf("separate answer = %+v", answer)
	}

	x.ev(call("c3", "t3", "Edit", agents.ToolEdit, "a.go"), 2.6)
	repost := find[turn.RepostProgress](t, x.tick(4))
	if strings.Contains(repost.HTML, "Here is the answer") || strings.Contains(repost.HTML, "Working through the task") {
		t.Fatalf("progress still contains a reply or stale commentary: %q", repost.HTML)
	}
	if !strings.Contains(repost.HTML, "Working…") {
		t.Fatalf("progress should show work without a commentary headline: %q", repost.HTML)
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
	as = y.ev(text("e1", agents.Error, "rate limit <429>\nmore"), 4)
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
	// The turn goes on: only the timer moves, nothing is finished.
	if got := types(without(x.tick(12))); got != "EditProgress" {
		t.Errorf("got %s", got)
	}
}

// TestTimerMovesWhileTheAgentThinks: with no tool and no commentary, the
// progress message still edits its elapsed time, at a step that grows with
// the turn, and not while the turn waits for the user's answer.
func TestTimerMovesWhileTheAgentThinks(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p", "go"), 0)
	if got := types(without(x.tick(2))); got != "ShowProgress" {
		t.Fatalf("show: got %s", got)
	}
	if got := without(x.tick(11)); len(got) != 0 {
		t.Fatalf("before the step: got %s", types(got))
	}
	as := without(x.tick(12))
	if types(as) != "EditProgress" || !strings.Contains(as[0].(turn.EditProgress).HTML, "⏱ 12s · 0 tools") {
		t.Fatalf("after 10s: got %+v", as)
	}
	// At 3 minutes the step is a sixth of the elapsed time: about 36s.
	x.tick(180)
	if got := without(x.tick(215)); len(got) != 0 {
		t.Fatalf("within the step: got %s", types(got))
	}
	as = without(x.tick(216))
	if types(as) != "EditProgress" || !strings.Contains(as[0].(turn.EditProgress).HTML, "⏱ 3m 36s") {
		t.Fatalf("after the step: got %+v", as)
	}
	// Past 6 minutes the step stays at one minute.
	x.tick(600)
	if got := without(x.tick(659)); len(got) != 0 {
		t.Fatalf("within the minute: got %s", types(got))
	}
	if got := types(without(x.tick(660))); got != "EditProgress" {
		t.Fatalf("after a minute: got %s", got)
	}
}

func TestTimerWaitsWhileBlocked(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p", "go"), 0)
	x.tick(2)
	x.k.State(turn.StateBlocked, at(3))
	x.tick(5) // the edit to "Waiting for your answer"
	for _, s := range []float64{20, 100, 400} {
		if got := without(x.tick(s)); len(got) != 0 {
			t.Fatalf("tick %v while blocked: got %s", s, types(got))
		}
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

// A prompt that the agent's own program wrote (a background task's
// notification) is not something the user typed on the Mac: it is never
// posted, but it still starts a turn, so the agent's answer to it is sent.
func TestAutoPromptStartsATurnUnseen(t *testing.T) {
	x := newH(t, turn.Config{}, turn.Settings{Mode: "progress", ShowMacPrompts: true})
	e := prompt("n1", "<task-notification>done</task-notification>")
	e.Auto = true
	if as := x.k.Event(e, false, at(0)); len(as) != 0 {
		t.Fatalf("auto prompt posted: %+v", as)
	}
	if types(x.tick(0)) != "Typing" {
		t.Errorf("auto prompt did not start a turn")
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
	if strings.Contains(as[1].(turn.EditProgress).HTML, "Waiting") || !strings.Contains(strings.SplitN(as[1].(turn.EditProgress).HTML, "\n", 2)[0], " Running things") {
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
	as := y.Event(text("a", agents.Final, "ok"), false, at(7))
	fin := find[turn.FinishProgress](t, as).HTML
	if !strings.HasPrefix(fin, "✅ Done · 7s · 2 tools") {
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
		if n := len([]rune(l)); n > 20+len([]rune("⏳ ")) {
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

func TestFinishHeldThenEmittedOnTick(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p", "go"), 0)
	x.tick(2) // show
	x.ev(call("c", "t", "Read", agents.ToolRead, "a"), 2.5)
	find[turn.EditProgress](t, x.tick(4)) // edit at 4
	as := x.ev(text("a", agents.Final, "ans"), 4.2)
	if types(as) != "Post" || as[0].(turn.Post).Kind != "answer" {
		t.Fatalf("answer must be immediate: %s", types(as))
	}
	if got := without(x.tick(5.9)); len(got) != 0 {
		t.Fatalf("too early: %s", types(got))
	}
	as = without(x.tick(6))
	if types(as) != "FinishProgress" || !strings.HasPrefix(as[0].(turn.FinishProgress).HTML, "✅ Done · 4s · 1 tool") {
		t.Fatalf("got %v", as)
	}
	if got := x.tick(20); len(got) != 0 {
		t.Errorf("emitted twice: %s", types(got))
	}
}

func TestFinishImmediateWhenLastEditOld(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p", "go"), 0)
	x.tick(2)
	if as := x.ev(text("a", agents.Final, "ans"), 4); types(as) != "Post,FinishProgress" {
		t.Errorf("got %s", types(as))
	}
}

func TestLateFinalCorrectionIsHeld(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p", "go"), 0)
	x.tick(2)
	x.k.State(turn.StateIdle, at(10))
	x.tick(15) // interrupted summary at 15
	as := x.ev(text("a", agents.Final, "late"), 16)
	if types(as) != "Post" {
		t.Fatalf("got %s", types(as))
	}
	as = x.tick(17)
	if types(as) != "FinishProgress" || as[0].(turn.FinishProgress).HTML != "✅ Done · 10s · 0 tools" {
		t.Errorf("got %v", as)
	}
}

func TestNewTurnFlushesHeldFinish(t *testing.T) {
	for name, start := range map[string]func(x *h) []turn.Action{
		"prompt": func(x *h) []turn.Action { return x.ev(prompt("p2", "next"), 4.5) },
		"state":  func(x *h) []turn.Action { return x.k.State(turn.StateWorking, at(4.5)) },
		"tool": func(x *h) []turn.Action {
			return x.ev(call("c2", "t2", "Read", agents.ToolRead, "b"), 4.5)
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := newH(t, turn.Config{}, progress)
			x.ev(prompt("p", "go"), 0)
			x.tick(2)
			x.ev(text("a", agents.Final, "ans"), 3) // held
			as := start(x)
			if len(as) == 0 || types(as[:1]) != "FinishProgress" {
				t.Fatalf("held finish must come first: %s", types(as))
			}
			if !strings.HasPrefix(as[0].(turn.FinishProgress).HTML, "✅ Done · 3s") {
				t.Errorf("%v", as[0])
			}
			// The new turn then shows a fresh progress message.
			find[turn.ShowProgress](t, x.tick(6.6))
		})
	}
}

func TestSnapshotRoundTripHeldFinish(t *testing.T) {
	cfg := turn.Config{}
	x := newH(t, cfg, progress)
	x.ev(prompt("p", "go"), 0)
	x.tick(2)
	x.ev(text("a", agents.Final, "ans"), 3)
	raw, err := json.Marshal(x.k.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var snap turn.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	y := turn.Restore(cfg, progress, snap)
	if got := y.Tick(at(3.5)); len(got) != 0 {
		t.Fatalf("too early: %s", types(got))
	}
	as := y.Tick(at(4))
	if types(as) != "FinishProgress" || !strings.HasPrefix(as[0].(turn.FinishProgress).HTML, "✅ Done · 3s") {
		t.Errorf("got %v", as)
	}
}

// A prompt of several lines starts on its own line, so a code fence at its
// start still opens a code block.
func TestMacPromptOfSeveralLines(t *testing.T) {
	x := newH(t, turn.Config{}, turn.Settings{Mode: "progress", ShowMacPrompts: true})
	as := x.k.Event(prompt("p1", "```go\nfmt.Println(1)\n```"), false, at(0))
	if p := as[0].(turn.Post); p.Markdown != "💻 You (on Mac):\n\n```go\nfmt.Println(1)\n```" {
		t.Errorf("markdown = %q", p.Markdown)
	}
}

// A turn whose agent stopped ends with a "stopped" summary.
func TestStoppedTurnSummary(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p", "go"), 0)
	x.tick(2)
	x.k.State(turn.StateStopped, at(7))
	if as := x.tick(8); len(as) != 0 {
		t.Fatalf("ended before the short grace: %v", types(as))
	}
	as := x.tick(9.5) // the grace for a stopped agent is 2 s, not the 5 s for an answer
	if len(as) == 0 {
		t.Fatal("no action after the stop")
	}
	if got := as[0].(turn.FinishProgress).HTML; got != "🔴 Stopped · 7s · 0 tools" {
		t.Fatalf("summary %q", got)
	}
}

// macHold is a topic that holds the turns that start on the Mac.
var macHold = turn.Settings{Mode: "progress", ShowMacPrompts: true, MacNotice: true}

// ticks runs Tick every second from a to b and returns all actions.
func (x *h) ticks(a, b float64) []turn.Action {
	var out []turn.Action
	for s := a; s <= b; s++ {
		out = append(out, x.tick(s)...)
	}
	return out
}

func TestMacTurnIsHeldBehindOneNotice(t *testing.T) {
	x := newH(t, turn.Config{}, macHold)
	as := x.ev(prompt("p1", "fix the login page"), 0)
	if types(as) != "MacNotice" {
		t.Fatalf("Mac prompt: %s, want one MacNotice", types(as))
	}
	n := find[turn.MacNotice](t, as)
	for _, want := range []string{"In use on the Mac", "working…", "fix the login page", "Write here to go on in Telegram"} {
		if !strings.Contains(n.HTML, want) {
			t.Errorf("notice %q lacks %q", n.HTML, want)
		}
	}
	got := append(x.ev(call("c1", "t1", "Read", agents.ToolRead, "a.go"), 1), x.ticks(1, 10)...)
	got = append(got, x.ev(text("a1", agents.Final, "Fixed it."), 11)...)
	if s := types(got); s != "HeldAnswer,MacNotice" {
		t.Fatalf("held turn: %s, want only HeldAnswer,MacNotice", s)
	}
	if a := find[turn.HeldAnswer](t, got); a.Key != "a1" || a.Markdown != "Fixed it." {
		t.Errorf("held answer = %+v", a)
	}
	if n := find[turn.MacNotice](t, got); !strings.Contains(n.HTML, "✅ done") {
		t.Errorf("end notice = %q, want done", n.HTML)
	}

	// The next turn on the Mac edits the same notice.
	as = x.ev(prompt("p2", "now the signup page"), 20)
	if n := find[turn.MacNotice](t, as); !strings.Contains(n.HTML, "now the signup page") || !strings.Contains(n.HTML, "working…") {
		t.Errorf("second notice = %q", n.HTML)
	}
}

func TestTelegramMessageEndsTheMacHold(t *testing.T) {
	x := newH(t, turn.Config{}, macHold)
	x.ev(prompt("p1", "on the mac"), 0)
	x.ev(text("a1", agents.Final, "ok"), 1)

	// Typing a Telegram message reports "working" before its prompt line.
	x.k.State(turn.StateWorking, at(10))
	if got := x.tick(10); got != nil {
		t.Errorf("held before the echo: %s", types(got))
	}
	as := x.k.Event(prompt("p2", "from the phone"), true, at(10.5))
	if types(as) != "MacBack" {
		t.Fatalf("echo: %s, want MacBack", types(as))
	}
	got := x.ticks(11, 13)
	if !strings.Contains(types(got), "Typing") || !strings.Contains(types(got), "ShowProgress") {
		t.Errorf("Telegram turn not shown: %s", types(got))
	}
	as = x.ev(text("a2", agents.Final, "done"), 14)
	if p := find[turn.Post](t, as); p.Kind != "answer" || p.Markdown != "done" {
		t.Errorf("answer = %+v, want it posted", p)
	}

	// A new Mac prompt starts a new hold with a new notice.
	if as := x.ev(prompt("p3", "back at the desk"), 30); types(as) != "MacNotice" {
		t.Errorf("new hold: %s", types(as))
	}
}

func TestMacPromptDuringATelegramTurnKeepsItShown(t *testing.T) {
	x := newH(t, turn.Config{}, macHold)
	x.k.Event(prompt("p1", "from the phone"), true, at(0))
	x.ticks(0, 3)
	as := x.ev(prompt("p2", "steer from the mac"), 4)
	if types(as) != "MacNotice" {
		t.Fatalf("Mac prompt mid-turn: %s", types(as))
	}
	as = x.ev(text("a1", agents.Final, "answer for the phone"), 5)
	if p := find[turn.Post](t, as); p.Markdown != "answer for the phone" {
		t.Errorf("the Telegram turn's answer was held: %s", types(as))
	}
	if n := find[turn.MacNotice](t, as); !strings.Contains(n.HTML, "done") {
		t.Errorf("the notice stays at working after the turn ended: %s", types(as))
	}
}

func TestAutomaticTurnDuringTheMacHoldIsHeld(t *testing.T) {
	x := newH(t, turn.Config{}, macHold)
	x.ev(prompt("p1", "on the mac"), 0)
	x.ev(text("a1", agents.Final, "ok"), 1)
	e := prompt("n1", "<task-notification>x</task-notification>")
	e.Auto = true
	got := append(x.ev(e, 10), x.ticks(10, 14)...)
	got = append(got, x.ev(text("a2", agents.Final, "the task ended"), 15)...)
	if s := types(got); s != "HeldAnswer" {
		t.Errorf("automatic turn on the Mac: %s, want only HeldAnswer", s)
	}
}

func TestMacHoldSurvivesARestart(t *testing.T) {
	cfg := turn.Config{}
	x := newH(t, cfg, macHold)
	x.ev(prompt("p1", "on the mac"), 0)
	b, err := json.Marshal(x.k.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var snap turn.Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}
	x.k = turn.Restore(cfg, macHold, snap)
	got := append(x.ticks(1, 5), x.ev(text("a1", agents.Final, "ok"), 6)...)
	if s := types(got); s != "HeldAnswer,MacNotice" {
		t.Errorf("after restore: %s", s)
	}
	if as := x.k.Event(prompt("p2", "phone"), true, at(10)); types(as) != "MacBack" {
		t.Errorf("echo after restore: %s", types(as))
	}
}

func TestMacHoldEndsWhenTheSettingIsOffAfterARestart(t *testing.T) {
	x := newH(t, turn.Config{}, macHold)
	x.ev(prompt("p1", "on the mac"), 0)
	off := turn.Settings{Mode: "progress", ShowMacPrompts: true}
	x.k = turn.Restore(turn.Config{}, off, x.k.Snapshot())
	as := x.ev(text("a1", agents.Final, "the answer"), 5)
	if p := find[turn.Post](t, as); p.Markdown != "the answer" {
		t.Errorf("with mac_notice off, the answer of a turn held before is %s, want posted", types(as))
	}
	if strings.Contains(types(as), "MacNotice") || strings.Contains(types(as), "HeldAnswer") {
		t.Errorf("hold actions with mac_notice off: %s", types(as))
	}
}

func TestMacNoticeOffShowsMacTurns(t *testing.T) {
	x := newH(t, turn.Config{}, turn.Settings{Mode: "progress", ShowMacPrompts: true})
	as := x.ev(prompt("p1", "typed on the mac"), 0)
	if p := find[turn.Post](t, as); p.Kind != "prompt" {
		t.Fatalf("prompt not posted: %s", types(as))
	}
	if as := x.ev(text("a1", agents.Final, "ok"), 1); types(as) != "Post" {
		t.Errorf("answer: %s", types(as))
	}
}

// A draft belongs to a running turn that Telegram sees, until its answer is
// out. It shows the latest commentary, redacted; block mode has none, and
// final mode no draft.
func TestStreaming(t *testing.T) {
	redact := func(s string) string { return strings.ReplaceAll(s, "SECRET", "‹redacted›") }
	x := newH(t, turn.Config{Redact: redact}, progress)
	if _, ok := x.k.Streaming(); ok {
		t.Fatal("a draft before any turn")
	}
	x.ev(call("c1", "t1", "Read", agents.ToolRead, "a.go"), 0)
	if s, ok := x.k.Streaming(); !ok || s != "" {
		t.Fatalf("running turn without commentary: %q %v", s, ok)
	}
	x.ev(text("m1", agents.Commentary, "Reading SECRET first"), 1)
	if s, ok := x.k.Streaming(); !ok || s != "Reading ‹redacted› first" {
		t.Fatalf("commentary: %q %v", s, ok)
	}
	x.ev(text("a1", agents.Final, "Done."), 2)
	if _, ok := x.k.Streaming(); ok {
		t.Fatal("a draft after the answer")
	}

	b := newH(t, turn.Config{}, turn.Settings{Mode: "block"})
	b.ev(text("m1", agents.Commentary, "a block"), 0)
	if s, ok := b.k.Streaming(); !ok || s != "" {
		t.Fatalf("block mode: %q %v", s, ok)
	}

	f := newH(t, turn.Config{}, turn.Settings{Mode: "final"})
	f.ev(text("m1", agents.Commentary, "not in final mode"), 0)
	if _, ok := f.k.Streaming(); ok {
		t.Fatal("a draft in final mode")
	}

	m := newH(t, turn.Config{}, macHold)
	m.ev(prompt("p1", "on the mac"), 0)
	m.ev(text("m1", agents.Commentary, "held"), 1)
	if _, ok := m.k.Streaming(); ok {
		t.Fatal("a draft for a turn held for the Mac")
	}
}

// TestMessageBelowMovesTheProgressDown: a message from outside the turn
// below the progress message moves it down at the next edit, but not while
// the turn waits for an answer: then the alert stays the last message.
func TestMessageBelowMovesTheProgressDown(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p", "go"), 0)
	x.tick(2) // shown
	x.k.MessageBelow()
	if got := types(without(x.tick(4))); got != "RepostProgress" {
		t.Fatalf("after a message below: got %s", got)
	}
	x.k.State(turn.StateBlocked, at(5))
	x.k.MessageBelow() // the alert
	for _, s := range []float64{7, 9, 30} {
		for _, a := range x.tick(s) {
			if _, ok := a.(turn.RepostProgress); ok {
				t.Fatalf("tick %v: the progress moved below the waiting alert", s)
			}
		}
	}
	x.k.State(turn.StateWorking, at(31))
	if got := types(without(x.tick(33))); got != "RepostProgress" {
		t.Fatalf("after the answer: got %s", got)
	}
}

// TestSpinnerTurnsWithEachEdit: the hourglass turns one step with each edit
// that is sent anyway, and without an animation it never causes one itself.
func TestSpinnerTurnsWithEachEdit(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p", "go"), 0)
	if show := find[turn.ShowProgress](t, x.tick(2)).HTML; !strings.HasPrefix(show, "⏳ Working…") {
		t.Fatalf("show = %q", show)
	}
	if got := without(x.tick(4)); len(got) != 0 {
		t.Fatalf("an edit only for the spinner: %s", types(got))
	}
	for i, s := range []float64{12, 22, 32, 42} {
		as := without(x.tick(s))
		if types(as) != "EditProgress" {
			t.Fatalf("tick %v: got %s", s, types(as))
		}
		if want := []string{"⌛", "⏳", "⌛", "⏳"}[i]; !strings.HasPrefix(as[0].(turn.EditProgress).HTML, want+" ") {
			t.Errorf("tick %v: %q, want %s", s, as[0].(turn.EditProgress).HTML, want)
		}
	}
}

// TestAnimationEditsEveryInterval: with AnimateInterval, the progress
// message is edited that often while nothing else changes, also when its
// text stays (a turn over an hour shows minutes only), but not while the
// turn waits for an answer.
func TestAnimationEditsEveryInterval(t *testing.T) {
	x := newH(t, turn.Config{AnimateInterval: 3 * time.Second}, progress)
	x.ev(prompt("p", "go"), 0)
	x.tick(2) // shown: ⏳
	if got := without(x.tick(4.9)); len(got) != 0 {
		t.Fatalf("before 3s: %s", types(got))
	}
	as := without(x.tick(5))
	if types(as) != "EditProgress" || !strings.HasPrefix(as[0].(turn.EditProgress).HTML, "⌛ ") || !strings.Contains(as[0].(turn.EditProgress).HTML, "⏱ 5s") {
		t.Fatalf("at 5s: %+v", as)
	}
	if as = without(x.tick(8)); types(as) != "EditProgress" || !strings.HasPrefix(as[0].(turn.EditProgress).HTML, "⏳ ") {
		t.Fatalf("at 8s: %+v", as)
	}
	// Past an hour the time shows minutes, and the hourglass still turns.
	x.tick(3700)
	if as = without(x.tick(3703)); types(as) != "EditProgress" {
		t.Fatalf("past an hour: %s", types(as))
	}
	x.k.State(turn.StateBlocked, at(3704))
	x.tick(3706) // the edit to "Waiting for your answer"
	for _, s := range []float64{3709, 3712, 3715} {
		if got := without(x.tick(s)); len(got) != 0 {
			t.Fatalf("tick %v while blocked: %s", s, types(got))
		}
	}
}

// The headline shows the commentary's first line as plain text, without the
// Markdown marks that a message would render.
func TestHeadlineDropsMarkdownMarks(t *testing.T) {
	x := newH(t, turn.Config{}, progress)
	x.ev(prompt("p", "go"), 0)
	x.ev(text("m", agents.Commentary, "No report found for **Full Stack Engineer APAC** in `docs`\nmore"), 0.5)
	html := find[turn.ShowProgress](t, x.tick(2)).HTML
	if first := strings.SplitN(html, "\n", 2)[0]; first != "⏳ No report found for Full Stack Engineer APAC in docs" {
		t.Errorf("headline = %q", first)
	}
}

// TestShortTurnLeavesNoSummary: with SummaryAfter, a turn that ends with its
// answer sooner deletes its progress message; a longer one, an error, and a
// stop keep the summary.
func TestShortTurnLeavesNoSummary(t *testing.T) {
	cfg := turn.Config{SummaryAfter: 2 * time.Minute}
	x := newH(t, cfg, progress)
	x.ev(prompt("p", "go"), 0)
	x.ev(call("c", "t", "Read", agents.ToolRead, "a"), 1)
	x.tick(2)
	if got := types(x.ev(text("a", agents.Final, "Done."), 30)); got != "Post,RemoveProgress" {
		t.Fatalf("short turn: %s", got)
	}
	if got := x.ev(text("a2", agents.Final, "Done, corrected."), 31); strings.Contains(types(got), "Progress") {
		t.Errorf("a late answer brought the summary back: %s", types(got))
	}

	x = newH(t, cfg, progress)
	x.ev(prompt("p", "go"), 0)
	x.tick(2)
	if got := types(x.ev(text("a", agents.Final, "Done."), 150)); got != "Post,FinishProgress" {
		t.Fatalf("long turn: %s", got)
	}

	x = newH(t, cfg, progress)
	x.ev(prompt("p", "go"), 0)
	x.tick(2)
	if got := types(x.ev(text("e", agents.Error, "boom"), 10)); !strings.HasSuffix(got, "FinishProgress") {
		t.Fatalf("error: %s", got)
	}
}
