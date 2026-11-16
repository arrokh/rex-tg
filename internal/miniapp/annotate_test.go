package miniapp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeLauncher struct {
	view   string
	thread int64
}

func (f *fakeLauncher) Launch(_ context.Context, view string, thread int64) LaunchResult {
	f.view, f.thread = view, thread
	if thread == 0 && view != ViewPanel {
		return LaunchResult{Pick: []TopicPick{{Thread: 7, Name: "api", Live: true}}}
	}
	return LaunchResult{Page: "an1"}
}

type fakeAnnotations struct {
	before, conv int64
	drafts       map[int64]string
}

func (f *fakeAnnotations) Answers(_ context.Context, thread, conv, before int64, n int) ([]AnswerItem, bool, error) {
	f.before, f.conv = before, conv
	at := time.Date(2026, 11, 16, 10, 20, 0, 0, time.Local)
	return []AnswerItem{
		{ID: 12, At: at, Markdown: "All **done**."},
		{ID: 9, At: at.Add(-time.Hour), Markdown: "a <b>tag</b>\nnext line\n\nnew paragraph", Plain: true},
	}, true, nil
}

func (f *fakeAnnotations) SaveDraft(_ context.Context, thread, conv int64, text string) (string, error) {
	f.conv = conv
	if f.drafts == nil {
		f.drafts = map[int64]string{}
	}
	f.drafts[thread] = text
	return "d1", nil
}

func (f *fakeAnnotations) BotName() string { return "rex_bot" }

const testKey = "k3y-0123456789abcdef"

// newKeyServer serves an answer page, the control panel, and an Annotate
// page, with the keyboard's key on.
func newKeyServer(t *testing.T) (*Server, *fakeControl, *fakeLauncher, *fakeAnnotations) {
	pages := map[string]Page{
		"a1":  {Kind: KindAnswer, Title: "Answer", Text: "hello"},
		"c1":  {Kind: KindControl, Title: "rex-tg"},
		"an1": {Kind: KindAnnotate, Title: "✍️ api", Thread: 7, Conv: 5},
	}
	fc, fl, fa := &fakeControl{}, &fakeLauncher{}, &fakeAnnotations{}
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) { p, ok := pages[id]; return p, ok }, nil).
		WithControl(fc, t.TempDir()).
		WithLaunch(fl, func(k string) bool { return k == testKey }).
		WithAnnotate(fa)
	return s, fc, fl, fa
}

type caller struct {
	t   *testing.T
	url string
}

func (c caller) do(method, path string, hdr map[string]string, body string) (int, map[string]any) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.url+path, bytes.NewBufferString(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// The keyboard's key opens pages but acts on nothing; a wrong key opens
// nothing.
func TestKeyOnlyShows(t *testing.T) {
	s, fc, _, _ := newKeyServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := caller{t, srv.URL}
	key := map[string]string{"X-Rex-Key": testKey}

	if code, body := c.do("GET", "/api/page?id=a1", key, ""); code != http.StatusOK || !strings.Contains(body["html"].(string), "hello") {
		t.Fatalf("a page with the key: %d %v", code, body)
	}
	if code, body := c.do("GET", "/api/control?id=c1", key, ""); code != http.StatusOK || body["readonly"] != true {
		t.Fatalf("the panel with the key: %d %v", code, body)
	}
	action := `{"op":"open","act":"0123456789abcdef","pane":3}`
	code, body := c.do("POST", "/api/control/do?id=c1", map[string]string{"X-Rex-Key": testKey, "Content-Type": "application/json"}, action)
	if msg, _ := body["error"].(string); code != http.StatusForbidden || !strings.Contains(msg, "View only") || len(fc.did) != 0 {
		t.Fatalf("an action with the key: %d %v, ran %d", code, body, len(fc.did))
	}
	code, body = c.do("GET", "/api/page?id=a1", map[string]string{"X-Rex-Key": "wrong"}, "")
	if msg, _ := body["error"].(string); code != http.StatusForbidden || !strings.Contains(msg, "/help") {
		t.Fatalf("a wrong key: %d %v", code, body)
	}
	// Signed init data still acts, and is not read-only.
	owner := map[string]string{"X-Telegram-Init-Data": sign(t, fakeToken, fields(42, time.Now()))}
	if code, body := c.do("GET", "/api/control?id=c1", owner, ""); code != http.StatusOK || body["readonly"] != nil {
		t.Fatalf("the panel signed: %d %v", code, body)
	}
	// Without a pairing, the key opens nothing.
	s.owner = func() int64 { return 0 }
	if code, _ := c.do("GET", "/api/page?id=a1", key, ""); code != http.StatusForbidden {
		t.Fatalf("a key before pairing: %d", code)
	}
}

// With tailnet_only, only requests that came through tailscale serve from a
// tailnet user's device are answered, the page shell too.
func TestTailnetOnly(t *testing.T) {
	s, _, _, _ := newKeyServer(t)
	s.WithTailnetOnly(true)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := caller{t, srv.URL}
	for _, h := range []map[string]string{
		{},
		{"X-Rex-Key": testKey},
		{"X-Rex-Key": testKey, "Tailscale-User-Login": "me@example.com", "Tailscale-Funnel-Request": "?1"},
	} {
		if code, _ := c.do("GET", "/api/page?id=a1", h, ""); code != http.StatusForbidden {
			t.Errorf("%v: %d", h, code)
		}
	}
	if code, _ := c.do("GET", "/app", nil, ""); code != http.StatusForbidden {
		t.Errorf("the shell from outside: %d", code)
	}
	if code, _ := c.do("GET", "/api/page?id=a1", map[string]string{"X-Rex-Key": testKey, "Tailscale-User-Login": "me@example.com"}, ""); code != http.StatusOK {
		t.Errorf("from the tailnet: %d", code)
	}
}

// A keyboard button asks for its page; a button without a topic gets the
// topics to pick from.
func TestLaunch(t *testing.T) {
	s, _, fl, _ := newKeyServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := caller{t, srv.URL}
	key := map[string]string{"X-Rex-Key": testKey}
	if code, body := c.do("GET", "/api/launch?go=screen&t=7", key, ""); code != http.StatusOK || body["page"] != "an1" || fl.view != ViewScreen || fl.thread != 7 {
		t.Fatalf("screen of 7: %d %v %+v", code, body, fl)
	}
	if code, body := c.do("GET", "/api/launch?go=annotate", key, ""); code != http.StatusOK || len(body["pick"].([]any)) != 1 {
		t.Fatalf("no topic: %d %v", code, body)
	}
	for _, q := range []string{"go=wipe", "go=screen&t=x", "go=screen&t=-1"} {
		if code, _ := c.do("GET", "/api/launch?"+q, key, ""); code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, code)
		}
	}
	if code, _ := c.do("GET", "/api/launch?go=panel", nil, ""); code != http.StatusForbidden {
		t.Errorf("without key or init data: %d", code)
	}
}

