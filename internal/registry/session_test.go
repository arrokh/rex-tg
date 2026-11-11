package registry

import (
	"testing"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/rex"
	"rex-tg/internal/store"
)

func TestClaudeSessionStartedWithTailStart(t *testing.T) {
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
	ch := e.claudeHook("b1", "SessionStart", "c1", "startup", e.at(5))
	wantKinds(t, ch, SessionStarted)
	if ch[0].Start != (TailStart{FromStart: true}) {
		t.Errorf("start = %+v", ch[0].Start)
	}
	conv := e.conv(agents.Claude, "c1")
	p := e.pane("b1")
	if p.ConversationID != conv.ID || p.Source != store.SourceHook || p.PID != 100 || p.ProcessName != "2.1.322" {
		t.Errorf("pane = %+v", p)
	}
	if ch[0].PaneID != p.ID || ch[0].ConversationID != conv.ID {
		t.Errorf("change = %+v", ch[0])
	}
	if conv.Transcript != e.claudePath("c1") || conv.CWD != cwdA {
		t.Errorf("conv = %+v", conv)
	}
	// The same report again changes nothing.
	wantKinds(t, e.claudeHook("b1", "UserPromptSubmit", "c1", "", e.at(6)), StateChanged)
}

// cux starts claude as its child, so cux is in front of the pane. Claude's
// hooks from that pane count, and the pane's agent process is cux: typing
// checks that cux is still in front.
func TestCuxPaneReportsClaudeSessions(t *testing.T) {
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", cuxProc(200, cwdA))
	wantKinds(t, e.claudeHook("b1", "SessionStart", "c1", "startup", e.at(5)), SessionStarted)
	p := e.pane("b1")
	if p.ConversationID != e.conv(agents.Claude, "c1").ID || p.PID != 200 || p.ProcessName != "cux" {
		t.Fatalf("pane = %+v", p)
	}
	proc, err := e.rx.Process(e.ctx, sess1, "b1")
	if err != nil || !AgentInFront(proc, p.PID, p.ProcessName) {
		t.Fatalf("cux is not in front: %+v, %v", proc, err)
	}
	wantKinds(t, e.claudeHook("b1", "UserPromptSubmit", "c1", "", e.at(6)), StateChanged)
}

func TestNestedAgentReportRejected(t *testing.T) {
	e := newEnv(t)
	// A codex exec started by Claude inherits REX_BLOCK, and codex is in front.
	e.rx.setBlock(sess1, "b1", codexProc(300, cwdA))
	ch := e.claudeHook("b1", "SessionStart", "nested", "startup", e.at(5))
	if len(ch) != 0 {
		t.Fatalf("changes = %v", kinds(ch))
	}
	if _, ok, _ := e.st.ConversationByNative(e.ctx, agents.Claude, "nested"); ok {
		t.Error("the nested report created a conversation")
	}
}

func TestSubagentTranscriptIgnored(t *testing.T) {
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
	ch := e.hook(agents.Claude, "SessionStart", "b1", e.at(5), map[string]any{
		"session_id": "sub", "cwd": cwdA, "source": "startup",
		"transcript_path": "/h/.claude/projects/-w/main/subagents/agent-1.jsonl"})
	if len(ch) != 0 {
		t.Fatalf("changes = %v", kinds(ch))
	}
}

func TestReportOlderThanProcessIgnored(t *testing.T) {
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
	// The process started at base; this report is from before.
	if ch := e.claudeHook("b1", "SessionStart", "old", "startup", e.at(-10)); len(ch) != 0 {
		t.Fatalf("changes = %v", kinds(ch))
	}
	if _, ok, _ := e.st.ConversationByNative(e.ctx, agents.Claude, "old"); ok {
		t.Error("stale report created a conversation")
	}
}

func TestProcStartFailureFallsBackToFirstSeen(t *testing.T) {
	e := newEnv(t)
	e.reg = New(e.st, e.rx, Options{Host: "h", Home: e.home, Rules: rules, Now: func() time.Time { return e.now },
		ProcStart: func(int) (time.Time, error) { return time.Time{}, errTest }})
	e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
	// First seen "now" (base+1h); a report from later is accepted, earlier is not.
	e.claudeHook("b1", "SessionStart", "c1", "startup", e.now.Add(time.Second))
	if _, ok, _ := e.st.ConversationByNative(e.ctx, agents.Claude, "c1"); !ok {
		t.Fatal("report after first sight should count")
	}
}

