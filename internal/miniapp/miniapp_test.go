package miniapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A made-up token, built from pieces so no scanner takes it for a real one.
var fakeToken = "123456" + ":" + "AAtest" + "-fake-token"

// sign makes init data the way Telegram does.
func sign(t *testing.T, token string, fields map[string]string) string {
	t.Helper()
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var lines []string
	for _, k := range keys {
		lines = append(lines, k+"="+fields[k])
	}
	m := hmac.New(sha256.New, Secret(token))
	m.Write([]byte(strings.Join(lines, "\n")))
	v := url.Values{}
	for k, val := range fields {
		v.Set(k, val)
	}
	v.Set("hash", hex.EncodeToString(m.Sum(nil)))
	return v.Encode()
}

func fields(user int64, at time.Time) map[string]string {
	return map[string]string{
		"auth_date": strconv.FormatInt(at.Unix(), 10),
		"query_id":  "AAH",
		"user":      `{"id":` + strconv.FormatInt(user, 10) + `,"first_name":"T"}`,
	}
}

func TestCheckInitData(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	good := sign(t, fakeToken, fields(42, now.Add(-time.Minute)))
	if u, err := CheckInitData(good, Secret(fakeToken), now, MaxAge); err != nil || u != 42 {
		t.Fatalf("good data: %d, %v", u, err)
	}
	if _, err := CheckInitData(good, Secret("123456:other"), now, MaxAge); err != ErrBadSignature {
		t.Errorf("another bot's key: %v", err)
	}
	tampered := strings.Replace(good, "42", "43", 1)
	if _, err := CheckInitData(tampered, Secret(fakeToken), now, MaxAge); err != ErrBadSignature {
		t.Errorf("a changed user: %v", err)
	}
	old := sign(t, fakeToken, fields(42, now.Add(-25*time.Hour)))
	if _, err := CheckInitData(old, Secret(fakeToken), now, MaxAge); err != ErrTooOld {
		t.Errorf("old data: %v", err)
	}
	future := sign(t, fakeToken, fields(42, now.Add(time.Hour)))
	if _, err := CheckInitData(future, Secret(fakeToken), now, MaxAge); err != ErrTooOld {
		t.Errorf("data from the future: %v", err)
	}
	if _, err := CheckInitData("", Secret(fakeToken), now, MaxAge); err != ErrNoSignature {
		t.Errorf("no data: %v", err)
	}
}

func TestPageOnlyForTheOwner(t *testing.T) {
	pages := map[string]Page{"p1": {Kind: KindAnswer, Title: "Answer", Text: "**bold** <script>x</script>\n\n| a | b |\n|---|---|\n| 1 | 2 |"}}
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) { p, ok := pages[id]; return p, ok }, nil)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	get := func(id, initData string) (int, map[string]string) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/page?id="+id, nil)
		req.Header.Set("X-Telegram-Init-Data", initData)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var body map[string]string
		_ = json.NewDecoder(res.Body).Decode(&body)
		return res.StatusCode, body
	}
	now := time.Now()
	if code, _ := get("p1", ""); code != http.StatusForbidden {
		t.Errorf("no init data: %d", code)
	}
	if code, _ := get("p1", sign(t, fakeToken, fields(7, now))); code != http.StatusForbidden {
		t.Errorf("another user: %d", code)
	}
	if code, _ := get("nope", sign(t, fakeToken, fields(42, now))); code != http.StatusNotFound {
		t.Errorf("an unknown page: %d", code)
	}
	code, body := get("p1", sign(t, fakeToken, fields(42, now)))
	if code != http.StatusOK || !strings.Contains(body["html"], "<strong>bold</strong>") || !strings.Contains(body["html"], "<table>") {
		t.Fatalf("owner: %d %+v", code, body)
	}
	if strings.Contains(body["html"], "<script>") {
		t.Errorf("raw HTML from the text reached the page: %s", body["html"])
	}
	// The shell holds no data and needs no signature.
	res, err := http.Get(srv.URL + "/app")
	if err != nil || res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("shell: %v %v", res, err)
	}
}

func TestRenderDiff(t *testing.T) {
	d := "diff --git a/x.go b/x.go\nindex 1..2 100644\n--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,2 @@\n ctx <a>\n-old\n+new\n"
	h := RenderDiff(d)
	for _, want := range []string{`<div class="name">x.go</div>`, `<div class="add">+new</div>`, `<div class="del">-old</div>`, `<div class="hunk">@@ -1,2 +1,2 @@</div>`, "ctx &lt;a&gt;"} {
		if !strings.Contains(h, want) {
			t.Errorf("diff lacks %q:\n%s", want, h)
		}
	}
}

// The live screen and the file browser answer only the owner, only for a
// page of their kind, and the browser only while it is on.
func TestLiveScreenAndFilesOnlyForTheOwner(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	pages := map[string]Page{
		"s1": {Kind: KindScreen, Title: "pi", Thread: 7},
		"f1": {Kind: KindFiles, Title: "📂 repo", Thread: 7, Dir: root},
		"a1": {Kind: KindAnswer, Title: "Answer", Text: "x"},
	}
	var asked []int64
	filesOn := true
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) { p, ok := pages[id]; return p, ok }, nil).
		WithScreen(func(p Page) Screen { asked = append(asked, p.Thread); return Screen{Text: "$ ls", At: "12:00:00"} }).
		WithFiles(func() bool { return filesOn }, nil)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	get := func(path, initData string) (int, map[string]any) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("X-Telegram-Init-Data", initData)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(res.Body).Decode(&body)
		return res.StatusCode, body
	}
	owner, other := sign(t, fakeToken, fields(42, time.Now())), sign(t, fakeToken, fields(7, time.Now()))

	for _, p := range []string{"/api/screen?id=s1", "/api/files?id=f1"} {
		if code, _ := get(p, other); code != http.StatusForbidden {
			t.Errorf("%s for another user: %d", p, code)
		}
	}
	if len(asked) != 0 {
		t.Fatal("the screen was read for a refused request")
	}
	if code, body := get("/api/screen?id=s1", owner); code != http.StatusOK || body["text"] != "$ ls" || len(asked) != 1 || asked[0] != 7 {
		t.Errorf("screen: %d %v %v", code, body, asked)
	}
	if code, body := get("/api/page?id=s1", owner); code != http.StatusOK || body["kind"] != KindScreen || body["html"] != "" {
		t.Errorf("screen page: %d %v", code, body)
	}
	// A page of another kind does not open as a screen or a folder.
	for _, p := range []string{"/api/screen?id=a1", "/api/screen?id=f1", "/api/files?id=a1", "/api/files?id=s1"} {
		if code, _ := get(p, owner); code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, code)
		}
	}
	code, body := get("/api/files?id=f1&path=", owner)
	if code != http.StatusOK || body["dir"] != true {
		t.Fatalf("files: %d %v", code, body)
	}
	if code, body := get("/api/files?id=f1&path=a.txt", owner); code != http.StatusOK || !strings.Contains(body["html"].(string), "hello") {
		t.Errorf("a.txt: %d %v", code, body)
	}
	if code, _ := get("/api/files?id=f1&path=../../etc/passwd", owner); code != http.StatusNotFound {
		t.Errorf("a path out of the folder: %d", code)
	}
	filesOn = false
	if code, _ := get("/api/files?id=f1&path=a.txt", owner); code != http.StatusNotFound {
		t.Errorf("the browser is off, yet: %d", code)
	}
}
