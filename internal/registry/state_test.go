package registry

import (
	"strings"
	"testing"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/rex"
)

// stateEnv has one Claude session running in pane b1.
func stateEnv(t *testing.T) (*env, int64) {
	t.Helper()
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
	e.claudeHook("b1", "SessionStart", "c1", "startup", e.at(1))
	return e, e.pane("b1").ID
}

func (e *env) paneState(block string) string { return e.pane(block).State }

func (e *env) ready(paneID int64, at time.Time) (bool, string) {
	e.t.Helper()
	ok, why, err := e.reg.Ready(e.ctx, paneID, at)
	if err != nil {
		e.t.Fatal(err)
	}
	if ok && why != "" {
		e.t.Errorf("ready with a reason %q", why)
	}
	if !ok && why == "" {
		e.t.Error("not ready without a reason")
	}
	return ok, why
}

func TestStateNewestSignalWins(t *testing.T) {
	e, _ := stateEnv(t)
	wantKinds(t, e.status("b1", "working", "", e.at(10)), StateChanged)
	wantKinds(t, e.status("b1", "done", "", e.at(20)), StateChanged)
	// A late, older signal does not undo the newer one.
	if ch := e.status("b1", "working", "", e.at(15)); len(ch) != 0 {
		t.Errorf("stale signal changed %v", kinds(ch))
	}
	if got := e.paneState("b1"); got != "done" {
		t.Errorf("state = %q", got)
	}
	// The same state again is not a change.
	if ch := e.status("b1", "done", "", e.at(21)); len(ch) != 0 {
		t.Errorf("repeat changed %v", kinds(ch))
	}
}

func TestPromptMeansWorkingAfterStaleDone(t *testing.T) {
	prompts := map[string]func(e *env, at time.Time){
		"UserPromptSubmit": func(e *env, at time.Time) { e.claudeHook("b1", "UserPromptSubmit", "c1", "", at) },
		"transcript prompt": func(e *env, at time.Time) {
			if _, err := e.reg.TranscriptSignal(e.ctx, e.conv(agents.Claude, "c1").ID, Signal{State: "working", Kind: "prompt", At: at}); err != nil {
				e.t.Fatal(err)
			}
		},
	}
	for name, prompt := range prompts {
		t.Run(name, func(t *testing.T) {
			e, id := stateEnv(t)
			e.status("b1", "done", "", e.at(20))
			prompt(e, e.at(19)) // reaches us after the done, with an older time
			if got := e.paneState("b1"); got != "working" {
				t.Fatalf("state = %q", got)
			}
			if ok, _ := e.ready(id, e.at(30)); ok {
				t.Error("must not be ready while working")
			}
			// The stale done, seen again, does not win either.
			e.status("b1", "done", "", e.at(18))
			if got := e.paneState("b1"); got != "working" {
				t.Errorf("state = %q after stale done", got)
			}
			e.status("b1", "idle", "", e.at(40))
			if ok, why := e.ready(id, e.at(41)); !ok {
				t.Errorf("not ready after idle: %s", why)
			}
		})
	}
}

func TestPiAgentStartIsWorkingAfterStaleDone(t *testing.T) {
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", piProc(100, cwdA))
	e.piHook("b1", "session_start", "p1", "startup", e.at(1))
	e.status("b1", "done", "", e.at(20))
	e.piHook("b1", "agent_start", "p1", "", e.at(19))
	if got := e.paneState("b1"); got != "working" {
		t.Fatalf("state = %q", got)
	}
}

func TestClaudeNotificationBlocked(t *testing.T) {
	e, id := stateEnv(t)
	e.status("b1", "idle", "", e.at(5))
	notif := func(kind string, at time.Time) []Change {
		return e.hook(agents.Claude, "Notification", "b1", at, map[string]any{
			"hook_event_name": "Notification", "session_id": "c1", "transcript_path": e.claudePath("c1"),
			"cwd": cwdA, "notification_type": kind, "message": "x"})
	}
	if ch := notif("idle_prompt", e.at(6)); len(ch) != 0 {
		t.Fatalf("idle notification changed %v", kinds(ch))
	}
	ch := notif("permission_prompt", e.at(7))
	sc := find(t, ch, StateChanged)
	if sc.State != "blocked" || sc.StateKind != "permission" {
		t.Errorf("change = %+v", sc)
	}
	ok, why := e.ready(id, e.at(8))
	if ok || !strings.Contains(why, "prompt") {
		t.Errorf("ready = %v %q", ok, why)
	}
	// The permission was answered.
	e.status("b1", "working", "", e.at(9))
	e.status("b1", "done", "", e.at(12))
	if ok, why := e.ready(id, e.at(13)); !ok {
		t.Errorf("not ready: %s", why)
	}
}

