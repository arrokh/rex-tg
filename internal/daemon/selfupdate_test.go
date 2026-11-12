package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSelfWatchWaitsUntilTheNewBinarySettles(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "rex-tg")
	write := func(s string) {
		t.Helper()
		next := bin + ".tmp"
		if err := os.WriteFile(next, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(next, bin); err != nil {
			t.Fatal(err)
		}
	}
	write("v1")
	w := newSelfWatch(bin)
	if w == nil {
		t.Fatal("no watch")
	}
	if w.check() {
		t.Fatal("unchanged binary counts as updated")
	}
	write("v2, still being copied")
	if w.check() {
		t.Fatal("a fresh change counts at once; want one more check")
	}
	write("v2, complete")
	if w.check() {
		t.Fatal("a binary that still changes counts as settled")
	}
	if !w.check() {
		t.Fatal("a settled new binary does not count")
	}
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	if w.check() {
		t.Fatal("a missing binary counts as updated")
	}
	if newSelfWatch("") != nil || newSelfWatch(filepath.Join(t.TempDir(), "none")) != nil {
		t.Error("a watch without a binary")
	}
}

func TestAgoTextAndStateRank(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{time.Time{}, ""},
		{now.Add(time.Minute), ""},
		{now.Add(-20 * time.Second), "now"},
		{now.Add(-12 * time.Minute), "12m"},
		{now.Add(-5 * time.Hour), "5h"},
		{now.Add(-72 * time.Hour), "3d"},
	} {
		if got := agoText(now, c.at); got != c.want {
			t.Errorf("agoText(%v) = %q, want %q", now.Sub(c.at), got, c.want)
		}
	}
	if !(stateRank("blocked") < stateRank("working") && stateRank("working") < stateRank("idle") && stateRank("idle") < stateRank("error")) {
		t.Error("waiting agents must come first, then working, then idle")
	}
}
