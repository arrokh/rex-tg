package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/store"
	"rex-tg/internal/telegram/tgtest"
)

func storeThreadID(t *testing.T, e *env) int64 {
	t.Helper()
	ths, err := e.peek().Threads(context.Background())
	if err != nil || len(ths) != 1 {
		t.Fatalf("threads: %v %v", ths, err)
	}
	return ths[0].ID
}

func miniKeyCall(t *testing.T, addr, method, path, key, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, "http://"+addr+path, bytes.NewBufferString(body))
	req.Header.Set("X-Rex-Key", key)
	req.Header.Set("Tailscale-User-Login", "owner@example.com")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, out
}

// With the Mini App on, every button opens its page with the key; /rekey
// makes a new key and gives every chat new buttons.
func TestReplyKeyboardWebApp(t *testing.T) {
	e := newEnv(t)
	const base = "https://mac.example.ts.net:8443"
	e.cfg.MiniApp.URL = base
	e.seedOwner()
	p := e.newPi("pi-webkeys")
	p.User("start")
	p.Final("ready")
	e.start()
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "ready")
	// The Mini App's address is new for this daemon: the start has the
	// markers of the same address, so no note came yet. Ask for the buttons.
	e.say(0, "/help")
	keyRe := regexp.MustCompile(`^` + regexp.QuoteMeta(base) + `/app\?k=([0-9a-f]{64})&go=(panel|screen|project|config|annotate|bookmarks|templates)(&t=\d+)?$`)
	check := func(m tgtest.Message, wantT bool) string {
		t.Helper()
		kb := m.Keyboard
		if kb == nil || len(kb.Keyboard) != 2 {
			t.Fatalf("%q: keyboard %+v", m.Text, kb)
		}
		key := ""
		for _, row := range kb.Keyboard {
			for _, b := range row {
				if b.Text == "📥 Queue" {
					if b.WebApp != nil {
						t.Fatalf("Queue must remain a text command: %+v", b)
					}
					continue
				}
				if b.WebApp == nil {
					t.Fatalf("%q: not a web_app button", b.Text)
				}
				g := keyRe.FindStringSubmatch(b.WebApp.URL)
				if g == nil || (g[3] != "") != wantT {
					t.Fatalf("%q: url %q (topic %v)", b.Text, b.WebApp.URL, wantT)
				}
				key = g[1]
			}
		}
		return key
	}
	key1 := check(e.waitText(0, "Main chat"), false)
	e.say(thread, "/help")
	if k := check(e.waitText(thread, "In a topic"), true); k != key1 {
		t.Error("the topic's key is not the main chat's")
	}
	// /start in a topic keeps the topic's own buttons.
	e.say(thread, "/start")
	check(e.waitText(thread, "Already paired"), true)
	if len(e.pastes()) != 0 {
		t.Errorf("a button was typed into the pane: %v", e.pastes())
	}

	// /rekey: a new key, and every chat gets the new buttons.
	e.say(thread, "/rekey")
	e.waitText(thread, "New key for the buttons")
	e.waitFor("the new notes", func() bool {
		return e.countText(0, "Buttons under the message box") == 1 && e.countText(thread, "Buttons under the message box") == 1
	})
	var topicNote, mainNote tgtest.Message
	for _, m := range e.botMessages(thread) {
		if strings.Contains(m.Text, "Buttons under the message box") {
			topicNote = m
		}
	}
	for _, m := range e.botMessages(0) {
		if strings.Contains(m.Text, "Buttons under the message box") {
			mainNote = m
		}
	}
	if k := check(topicNote, true); k == key1 {
		t.Error("the key did not change")
	}
	check(mainNote, false)

	// The next start has the new key and posts no note again.
	e.stop()
	e.start()
	e.waitFor("the second start", func() bool { return len(e.tg.Calls("getMe")) >= 2 })
	e.say(0, "/status")
	e.waitText(0, "Topics: 1 live")
	if n := e.countText(0, "Buttons under the message box"); n != 1 {
		t.Errorf("keyboard notes in the main chat = %d, want 1", n)
	}
}