func TestPermissionRequestHookWithoutTranscriptPath(t *testing.T) {
	e, _ := stateEnv(t)
	ch := e.hook(agents.Claude, "PermissionRequest", "b1", e.at(5), map[string]any{
		"hook_event_name": "PermissionRequest", "session_id": "c1", "tool_name": "Bash"})
	sc := find(t, ch, StateChanged)
	if sc.State != "blocked" || sc.StateKind != "permission" {
		t.Errorf("change = %+v", sc)
	}
}

func TestRexBlockedKindAndMsg(t *testing.T) {
	e, _ := stateEnv(t)
	ch, err := e.reg.RexEvent(e.ctx, rex.Event{Type: rex.ProgramStatusChanged, SessionID: sess1, BlockID: "b1",
		Status: &rex.StatusRecord{State: "blocked", Kind: "permission", Msg: "approve Bash: ls", UpdatedAt: e.at(5)}})
	if err != nil {
		t.Fatal(err)
	}
	sc := find(t, ch, StateChanged)
	if sc.State != "blocked" || sc.StateKind != "permission" || sc.Msg != "approve Bash: ls" {
		t.Errorf("change = %+v", sc)
	}
	if p := e.pane("b1"); p.State != "blocked" || p.StateKind != "permission" {
		t.Errorf("pane = %+v", p)
	}
}

func TestCodexDoneThenIdleAfter15s(t *testing.T) {
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", codexProc(100, cwdA))
	e.hook(agents.Codex, "SessionStart", "b1", e.at(1), map[string]any{
		"hook_event_name": "SessionStart", "session_id": "x1", "transcript_path": "/h/.codex/sessions/r.jsonl", "cwd": cwdA, "source": "startup"})
	id := e.pane("b1").ID
	e.status("b1", "working", "", e.at(10))
	e.status("b1", "done", "", e.at(20))
	if ok, _ := e.ready(id, e.at(21)); !ok {
		t.Fatal("done should allow delivery")
	}
	ch := e.status("b1", "idle", "", e.at(35))
	if sc := find(t, ch, StateChanged); sc.State != "idle" {
		t.Errorf("change = %+v", sc)
	}
	if ok, why := e.ready(id, e.at(36)); !ok {
		t.Errorf("not ready: %s", why)
	}
}

func TestEscapeGivesIdle(t *testing.T) {
	e, id := stateEnv(t)
	e.status("b1", "working", "", e.at(10))
	e.status("b1", "idle", "", e.at(14)) // Claude and Codex go working -> idle on Escape, with no Stop
	if got := e.paneState("b1"); got != "idle" {
		t.Fatalf("state = %q", got)
	}
	if ok, why := e.ready(id, e.at(15)); !ok {
		t.Errorf("not ready: %s", why)
	}
}

