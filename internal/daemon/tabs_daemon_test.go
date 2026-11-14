package daemon_test

import (
	"context"
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

	// With the right label, a check renames nothing.
	lists := e.countRex("window", "ls")
	e.setTab("pane one")
	skew.Add(int64(31 * time.Second))
	e.waitFor("a name check", func() bool { return e.countRex("window", "ls") > lists })
	if got := e.tabRenames(); len(got) != 1 {
		t.Errorf("tab renames = %q, want 1", got)
	}

	// Someone renames the tab (you, or an agent with rex window rename): the
	// name stays, and the topic takes it.
	e.setTab("[x] preps")
	skew.Add(int64(31 * time.Second))
	topic := func() string {
		for _, tp := range e.tg.Topics(chatID) {
			if tp.ThreadID == thread {
				return tp.Name
			}
		}
		return ""
	}
	e.waitFor("the topic follows the tab", func() bool { return topic() == "preps [x]" })
	skew.Add(int64(31 * time.Second))
	lists = e.countRex("window", "ls")
	e.waitFor("another name check", func() bool { return e.countRex("window", "ls") > lists })
	if got := e.tabRenames(); len(got) != 1 {
		t.Errorf("tab renames = %q: the tab's new name was undone", got)
	}

	// A " #2" that was typed is part of the name.
	e.setTab("Sprint #2")
	skew.Add(int64(31 * time.Second))
	e.waitFor("the topic takes the typed name", func() bool { return topic() == "Sprint #2" })

	// Named back to its "<folder> · <agent>" form, the tab and the topic
	// follow the pane and the session again.
	e.setTab("proj · pi") // the folder of paneCWD and the agent
	skew.Add(int64(31 * time.Second))
	e.waitFor("the topic named after the pane again", func() bool { return topic() == "pane one" })
	skew.Add(int64(31 * time.Second))
	e.waitFor("the tab named after the topic again", func() bool {
		r := e.tabRenames()
		return len(r) == 2 && r[1] == "pane one"
	})
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
// their session): rex-tg still knows the session by its ID, still finds its
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
	// rex-tg still finds its tab in the renamed session: the tab's new label
	// (given outside rex-tg) names the topic.
	e.waitFor("the topic follows its tab", func() bool {
		for _, tp := range e.tg.Topics(chatID) {
			if tp.ThreadID == thread {
				return tp.Name == "stale"
			}
		}
		return false
	})
}

// /rename names the topic and its tab in the session of /new; the name
// sticks until /rename auto.
func TestRenameCommandNamesTheTopicAndItsTab(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	e.tabSession("pane one")
	e.fake.On([]string{"block", "rename"}, "", 0)
	e.fake.FollowWindowRenames() // Rex shows a tab's new name
	p := e.newPi("pi-rename")
	p.User("start")
	p.Final("ready")
	e.start()
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "ready")
	topic := func() string {
		for _, tp := range e.tg.Topics(chatID) {
			if tp.ThreadID == thread {
				return tp.Name
			}
		}
		return ""
	}

	e.say(thread, "/rename explore lovina")
	e.waitFor("the topic renamed", func() bool { return topic() == "explore lovina" })
	e.waitFor("the tab renamed", func() bool {
		r := e.tabRenames()
		return len(r) > 0 && r[len(r)-1] == "explore lovina"
	})
	e.waitText(thread, "Renamed the topic, its Rex pane, and its tab to explore lovina")
	if len(e.pastes()) != 0 {
		t.Errorf("/rename was typed into the pane: %q", e.pastes())
	}
	// The pane is named in Rex. The tab is in the session of /new: only
	// syncTab renames it (once, to the name that was kept).
	if got := e.fake.BlockLabels()[rexBlock]; got != "explore lovina" {
		t.Errorf("pane label %q", got)
	}
	if got := e.tabRenames(); !slices.Equal(got, []string{"explore lovina"}) {
		t.Errorf("tab renames %q, want one", got)
	}

	// The same name again still reaches Telegram, which says it has it.
	edits := len(e.tg.Calls("editForumTopic"))
	e.say(thread, "/rename explore lovina")
	e.waitText(thread, "The topic already had that name in Telegram.")
	if n := len(e.tg.Calls("editForumTopic")); n <= edits {
		t.Errorf("%d editForumTopic calls after the repeat, want more than %d", n, edits)
	}
	// A name that Telegram refuses is not saved, and the reply says so.
	e.tg.FailNext("editForumTopic", 1, 400, "Bad Request: nope", 0)
	e.say(thread, "/rename other name")
	e.waitText(thread, "the topic (Telegram refused it; rex-tg tries again")
	if th, err := e.peek().Thread(context.Background(), 1); err != nil || th.Name != "explore lovina" {
		t.Errorf("stored name after a refused rename: %+v %v", th, err)
	}

	e.setTab("explore lovina")
	e.say(thread, "/rename auto")
	e.waitText(thread, "to the automatic name")
	if got := e.fake.BlockLabels()[rexBlock]; got != "shell" {
		t.Errorf("pane label %q after auto, want shell", got)
	}
	e.waitFor("the topic named after the session again", func() bool { return topic() != "explore lovina" && topic() != "" })
}
