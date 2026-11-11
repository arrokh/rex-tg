package store

import (
	"errors"
	"testing"
	"time"

	"rex-tg/internal/agents"
)

func TestConversationUpsert(t *testing.T) {
	s := newStore(t)
	a := must(s.UpsertConversation(ctx, Conversation{Runtime: agents.Pi, NativeID: "n1", Transcript: "/a", CWD: "/w", FirstSeenAt: t0}))
	b := must(s.UpsertConversation(ctx, Conversation{Runtime: agents.Pi, NativeID: "n1", Transcript: "/b", FirstSeenAt: t0.Add(time.Hour)}))
	if a.ID != b.ID || b.Transcript != "/b" || b.CWD != "/w" || !b.FirstSeenAt.Equal(t0) {
		t.Fatalf("upsert: %+v %+v", a, b)
	}
	other := must(s.UpsertConversation(ctx, Conversation{Runtime: agents.Claude, NativeID: "n1", Transcript: "/c", FirstSeenAt: t0}))
	if other.ID == a.ID {
		t.Fatal("runtime must be part of the key")
	}
	noErr(t, s.SetConversationTitle(ctx, a.ID, "hello"))
	got := must(s.Conversation(ctx, a.ID))
	if got.Title != "hello" {
		t.Fatalf("title %q", got.Title)
	}
	if _, ok, err := s.ConversationByNative(ctx, agents.Codex, "n1"); ok || err != nil {
		t.Fatalf("by native: %v %v", ok, err)
	}
	if _, err := s.Conversation(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.UpsertConversation(ctx, Conversation{Runtime: "x", NativeID: "n"}); err == nil {
		t.Fatal("invalid runtime accepted")
	}
}

func TestPaneBasics(t *testing.T) {
	s := newStore(t)
	p := must(s.UpsertPane(ctx, "h", "b1", "s1", "label", t0))
	p2 := must(s.UpsertPane(ctx, "h", "b1", "s2", "", t0.Add(time.Minute)))
	if p.ID != p2.ID || p2.Session != "s2" || p2.Label != "label" || !p2.SeenAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("%+v %+v", p, p2)
	}
	noErr(t, s.SetPaneState(ctx, p.ID, "working", "permission", t0))
	noErr(t, s.SetPaneBusyUntil(ctx, p.ID, t0.Add(15*time.Second)))
	got := must(s.Pane(ctx, p.ID))
	if got.State != "working" || got.StateKind != "permission" || !got.StateAt.Equal(t0) || !got.BusyUntil.Equal(t0.Add(15*time.Second)) {
		t.Fatalf("%+v", got)
	}
	noErr(t, s.SetPaneBusyUntil(ctx, p.ID, time.Time{}))
	if got := must(s.Pane(ctx, p.ID)); !got.BusyUntil.IsZero() {
		t.Fatal("busy not cleared")
	}
	if _, ok, _ := s.PaneByBlock(ctx, "h", "nope"); ok {
		t.Fatal("found missing pane")
	}
	if _, err := s.Pane(ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestPaneOccupantAndGone(t *testing.T) {
	s := newStore(t)
	p := must(s.UpsertPane(ctx, "h", "b1", "s", "", t0))
	a, b := conv(t, s, "a"), conv(t, s, "b")

	noErr(t, s.SetPaneOccupant(ctx, p.ID, a.ID, 42, "claude", SourceHook))
	got := must(s.Pane(ctx, p.ID))
	if got.ConversationID != a.ID || got.LastConversationID != 0 || got.PID != 42 || got.ProcessName != "claude" || got.Source != SourceHook {
		t.Fatalf("%+v", got)
	}
	noErr(t, s.SetPaneOccupant(ctx, p.ID, b.ID, 43, "claude", SourceScan))
	got = must(s.Pane(ctx, p.ID))
	if got.ConversationID != b.ID || got.LastConversationID != a.ID {
		t.Fatalf("take-over: %+v", got)
	}
	// Same occupant again does not overwrite last.
	noErr(t, s.SetPaneOccupant(ctx, p.ID, b.ID, 44, "claude", SourceHook))
	if got := must(s.Pane(ctx, p.ID)); got.LastConversationID != a.ID || got.PID != 44 {
		t.Fatalf("%+v", got)
	}
	// Clearing keeps last = previous occupant.
	noErr(t, s.SetPaneOccupant(ctx, p.ID, 0, 0, "", ""))
	got = must(s.Pane(ctx, p.ID))
	if got.ConversationID != 0 || got.LastConversationID != b.ID || got.PID != 0 || got.Source != "" {
		t.Fatalf("cleared: %+v", got)
	}

	noErr(t, s.SetPaneOccupant(ctx, p.ID, a.ID, 1, "claude", SourceHook))
	noErr(t, s.MarkPaneGone(ctx, p.ID, t0.Add(time.Hour)))
	got = must(s.Pane(ctx, p.ID))
	if got.ConversationID != 0 || got.LastConversationID != a.ID || !got.GoneAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("gone: %+v", got)
	}
	if ps := must(s.Panes(ctx)); len(ps) != 0 {
		t.Fatalf("gone pane listed: %+v", ps)
	}
	// Revived by upsert.
	must(s.UpsertPane(ctx, "h", "b1", "s", "", t0))
	if ps := must(s.Panes(ctx)); len(ps) != 1 {
		t.Fatalf("revived pane not listed")
	}
}

func TestPaneUniqueness(t *testing.T) {
	s := newStore(t)
	p1 := must(s.UpsertPane(ctx, "h", "b1", "s", "", t0))
	p2 := must(s.UpsertPane(ctx, "h", "b2", "s", "", t0))
	a := conv(t, s, "a")
	noErr(t, s.SetPaneOccupant(ctx, p1.ID, a.ID, 1, "claude", SourceHook))
	noErr(t, s.SetPaneOccupant(ctx, p2.ID, a.ID, 2, "claude", SourceHook))

	old := must(s.Pane(ctx, p1.ID))
	if old.ConversationID != 0 || old.LastConversationID != a.ID || old.PID != 0 {
		t.Fatalf("old pane: %+v", old)
	}
	got, ok, err := s.PaneByConversation(ctx, a.ID)
	noErr(t, err)
	if !ok || got.ID != p2.ID {
		t.Fatalf("PaneByConversation = %+v %v", got, ok)
	}
}

func TestLinkTopicAndInvariants(t *testing.T) {
	s := newStore(t)
	a := conv(t, s, "a")
	th, err := s.LinkTopic(ctx, 1, 10, a.ID, "name", t0)
	noErr(t, err)
	if th.CurrentConversationID != a.ID || th.Status != ThreadLive || th.Mode != ModeProgress || th.Verbose != VerboseOff || th.Reasoning || th.Muted {
		t.Fatalf("%+v", th)
	}
	if got := must(s.Conversation(ctx, a.ID)); got.ThreadID != th.ID {
		t.Fatalf("conversation thread_id = %d", got.ThreadID)
	}
	noErr(t, s.CheckInvariants(ctx))

	if _, err := s.LinkTopic(ctx, 1, 11, a.ID, "again", t0); !errors.Is(err, ErrTopicTaken) {
		t.Fatalf("err = %v", err)
	}
	if ths := must(s.Threads(ctx)); len(ths) != 1 {
		t.Fatalf("failed link left a thread: %+v", ths)
	}
	// Duplicate topic IDs fail and leave nothing behind.
	b := conv(t, s, "b")
	if _, err := s.LinkTopic(ctx, 1, 10, b.ID, "dup", t0); err == nil {
		t.Fatal("duplicate topic accepted")
	}
	if got := must(s.Conversation(ctx, b.ID)); got.ThreadID != 0 {
		t.Fatal("conversation linked despite failure")
	}

	// After closing, the conversation can get a new topic.
	noErr(t, s.SetThreadStatus(ctx, th.ID, ThreadClosed))
	if _, ok, _ := s.ThreadByConversation(ctx, a.ID); ok {
		t.Fatal("closed topic returned")
	}
	th2, err := s.LinkTopic(ctx, 1, 12, a.ID, "relinked", t0)
	noErr(t, err)
	got, ok, _ := s.ThreadByConversation(ctx, a.ID)
	if !ok || got.ID != th2.ID {
		t.Fatalf("ThreadByConversation = %+v %v", got, ok)
	}
	noErr(t, s.CheckInvariants(ctx))
}

func TestTakeOver(t *testing.T) {
	s := newStore(t)
	th, a := topic(t, s, 1)
	b, c := conv(t, s, "b"), conv(t, s, "c")

	noErr(t, s.TakeOver(ctx, th.ID, b.ID))
	got := must(s.Thread(ctx, th.ID))
	if got.CurrentConversationID != b.ID {
		t.Fatalf("%+v", got)
	}
	noErr(t, s.CheckInvariants(ctx))
	// The old session keeps its topic as history, and can resume it.
	if old := must(s.Conversation(ctx, a.ID)); old.ThreadID != th.ID {
		t.Fatal("history link lost")
	}
	noErr(t, s.TakeOver(ctx, th.ID, a.ID))
	noErr(t, s.CheckInvariants(ctx))
	if got := must(s.Thread(ctx, th.ID)); got.CurrentConversationID != a.ID {
		t.Fatal("resume failed")
	}

	// A session that has another open topic cannot be taken over.
	th2, d := topic(t, s, 2)
	if err := s.TakeOver(ctx, th.ID, d.ID); !errors.Is(err, ErrTopicTaken) {
		t.Fatalf("err = %v", err)
	}
	noErr(t, s.CheckInvariants(ctx))

	// Closed topics refuse take-over; unknown ones are not found.
	noErr(t, s.SetThreadStatus(ctx, th2.ID, ThreadClosed))
	if err := s.TakeOver(ctx, th2.ID, c.ID); err == nil {
		t.Fatal("closed topic accepted take-over")
	}
	if err := s.TakeOver(ctx, 999, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	// A conversation whose topic is closed can be taken over.
	noErr(t, s.TakeOver(ctx, th.ID, d.ID))
	noErr(t, s.CheckInvariants(ctx))
}

func TestCheckInvariantsDetectsBreakage(t *testing.T) {
	s := newStore(t)
	th, a := topic(t, s, 1)
	_, err := s.db.ExecContext(ctx, `UPDATE conversation SET thread_id=NULL WHERE id=?`, a.ID)
	noErr(t, err)
	if err := s.CheckInvariants(ctx); err == nil {
		t.Fatal("broken invariant not reported")
	}
	noErr(t, s.SetThreadStatus(ctx, th.ID, ThreadClosed))
	noErr(t, s.CheckInvariants(ctx)) // closed topics are exempt
}

func TestThreadLookupsAndSettings(t *testing.T) {
	s := newStore(t)
	th, _ := topic(t, s, 5)
	got, ok, err := s.ThreadByTopic(ctx, 100, 5)
	noErr(t, err)
	if !ok || got.ID != th.ID {
		t.Fatal("ThreadByTopic")
	}
	if _, ok, _ := s.ThreadByTopic(ctx, 100, 6); ok {
		t.Fatal("found missing topic")
	}
	noErr(t, s.SetThreadName(ctx, th.ID, "new name"))
	noErr(t, s.SetThreadSettings(ctx, th.ID, ModeFinal, VerboseFull, true, true))
	got = must(s.Thread(ctx, th.ID))
	if got.Name != "new name" || got.Mode != ModeFinal || got.Verbose != VerboseFull || !got.Reasoning || !got.Muted {
		t.Fatalf("%+v", got)
	}
	if err := s.SetThreadSettings(ctx, th.ID, "bogus", VerboseOff, false, false); err == nil {
		t.Fatal("bad mode accepted")
	}

	th2, _ := topic(t, s, 6)
	noErr(t, s.SetThreadStatus(ctx, th2.ID, ThreadOffline))
	if ths := must(s.Threads(ctx, ThreadOffline)); len(ths) != 1 || ths[0].ID != th2.ID {
		t.Fatalf("%+v", ths)
	}
	if ths := must(s.Threads(ctx, ThreadLive, ThreadOffline)); len(ths) != 2 {
		t.Fatalf("%+v", ths)
	}
	noErr(t, s.SetThreadStatus(ctx, th2.ID, ThreadClosed))
	if got := must(s.Thread(ctx, th2.ID)); !got.ClosedAt.Equal(t0) {
		t.Fatalf("closed_at = %v", got.ClosedAt)
	}
	noErr(t, s.SetThreadStatus(ctx, th2.ID, ThreadLive))
	if got := must(s.Thread(ctx, th2.ID)); !got.ClosedAt.IsZero() {
		t.Fatal("closed_at not cleared")
	}
}