func TestReadyRules(t *testing.T) {
	e, id := stateEnv(t)

	// Silence is not idle.
	if ok, why := e.ready(id, e.at(5)); ok || !strings.Contains(why, "idle") {
		t.Errorf("fresh pane: ready = %v %q", ok, why)
	}
	e.status("b1", "working", "", e.at(10))
	if ok, why := e.ready(id, e.at(11)); ok || !strings.Contains(why, "working") {
		t.Errorf("working: ready = %v %q", ok, why)
	}
	// A long quiet time does not make it idle.
	if ok, _ := e.ready(id, e.at(3600)); ok {
		t.Error("working for an hour is still working")
	}
	e.status("b1", "idle", "", e.at(20))
	if ok, why := e.ready(id, e.at(21)); !ok {
		t.Errorf("idle: %s", why)
	}
	// Typing starts the busy window.
	if err := e.reg.MarkTyped(e.ctx, id, e.at(30)); err != nil {
		t.Fatal(err)
	}
	if ok, why := e.ready(id, e.at(31)); ok || !strings.Contains(why, "typed") {
		t.Errorf("busy: ready = %v %q", ok, why)
	}
	if ok, why := e.ready(id, e.at(30).Add(16*time.Second)); !ok {
		t.Errorf("busy window should end after 15 s: %s", why)
	}
	// A working signal clears the window early.
	if err := e.reg.MarkTyped(e.ctx, id, e.at(100)); err != nil {
		t.Fatal(err)
	}
	e.status("b1", "working", "", e.at(101))
	e.status("b1", "done", "", e.at(103))
	if ok, why := e.ready(id, e.at(104)); !ok {
		t.Errorf("working should clear the busy window: %s", why)
	}
	// So does a prompt.
	if err := e.reg.MarkTyped(e.ctx, id, e.at(200)); err != nil {
		t.Fatal(err)
	}
	e.claudeHook("b1", "UserPromptSubmit", "c1", "", e.at(201))
	e.claudeHook("b1", "Stop", "c1", "", e.at(205))
	if ok, why := e.ready(id, e.at(206)); !ok {
		t.Errorf("prompt should clear the busy window: %s", why)
	}
	if p := e.pane("b1"); !p.BusyUntil.IsZero() {
		t.Errorf("busy_until = %v", p.BusyUntil)
	}
}

func TestReadyNeedsSessionAndPane(t *testing.T) {
	e, id := stateEnv(t)
	e.status("b1", "idle", "", e.at(5))
	e.rx.setBlock(sess1, "b1", shellProc(5))
	e.event(rex.ProcessChanged, "b1")
	if ok, _ := e.ready(id, e.at(10)); ok {
		t.Error("no agent in the pane")
	}
	e.event(rex.BlockClosed, "b1")
	if ok, _ := e.ready(id, e.at(10)); ok {
		t.Error("pane is gone")
	}
	if _, _, err := e.reg.Ready(e.ctx, 999, e.at(10)); err == nil {
		t.Error("unknown pane should be an error")
	}
}

func TestStatePersistsAcrossRestart(t *testing.T) {
	e, id := stateEnv(t)
	e.status("b1", "working", "", e.at(10))
	e.status("b1", "idle", "", e.at(20))
	reg2 := New(e.st, e.rx, Options{Host: "h", Home: e.home, Rules: rules, Now: func() time.Time { return e.now },
		ProcStart: func(int) (time.Time, error) { return e.base, nil }})
	if ok, why, err := reg2.Ready(e.ctx, id, e.at(30)); err != nil || !ok {
		t.Errorf("ready = %v %q %v", ok, why, err)
	}
}

func TestTranscriptSignalWithoutPane(t *testing.T) {
	e := newEnv(t)
	ch, err := e.reg.TranscriptSignal(e.ctx, 42, Signal{State: "idle", At: e.at(1)})
	if err != nil || len(ch) != 0 {
		t.Fatalf("got %v, %v", ch, err)
	}
}

func TestAgentsView(t *testing.T) {
	e := newEnv(t)
	e.rx.setBlock(sess1, "b1", claudeProc(100, cwdA))
	e.rx.setBlock(sess1, "b2", piProc(200, "/w/other"))
	e.rx.setBlock(sess1, "b3", shellProc(5))
	e.claudeHook("b1", "SessionStart", "c1", "startup", e.at(1))
	th := e.link(e.conv(agents.Claude, "c1"))
	if _, err := e.reg.Scan(e.ctx); err != nil {
		t.Fatal(err)
	}
	views, err := e.reg.Agents(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("views = %d, want 2 (the shell is not an agent)", len(views))
	}
	a, b := views[0], views[1]
	if a.Pane.Block != "b1" || a.Runtime != agents.Claude || a.Resolution != "hook" ||
		a.Conversation == nil || a.Conversation.NativeID != "c1" || a.Thread == nil || a.Thread.ID != th.ID {
		t.Errorf("first = %+v", a)
	}
	if b.Pane.Block != "b2" || b.Runtime != agents.Pi || b.Resolution != "unresolved" || b.Conversation != nil || b.Thread != nil {
		t.Errorf("second = %+v", b)
	}
	e.event(rex.BlockClosed, "b2")
	if views, _ = e.reg.Agents(e.ctx); len(views) != 1 {
		t.Errorf("closed pane still listed: %d", len(views))
	}
}
