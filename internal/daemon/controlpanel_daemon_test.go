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

	// Rename the topic: the name sticks, and shows as custom.
	r = do(miniapp.ControlAction{Op: miniapp.OpRenameTopic, Act: act(), Thread: thread, Name: "Billing API"})
	if !r.OK || strings.Contains(r.Message, "<") {
		t.Fatalf("rename topic: %+v", r)
	}
	e.waitFor("the topic's new name", func() bool {
		ts := e.tg.Topics(chatID)
		return len(ts) == 1 && strings.Contains(ts[0].Name, "Billing API")
	})
	st = miniapp.ControlState{}
	miniControl(t, e, addr, base, nil, &st)
	if !st.Agents[0].Topic.Custom {
		t.Errorf("the topic is not custom: %+v", st.Agents[0].Topic)
	}

	// Rename the pane: Rex gets the call once, even when the act repeats, and
	// the topic keeps the name that was given to it.
	same := act()
	rp := miniapp.ControlAction{Op: miniapp.OpRenamePane, Act: same, Pane: ag.Pane, Name: "api server"}
	r = do(rp)
	again := do(rp)
	if !r.OK || !strings.Contains(r.Message, "keeps") || again != r {
		t.Fatalf("rename pane: %+v, again %+v", r, again)
	}
	if got := e.fake.BlockLabels()[rexBlock]; got != "api server" || e.countRex("block", "rename") != 1 {
		t.Errorf("block label %q after %d renames", got, e.countRex("block", "rename"))
	}
	st = miniapp.ControlState{}
	miniControl(t, e, addr, base, nil, &st)
	if st.Agents[0].Label != "api server" {
		t.Errorf("pane label %q", st.Agents[0].Label)
	}

	// Pause: typing stops, the main chat hears it, and a new agent is refused.
	if r = do(miniapp.ControlAction{Op: miniapp.OpPause, Act: act(), On: true}); !r.OK {
		t.Fatalf("pause: %+v", r)
	}
	e.waitText(0, "Typing is paused")
	st = miniapp.ControlState{}
	miniControl(t, e, addr, base, nil, &st)
	if !st.Paused {
		t.Error("the state does not say paused")
	}
	if r = do(miniapp.ControlAction{Op: miniapp.OpNew, Act: act(), Agent: "pi", Path: e.home}); r.OK || !strings.Contains(r.Message, "paused") {
		t.Errorf("new while paused: %+v", r)
	}
	if r = do(miniapp.ControlAction{Op: miniapp.OpPause, Act: act(), On: false}); !r.OK {
		t.Fatalf("unpause: %+v", r)
	}

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
		{Op: miniapp.OpRenamePane, Pane: 99999, Name: "x"},
		{Op: miniapp.OpRenameTopic, Thread: 99999, Name: "x"},
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
