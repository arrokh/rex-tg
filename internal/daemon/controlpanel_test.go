package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rex-tg/internal/miniapp"
	"rex-tg/internal/store"
)

// The reply of /rename is HTML; the panel shows plain text.
func TestPlainText(t *testing.T) {
	got := plainText("✏️ Renaming this topic to <b>R&amp;D &lt;x&gt;</b>.")
	if want := "✏️ Renaming this topic to R&D <x>."; got != want {
		t.Errorf("plainText = %q, want %q", got, want)
	}
}

// A claim keeps its result; one that cannot be read counts as still running.
func TestParseControlResult(t *testing.T) {
	if r := parseControlResult(`123|{"ok":true,"message":"done","link":"https://t.me/b/1"}`); !r.OK || r.Message != "done" || r.Link == "" {
		t.Errorf("result %+v", r)
	}
	if r := parseControlResult("123|"); r.OK || !r.Pending {
		t.Errorf("unreadable claim: %+v", r)
	}
}

// Housekeeping drops the claims of acts that are over a day old.
func TestPruneActRecords(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now()
	d := &daemon{st: st, ctx: ctx, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for k, v := range map[string]string{
		"cp_act:new":  fmt.Sprintf("%d|{}", now.Add(-time.Hour).Unix()),
		"cp_act:old":  fmt.Sprintf("%d|{}", now.Add(-25*time.Hour).Unix()),
		"cp_act:junk": "x",
	} {
		if err := st.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	d.pruneActRecords(now)
	if keys, _ := st.KeysWithPrefix(ctx, "cp_act:"); len(keys) != 1 || keys[0] != "cp_act:new" {
		t.Errorf("left: %v, want only cp_act:new", keys)
	}
}

// A panel on a store, with a core that the test runs by hand and an action
// that counts its runs.
func testPanel(t *testing.T, runs *atomic.Int32, wait <-chan miniapp.ControlResult) *controlPanel {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d := &daemon{st: st, ctx: ctx, events: make(chan any, 8), now: time.Now,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		timing: Timing{ControlCoreWait: 50 * time.Millisecond, ControlRunWait: 50 * time.Millisecond}.withDefaults()}
	c := newControlPanel(d)
	c.exec = func(miniapp.ControlAction) (miniapp.ControlResult, <-chan miniapp.ControlResult) {
		runs.Add(1)
		return miniapp.ControlResult{OK: true, Message: "done"}, wait
	}
	return c
}

// runQueued runs the events that wait for the core.
func runQueued(c *controlPanel) {
	for len(c.d.events) > 0 {
		(<-c.d.events).(evLater).f()
	}
}

// A request that the busy core did not take is pending, and its event may
// still run. Asking again with the same act runs the action once. A claim
// that no run holds (the daemon stopped meanwhile) is not run again either,
// and says that the outcome is unknown.
func TestControlPanelRetryRunsOnce(t *testing.T) {
	var runs atomic.Int32
	c := testPanel(t, &runs, nil)
	a := miniapp.ControlAction{Op: miniapp.OpUnlink, Thread: 1, Act: "0123456789abcdef"}
	ctx := context.Background()
	for i := 0; i < 2; i++ { // the core is not running: both events stay queued
		if r := c.Do(ctx, a); r.OK || !r.Pending {
			t.Fatalf("busy: %+v", r)
		}
	}
	runQueued(c)
	if runs.Load() != 1 {
		t.Fatalf("%d runs after two queued events, want 1", runs.Load())
	}
	go func() { // the core runs now
		for ev := range c.d.events {
			ev.(evLater).f()
		}
	}()
	if r := c.Do(ctx, a); !r.OK || r.Pending || r.Message != "done" || runs.Load() != 1 {
		t.Errorf("retry: %+v after %d runs", r, runs.Load())
	}

	// A claim from a daemon that died: nothing runs, the outcome is unknown.
	if err := c.storeResult("fedcba9876543210", controlRunning); err != nil {
		t.Fatal(err)
	}
	r := c.Do(ctx, miniapp.ControlAction{Op: miniapp.OpUnlink, Thread: 1, Act: "fedcba9876543210"})
	if r.OK || r.Pending || !strings.Contains(r.Message, "stopped while it ran") || runs.Load() != 1 {
		t.Errorf("crashed claim: %+v after %d runs", r, runs.Load())
	}
}

// An action that runs in the background and takes longer than the wait is
// pending; asking again says so, and does not run it again.
func TestControlPanelStillRunning(t *testing.T) {
	var runs atomic.Int32
	c := testPanel(t, &runs, make(chan miniapp.ControlResult)) // never ends
	go func() {
		for ev := range c.d.events {
			ev.(evLater).f()
		}
	}()
	a := miniapp.ControlAction{Op: miniapp.OpOpen, Act: "0123456789abcdef", Pane: 1}
	for i := 0; i < 2; i++ {
		if r := c.Do(context.Background(), a); r.OK || !r.Pending {
			t.Fatalf("round %d: %+v", i, r)
		}
	}
	if runs.Load() != 1 {
		t.Errorf("%d runs, want 1", runs.Load())
	}
}

// The report says what a rename did, and what it did not, and why.
func TestRenameReportText(t *testing.T) {
	for _, c := range []struct {
		rep  renameReport
		auto bool
		want string
	}{
		{renameReport{topic: true, pane: true, tab: true}, false, "✏️ Renamed the topic, its Rex pane, and its tab to X."},
		{renameReport{pane: true, tab: true}, false, "✏️ Renamed the Rex pane and its tab to X."},
		{renameReport{topic: true, pane: true, tab: true}, true, "✏️ Reset the topic, its Rex pane, and its tab to the automatic name."},
		{renameReport{topic: true, pane: true, skips: []string{"its tab (it is shared with another agent)"}}, false,
			"✏️ Renamed the topic and its Rex pane to X. Not renamed: its tab (it is shared with another agent)."},
		{renameReport{skips: []string{"its Rex pane and its tab (the agent has no pane now)"}}, false,
			"Nothing was renamed. Not renamed: its Rex pane and its tab (the agent has no pane now)."},
	} {
		if got := c.rep.text("X", c.auto); got != c.want {
			t.Errorf("%+v:\n got %q\nwant %q", c.rep, got, c.want)
		}
	}
}