type testErr string

func (e testErr) Error() string { return string(e) }

const errTest = testErr("ps failed")

func TestClearTakesOverTopic(t *testing.T) {
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
	e.claudeHook("b1", "SessionStart", "c1", "startup", e.at(1))
	c1 := e.conv(agents.Claude, "c1")
	th := e.link(c1)

	if ch := e.claudeHook("b1", "SessionEnd", "c1", "", e.at(10)); len(ch) != 0 {
		t.Fatalf("SessionEnd changed %v", kinds(ch))
	}
	ch := e.claudeHook("b1", "SessionStart", "c2", "clear", e.at(10.1))
	to := find(t, ch, TakeOver)
	if to.Divider != "claude · new session (clear)" || to.ThreadID != th.ID || !to.Start.FromStart {
		t.Errorf("take over = %+v", to)
	}
	c2 := e.conv(agents.Claude, "c2")
	if to.ConversationID != c2.ID {
		t.Errorf("take over conv = %d, want %d", to.ConversationID, c2.ID)
	}
	if got := find(t, ch, SessionStopped); got.ConversationID != c1.ID {
		t.Errorf("stopped = %+v", got)
	}
	if has(ch, TopicOffline) || has(ch, TopicLive) {
		t.Errorf("topic status changed: %v", kinds(ch))
	}
	if got := e.thread(th.ID); got.CurrentConversationID != c2.ID || got.Status != store.ThreadLive {
		t.Errorf("thread = %+v", got)
	}
	if e.pane("b1").ConversationID != c2.ID {
		t.Error("pane should run c2")
	}
	e.invariants()
}

func TestResumeOfflineTopicInNewPane(t *testing.T) {
	for _, withCursor := range []bool{false, true} {
		name := "no cursor"
		if withCursor {
			name = "cursor"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
			e.claudeHook("b1", "SessionStart", "c1", "startup", e.at(1))
			e.writeClaude("c1", cwdA, e.at(1), e.at(2))
			c1 := e.conv(agents.Claude, "c1")
			th := e.link(c1)
			// The agent exits.
			e.rx.setBlock(sess1, "b1", shellProc(5))
			ch := e.event(rex.ProcessChanged, "b1")
			wantKinds(t, ch, SessionStopped, TopicOffline)
			if e.thread(th.ID).Status != store.ThreadOffline {
				t.Fatal("topic should be offline")
			}
			if withCursor {
				if err := e.st.SaveCursor(e.ctx, store.Cursor{Transcript: c1.Transcript, DevIno: "1:1", Offset: 10, UpdatedAt: e.now}); err != nil {
					t.Fatal(err)
				}
			}
			// Resume in another pane, same session ID and file.
			e.rx.setBlock(sess1, "b2", claudeProc(200, cwdA))
			ch = e.claudeHook("b2", "SessionStart", "c1", "resume", e.at(100))
			wantKinds(t, ch, Resume, TopicLive)
			r := ch[0]
			if r.Divider != "claude · resumed" || r.ThreadID != th.ID {
				t.Errorf("resume = %+v", r)
			}
			want := TailStart{AtEnd: true, Backfill: true}
			if withCursor {
				want = TailStart{}
			}
			if r.Start != want {
				t.Errorf("start = %+v, want %+v", r.Start, want)
			}
			if e.thread(th.ID).Status != store.ThreadLive {
				t.Error("topic should be live")
			}
			if e.pane("b2").ConversationID != c1.ID {
				t.Error("b2 should run c1")
			}
			e.invariants()
		})
	}
}