// "Send to agent": the owner's web_app_data with a saved draft types the
// notes once; the repeat, and a new update for the same draft, do nothing.
func TestAnnotateSendsNotesOnce(t *testing.T) {
	e, _, thread := piTopic(t, "pi-annotate")
	id := storeThreadID(t, e)
	th, err := e.peek().Thread(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	e.stop()
	draft := func(name string, at time.Time) {
		v, _ := json.Marshal(map[string]any{"thread": id, "conv": th.CurrentConversationID, "text": "Notes on your answer:\n\n> ready\n\nlooks <good>", "at": at.Unix()})
		e.withStore(func(st *store.Store) {
			if err := st.Set(context.Background(), "annot_draft:"+name, string(v)); err != nil {
				t.Fatal(err)
			}
		})
	}
	const d1, old, nope = "0123456789abcdef01234567", "abcdefabcdefabcdefabcdef", "ffffffffffffffffffffffff"
	draft(d1, time.Now())
	draft(old, time.Now().Add(-2*time.Hour))
	e.start()
	data := func(d string) string { return fmt.Sprintf(`{"v":1,"a":"annotate","d":%q}`, d) }

	// Not from the owner: nothing happens.
	e.tg.PushWebAppData(chatID, thread, 555, data(d1))
	e.never("a note for a stranger", 500*time.Millisecond, func() bool { return e.countText(thread, "Notes sent") > 0 })

	// Telegram may return a keyboard Mini App's sendData in the main chat,
	// without a topic ID. The saved draft must still route notes to its topic.
	u := e.tg.PushWebAppData(chatID, 0, ownerID, data(d1))
	note := e.waitText(thread, "Notes sent to the agent")
	if !note.Silent || !strings.Contains(note.Text, "<blockquote expandable>") || !strings.Contains(note.Text, "looks &lt;good&gt;") {
		t.Errorf("note: %+v", note)
	}
	e.waitFor("the notes typed", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; !strings.Contains(got, "looks <good>") || strings.Contains(got, "web_app") {
		t.Errorf("pasted %q", got)
	}

	// The same update again, and a new update for the used draft: its row
	// is there, so it was sent; nothing more happens.
	e.tg.PushUpdate(u)
	e.tg.PushWebAppData(chatID, thread, ownerID, data(d1))
	// A draft that is too old, and one that never existed.
	e.tg.PushWebAppData(chatID, thread, ownerID, data(old))
	e.tg.PushWebAppData(chatID, 0, ownerID, data(nope))
	e.waitFor("the refusals", func() bool {
		return e.countText(thread, "These notes were not sent") == 1 && e.countText(0, "These notes were not sent") == 1
	})
	e.say(thread, "/info") // the daemon handled everything before
	e.waitText(thread, "Topic: live")
	if n := e.countText(thread, "Notes sent to the agent"); n != 1 {
		t.Errorf("sent notes = %d, want 1", n)
	}
	if n := len(e.pastes()); n != 1 {
		t.Errorf("pastes = %d, want 1: %v", n, e.pastes())
	}
}

func TestKeyboardAnnotateSendsDirectlyAndKeepsNotesOnce(t *testing.T) {
	addr := freeAddr(t)
	e, _, thread := piTopicCfg(t, "pi-key-annotate", func(e *env) {
		e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
		e.cfg.MiniApp.Listen = addr
	})
	storeThread := storeThreadID(t, e)
	key, ok, err := e.peek().Get(context.Background(), "kb_key")
	if err != nil || !ok {
		t.Fatalf("keyboard key: %q %v", key, err)
	}

	var launch map[string]any
	e.waitFor("the Mini App", func() bool {
		code, body := miniKeyCall(t, addr, http.MethodGet, fmt.Sprintf("/api/launch?go=annotate&t=%d", storeThread), key, "")
		if code != http.StatusOK || body["page"] == nil {
			return false
		}
		launch = body
		return true
	})
	pageID := launch["page"].(string)
	code, page := miniKeyCall(t, addr, http.MethodGet, "/api/page?id="+pageID, key, "")
	if code != http.StatusOK || page["can_send_notes"] != true || page["thread"] != float64(storeThread) {
		t.Fatalf("keyboard Annotate page: %d %v", code, page)
	}
	body := `{"act":"0123456789abcdef0123456789abcdef","text":"Notes on your answer:\n\n> ready\n\nfix this"}`
	send := func() {
		code, result := miniKeyCall(t, addr, http.MethodPost, "/api/annotate/send?id="+pageID, key, body)
		if code != http.StatusOK || result["ok"] != true {
			t.Fatalf("direct send: %d %v", code, result)
		}
	}
	send()
	send() // same act: the note must not be queued twice
	e.waitText(thread, "Notes sent to the agent")
	e.waitFor("the note to reach the agent", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; !strings.Contains(got, "Notes on your answer:\n\n> ready\n\nfix this") {
		t.Errorf("pasted %q", got)
	}
}

// Notes on a whole answer go to the agent from the answer's page, signed:
// the page knows its topic, and the same act sends once.
func TestWholeAnswerNotesGoToTheAgent(t *testing.T) {
	addr := freeAddr(t)
	e, p, thread := piTopicCfg(t, "pi-whole-notes", func(e *env) {
		e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
		e.cfg.MiniApp.Listen = addr
		e.cfg.MiniApp.AnswerPreview = 2000
	})
	long := "# Report\n\n" + strings.Repeat("A long paragraph of the report. ", 30) + "\n\n" + strings.Repeat("More text. ", 200) + "\n\nTHE END"
	p.User("write the report")
	time.Sleep(10 * time.Millisecond) // the answer comes after the prompt: the pane is idle then
	p.Final(long)
	e.waitText(thread, "A long paragraph")
	id := pageID(webAppURL(e, thread, "Whole answer"))
	e.waitFor("the Mini App", func() bool { code, _ := miniGet(t, e, addr, "/api/page?id="+id, ownerID); return code == http.StatusOK })
	if _, page := miniGet(t, e, addr, "/api/page?id="+id, ownerID); page["thread"] == float64(0) || page["at"] == "" {
		t.Fatalf("the page does not know its topic: %v", page)
	}
	send := func() map[string]any {
		b := `{"act":"00112233445566778899aabbccddeeff","text":"Notes on your answer:\n\n> THE END\n\nadd a summary"}`
		req, _ := http.NewRequest("POST", "http://"+addr+"/api/annotate/send?id="+id, bytes.NewBufferString(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Telegram-Init-Data", signedInit(e.tg.Token(), ownerID))
		req.Header.Set("Tailscale-User-Login", "owner@example.com")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return out
	}
	if r := send(); r["ok"] != true {
		t.Fatalf("send: %v", r)
	}
	if r := send(); r["ok"] != true { // the same act: its first result, nothing more
		t.Fatalf("send again: %v", r)
	}
	e.waitFor("the notes typed", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; !strings.Contains(got, "add a summary") {
		t.Errorf("typed %q", got)
	}
	e.waitText(thread, "Notes sent to the agent")
	e.say(thread, "/info") // the daemon handled everything before
	e.waitText(thread, "Topic: live")
	if n, m := len(e.pastes()), e.countText(thread, "Notes sent to the agent"); n != 1 || m != 1 {
		t.Errorf("pastes %d, notes %d; want 1 and 1", n, m)
	}
}