// The Annotate page reads answers ten at a time, rendered, and keeps notes
// as a draft that sends nothing by itself.
func TestAnnotate(t *testing.T) {
	s, _, _, fa := newKeyServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := caller{t, srv.URL}
	key := map[string]string{"X-Rex-Key": testKey}
	code, body := c.do("GET", "/api/annotate?id=an1&before=20", key, "")
	if code != http.StatusOK || body["bot"] != "rex_bot" || body["more"] != true || fa.before != 20 || fa.conv != 5 {
		t.Fatalf("list: %d %v before=%d conv=%d", code, body, fa.before, fa.conv)
	}
	items := body["items"].([]any)
	first, second := items[0].(map[string]any), items[1].(map[string]any)
	if first["id"] != float64(12) || first["at"] != "Nov 16 10:20" || !strings.Contains(first["html"].(string), "<strong>done</strong>") {
		t.Errorf("markdown item: %v", first)
	}
	if h := second["html"].(string); !strings.Contains(h, "a &lt;b&gt;tag&lt;/b&gt;<br>next line</p>") || !strings.Contains(h, "<p>new paragraph</p>") {
		t.Errorf("plain item: %q", h)
	}
	if code, _ := c.do("GET", "/api/annotate?id=a1", key, ""); code != http.StatusNotFound {
		t.Errorf("another kind of page: %d", code)
	}

	jsonKey := map[string]string{"X-Rex-Key": testKey, "Content-Type": "application/json"}
	if code, body := c.do("POST", "/api/annotate/draft?id=an1", jsonKey, `{"text":"> quote\n\nnote"}`); code != http.StatusOK || body["draft"] != "d1" || fa.drafts[7] != "> quote\n\nnote" {
		t.Fatalf("draft: %d %v %v", code, body, fa.drafts)
	}
	for _, b := range []string{`{"text":"  "}`, `{"text":"x","more":1}`, `{"text":"` + strings.Repeat("x", maxDraftBytes+1) + `"}`} {
		if code, _ := c.do("POST", "/api/annotate/draft?id=an1", jsonKey, b); code != http.StatusBadRequest {
			t.Errorf("%.40s: %d", b, code)
		}
	}
	if code, _ := c.do("POST", "/api/annotate/draft?id=an1", key, `{"text":"x"}`); code != http.StatusUnsupportedMediaType {
		t.Errorf("not JSON: %d", code)
	}
}