func TestResumeBusyWhenTopicLiveElsewhere(t *testing.T) {
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
	e.claudeHook("b1", "SessionStart", "old", "startup", e.at(1))
	old := e.conv(agents.Claude, "old")
	th := e.link(old)
	e.claudeHook("b1", "SessionStart", "cur", "clear", e.at(2)) // topic now shows "cur", live in b1
	cur := e.conv(agents.Claude, "cur")

	// "old" is resumed in another pane while the topic is live in b1.
	e.rx.setBlock(sess1, "b2", claudeProc(200, cwdA))
	ch := e.claudeHook("b2", "SessionStart", "old", "resume", e.at(50))
	wantKinds(t, ch, ResumeBusy)
	if ch[0].ThreadID != th.ID || ch[0].ConversationID != old.ID {
		t.Errorf("change = %+v", ch[0])
	}
	if got := e.thread(th.ID); got.CurrentConversationID != cur.ID || got.Status != store.ThreadLive {
		t.Errorf("thread changed: %+v", got)
	}
	if e.pane("b2").ConversationID != old.ID || e.pane("b1").ConversationID != cur.ID {
		t.Error("panes should keep their sessions")
	}
	e.invariants()
}

func TestResumeUnderNewIDInSamePaneTakesOver(t *testing.T) {
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
	e.claudeHook("b1", "SessionStart", "c1", "startup", e.at(1))
	c1 := e.conv(agents.Claude, "c1")
	th := e.link(c1)
	e.rx.setBlock(sess1, "b1", shellProc(5))
	e.event(rex.ProcessChanged, "b1")
	e.rx.setBlock(sess1, "b1", claudeProc(101, cwdA))
	e.writeClaude("c9", cwdA, e.at(1), e.at(2))
	ch := e.claudeHook("b1", "SessionStart", "c9", "resume", e.at(30))
	to := find(t, ch, TakeOver)
	if to.ThreadID != th.ID || to.Divider != "claude · resumed" || to.Start != (TailStart{AtEnd: true, Backfill: true}) {
		t.Errorf("take over = %+v", to)
	}
	if !has(ch, TopicLive) {
		t.Errorf("changes = %v", kinds(ch))
	}
	if got := e.thread(th.ID); got.CurrentConversationID != e.conv(agents.Claude, "c9").ID {
		t.Errorf("thread = %+v", got)
	}
	e.invariants()
}

func TestNewSessionAfterExitTakesOverAndTopicLive(t *testing.T) {
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
	e.claudeHook("b1", "SessionStart", "c1", "startup", e.at(1))
	th := e.link(e.conv(agents.Claude, "c1"))
	e.rx.setBlock(sess1, "b1", shellProc(5))
	e.event(rex.ProcessChanged, "b1")

	e.rx.setBlock(sess1, "b1", claudeProc(150, cwdA))
	ch := e.claudeHook("b1", "SessionStart", "c2", "startup", e.at(60))
	wantKinds(t, ch, TakeOver, TopicLive)
	if ch[0].Divider != "claude · new session (startup)" || !ch[0].Start.FromStart || ch[0].ThreadID != th.ID {
		t.Errorf("take over = %+v", ch[0])
	}
	if e.thread(th.ID).Status != store.ThreadLive {
		t.Error("topic should be live")
	}
	e.invariants()
}

func TestNoTakeOverWhenTopicLiveInAnotherPane(t *testing.T) {
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
	e.claudeHook("b1", "SessionStart", "c1", "startup", e.at(1))
	c1 := e.conv(agents.Claude, "c1")
	th := e.link(c1)
	// c1 moves to b2 (resumed there). b1 still remembers it as its last session.
	e.rx.setBlock(sess1, "b2", claudeProc(200, cwdA))
	e.claudeHook("b2", "SessionStart", "c1", "resume", e.at(20))
	if e.pane("b1").ConversationID != 0 || e.pane("b2").ConversationID != c1.ID {
		t.Fatal("c1 should run in b2 only")
	}
	// A new session in b1 must not steal the topic from b2.
	e.rx.setBlock(sess1, "b1", claudeProc(101, cwdA))
	ch := e.claudeHook("b1", "SessionStart", "c3", "startup", e.at(30))
	wantKinds(t, ch, SessionStarted)
	if e.thread(th.ID).CurrentConversationID != c1.ID {
		t.Error("topic should stay with c1")
	}
}

