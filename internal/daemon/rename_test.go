package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/config"
	"rex-tg/internal/rex"
	"rex-tg/internal/store"
)

// renameRex is an in-memory Rex for the rename tests.
type renameRex struct {
	rexClient // the methods the rename does not use; calling one panics
	mu        sync.Mutex
	sessions  []rex.Session
	blocks    []rex.Block
	windows   []rex.Window
	failBlock bool
	panes     []string // "block=label"
	tabs      []string // "window=label"
}

func (r *renameRex) Sessions(context.Context) ([]rex.Session, error) { return r.sessions, nil }
func (r *renameRex) Blocks(context.Context, string) ([]rex.Block, error) {
	return r.blocks, nil
}
func (r *renameRex) Windows(context.Context, string) ([]rex.Window, error) { return r.windows, nil }
func (r *renameRex) RenameBlock(_ context.Context, _, block, label string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failBlock {
		return errors.New("rex failed")
	}
	r.panes = append(r.panes, block+"="+label)
	return nil
}
func (r *renameRex) RenameWindow(_ context.Context, _, window, label string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tabs = append(r.tabs, window+"="+label)
	return nil
}

// renameFixture is an agent in pane B2 of session S1 (tab W1), with a topic.
type renameFixture struct {
	d    *daemon
	rx   *renameRex
	pane store.Pane
	conv store.Conversation
	th   store.Thread
}