// An answer or Annotate page with a topic sends its notes as the action
// OpNotes, signed in the last hour, with the page's own topic; the key and
// the control panel's own endpoint cannot.
func TestAnnotateSend(t *testing.T) {
	pages := map[string]Page{
		"w1": {Kind: KindAnswer, Title: "Report", Text: "THE END", Thread: 7, Conv: 5, At: time.Date(2026, 11, 16, 10, 20, 0, 0, time.Local)},
		"w7": {Kind: KindAnswer, Title: "v0.4.0", Text: "x", Thread: 7}, // made before pages knew their session
		"w0": {Kind: KindAnswer, Title: "Old", Text: "x"},
		"c1": {Kind: KindControl, Title: "rex-tg"},
	}
	fc := &fakeControl{}
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) { p, ok := pages[id]; return p, ok }, nil).
		WithControl(fc, t.TempDir()).
		WithLaunch(&fakeLauncher{}, func(k string) bool { return k == testKey }).
		WithAnnotate(&fakeAnnotations{})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := caller{t, srv.URL}
	signed := map[string]string{"X-Telegram-Init-Data": sign(t, fakeToken, fields(42, time.Now())), "Content-Type": "application/json"}
	body := `{"act":"0123456789abcdef0123456789abcdef","text":"> THE END\n\nmore"}`

	if code, p := c.do("GET", "/api/page?id=w1", signed, ""); code != http.StatusOK || p["thread"] != float64(7) || p["at"] != "Nov 16 10:20" || p["bot"] != "rex_bot" {
		t.Fatalf("page: %d %v", code, p)
	}
	if code, res := c.do("POST", "/api/annotate/send?id=w1", signed, body); code != http.StatusOK || res["ok"] != true || len(fc.did) != 1 {
		t.Fatalf("send: %d %v", code, res)
	}
	if a := fc.did[0]; a.Op != OpNotes || a.Thread != 7 || a.Conv != 5 || a.Text != "> THE END\n\nmore" {
		t.Errorf("action: %+v", a)
	}
	old := map[string]string{"X-Telegram-Init-Data": sign(t, fakeToken, fields(42, time.Now().Add(-2*time.Hour))), "Content-Type": "application/json"}
	for _, r := range []struct {
		path string
		hdr  map[string]string
		body string
		want int
	}{
		{"/api/annotate/send?id=w0", signed, body, http.StatusBadRequest},                                                                     // no topic
		{"/api/annotate/send?id=w7", signed, body, http.StatusBadRequest},                                                                     // no session
		{"/api/annotate/send?id=c1", signed, body, http.StatusBadRequest},                                                                     // not an answer
		{"/api/annotate/send?id=w1", old, body, http.StatusForbidden},                                                                         // signed too long ago
		{"/api/annotate/send?id=w1", map[string]string{"X-Rex-Key": testKey, "Content-Type": "application/json"}, body, http.StatusForbidden}, // the key
		{"/api/annotate/send?id=w1", signed, `{"act":"0123456789abcdef0123456789abcdef","text":"  "}`, http.StatusBadRequest},
		{"/api/annotate/send?id=w1", signed, `{"act":"nope","text":"x"}`, http.StatusBadRequest},
		{"/api/annotate/send?id=w1", signed, `{"act":"0123456789abcdef0123456789abcdef","text":"x","thread":9}`, http.StatusBadRequest},
		{"/api/control/do?id=c1", signed, `{"op":"notes","act":"0123456789abcdef0123456789abcdef","thread":7,"text":"x"}`, http.StatusBadRequest},
	} {
		if code, res := c.do("POST", r.path, r.hdr, r.body); code != r.want {
			t.Errorf("%s %.50s: %d %v, want %d", r.path, r.body, code, res, r.want)
		}
	}
	if len(fc.did) != 1 {
		t.Errorf("actions ran: %+v", fc.did)
	}
}

type fakeLogins struct {
	owners []string
	allow  map[string]bool
}

func (f *fakeLogins) Owner(login string)                { f.owners = append(f.owners, login) }
func (f *fakeLogins) KeyLoginOK(key, login string) bool { return key == testKey && f.allow[login] }

// With tailnet_only, the key works only from the owner's Tailscale logins; a
// signed request teaches the owner's login; a device outside the tailnet
// gets a page that says what to do, not raw JSON.
func TestKeyOnlyFromTheOwnersLogin(t *testing.T) {
	s, _, _, _ := newKeyServer(t)
	fl := &fakeLogins{allow: map[string]bool{"me@example.com": true}}
	s.WithTailnetOnly(true).WithLogins(fl)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := caller{t, srv.URL}
	if code, _ := c.do("GET", "/api/page?id=a1", map[string]string{"X-Rex-Key": testKey, "Tailscale-User-Login": "me@example.com"}, ""); code != http.StatusOK {
		t.Errorf("the owner's login: %d", code)
	}
	code, body := c.do("GET", "/api/page?id=a1", map[string]string{"X-Rex-Key": testKey, "Tailscale-User-Login": "coworker@example.com"}, "")
	if msg, _ := body["error"].(string); code != http.StatusForbidden || !strings.Contains(msg, "your own Tailscale account") {
		t.Errorf("another login: %d %v", code, body)
	}
	signed := map[string]string{"X-Telegram-Init-Data": sign(t, fakeToken, fields(42, time.Now())), "Tailscale-User-Login": "phone@example.com"}
	if code, _ := c.do("GET", "/api/page?id=a1", signed, ""); code != http.StatusOK || len(fl.owners) != 1 || fl.owners[0] != "phone@example.com" {
		t.Errorf("a signed request: %d, owners %v", code, fl.owners)
	}

	req, _ := http.NewRequest("GET", srv.URL+"/app?p=a1", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusForbidden || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") || !strings.Contains(string(b), "Turn on Tailscale") {
		t.Errorf("the page outside the tailnet: %d %q %.80q", res.StatusCode, res.Header.Get("Content-Type"), b)
	}
}
