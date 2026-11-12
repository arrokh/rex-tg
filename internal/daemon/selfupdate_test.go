package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"rex-tg/internal/agents"
	"rex-tg/internal/registry"
	"rex-tg/internal/store"
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

func TestAgentListStaysWithinTelegramLimits(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	var views []registry.AgentView
	for i := range 40 {
		state := "idle"
		if i == 39 {
			state = "blocked" // the last one waits: it must still be shown
		}
		views = append(views, registry.AgentView{
			Pane:         store.Pane{ID: int64(i + 1), Label: strings.Repeat("long pane name ", 4), State: state, StateAt: now.Add(-time.Hour)},
			Runtime:      agents.Claude,
			Conversation: &store.Conversation{CWD: "/work/" + strings.Repeat("folder", 8), Title: strings.Repeat("a long session title ", 5)},
		})
	}
	text, markup := (&daemon{}).renderAgents(views, nil, now)
	if n := utf8.RuneCountInString(text); n > 4096 {
		t.Errorf("text has %d characters, over Telegram's 4096", n)
	}
	if len(markup.InlineKeyboard) > 100 {
		t.Errorf("%d button rows, over Telegram's 100", len(markup.InlineKeyboard))
	}
	if !strings.Contains(text, "more") || !strings.Contains(text, "Active agents (40)") {
		t.Errorf("text does not say that more agents run: %q", text[len(text)-200:])
	}
	if !strings.Contains(text, "waiting for you") {
		t.Error("the agent that waits was cut, but it must come first")
	}
}

func TestWaitedText(t *testing.T) {
	for d, want := range map[time.Duration]string{
		3 * time.Second:                 "3 s",
		10 * time.Minute:                "10 min",
		59*time.Minute + 59*time.Second: "59 min",
		2*time.Hour + 5*time.Minute:     "2h 05m",
		time.Minute + 30*time.Second:    "1 min",
	} {
		if got := waitedText(d); got != want {
			t.Errorf("waitedText(%s) = %q, want %q", d, got, want)
		}
	}
}