func TestOnePanePerSession(t *testing.T) {
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
	e.claudeHook("b1", "SessionStart", "c1", "startup", e.at(1))
	c1 := e.conv(agents.Claude, "c1")
	e.rx.setBlock(sess1, "b2", claudeProc(200, cwdA))
	ch := e.claudeHook("b2", "SessionStart", "c1", "resume", e.at(20))
	stopped := find(t, ch, SessionStopped)
	if stopped.PaneID != e.pane("b1").ID || stopped.ConversationID != c1.ID {
		t.Errorf("stopped = %+v", stopped)
	}
	if e.pane("b1").ConversationID != 0 || e.pane("b2").ConversationID != c1.ID {
		t.Error("session should run in b2 only")
	}
	pb, ok, _ := e.st.PaneByConversation(e.ctx, c1.ID)
	if !ok || pb.Block != "b2" {
		t.Errorf("pane by conversation = %+v", pb)
	}
}

func TestPiSessionReasons(t *testing.T) {
	tests := []struct {
		reason  string
		want    ChangeKind
		divider string
	}{
		{"new", TakeOver, "pi · new session (new)"},
		{"fork", TakeOver, "pi · new session (fork)"},
		{"resume", TakeOver, "pi · resumed"},
	}
	for _, tc := range tests {
		t.Run(tc.reason, func(t *testing.T) {
			e := newEnv(t)
			e.rx.setBlock(sess1, "b1", piProc(100, cwdA))
			ch := e.piHook("b1", "session_start", "p1", "startup", e.at(1))
			wantKinds(t, ch, SessionStarted)
			if !ch[0].Start.FromStart {
				t.Errorf("start = %+v", ch[0].Start)
			}
			th := e.link(e.conv(agents.Pi, "p1"))
			ch = e.piHook("b1", "session_start", "p2", tc.reason, e.at(10))
			to := find(t, ch, tc.want)
			if to.Divider != tc.divider || to.ThreadID != th.ID {
				t.Errorf("change = %+v", to)
			}
			e.invariants()
		})
	}
}

func TestPiResumeOfSessionWithTopic(t *testing.T) {
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", piProc(100, cwdA))
	e.piHook("b1", "session_start", "p1", "startup", e.at(1))
	th := e.link(e.conv(agents.Pi, "p1"))
	e.piHook("b1", "session_start", "p2", "new", e.at(5)) // topic now shows p2
	ch := e.piHook("b1", "session_start", "p1", "resume", e.at(9))
	r := find(t, ch, Resume)
	if r.Divider != "pi · resumed" || r.ThreadID != th.ID {
		t.Errorf("resume = %+v", r)
	}
	if e.thread(th.ID).CurrentConversationID != e.conv(agents.Pi, "p1").ID {
		t.Error("topic should show p1 again")
	}
	e.invariants()
}

func TestPiShutdownAndAgentEvents(t *testing.T) {
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", piProc(100, cwdA))
	e.piHook("b1", "session_start", "p1", "startup", e.at(1))
	if ch := e.piHook("b1", "session_shutdown", "p1", "new", e.at(2)); len(ch) != 0 {
		t.Fatalf("session_shutdown changed %v", kinds(ch))
	}
	wantKinds(t, e.piHook("b1", "agent_start", "p1", "", e.at(3)), StateChanged)
	ch := e.piHook("b1", "agent_end", "p1", "", e.at(4))
	if sc := find(t, ch, StateChanged); sc.State != "idle" {
		t.Errorf("state = %+v", sc)
	}
}

func TestAgentExitStopsSessionAndTopicOffline(t *testing.T) {
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
	e.claudeHook("b1", "SessionStart", "c1", "startup", e.at(1))
	c1 := e.conv(agents.Claude, "c1")
	th := e.link(c1)
	e.status("b1", "working", "", e.at(2))

	e.rx.setBlock(sess1, "b1", shellProc(5))
	ch := e.event(rex.ProcessChanged, "b1")
	wantKinds(t, ch, SessionStopped, TopicOffline)
	if ch[0].ConversationID != c1.ID || ch[1].ThreadID != th.ID {
		t.Errorf("changes = %+v", ch)
	}
	p := e.pane("b1")
	if p.ConversationID != 0 || p.LastConversationID != c1.ID || p.State != "" {
		t.Errorf("pane = %+v", p)
	}
	if e.thread(th.ID).Status != store.ThreadOffline {
		t.Error("topic should be offline")
	}
	// Nothing more happens on the next event.
	if ch := e.event(rex.ProcessChanged, "b1"); len(ch) != 0 {
		t.Errorf("changes = %v", kinds(ch))
	}
}

