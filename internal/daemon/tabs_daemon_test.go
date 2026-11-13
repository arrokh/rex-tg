package daemon_test

import (
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"testing"
)

// tabSession scripts Rex so that the agent's pane is in the session for /new,
// in tab W1, which has the given label.
func (e *env) tabSession(label string) {
	e.fake.On([]string{"ls"}, fmt.Sprintf(`{"sessions":[{"session_id":%q,"label":"rex-tg"}]}`, rexSess), 0)
	e.fake.On([]string{"block", "ls"}, fmt.Sprintf(`{"blocks":[{"block_id":%q,"label":"pane one","window_id":"W1"}]}`, rexBlock), 0)
	e.fake.On([]string{"window", "rename"}, "", 0)
	e.setTab(label)
}

// setTab scripts the label that Rex reports for tab W1.
func (e *env) setTab(label string) {
	e.fake.On([]string{"window", "ls"}, fmt.Sprintf(`{"windows":[{"window_id":"W1","label":%q},{"window_id":"W9","label":"other"}]}`, label), 0)
}

// tabRenames lists the new names given to tabs, oldest first.
func (e *env) tabRenames() []string {
	var out []string
	for _, c := range e.fake.Calls() {
		if i := slices.Index(c.Args, "rename"); i >= 0 && slices.Contains(c.Args, "window") {
			out = append(out, c.Args[len(c.Args)-1])
		}
	}
	return out
}

// A tab follows its topic's name when the topic is linked, and at the next
// name check when its label differs. A tab with the right label is not
// renamed.
func TestTabFollowsTheTopicName(t *testing.T) {
	e := newEnv(t)
	var skew atomic.Int64
	opt := e.options()
	opt.Now = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	e.seedOwner()
	e.tabSession("cux") // the tab opened with another label
	p := e.newPi("pi-tab-follow")
	p.User("start")
	p.Final("ready")
	e.startWith(opt)
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "ready")

	e.waitFor("the tab takes the topic's name at link time", func() bool {
		return slices.Equal(e.tabRenames(), []string{"pane one"})
	})

	// Someone gives the tab another label: the next check puts the name back.
	e.setTab("stale")
	skew.Add(int64(31 * time.Second))
	e.waitFor("the tab follows again", func() bool {
		return slices.Equal(e.tabRenames(), []string{"pane one", "pane one"})
	})

	// With the right label, a check renames nothing.
	e.setTab("pane one")
	lists := e.countRex("window", "ls")
	skew.Add(int64(31 * time.Second))
	e.waitFor("a name check", func() bool { return e.countRex("window", "ls") > lists })
	if got := e.tabRenames(); len(got) != 2 {
		t.Errorf("tab renames = %q, want 2", got)
	}
}

// The tab of a stopped agent shows a 🔴, as the topic does. It goes away when
// the agent runs again.
func TestTabShowsThatTheAgentStopped(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	e.tabSession("pane one")
	p := e.newPi("pi-tab-offline")
	p.User("start")
	p.Final("ready")
	e.start()
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "ready")

	e.setProcess("zsh", "-zsh", 777)
	e.waitText(thread, "pi stopped: it exited, and the pane is still open.")
	e.waitFor("the 🔴 on the tab", func() bool {
		r := e.tabRenames()
		return len(r) > 0 && r[len(r)-1] == "🔴 pane one"
	})

	e.setTab("🔴 pane one")
	e.setProcess("node", "pi", 4343)
	e.hookPi(p, "resume")
	e.waitText(thread, "pi is running again")
	e.waitFor("the tab without the 🔴", func() bool {
		r := e.tabRenames()
		return len(r) > 1 && r[len(r)-1] == "pane one"
	})
}

// An agent in the session for /new renames it (Rex tells agents to name
// their session): rex-tg still knows the session by its ID, keeps naming its
// tabs, and names the session back at the next name check.
func TestRenamedNewSessionIsNamedBack(t *testing.T) {
	e := newEnv(t)
	var skew atomic.Int64
	opt := e.options()
	opt.Now = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	e.seedOwner()
	e.tabSession("cux")
	e.fake.On([]string{"session", "rename"}, "", 0)
	p := e.newPi("pi-session-name")
	p.User("start")
	p.Final("ready")
	e.startWith(opt)
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "ready")
	e.waitFor("the tab takes the topic's name", func() bool { return slices.Equal(e.tabRenames(), []string{"pane one"}) })

	e.fake.On([]string{"ls"}, fmt.Sprintf(`{"sessions":[{"session_id":%q,"label":"lovoice-handoff-status"}]}`, rexSess), 0)
	e.setTab("stale")
	skew.Add(int64(31 * time.Second))
	e.waitFor("the session named back", func() bool {
		for _, c := range e.fake.Calls() {
			if slices.Contains(c.Args, "session") && slices.Contains(c.Args, "rename") {
				return c.Args[len(c.Args)-2] == rexSess && c.Args[len(c.Args)-1] == "rex-tg"
			}
		}
		return false
	})
	e.waitFor("the tab still follows its topic", func() bool {
		return slices.Equal(e.tabRenames(), []string{"pane one", "pane one"})
	})
}
