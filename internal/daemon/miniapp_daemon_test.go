package daemon_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/miniapp"
	"rex-tg/internal/telegram/tgtest"
)

func webAppURL(e *env, thread int64, text string) string {
	for _, m := range e.botMessages(thread) {
		if m.Markup == nil {
			continue
		}
		for _, row := range m.Markup.InlineKeyboard {
			for _, b := range row {
				if strings.Contains(b.Text, text) && b.WebApp != nil {
					return b.WebApp.URL
				}
			}
		}
	}
	return ""
}

// repoTopic is piTopicCfg for an agent whose folder is a real git
// repository: the pane's process, the transcript header, and the hook all
// name that folder instead of paneCWD.
func repoTopic(t *testing.T, id string, tweak func(*env)) (*env, *piLog, int64, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	repoGit(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repoGit(t, repo, "add", "-A")
	repoGit(t, repo, "commit", "-q", "-m", "start")

	e := newEnv(t)
	tweak(e)
	e.seedOwner()
	e.fake.On([]string{"block", "call", "-s", rexSess, "-b", rexBlock, "process"},
		fmt.Sprintf(`{"foreground":{"pid":%d,"name":"node","argv0":"pi","cwd":%q},"ancestors":[]}`, piPID, repo), 0)
	dir := filepath.Join(e.home, ".pi", "agent", "sessions", "--repo--")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := &piLog{t: t, ID: id, Path: filepath.Join(dir, "2026-01-01T00-00-00-000Z_"+id+".jsonl")}
	p.write(fmt.Sprintf(`{"type":"session","version":3,"id":%q,"timestamp":%q,"cwd":%q}`, id, now(), repo))
	p.User("start")
	p.Final("ready")
	e.start()
	e.spool("pi", "session_start", map[string]any{
		"hook_event_name": "session_start", "reason": "startup", "session_file": p.Path, "cwd": repo, "session_id": id,
	})
	thread := e.openTopic()
	e.waitText(thread, "ready")
	e.pi = p
	return e, p, thread, repo
}

func repoGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func buttonTexts(m tgtest.Message) [][]string {
	var out [][]string
	if m.Markup == nil {
		return nil
	}
	for _, row := range m.Markup.InlineKeyboard {
		var r []string
		for _, b := range row {
			r = append(r, b.Text)
		}
		out = append(out, r)
	}
	return out
}

// A turn that edits a file in the agent's git repository gets a button with
// the change count, which opens the diff in the Mini App.
func TestAnswerGetsTheChangesButton(t *testing.T) {
	e, p, thread, repo := repoTopic(t, "pi-changes", func(e *env) {
		e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
	})
	p.User("change a.txt")
	// The snapshot of the turn's start is taken in the background; it has no
	// outward sign, so give it a moment before the agent edits the file.
	time.Sleep(500 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.Final("Edited a.txt.")
	e.waitText(thread, "Edited a.txt.")
	// The button is on the answer when it is sent, or comes by a markup edit.
	e.waitFor("the changes button", func() bool { return webAppURL(e, thread, "📝 1 file · +1 −0") != "" })
	if u := webAppURL(e, thread, "📝 1 file · +1 −0"); !strings.HasPrefix(u, "https://mac.example.ts.net:8443/app?p=") {
		t.Errorf("changes button url = %q", u)
	}
}

// A turn that changes nothing gets no changes button.
func TestAnswerWithoutChangesHasNoChangesButton(t *testing.T) {
	e, p, thread, _ := repoTopic(t, "pi-nochanges", func(e *env) {
		e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
	})
	p.User("just look")
	time.Sleep(500 * time.Millisecond)
	p.Final("Nothing to change.")
	e.waitText(thread, "Nothing to change.")
	e.never("a changes button", 800*time.Millisecond, func() bool { return webAppURL(e, thread, "📝") != "" })
}

// Only the agent's answer and alerts make a sound. A notice, a command
// reply, and the waiting notice are silent.
func TestOnlyAnswersAndAlertsMakeASound(t *testing.T) {
	e, p, thread := piTopic(t, "pi-sound")

	// The notice of the link (sent when the topic was made), and a command reply.
	if intro := e.waitText(thread, "Linked"); !intro.Silent {
		t.Errorf("the link notice made a sound: %+v", intro)
	}
	e.say(thread, "/unmute")
	if reply := e.waitText(thread, "make sound again"); !reply.Silent {
		t.Errorf("a command reply made a sound: %+v", reply)
	}

	// The waiting notice, while the agent is busy.
	p.User("busy work")
	p.ToolCall("c1", "sleep 5")
	e.waitFor("the progress message", func() bool { return e.hasText(thread, "⏱") })
	e.say(thread, "also this")
	if waiting := e.waitText(thread, "Waiting until the agent is idle"); !waiting.Silent {
		t.Errorf("the waiting notice made a sound: %+v", waiting)
	}

	// The answer makes a sound.
	p.ToolResult("c1")
	p.Final("Work is done.")
	if answer := e.waitText(thread, "Work is done."); answer.Silent {
		t.Errorf("the answer was silent: %+v", answer)
	}
	e.waitFor("the queued text typed", func() bool { return len(e.pastes()) == 1 })

	// An alert makes a sound.
	e.setScreen(permScreen)
	e.setStatus("blocked", "permission")
	if alert := e.waitText(thread, "Permission needed"); alert.Silent {
		t.Errorf("the alert was silent: %+v", alert)
	}
}

// An answer with a whole-answer page and quick replies has both rows, the
// Mini App row first.
func TestMiniAppRowComesBeforeQuickReplies(t *testing.T) {
	e, p, thread := piTopicCfg(t, "pi-rows", func(e *env) {
		e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
		e.cfg.MiniApp.AnswerPreview = 500
		e.cfg.Display.QuickReplies = []string{"Continue", "Yes"}
	})
	long := "# Report\n\n" + strings.Repeat("A long paragraph of the report. ", 30) + "\n\n" + strings.Repeat("More text. ", 200) + "\n\nTHE END"
	p.User("write the report")
	p.Final(long)
	answer := e.waitText(thread, "A long paragraph")
	e.waitFor("both rows", func() bool {
		m, _ := e.message(answer.ID)
		answer = m
		return len(buttonTexts(m)) == 2
	})
	rows := buttonTexts(answer)
	if len(rows[0]) != 1 || rows[0][0] != "📄 Whole answer" {
		t.Errorf("first row = %v, want the Mini App row", rows[0])
	}
	if got := strings.Join(rows[1], "|"); got != "Continue|Yes" {
		t.Errorf("second row = %q, want the quick replies", got)
	}
	if answer.Markup.InlineKeyboard[0][0].WebApp == nil {
		t.Error("the first button is no web_app button")
	}
}

// An answer gets the quick replies; a tap sends that text to the agent, as
// if typed, once.
func TestQuickReplyTypesItsText(t *testing.T) {
	e, p, thread := piTopicCfg(t, "pi-quick", func(e *env) {
		e.cfg.Display.QuickReplies = []string{"Continue", "Yes", "Run tests", "Commit"} // off by default
	})
	p.User("check it")
	time.Sleep(10 * time.Millisecond) // the answer's time comes after the prompt's: the pane is idle then
	p.Final("All good. Shall I go on?")
	answer := e.waitText(thread, "Shall I go on?")
	data := buttonData(answer, "Continue")
	if data == "" {
		t.Fatalf("the answer has no quick replies: %+v", answer.Markup)
	}
	// Two to a row, so a phone shows the labels whole.
	if rows := buttonTexts(answer); len(rows) != 2 || strings.Join(rows[0], "|") != "Continue|Yes" || strings.Join(rows[1], "|") != "Run tests|Commit" {
		t.Errorf("rows = %v, want two of two", rows)
	}
	e.pressButton(answer, data)
	e.waitFor("the quick reply typed", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; got != "Continue" {
		t.Errorf("typed %q", got)
	}
}

// With the Mini App on, a long answer shows its start, and a button opens
// the whole answer there.
func TestLongAnswerOpensInTheMiniApp(t *testing.T) {
	e, p, thread := piTopicCfg(t, "pi-long-app", func(e *env) {
		e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
		e.cfg.MiniApp.AnswerPreview = 2000
	})
	long := "# Report\n\n" + strings.Repeat("A long paragraph of the report. ", 30) + "\n\n" + strings.Repeat("More text. ", 200) + "\n\nTHE END"
	p.User("write the report")
	p.Final(long)
	e.waitText(thread, "A long paragraph")
	u := webAppURL(e, thread, "Whole answer")
	if !strings.HasPrefix(u, "https://mac.example.ts.net:8443/app?p=") {
		t.Fatalf("no Mini App button: %q", u)
	}
	if e.hasText(thread, "THE END") {
		t.Error("the whole answer was posted in the chat")
	}
}

func TestSplitAnswerUsesWholePageWhenRichMessagesAreOff(t *testing.T) {
	e, p, thread := piTopicCfg(t, "pi-split-answer-app", func(e *env) {
		e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
		e.cfg.Display.RichMessages = false
		e.cfg.Display.MessageLength = 4000
	})
	para := "A long paragraph. " + strings.Repeat("😀", 600)
	long := "# Report\n\n" + strings.Repeat(para+"\n\n", 4) + "TAIL_MARKER"
	p.User("write a report that would split")
	p.Final(long)
	answer := e.waitText(thread, "A long paragraph")
	e.waitFor("the whole-answer button", func() bool {
		return webAppURL(e, thread, "Whole answer") != ""
	})
	if e.hasText(thread, "TAIL_MARKER") {
		t.Error("the split answer tail was posted in a second Telegram message")
	}
	count := 0
	for _, m := range e.botMessages(thread) {
		if strings.Contains(m.Text, "A long paragraph") {
			count++
		}
	}
	if count != 1 || len(buttonTexts(answer)) == 0 {
		t.Errorf("answer messages = %d; markup = %+v, want one preview and a Whole answer button", count, answer.Markup)
	}
}

func TestSingleParagraphStaysInOneMessageWhenRichMessagesAreOff(t *testing.T) {
	e, p, thread := piTopicCfg(t, "pi-single-paragraph-app", func(e *env) {
		e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
		e.cfg.Display.RichMessages = false
	})
	answerText := "A single paragraph. " + strings.Repeat("x", 1600)
	p.User("write one long paragraph")
	p.Final(answerText)
	answer := e.waitText(thread, "A single paragraph.")
	if url := webAppURL(e, thread, "Whole answer"); url != "" {
		t.Fatalf("a paragraph that fits one message opened a Whole answer page: %q", url)
	}
	count := 0
	for _, m := range e.botMessages(thread) {
		if strings.Contains(m.Text, "A single paragraph.") {
			count++
		}
	}
	if count != 1 || len(buttonTexts(answer)) != 0 {
		t.Errorf("answer messages = %d; markup = %+v, want one message without a page button", count, answer.Markup)
	}
}

// freeAddr is a local address with a free port, for a test that talks to
// the Mini App's server.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// signedInit is Mini App init data for a user, signed the way Telegram
// signs it with the bot's token.
func signedInit(token string, user int64) string {
	vals := map[string]string{"auth_date": fmt.Sprint(time.Now().Unix()), "user": fmt.Sprintf(`{"id":%d}`, user)}
	keys := []string{"auth_date", "user"}
	var lines []string
	for _, k := range keys {
		lines = append(lines, k+"="+vals[k])
	}
	m := hmac.New(sha256.New, miniapp.Secret(token))
	m.Write([]byte(strings.Join(lines, "\n")))
	q := url.Values{}
	for k, v := range vals {
		q.Set(k, v)
	}
	q.Set("hash", hex.EncodeToString(m.Sum(nil)))
	return q.Encode()
}

// miniGet asks the Mini App's server for an API path as a user.
func miniGet(t *testing.T, e *env, addr, path string, user int64) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://"+addr+path, nil)
	req.Header.Set("X-Telegram-Init-Data", signedInit(e.tg.Token(), user))
	req.Header.Set("Tailscale-User-Login", "owner@example.com") // as tailscale serve sets it ([miniapp] tailnet_only)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	return res.StatusCode, body
}

func pageID(u string) string {
	_, id, _ := strings.Cut(u, "/app?p=")
	return id
}

// TestConfigUsesTheLinkedSession opens Config for a linked topic, reads its
// settings, and queues one combined model/effort change through the input path.
func TestConfigUsesTheLinkedSession(t *testing.T) {
	addr := freeAddr(t)
	e, _, thread := piTopicCfg(t, "pi-config", func(e *env) {
		e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
		e.cfg.MiniApp.Listen = addr
		dir := filepath.Join(e.cfg.LiveDir(), "models")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "pi-config.json"), []byte(`["test/model-one","test/model-two","test/model-one","invalid model"]`), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	topic, ok, err := e.peek().ThreadByTopic(context.Background(), chatID, thread)
	if err != nil || !ok {
		t.Fatalf("read linked topic: %v, %v", ok, err)
	}
	code, launched := miniGet(t, e, addr, fmt.Sprintf("/api/launch?go=config&t=%d", topic.ID), ownerID)
	if code != http.StatusOK || launched["page"] == nil {
		t.Fatalf("launch Config: %d %v", code, launched)
	}
	id := fmt.Sprint(launched["page"])
	code, state := miniGet(t, e, addr, "/api/config?id="+id, ownerID)
	if code != http.StatusOK || len(state["controls"].([]any)) != 2 {
		t.Fatalf("Config state: %d %v", code, state)
	}
	model := state["controls"].([]any)[0].(map[string]any)
	if model["name"] != "model" || model["free"] != nil || fmt.Sprint(model["choices"]) != "[test/model-one test/model-two]" {
		t.Fatalf("model picker does not match the session registry: %v", model)
	}
	post := func(body string) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequest("POST", "http://"+addr+"/api/config/do?id="+id, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Telegram-Init-Data", signedInit(e.tg.Token(), ownerID))
		req.Header.Set("Tailscale-User-Login", "owner@example.com")
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var result map[string]any
		if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, result
	}
	if code, result := post(`{"act":"2123456789abcdef0123456789abcdef","name":"model","value":"test/not-available"}`); code != http.StatusOK || result["ok"] != false || !strings.Contains(fmt.Sprint(result["message"]), "not in this session") {
		t.Fatalf("unlisted model: %d %v", code, result)
	}
	if code, result := post(`{"act":"0123456789abcdef0123456789abcdef","changes":[{"name":"model","value":"test/model-two"},{"name":"effort","value":"high"}]}`); code != http.StatusOK || result["ok"] != true {
		t.Fatalf("combined model and effort change: %d %v", code, result)
	}
	e.waitFor("both Config commands typed", func() bool { return len(e.pastes()) == 2 })
	got := e.pastes()
	if got[0] != "/model test/model-two" || got[1] != "/thinking high" {
		t.Errorf("typed commands %q, want model then thinking", got)
	}

	th, ok, err := e.peek().ThreadByTopic(context.Background(), chatID, thread)
	if err != nil || !ok {
		t.Fatalf("read linked topic: %v, %v", ok, err)
	}
	oldConv := th.CurrentConversationID
	newSession := e.newPi("pi-config-new")
	e.hookPi(newSession, "clear")
	e.waitFor("Config topic session to change", func() bool {
		current, ok, err := e.peek().ThreadByTopic(context.Background(), chatID, thread)
		return err == nil && ok && current.CurrentConversationID != oldConv
	})
	if code, _ := miniGet(t, e, addr, "/api/config?id="+id, ownerID); code != http.StatusConflict {
		t.Errorf("stale Config page read: %d, want conflict", code)
	}
	if code, result := post(`{"act":"1123456789abcdef0123456789abcdef","name":"effort","value":"medium"}`); code != http.StatusOK || result["ok"] != false {
		t.Errorf("stale Config action: %d %v", code, result)
	}
	if got := len(e.pastes()); got != 2 {
		t.Errorf("stale action typed another command: %v", e.pastes())
	}
}

// /screen's picture has a button that opens the live screen. The live
// screen reads the pane again on each refresh, redacted, only for the owner.
func TestScreenOpensTheLiveScreen(t *testing.T) {
	addr := freeAddr(t)
	e, _, thread := piTopicCfg(t, "pi-live", func(e *env) {
		e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
		e.cfg.MiniApp.Listen = addr
	})
	e.setScreen("$ make test\nsecret sk-ant-" + "api03-abcdefghijklmnopqrstuvwxyz123456\n")
	e.say(thread, "/screen")
	var u string
	e.waitFor("the live screen button", func() bool { u = webAppURL(e, thread, "🖥 Live screen"); return u != "" })
	id := pageID(u)
	if code, _ := miniGet(t, e, addr, "/api/screen?id="+id, ownerID+1); code != http.StatusForbidden {
		t.Errorf("another user: %d", code)
	}
	code, body := miniGet(t, e, addr, "/api/screen?id="+id, ownerID)
	text, _ := body["text"].(string)
	if code != http.StatusOK || !strings.Contains(text, "$ make test") {
		t.Fatalf("screen: %d %v", code, body)
	}
	if strings.Contains(text, "abcdefghijklmnop") || strings.Contains(fmt.Sprint(body["vt"]), "abcdefghijklmnop") {
		t.Errorf("the live screen shows a secret: %q / %q", text, body["vt"])
	}
	if vt, _ := body["vt"].(string); !strings.Contains(vt, "$ make test\x1b[0m") {
		t.Errorf("vt = %q, want the lines with a reset each", vt)
	}
	time.Sleep(1100 * time.Millisecond) // reads of one topic are a second apart
	e.setScreen("$ make test\nok\n")
	if _, body := miniGet(t, e, addr, "/api/screen?id="+id, ownerID); !strings.Contains(fmt.Sprint(body["text"]), "ok") {
		t.Errorf("the screen was not read again: %v", body)
	}
	// A second /screen reuses the topic's page.
	e.say(thread, "/screen")
	e.waitFor("a second photo", func() bool {
		n := 0
		for _, m := range e.botMessages(thread) {
			if m.Photo {
				n++
			}
		}
		return n == 2
	})
	for _, m := range e.botMessages(thread) {
		if m.Photo && m.Markup != nil && pageID(m.Markup.InlineKeyboard[0][0].WebApp.URL) != id {
			t.Errorf("a new page for the same topic: %s", m.Markup.InlineKeyboard[0][0].WebApp.URL)
		}
	}
}

// /files answers with a button that opens the agent's repository in the
// Mini App's file browser.
func TestFilesOpensTheBrowser(t *testing.T) {
	addr := freeAddr(t)
	e, _, thread, repo := repoTopic(t, "pi-files", func(e *env) {
		e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
		e.cfg.MiniApp.Listen = addr
	})
	e.say(thread, "/files")
	var u string
	e.waitFor("the files button", func() bool { u = webAppURL(e, thread, "📂 Browse files"); return u != "" })
	code, body := miniGet(t, e, addr, "/api/files?id="+pageID(u)+"&path=a.txt", ownerID)
	if code != http.StatusOK || !strings.Contains(fmt.Sprint(body["html"]), "one") {
		t.Fatalf("a.txt: %d %v", code, body)
	}
	if _, body := miniGet(t, e, addr, "/api/files?id="+pageID(u), ownerID); body["dir"] != true {
		t.Errorf("the root of %s: %v", repo, body)
	}
}

// Without the Mini App, /files says how to turn it on.
func TestFilesWithoutTheMiniApp(t *testing.T) {
	e, _, thread := piTopic(t, "pi-files-off")
	e.say(thread, "/files")
	e.waitText(thread, "[miniapp] url")
}

// /changes says what changed in the agent's repository. With nothing
// changed it sends no button; after a turn that edits a file, its button
// opens a view with the changes not committed and the last turn's.
func TestChangesOpensTheView(t *testing.T) {
	addr := freeAddr(t)
	e, p, thread, repo := repoTopic(t, "pi-changes-cmd", func(e *env) {
		e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
		e.cfg.MiniApp.Listen = addr
	})
	e.say(thread, "/changes")
	e.waitText(thread, "Not committed: nothing")
	e.never("a button for no changes", 300*time.Millisecond, func() bool { return webAppURL(e, thread, "📝 View changes") != "" })

	p.User("change a.txt")
	time.Sleep(500 * time.Millisecond) // the snapshot of the turn's start
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.Final("Edited a.txt.")
	e.waitFor("the changes button", func() bool { return webAppURL(e, thread, "📝 1 file · +1 −0") != "" })

	e.say(thread, "/changes")
	m := e.waitText(thread, "Last turn (")
	if !strings.Contains(m.Text, "Not committed: 1 file · +1 −0") || !strings.Contains(m.Text, "Last turn (") {
		t.Errorf("reply: %q", m.Text)
	}
	var u string
	e.waitFor("the view button", func() bool { u = webAppURL(e, thread, "📝 View changes"); return u != "" })
	code, body := miniGet(t, e, addr, "/api/changes?id="+pageID(u), ownerID)
	if code != http.StatusOK || body["base"] != "head" || body["total"] != float64(1) || body["turn_at"] == "" {
		t.Fatalf("list: %d %v", code, body)
	}
	code, body = miniGet(t, e, addr, "/api/changes?id="+pageID(u)+"&base=turn&path=a.txt", ownerID)
	if code != http.StatusOK || !strings.Contains(fmt.Sprint(body["html"]), "two") {
		t.Errorf("the turn's a.txt: %d %v", code, body)
	}
}

// Without the Mini App, /changes says how to turn it on.
func TestChangesWithoutTheMiniApp(t *testing.T) {
	e, _, thread := piTopic(t, "pi-changes-off")
	e.say(thread, "/changes")
	e.waitText(thread, "[miniapp] url")
}