// newRenameFixture makes the fixture. own puts the pane in the session of
// /new; shared puts another agent's pane B1 in front of it in the same tab.
func newRenameFixture(t *testing.T, own, shared bool) *renameFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	label := "main"
	if own {
		label = "rex-tg"
	}
	rx := &renameRex{
		sessions: []rex.Session{{ID: "S1", Label: label}},
		windows:  []rex.Window{{ID: "W1", Label: "old"}, {ID: "W9", Label: "other"}},
	}
	cfg := config.Default(t.TempDir())
	d := &daemon{st: st, ctx: ctx, rx: rx, cfg: cfg, now: time.Now, events: make(chan any, 16), regq: newJobQueue(),
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		timing: Timing{}.withDefaults()}
	go func() { // a scan that finds nothing new
		for {
			j, ok := d.regq.pop(ctx)
			if !ok {
				return
			}
			if j.done != nil {
				j.done <- nil
			}
		}
	}()
	mk := func(block, native string) (store.Pane, store.Conversation) {
		conv, err := st.UpsertConversation(ctx, store.Conversation{Runtime: agents.Pi, NativeID: native, Transcript: "/x/" + native, CWD: "/work/app", Title: "Fix login", FirstSeenAt: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		p, err := st.UpsertPane(ctx, "local", block, "S1", "pane one", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetPaneOccupant(ctx, p.ID, conv.ID, 1, "node", "hook"); err != nil {
			t.Fatal(err)
		}
		p, _ = st.Pane(ctx, p.ID)
		return p, conv
	}
	f := &renameFixture{d: d, rx: rx}
	if shared {
		mk("B1", "other")
		rx.blocks = append(rx.blocks, rex.Block{ID: "B1", WindowID: "W1"})
	}
	f.pane, f.conv = mk("B2", "mine")
	rx.blocks = append(rx.blocks, rex.Block{ID: "B2", WindowID: "W1"})
	th, err := st.LinkTopic(ctx, 1, 5, f.conv.ID, "name", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.th = th
	return f
}

func (f *renameFixture) plan(name string, auto bool) renamePlan {
	pl := renamePlan{name: name, auto: auto, th: &f.th, conv: &f.conv, pane: &f.pane}
	if auto {
		pl.name = ""
	}
	return pl
}

func TestRunRename(t *testing.T) {
	cases := []struct {
		desc                  string
		own, shared           bool
		tweak                 func(*renameFixture, *renamePlan)
		auto                  bool
		wantPanes, wantTabs   []string
		wantTopic, wantTabRep bool
		wantText              string // a part of the report
	}{
		{desc: "own session, own tab: syncTab renames it", own: true,
			wantPanes: []string{"B2=X"}, wantTopic: true, wantTabRep: true, wantText: "Renamed the topic, its Rex pane, and its tab to X."},
		{desc: "own session, shared tab: skipped", own: true, shared: true,
			wantPanes: []string{"B2=X"}, wantTopic: true, wantText: "its tab (it also holds another agent)"},
		{desc: "user session, shared tab: skipped", shared: true,
			wantPanes: []string{"B2=X"}, wantTopic: true, wantText: "its tab (it also holds another agent)"},
		{desc: "user session: the tab is renamed here",
			wantPanes: []string{"B2=X"}, wantTabs: []string{"W1=X"}, wantTopic: true, wantTabRep: true},
		{desc: "user session, auto: the tab is named like syncTab names it", auto: true,
			tweak: func(f *renameFixture, pl *renamePlan) { // the scan has seen the new pane name
				if _, err := f.d.st.UpsertPane(f.d.ctx, "local", "B2", "S1", "shell", time.Now()); err != nil {
					panic(err)
				}
			},
			wantPanes: []string{"B2=shell"}, wantTabs: []string{"W1=Fix login"}, wantTopic: true, wantTabRep: true, wantText: "Reset the topic, its Rex pane, and its tab"},
		{desc: "a panel agent without a topic, in the session of /new", own: true,
			tweak:     func(f *renameFixture, pl *renamePlan) { pl.th = nil },
			wantPanes: []string{"B2=X"}, wantTabs: []string{"W1=X"}, wantTabRep: true, wantText: "Renamed the Rex pane and its tab to X."},
		{desc: "an offline topic keeps its name",
			tweak: func(f *renameFixture, pl *renamePlan) {
				f.th.Status = store.ThreadOffline
				pl.pane = nil
			},
			wantText: "the topic (it takes the name when the agent runs again)"},
		{desc: "no pane now",
			tweak:     func(f *renameFixture, pl *renamePlan) { pl.pane = nil },
			wantTopic: true, wantText: "the agent has no pane now"},
		{desc: "Rex cannot rename the pane: the tab still follows a name",
			tweak:     func(f *renameFixture, pl *renamePlan) { f.rx.failBlock = true },
			wantTabs:  []string{"W1=X"},
			wantTopic: true, wantTabRep: true, wantText: "its Rex pane (Rex could not rename it)"},
		{desc: "auto when Rex cannot rename the pane: nothing is reset", auto: true,
			tweak:    func(f *renameFixture, pl *renamePlan) { f.rx.failBlock = true },
			wantText: "the topic and its tab (the pane still has its name)"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			f := newRenameFixture(t, c.own, c.shared)
			pl := f.plan("X", c.auto)
			if c.tweak != nil {
				c.tweak(f, &pl)
			}
			rep := f.d.runRename(pl)
			if strings.Join(f.rx.panes, ",") != strings.Join(c.wantPanes, ",") || strings.Join(f.rx.tabs, ",") != strings.Join(c.wantTabs, ",") {
				t.Errorf("pane renames %q, tab renames %q; want %q, %q", f.rx.panes, f.rx.tabs, c.wantPanes, c.wantTabs)
			}
			if rep.topic != c.wantTopic || rep.tab != c.wantTabRep {
				t.Errorf("report topic %v tab %v, want %v %v: %+v", rep.topic, rep.tab, c.wantTopic, c.wantTabRep, rep)
			}
			if got := rep.text("X", c.auto); !strings.Contains(got, c.wantText) {
				t.Errorf("text %q lacks %q", got, c.wantText)
			}
		})
	}
}

// planRename refuses a pane that is gone, and finds the panel agent's pane
// and, when it has one, its topic. (A name for a pane with a topic would
// rename the topic, which needs the sender; auto does not.)
func TestPlanRename(t *testing.T) {
	f := newRenameFixture(t, false, false)
	d := f.d
	if pl, refusal := d.planRename(nil, f.pane.ID, "auto"); refusal != "" || !pl.auto || pl.pane == nil || pl.th == nil || pl.th.ID != f.th.ID {
		t.Errorf("auto on a live pane: %+v, %q", pl, refusal)
	}
	if _, refusal := d.planRename(nil, f.pane.ID, "   "); !strings.Contains(refusal, "Usage") {
		t.Errorf("no name: %q", refusal)
	}
	if _, refusal := d.planRename(nil, f.pane.ID, "---"); refusal == "" {
		t.Error("a name without letters or digits was taken")
	}
	// Without an open topic the pane alone is renamed.
	if err := d.st.SetThreadStatus(d.ctx, f.th.ID, store.ThreadClosed); err != nil {
		t.Fatal(err)
	}
	if pl, refusal := d.planRename(nil, f.pane.ID, "Billing"); refusal != "" || pl.pane == nil || pl.th != nil || pl.name != "Billing" {
		t.Errorf("a pane without a topic: %+v, %q", pl, refusal)
	}
	if err := d.st.MarkPaneGone(d.ctx, f.pane.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, refusal := d.planRename(nil, f.pane.ID, "Billing"); refusal != "That pane is gone." {
		t.Errorf("a gone pane: %q", refusal)
	}
}