func TestBlockClosedPaneGoneAndTopicOffline(t *testing.T) {
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
	e.claudeHook("b1", "SessionStart", "c1", "startup", e.at(1))
	c1 := e.conv(agents.Claude, "c1")
	th := e.link(c1)
	ch := e.event(rex.BlockClosed, "b1")
	wantKinds(t, ch, PaneGone, TopicOffline)
	if ch[0].ConversationID != c1.ID {
		t.Errorf("pane gone = %+v", ch[0])
	}
	p := e.pane("b1")
	if p.GoneAt.IsZero() || p.ConversationID != 0 || p.LastConversationID != c1.ID {
		t.Errorf("pane = %+v", p)
	}
	if e.thread(th.ID).Status != store.ThreadOffline {
		t.Error("topic should be offline")
	}
	if ch := e.event(rex.BlockClosed, "b1"); len(ch) != 0 {
		t.Errorf("second close changed %v", kinds(ch))
	}
	if ch := e.event(rex.BlockClosed, "never-seen"); len(ch) != 0 {
		t.Errorf("unknown block changed %v", kinds(ch))
	}
}

func TestPIDReuse(t *testing.T) {
	t.Run("same pid, now a shell", func(t *testing.T) {
		e := newEnv(t)
		e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
		e.claudeHook("b1", "SessionStart", "c1", "startup", e.at(1))
		e.rx.setBlock(sess1, "b1", rex.ProcInfo{PID: 100, Name: "zsh", Argv0: "zsh"})
		wantKinds(t, e.event(rex.ProcessChanged, "b1"), SessionStopped)
	})
	t.Run("same pid, another agent", func(t *testing.T) {
		e := newEnv(t)
		e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
		e.claudeHook("b1", "SessionStart", "c1", "startup", e.at(1))
		e.rx.setBlock(sess1, "b1", codexProc(100, cwdA))
		wantKinds(t, e.event(rex.ProcessChanged, "b1"), SessionStopped)
		views, err := e.reg.Agents(e.ctx)
		if err != nil || len(views) != 1 {
			t.Fatalf("views = %+v, %v", views, err)
		}
		if views[0].Runtime != agents.Codex || views[0].Resolution != "unresolved" || views[0].Conversation != nil {
			t.Errorf("view = %+v", views[0])
		}
	})
	t.Run("same pid and name is the same process", func(t *testing.T) {
		e := newEnv(t)
		e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
		e.claudeHook("b1", "SessionStart", "c1", "startup", e.at(1))
		if ch := e.event(rex.ProcessChanged, "b1"); len(ch) != 0 {
			t.Errorf("changes = %v", kinds(ch))
		}
	})
}

