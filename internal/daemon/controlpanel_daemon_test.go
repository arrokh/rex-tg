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
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/miniapp"
	"rex-tg/internal/store"
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
	req.Header.Set("Tailscale-User-Login", "owner@example.com") // as tailscale serve sets it ([miniapp] tailnet_only)
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

func TestControlPanelReconnectsWhenTelegramTopicWasDeleted(t *testing.T) {
	addr := freeAddr(t)
	e := newEnvSerial(t)
	e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
	e.cfg.MiniApp.Listen = addr
	e.seedOwner()
	p := e.newPi("pi-panel-reconnect")
	p.User("start")
	p.Final("ready")
	e.fake.On([]string{"block", "ls"}, fmt.Sprintf(`{"blocks":[{"block_id":%q,"label":"pane one","window_id":"W1"}]}`, rexBlock), 0)
	e.setTab("pane one")
	e.start()
	e.hookPi(p, "startup")
	e.waitFor("the Mini App server", func() bool {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	})
	e.say(0, "/rt")
	var u string
	e.waitFor("the control button", func() bool { u = webAppURL(e, 0, "🎛 Open control panel"); return u != "" })
	id := pageID(u)
	base := "/api/control?id=" + id
	var state miniapp.ControlState
	if code := miniControl(t, e, addr, base, nil, &state); code != http.StatusOK || len(state.Agents) != 1 {
		t.Fatalf("state: %d %+v", code, state)
	}
	open := func(act string) miniapp.ControlResult {
		t.Helper()
		var result miniapp.ControlResult
		a := miniapp.ControlAction{Op: miniapp.OpOpen, Act: act, Pane: state.Agents[0].Pane}
		if code := miniControl(t, e, addr, "/api/control/do?id="+id, &a, &result); code != http.StatusOK {
			t.Fatalf("open: status %d", code)
		}
		return result
	}
	first := open("0000000000000001")
	if !first.OK || first.Link == "" {
		t.Fatalf("first open: %+v", first)
	}
	state = miniapp.ControlState{}
	miniControl(t, e, addr, base, nil, &state)
	if state.Agents[0].Topic == nil {
		t.Fatal("first open did not link a topic")
	}
	oldThread := state.Agents[0].Topic.Thread
	var telegramThread int64
	for _, topic := range e.tg.Topics(chatID) {
		if !topic.Deleted && !topic.Closed {
			telegramThread = topic.ThreadID
		}
	}
	if telegramThread == 0 {
		t.Fatal("first Telegram topic is missing")
	}
	e.tg.DeleteTopic(chatID, telegramThread)

	second := open("0000000000000002")
	if !second.OK || second.Link == "" || second.Link == first.Link {
		t.Fatalf("reconnect: first %+v, second %+v", first, second)
	}
	state = miniapp.ControlState{}
	miniControl(t, e, addr, base, nil, &state)
	if state.Agents[0].Topic == nil || state.Agents[0].Topic.Thread == oldThread || state.Agents[0].Topic.Link != second.Link {
		t.Fatalf("topic after reconnect: %+v", state.Agents[0].Topic)
	}
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
	// The pane has a tab in a session of the user's.
	e.fake.On([]string{"block", "ls"}, fmt.Sprintf(`{"blocks":[{"block_id":%q,"label":"pane one","window_id":"W1"}]}`, rexBlock), 0)
	e.setTab("pane one")
	// Folders: one pinned and two that earlier sessions ran in.
	pinned, fa, fb := filepath.Join(e.home, "pinned"), filepath.Join(e.home, "work", "a"), filepath.Join(e.home, "work", "b")
	for _, dir := range []string{pinned, fa, fb} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.cfg.Rex.NewFolders = []string{pinned}
	e.withStore(func(st *store.Store) {
		ctx := context.Background()
		for i, dir := range []string{fa, fb} {
			_, err := st.UpsertConversation(ctx, store.Conversation{Runtime: agents.Pi, NativeID: "earlier" + dir, Transcript: dir + "/earlier.jsonl", CWD: dir, FirstSeenAt: time.Now().Add(-time.Duration(i+2) * time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
		}
	})
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
	if ag.Block != rexBlock || ag.Tab != "pane one" || ag.Session != "main" {
		t.Errorf("block %q, tab %q, session %q", ag.Block, ag.Tab, ag.Session)
	}
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

	// Topics lists the active topic with its session, tab, and pane.
	st = miniapp.ControlState{}
	miniControl(t, e, addr, base, nil, &st)
	if len(st.Topics) != 1 || st.Topics[0].Status != "live" {
		t.Fatalf("topics: %+v", st.Topics)
	}
	live := st.Topics[0]
	if live.Thread != thread || live.Pane != ag.Pane || live.Session != "main" || live.Tab != "pane one" || live.PaneLabel != "pane one" || live.Agent != "pi" {
		t.Errorf("live topic %+v", live)
	}

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

	// Folders: the pinned one, then the recent ones. Forgetting one drops it
	// from the panel and from /new's buttons; the pinned one cannot be
	// forgotten; "forget all" leaves only the pinned one.
	st = miniapp.ControlState{}
	miniControl(t, e, addr, base, nil, &st)
	var paths []string
	for _, f := range st.Folders {
		paths = append(paths, f.Path)
	}
	if !slices.Equal(paths, []string{pinned, fa, fb}) || !st.Folders[0].Pinned || st.Folders[1].Pinned {
		t.Fatalf("folders %+v", st.Folders)
	}
	newButtons := func() (a, b bool) {
		e.say(0, "/new")
		q := e.waitText(0, "Which coding agent?")
		e.pressButton(q, buttonData(q, "pi"))
		e.waitFor("the folder question", func() bool {
			m, _ := e.message(q.ID)
			q = m
			return strings.Contains(m.Text, "Which folder?")
		})
		return buttonData(q, "~/work/a") != "", buttonData(q, "~/work/b") != ""
	}
	if a, b := newButtons(); !a || !b {
		t.Fatalf("/new buttons before: %v %v", a, b)
	}
	if r = do(miniapp.ControlAction{Op: miniapp.OpForgetFolder, Act: act(), Path: pinned}); r.OK || !strings.Contains(r.Message, "pinned") {
		t.Errorf("forget a pinned folder: %+v", r)
	}
	if r = do(miniapp.ControlAction{Op: miniapp.OpForgetFolder, Act: act(), Path: fa}); !r.OK {
		t.Fatalf("forget: %+v", r)
	}
	st = miniapp.ControlState{}
	miniControl(t, e, addr, base, nil, &st)
	if len(st.Folders) != 2 || st.Folders[1].Path != fb {
		t.Errorf("folders after forgetting one: %+v", st.Folders)
	}
	if a, b := newButtons(); a || !b {
		t.Errorf("/new buttons after forgetting a: %v %v", a, b)
	}
	if r = do(miniapp.ControlAction{Op: miniapp.OpForgetFolders, Act: act()}); !r.OK {
		t.Fatalf("forget all: %+v", r)
	}
	st = miniapp.ControlState{}
	miniControl(t, e, addr, base, nil, &st)
	if len(st.Folders) != 1 || !st.Folders[0].Pinned {
		t.Errorf("folders after forgetting all: %+v", st.Folders)
	}

	// No stopped topics remain after termination cleanup; CleanTopics leaves
	// the active topic alone and returns the same result for a repeated act.
	clean := miniapp.ControlAction{Op: miniapp.OpCleanTopics, Act: act()}
	r = do(clean)
	if again := do(clean); r.OK || r.Message != "No stopped topics." || again != r {
		t.Fatalf("clean: %+v, again %+v", r, again)
	}
	st = miniapp.ControlState{}
	miniControl(t, e, addr, base, nil, &st)
	if len(st.Topics) != 1 || st.Topics[0].Status != "live" {
		t.Errorf("topics after cleaning: %+v", st.Topics)
	}

	// A new agent starts in a folder inside home, once, also when the act
	// repeats. The result names the new pane's Rex block.
	newAct := miniapp.ControlAction{Op: miniapp.OpNew, Act: act(), Agent: "pi", Path: proj}
	if r = do(newAct); !r.OK || r.Pending || r.Block != "B2" {
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
