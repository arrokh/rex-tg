package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"rex-tg/internal/miniapp"
)

// miniControl asks the control panel's API as the owner. body is a JSON
// action for POST, or nil for GET.
func miniControl(t *testing.T, e *env, addr, path string, body *miniapp.ControlAction, out any) int {
	t.Helper()
	var req *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		req, _ = http.NewRequest("POST", "http://"+addr+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest("GET", "http://"+addr+path, nil)
	}
	req.Header.Set("X-Telegram-Init-Data", signedInit(e.tg.Token(), ownerID))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		_ = json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

// /rt opens the control panel. From it the owner opens a topic, renames
// it and its pane, unlinks it, and pauses typing. An action with the same act
// id runs once.
func TestControlPanel(t *testing.T) {
	addr := freeAddr(t)
	e := newEnv(t)
	e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
	e.cfg.MiniApp.Listen = addr
	e.seedOwner()
	p := e.newPi("pi-panel")
	p.User("start")
	p.Final("ready")
	e.fake.On([]string{"block", "rename"}, "", 0)
	e.fake.On([]string{"new"}, `{"session_id":"S2","initial_windows":[{"window_id":"W2","block_ids":["B2"]}]}`, 0)
	e.fake.On([]string{"window", "rename"}, "", 0)
	proj := filepath.Join(e.home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	e.start()
	e.hookPi(p, "startup")
	e.waitFor("a resolved pane", func() bool {
		pn, ok, err := e.peek().PaneByBlock(context.Background(), "local", rexBlock)
		return err == nil && ok && pn.ConversationID != 0
	})
	e.waitFor("the Mini App's server", func() bool {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	})

	// The command posts a button to the one control page.
	var u string
	e.say(0, "/rt")
	e.waitFor("the control button", func() bool { u = webAppURL(e, 0, "🎛 Open control panel"); return u != "" })
	id := pageID(u)
	base := "/api/control?id=" + id
	var st miniapp.ControlState
	if code := miniControl(t, e, addr, base, nil, &st); code != http.StatusOK || len(st.Agents) != 1 {
		t.Fatalf("state: %d %+v", code, st)
	}
	ag := st.Agents[0]
	if !ag.Ready || ag.Topic != nil || ag.Agent != "pi" || ag.Label != "pane one" || len(st.Starters) == 0 || st.Home.Path != e.home {
		t.Errorf("agent %+v, starters %+v, home %+v", ag, st.Starters, st.Home)
	}
	do := func(a miniapp.ControlAction) miniapp.ControlResult {
		t.Helper()
		var r miniapp.ControlResult
		if code := miniControl(t, e, addr, "/api/control/do?id="+id, &a, &r); code != http.StatusOK {
			t.Fatalf("%s: status %d", a.Op, code)
		}
		return r
	}
	n := 0
	act := func() string { n++; return fmt.Sprintf("%016x", n) }

	// Open makes the topic and says where it is.
	r := do(miniapp.ControlAction{Op: miniapp.OpOpen, Act: act(), Pane: ag.Pane})
	if !r.OK || !strings.HasPrefix(r.Link, "https://t.me/") {
		t.Fatalf("open: %+v", r)
	}
	if len(e.tg.Topics(chatID)) != 1 {
		t.Fatalf("topics: %+v", e.tg.Topics(chatID))
	}
	st = miniapp.ControlState{}
	miniControl(t, e, addr, base, nil, &st)
	if st.Agents[0].Topic == nil || st.Agents[0].Topic.Link != r.Link || st.Agents[0].Topic.Custom {
		t.Fatalf("topic after open: %+v", st.Agents[0].Topic)
	}
	thread := st.Agents[0].Topic.Thread

	// The pane has a tab in a session of the user's.
	e.fake.On([]string{"block", "ls"}, fmt.Sprintf(`{"blocks":[{"block_id":%q,"label":"pane one","window_id":"W1"}]}`, rexBlock), 0)

	e.setTab("pane one")

	// Rename: the topic, the pane in Rex, and its tab, once, also when the
	// act repeats. The topic's name sticks, and shows as custom.
	rn := miniapp.ControlAction{Op: miniapp.OpRename, Act: act(), Pane: ag.Pane, Name: "Billing API"}
	r = do(rn)
	if again := do(rn); !r.OK || r.Link == "" || again != r || !strings.Contains(r.Message, "Renamed the topic, its Rex pane, and its tab to Billing API") {
		t.Fatalf("rename: %+v, again %+v", r, again)
	}
	e.waitFor("the topic's new name", func() bool {
		ts := e.tg.Topics(chatID)
		return len(ts) == 1 && strings.Contains(ts[0].Name, "Billing API")
	})
	if got := e.fake.BlockLabels()[rexBlock]; got != "Billing API" || e.countRex("block", "rename") != 1 {
		t.Errorf("block label %q after %d renames", got, e.countRex("block", "rename"))
	}
	if got := e.tabRenames(); !slices.Equal(got, []string{"Billing API"}) {
		t.Errorf("tab renames %q, want one", got)
	}
	st = miniapp.ControlState{}
	miniControl(t, e, addr, base, nil, &st)
	if !st.Agents[0].Topic.Custom || st.Agents[0].Label != "Billing API" {
		t.Errorf("after rename: %+v", st.Agents[0])
	}

	// Pages of the agent's topic: the id opens a page of the right kind. The
	// changes view refuses a folder outside a git repository.
	for kind, want := range map[string]string{miniapp.PageScreen: "screen", miniapp.PageFiles: "files"} {
		pr := do(miniapp.ControlAction{Op: miniapp.OpPage, Act: act(), Pane: ag.Pane, Kind: kind})
		if !pr.OK || pr.Page == "" {
			t.Fatalf("page %s: %+v", kind, pr)
		}
		if code, body := miniGet(t, e, addr, "/api/page?id="+pr.Page, ownerID); code != http.StatusOK || body["kind"] != want {
			t.Errorf("page %s: %d %v", kind, code, body)
		}
	}
	if pr := do(miniapp.ControlAction{Op: miniapp.OpPage, Act: act(), Pane: ag.Pane, Kind: miniapp.PageChanges}); pr.OK || !strings.Contains(pr.Message, "not in a git repository") {
		t.Errorf("changes outside a repository: %+v", pr)
	}
	if !st.Files || !st.Changes {
		t.Errorf("files %v, changes %v", st.Files, st.Changes)
	}

	// Auto: the pane gets Rex's default name, and the topic is named after
	// the session again.
	if r = do(miniapp.ControlAction{Op: miniapp.OpRename, Act: act(), Pane: ag.Pane, Name: "auto"}); !r.OK || !strings.Contains(r.Message, "Reset") {
		t.Fatalf("rename auto: %+v", r)
	}
	if got := e.fake.BlockLabels()[rexBlock]; got != "shell" {
		t.Errorf("block label %q after auto, want shell", got)
	}
	st = miniapp.ControlState{}
	miniControl(t, e, addr, base, nil, &st)
	if st.Agents[0].Topic.Custom {
		t.Errorf("still custom after auto: %+v", st.Agents[0].Topic)
	}

	// Typing is paused from the chat: the panel shows it, and a new agent is
	// refused.
	e.say(0, "/pause")
	e.waitText(0, "Typing is paused")
	st = miniapp.ControlState{}
	miniControl(t, e, addr, base, nil, &st)
	if !st.Paused {
		t.Error("the state does not say paused")
	}
	if r = do(miniapp.ControlAction{Op: miniapp.OpNew, Act: act(), Agent: "pi", Path: e.home}); r.OK || !strings.Contains(r.Message, "paused") {
		t.Errorf("new while paused: %+v", r)
	}
	e.say(0, "/unpause")
	e.waitText(0, "Typing is on again")

	// A new agent starts in a folder inside home, once, also when the act
	// repeats.
	newAct := miniapp.ControlAction{Op: miniapp.OpNew, Act: act(), Agent: "pi", Path: proj}
	if r = do(newAct); !r.OK || r.Pending {
		t.Fatalf("new: %+v", r)
	}
	var args []string
	e.waitFor("rex new", func() bool { args = e.rexCommand("new"); return args != nil })
	if !slices.Contains(args, "--cwd="+proj) {
		t.Errorf("rex %q: not in %s", args, proj)
	}
	if again := do(newAct); again != r {
		t.Errorf("repeated new: %+v, first %+v", again, r)
	}

	// Things that are not there fail cleanly.
	for _, a := range []miniapp.ControlAction{
		{Op: miniapp.OpOpen, Pane: 99999},
		{Op: miniapp.OpRename, Pane: 99999, Name: "x"},
		{Op: miniapp.OpPage, Pane: 99999, Kind: miniapp.PageScreen},
		{Op: miniapp.OpUnlink, Thread: 99999},
		{Op: miniapp.OpNew, Agent: "nope", Path: e.home},
		{Op: miniapp.OpNew, Agent: "pi", Path: e.home + "/no-such-folder"},
		{Op: miniapp.OpNew, Agent: "pi", Path: t.TempDir()}, // outside home, not offered
	} {
		a.Act = act()
		if r := do(a); r.OK || r.Message == "" {
			t.Errorf("%+v: %+v", a, r)
		}
	}

	if r := do(miniapp.ControlAction{Op: miniapp.OpNew, Act: act(), Agent: "pi", Path: t.TempDir()}); !strings.Contains(r.Message, "outside your home") {
		t.Errorf("a folder outside home: %+v", r)
	}

	// Unlink closes the topic; a second one says so.
	if r = do(miniapp.ControlAction{Op: miniapp.OpUnlink, Act: act(), Thread: thread}); !r.OK {
		t.Fatalf("unlink: %+v", r)
	}
	if r = do(miniapp.ControlAction{Op: miniapp.OpUnlink, Act: act(), Thread: thread}); r.OK || !strings.Contains(r.Message, "already") {
		t.Errorf("unlink twice: %+v", r)
	}
	st = miniapp.ControlState{}
	miniControl(t, e, addr, base, nil, &st)
	if n := rexNewCalls(e); n != 1 {
		t.Errorf("%d rex new calls, want 1", n)
	}
	if len(st.Agents) != 1 || st.Agents[0].Topic != nil {
		t.Errorf("agents after unlink: %+v", st.Agents)
	}
}

// rexNewCalls counts the calls that start a Rex session.
func rexNewCalls(e *env) int {
	n := 0
	for _, c := range e.fake.Calls() {
		args := c.Args
		for len(args) > 0 && strings.HasPrefix(args[0], "--autostart") {
			args = args[1:]
		}
		if len(args) > 0 && args[0] == "new" {
			n++
		}
	}
	return n
}