func TestGuessResolver(t *testing.T) {
	t.Run("the only file after process start", func(t *testing.T) {
		e := newEnv(t)
		e.writeClaude("older", cwdA, e.at(-3600), e.at(-3000)) // before the process started
		e.writeClaude("fresh", cwdA, e.at(5), e.at(30))
		e.writeClaude("elsewhere", "/w/other", e.at(5), e.at(30))
		e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
		ch := e.event(rex.ProcessChanged, "b1")
		wantKinds(t, ch, SessionStarted)
		if !ch[0].Start.FromStart {
			t.Errorf("start = %+v", ch[0].Start)
		}
		p := e.pane("b1")
		if p.Source != store.SourceGuess || p.ConversationID != e.conv(agents.Claude, "fresh").ID {
			t.Errorf("pane = %+v", p)
		}
		views, _ := e.reg.Agents(e.ctx)
		if len(views) != 1 || views[0].Resolution != "guess" {
			t.Errorf("views = %+v", views)
		}
		// A later hook replaces the guess with the exact answer.
		e.claudeHook("b1", "SessionStart", "fresh", "startup", e.at(40))
		if got := e.pane("b1").Source; got != store.SourceHook {
			t.Errorf("source = %q", got)
		}
	})
	t.Run("file written before the agent started counts as a resume", func(t *testing.T) {
		e := newEnv(t)
		e.writeClaude("c1", cwdA, e.at(-3600), e.at(30)) // started long ago, changed after the process began
		e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
		ch := e.event(rex.ProcessChanged, "b1")
		wantKinds(t, ch, SessionStarted)
		if ch[0].Start != (TailStart{AtEnd: true, Backfill: true}) {
			t.Errorf("start = %+v", ch[0].Start)
		}
	})
	t.Run("unresolved when two files fit", func(t *testing.T) {
		e := newEnv(t)
		e.writeClaude("a", cwdA, e.at(5), e.at(30))
		e.writeClaude("b", cwdA, e.at(6), e.at(31))
		e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
		if ch := e.event(rex.ProcessChanged, "b1"); len(ch) != 0 {
			t.Fatalf("changes = %v", kinds(ch))
		}
		views, _ := e.reg.Agents(e.ctx)
		if len(views) != 1 || views[0].Resolution != "unresolved" || views[0].Runtime != agents.Claude {
			t.Errorf("views = %+v", views)
		}
	})
	t.Run("unresolved when no file fits, resolved at the next scan", func(t *testing.T) {
		e := newEnv(t)
		e.rx.setBlock(sess1, "b1", piProc(100, cwdA))
		if ch, err := e.reg.Scan(e.ctx); err != nil || len(ch) != 0 {
			t.Fatalf("scan = %v, %v", kinds(ch), err)
		}
		e.writePi("p1", cwdA, e.at(20), e.at(20)) // pi creates the file at the first prompt
		ch, err := e.reg.Scan(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		wantKinds(t, ch, SessionStarted)
		if e.pane("b1").Source != store.SourceGuess {
			t.Error("want a guess")
		}
	})
	t.Run("file of a session that runs in another pane is skipped", func(t *testing.T) {
		e := newEnv(t)
		e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
		e.claudeHook("b1", "SessionStart", "c1", "startup", e.at(1))
		e.writeClaude("c1", cwdA, e.at(1), e.at(50))
		e.writeClaude("c2", cwdA, e.at(40), e.at(60))
		e.rx.setBlock(sess1, "b2", claudeProc(200, cwdA))
		ch := e.event(rex.ProcessChanged, "b2")
		wantKinds(t, ch, SessionStarted)
		if ch[0].ConversationID != e.conv(agents.Claude, "c2").ID {
			t.Errorf("guessed %+v", ch[0])
		}
	})
	t.Run("two unresolved agents of one runtime in one folder", func(t *testing.T) {
		e := newEnv(t)
		e.writeClaude("a", cwdA, e.at(5), e.at(30))
		e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
		e.rx.setBlock(sess1, "b2", claudeProc(200, cwdA))
		if _, err := e.reg.Scan(e.ctx); err != nil {
			t.Fatal(err)
		}
		views, _ := e.reg.Agents(e.ctx)
		for _, v := range views {
			if v.Resolution != "unresolved" {
				t.Errorf("view = %+v", v)
			}
		}
	})
	t.Run("codex rollout", func(t *testing.T) {
		e := newEnv(t)
		now := time.Now()
		e.start[100] = now.Add(-time.Hour)
		p := e.home + "/.codex/sessions/" + now.Format("2006/01/02") + "/rollout-x-cx1.jsonl"
		writeFile(t, p, `{"type":"session_meta","payload":{"id":"cx1","cwd":"`+cwdA+`","timestamp":"`+now.Format(time.RFC3339)+`"}}`+"\n", now)
		e.rx.setBlock(sess1, "b1", codexProc(100, cwdA))
		wantKinds(t, e.event(rex.ProcessChanged, "b1"), SessionStarted)
		if e.pane("b1").ConversationID != e.conv(agents.Codex, "cx1").ID {
			t.Error("codex guess missing")
		}
	})
}

func TestScanMarksMissingPanesGone(t *testing.T) {
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
	e.claudeHook("b1", "SessionStart", "c1", "startup", e.at(1))
	th := e.link(e.conv(agents.Claude, "c1"))
	e.rx.removeBlock(sess1, "b1")
	ch, err := e.reg.Scan(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds(t, ch, PaneGone, TopicOffline)
	if e.thread(th.ID).Status != store.ThreadOffline {
		t.Error("topic should be offline")
	}
}

func TestScanKeepsPanesAfterRexRestart(t *testing.T) {
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
	e.claudeHook("b1", "SessionStart", "c1", "startup", e.at(1))
	th := e.link(e.conv(agents.Claude, "c1"))
	// A fresh registry (daemon restart) sees the same block and process.
	reg2 := New(e.st, e.rx, Options{Host: "h", Home: e.home, Rules: rules, Now: func() time.Time { return e.now },
		ProcStart: func(int) (time.Time, error) { return e.base, nil }})
	e.rx.status["b1"] = []rex.StatusRecord{{State: "idle", UpdatedAt: e.at(3)}}
	ch, err := reg2.Scan(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds(t, ch, StateChanged)
	if e.thread(th.ID).Status != store.ThreadLive {
		t.Error("topic should stay live")
	}
	p := e.pane("b1")
	ok, why, err := reg2.Ready(e.ctx, p.ID, e.now)
	if err != nil || !ok {
		t.Errorf("ready = %v %q %v", ok, why, err)
	}
}

func TestHookProcessErrorIsReported(t *testing.T) {
	e := newEnv(t)
	e.rx.procErr = errTest
	if _, err := e.hookRaw(agents.Claude, "SessionStart", "b1", e.at(1), map[string]any{"session_id": "c1"}); err == nil {
		t.Fatal("want error")
	}
}

func TestHookWithoutBlockIsIgnored(t *testing.T) {
	e := newEnv(t)
	ch, err := e.reg.Hook(e.ctx, HookReport{Runtime: agents.Claude, Event: "SessionStart", At: e.at(1)})
	if err != nil || len(ch) != 0 {
		t.Fatalf("got %v, %v", ch, err)
	}
}

func TestHookResolvesWithMissingTranscript(t *testing.T) {
	t.Run("pi startup", func(t *testing.T) {
		e := newEnv(t)
		e.rx.setBlock(sess1, "b1", piProc(100, cwdA))
		file := e.home + "/.pi/agent/sessions/x/2026-11-11T03-00-00-000Z_pi-new-1.jsonl"
		ch := e.hook(agents.Pi, "session_start", "b1", e.at(1), map[string]any{
			"hook_event_name": "session_start", "reason": "startup", "cwd": cwdA, "session_file": file})
		wantKinds(t, ch, SessionStarted)
		if !ch[0].Start.FromStart {
			t.Errorf("start = %+v", ch[0].Start)
		}
		c := e.conv(agents.Pi, "pi-new-1")
		if e.pane("b1").ConversationID != c.ID || c.Transcript != file || c.CWD != cwdA {
			t.Errorf("conv = %+v", c)
		}
	})
	t.Run("claude startup", func(t *testing.T) {
		e := newEnv(t)
		e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
		ch := e.claudeHook("b1", "SessionStart", "c-missing", "startup", e.at(1))
		wantKinds(t, ch, SessionStarted)
		if !ch[0].Start.FromStart || e.pane("b1").ConversationID != e.conv(agents.Claude, "c-missing").ID {
			t.Errorf("change = %+v", ch[0])
		}
	})
	t.Run("claude resume of a missing file starts at byte 0", func(t *testing.T) {
		e := newEnv(t)
		e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
		ch := e.claudeHook("b1", "SessionStart", "c-gone", "resume", e.at(1))
		if !ch[0].Start.FromStart {
			t.Errorf("start = %+v", ch[0].Start)
		}
	})
	t.Run("pi /new takes over", func(t *testing.T) {
		e := newEnv(t)
		e.rx.setBlock(sess1, "b1", piProc(100, cwdA))
		e.piHook("b1", "session_start", "p1", "startup", e.at(1))
		th := e.link(e.conv(agents.Pi, "p1"))
		file := e.home + "/.pi/agent/sessions/x/2026-11-11T03-10-00-000Z_p2-new.jsonl"
		ch := e.hook(agents.Pi, "session_start", "b1", e.at(10), map[string]any{
			"hook_event_name": "session_start", "reason": "new", "cwd": cwdA, "session_file": file,
			"previous_session_file": e.home + "/.pi/agent/sessions/x/p1.jsonl"})
		to := find(t, ch, TakeOver)
		if to.ThreadID != th.ID || to.Divider != "pi · new session (new)" || !to.Start.FromStart {
			t.Errorf("take over = %+v", to)
		}
		if e.thread(th.ID).CurrentConversationID != e.conv(agents.Pi, "p2-new").ID {
			t.Error("topic should show the new session")
		}
		e.invariants()
	})
}
