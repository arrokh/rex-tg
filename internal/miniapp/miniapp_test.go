package miniapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
	// The terminal library comes from the same address, without a signature.
	for _, p := range []string{"/xterm-5.5.0.js", "/xterm-5.5.0.css"} {
		res, err := http.Get(srv.URL + p)
		if err != nil || res.StatusCode != http.StatusOK || res.ContentLength == 0 {
			t.Errorf("%s: %v %v", p, res, err)
		}
	}
}

func TestProjectTabsHideInactivePanesDespiteDisplayStyles(t *testing.T) {
	if !strings.Contains(string(appHTML), `.view-pane[hidden] { display: none !important; }`) {
		t.Fatal("project tab panes need an explicit hidden rule to override pane display styles")
	}
}

func TestKeyboardLaunchHasCloseInsteadOfPanelBack(t *testing.T) {
	js := string(appJS)
	for _, want := range []string{
		"else if (fromKb) {",
		"rootBack = closeMiniApp;",
		`el("button", "backlink", "Close")`,
		`setAttribute("aria-label", "Close the Mini App")`,
		"addEventListener(\"click\", closeMiniApp)",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks keyboard-launch close behavior %q", want)
		}
	}
}

func TestExplicitFileOfferCanPreviewGitIgnoredFile(t *testing.T) {
	root, _ := newRepo(t)
	write(t, filepath.Join(root, ".gitignore"), "node_modules/\nbuild.log\nresults/\n")
	rel := "results/copilot-pi-native-engine/2026-11-24/cpu-cap/summary.md"
	write(t, filepath.Join(root, filepath.FromSlash(rel)), "# CPU cap summary\n")
	write(t, filepath.Join(root, "results", "other.md"), "# Not offered\n")
	if filesKey(root, rel, true) == filesKey(root, rel, false) {
		t.Fatal("offered and ordinary reads share a cache key")
	}
	pages := map[string]Page{"offer": {Kind: KindFiles, Title: "Files", Dir: root, InitialFile: rel}}
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) { p, ok := pages[id]; return p, ok }, nil).
		WithFiles(func() bool { return true }, nil)
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	c := caller{t, server.URL}
	owner := map[string]string{"X-Telegram-Init-Data": sign(t, fakeToken, fields(42, time.Now()))}

	code, body := c.do("GET", "/api/files?id=offer&path="+url.QueryEscape(rel), owner, "")
	if code != http.StatusOK || !strings.Contains(body["html"].(string), "CPU cap summary") {
		t.Fatalf("explicit file offer preview: %d %v", code, body)
	}
	if code, _ := c.do("GET", "/api/files?id=offer&path="+url.QueryEscape("results/other.md"), owner, ""); code != http.StatusNotFound {
		t.Errorf("another ignored file was exposed: %d", code)
	}
	if code, listing := c.do("GET", "/api/files?id=offer", owner, ""); code != http.StatusOK {
		t.Fatalf("root listing: %d %v", code, listing)
	} else {
		for _, raw := range listing["entries"].([]any) {
			if raw.(map[string]any)["name"] == "results" {
				t.Error("normal listing exposed ignored results")
			}
		}
	}
}

func TestExternalHTMLRequiresOwnerTailnetIdentityAndStaysSandboxed(t *testing.T) {
	root := t.TempDir()
	source := `<!doctype html><html><body><h1>Local preview</h1><script>document.body.innerHTML='unsafe'</script><img src="https://example.invalid/pixel"></body></html>`
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("not HTML"), 0o644); err != nil {
		t.Fatal(err)
	}
	pages := map[string]Page{
		"files": {Kind: KindFiles, Title: "Files", Dir: root},
		"other": {Kind: KindScreen, Title: "Screen"},
	}
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) { p, ok := pages[id]; return p, ok }, nil).
		WithFiles(func() bool { return true }, nil).
		WithTailnetOnly(true).
		WithLogins(&fakeLogins{allow: map[string]bool{"me@example.com": true}})
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	c := caller{t, server.URL}
	owner := map[string]string{"Tailscale-User-Login": "me@example.com"}
	signedOwner := map[string]string{
		"Tailscale-User-Login": "me@example.com",
		"X-Telegram-Init-Data": sign(t, fakeToken, fields(42, time.Now())),
	}
	stranger := map[string]string{"Tailscale-User-Login": "other@example.com"}
	if code, page := c.do("GET", "/api/page?id=files", signedOwner, ""); code != http.StatusOK || page["can_open_external_html"] != true {
		t.Fatalf("external preview capability: %d %v", code, page)
	}

	code, body := c.doText("GET", "/external/file?id=files&path=index.html", owner)
	if code != http.StatusOK || !strings.Contains(body, "Local preview") || !strings.Contains(body, "document.body.innerHTML") {
		t.Fatalf("owner preview: %d %s", code, body)
	}
	req, err := http.NewRequest(http.MethodGet, server.URL+"/external/file?id=files&path=index.html", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tailscale-User-Login", "me@example.com")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if got := res.Header.Get("Content-Security-Policy"); !strings.Contains(got, "script-src 'none'") || !strings.Contains(got, "sandbox") || !strings.Contains(got, "connect-src 'none'") || !strings.Contains(got, "frame-ancestors 'none'") {
		t.Errorf("unsafe external preview CSP: %q", got)
	}
	if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Referrer-Policy") != "no-referrer" || res.Header.Get("X-Frame-Options") != "DENY" {
		t.Errorf("sensitive preview headers: cache=%q referrer=%q", res.Header.Get("Cache-Control"), res.Header.Get("Referrer-Policy"))
	}
	for _, tc := range []struct {
		name string
		path string
		head map[string]string
	}{
		{"stranger", "/external/file?id=files&path=index.html", stranger},
		{"wrong type", "/external/file?id=files&path=notes.txt", owner},
		{"wrong page kind", "/external/file?id=other&path=index.html", owner},
		{"hidden file", "/external/file?id=files&path=.env.html", owner},
	} {
		if code, _ := c.doText("GET", tc.path, tc.head); code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", tc.name, code)
		}
	}
}

func TestExternalVideoRequiresOwnerAndSupportsRangePreview(t *testing.T) {
	root := t.TempDir()
	video := []byte("synthetic-video-content")
	if err := os.WriteFile(filepath.Join(root, "clip.mp4"), video, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("not video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "clip.mp4"), filepath.Join(root, "clip-link.mp4")); err != nil {
		t.Fatal(err)
	}
	pages := map[string]Page{"files": {Kind: KindFiles, Title: "Files", Dir: root}}
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) { p, ok := pages[id]; return p, ok }, nil).
		WithFiles(func() bool { return true }, nil).
		WithTailnetOnly(true).
		WithLogins(&fakeLogins{allow: map[string]bool{"me@example.com": true}})
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	owner := map[string]string{"Tailscale-User-Login": "me@example.com"}
	stranger := map[string]string{"Tailscale-User-Login": "other@example.com"}
	c := caller{t, server.URL}
	if code, page := c.do("GET", "/api/page?id=files", map[string]string{
		"Tailscale-User-Login": "me@example.com",
		"X-Telegram-Init-Data": sign(t, fakeToken, fields(42, time.Now())),
	}, ""); code != http.StatusOK || page["can_open_external_video"] != true {
		t.Fatalf("external video capability: %d %v", code, page)
	}
	if code, body := c.doText("GET", "/external/file?id=files&path=clip.mp4", owner); code != http.StatusOK || body != string(video) {
		t.Fatalf("owner video: %d %q", code, body)
	}
	req, err := http.NewRequest(http.MethodGet, server.URL+"/external/file?id=files&path=clip.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tailscale-User-Login", "me@example.com")
	req.Header.Set("Range", "bytes=2-6")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusPartialContent || string(data) != string(video[2:7]) || res.Header.Get("Content-Type") != "video/mp4" || res.Header.Get("Accept-Ranges") != "bytes" || !strings.Contains(res.Header.Get("Content-Disposition"), "inline") {
		t.Fatalf("video range response: status=%d headers=%v body=%q", res.StatusCode, res.Header, data)
	}
	for _, tc := range []struct {
		path string
		head map[string]string
	}{
		{"/external/file?id=files&path=clip.mp4", stranger},
		{"/external/file?id=files&path=notes.txt", owner},
		{"/external/file?id=files&path=.env.mp4", owner},
		{"/external/file?id=files&path=clip-link.mp4", owner},
	} {
		if code, _ := c.doText("GET", tc.path, tc.head); code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", tc.path, code)
		}
	}
}

func TestExternalPDFRequiresOwnerAndSupportsRange(t *testing.T) {
	root, outside := newRepo(t)
	pdf := []byte("%PDF-1.4\nsynthetic PDF content\n")
	write(t, filepath.Join(root, "visible.pdf"), string(pdf))
	write(t, filepath.Join(root, ".gitignore"), "node_modules/\nbuild.log\nignored/\n")
	write(t, filepath.Join(root, "notes.txt"), "not a PDF")
	write(t, filepath.Join(root, ".env.pdf"), "hidden PDF")
	write(t, filepath.Join(root, "ignored", "offered.pdf"), "%PDF offered")
	write(t, filepath.Join(root, "ignored", "other.pdf"), "%PDF not offered")
	write(t, filepath.Join(outside, "secret.pdf"), "%PDF outside")
	if err := os.Symlink(filepath.Join(root, "visible.pdf"), filepath.Join(root, "link.pdf")); err != nil {
		t.Fatal(err)
	}
	pages := map[string]Page{
		"files":  {Kind: KindFiles, Title: "Files", Dir: root},
		"offer":  {Kind: KindFiles, Title: "Offer", Dir: root, InitialFile: "ignored/offered.pdf"},
		"answer": {Kind: KindAnswer, Title: "Answer", Dir: root},
		"screen": {Kind: KindScreen, Title: "Screen", Dir: root},
	}
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) { p, ok := pages[id]; return p, ok }, nil).
		WithFiles(func() bool { return true }, nil).
		WithTailnetOnly(true).
		WithLogins(&fakeLogins{allow: map[string]bool{"me@example.com": true}})
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	c := caller{t, server.URL}
	owner := map[string]string{"Tailscale-User-Login": "me@example.com"}
	stranger := map[string]string{"Tailscale-User-Login": "other@example.com"}
	signedOwner := map[string]string{
		"Tailscale-User-Login": "me@example.com",
		"X-Telegram-Init-Data": sign(t, fakeToken, fields(42, time.Now())),
	}
	if code, page := c.do("GET", "/api/page?id=files", signedOwner, ""); code != http.StatusOK || page["can_open_external_pdf"] != true {
		t.Fatalf("external PDF capability: %d %v", code, page)
	}

	check := func(target string, headers map[string]string) (int, http.Header, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL+target, nil)
		if err != nil {
			t.Fatal(err)
		}
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, res.Header, data
	}

	code, headers, body := check("/external/file?id=files&path=visible.pdf", owner)
	if code != http.StatusOK || string(body) != string(pdf) {
		t.Fatalf("owner PDF: status=%d body=%q", code, body)
	}
	if headers.Get("Content-Type") != "application/pdf" || !strings.HasPrefix(headers.Get("Content-Disposition"), "inline") || !strings.Contains(headers.Get("Content-Disposition"), "visible.pdf") ||
		headers.Get("Cache-Control") != "no-store" || headers.Get("X-Content-Type-Options") != "nosniff" ||
		headers.Get("Referrer-Policy") != "no-referrer" || headers.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("PDF response headers: %v", headers)
	}
	csp := headers.Get("Content-Security-Policy")
	if headers.Get("X-Frame-Options") != "DENY" || !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "sandbox") {
		t.Errorf("PDF response lacks restrictive browser headers: %v", headers)
	}
	code, headers, body = check("/external/file?id=files&path=visible.pdf", map[string]string{
		"Tailscale-User-Login": "me@example.com", "Range": "bytes=2-6",
	})
	if code != http.StatusPartialContent || string(body) != string(pdf[2:7]) || headers.Get("Content-Range") != fmt.Sprintf("bytes 2-6/%d", len(pdf)) {
		t.Fatalf("PDF range response: status=%d headers=%v body=%q", code, headers, body)
	}
	code, headers, _ = check("/external/file?id=files&path=visible.pdf", map[string]string{
		"Tailscale-User-Login": "me@example.com", "Range": fmt.Sprintf("bytes=%d-", len(pdf)+1),
	})
	if code != http.StatusRequestedRangeNotSatisfiable || headers.Get("Content-Range") != fmt.Sprintf("bytes */%d", len(pdf)) {
		t.Errorf("invalid PDF range: status=%d headers=%v", code, headers)
	}
	headReq, err := http.NewRequest(http.MethodHead, server.URL+"/external/file?id=files&path=visible.pdf", nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range owner {
		headReq.Header.Set(name, value)
	}
	headRes, err := http.DefaultClient.Do(headReq)
	if err != nil {
		t.Fatal(err)
	}
	headBody, err := io.ReadAll(headRes.Body)
	headRes.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if headRes.StatusCode != http.StatusOK || len(headBody) != 0 || headRes.Header.Get("Content-Type") != "application/pdf" || headRes.Header.Get("Content-Length") != fmt.Sprint(len(pdf)) {
		t.Errorf("PDF HEAD response: status=%d headers=%v body=%q", headRes.StatusCode, headRes.Header, headBody)
	}
	if code, body := c.doText("GET", "/external/file?id=offer&path=ignored/offered.pdf", owner); code != http.StatusOK || body != "%PDF offered" {
		t.Fatalf("page-bound ignored PDF offer: %d %q", code, body)
	}
	if code, body := c.doText("GET", "/external/file?id=answer&path=visible.pdf", owner); code != http.StatusOK || body != string(pdf) {
		t.Fatalf("linked answer PDF: %d %q", code, body)
	}

	for _, tc := range []struct {
		name    string
		target  string
		headers map[string]string
	}{
		{"missing Tailscale identity; URL key is not auth", "/external/file?id=files&path=visible.pdf&key=not-auth", nil},
		{"other Tailscale login", "/external/file?id=files&path=visible.pdf", stranger},
		{"wrong page kind", "/external/file?id=screen&path=visible.pdf", owner},
		{"missing page", "/external/file?id=missing&path=visible.pdf", owner},
		{"wrong file type", "/external/file?id=files&path=notes.txt", owner},
		{"missing file", "/external/file?id=files&path=missing.pdf", owner},
		{"hidden file", "/external/file?id=files&path=.env.pdf", owner},
		{"ignored file", "/external/file?id=files&path=ignored/offered.pdf", owner},
		{"different ignored file than offer", "/external/file?id=offer&path=ignored/other.pdf", owner},
		{"symlink file", "/external/file?id=files&path=link.pdf", owner},
		{"symlink directory", "/external/file?id=files&path=out/secret.pdf", owner},
		{"outside path", "/external/file?id=files&path=../../outside/secret.pdf", owner},
	} {
		if code, _, _ := check(tc.target, tc.headers); code != http.StatusNotFound && code != http.StatusForbidden {
			t.Errorf("%s: status %d, want refusal", tc.name, code)
		}
	}

	disabled := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) { p, ok := pages[id]; return p, ok }, nil).
		WithFiles(func() bool { return false }, nil).
		WithTailnetOnly(true).
		WithLogins(&fakeLogins{allow: map[string]bool{"me@example.com": true}})
	disabledServer := httptest.NewServer(disabled.Handler())
	defer disabledServer.Close()
	disabledReq, err := http.NewRequest(http.MethodGet, disabledServer.URL+"/external/file?id=files&path=visible.pdf", nil)
	if err != nil {
		t.Fatal(err)
	}
	disabledReq.Header.Set("Tailscale-User-Login", "me@example.com")
	disabledRes, err := http.DefaultClient.Do(disabledReq)
	if err != nil {
		t.Fatal(err)
	}
	disabledRes.Body.Close()
	if disabledRes.StatusCode != http.StatusNotFound {
		t.Errorf("PDF opened while file browser was disabled: %d", disabledRes.StatusCode)
	}
}

func TestAnswerAndAnnotatePagesCanOnlyOpenLinkedFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Project"), 0o644); err != nil {
		t.Fatal(err)
	}
	pages := map[string]Page{
		"answer":   {Kind: KindAnswer, Title: "Answer", Thread: 7, Conv: 8, Dir: root},
		"annotate": {Kind: KindAnnotate, Title: "Annotate", Thread: 7, Conv: 8, Dir: root},
		"no-root":  {Kind: KindAnswer, Title: "Answer", Thread: 7, Conv: 8},
	}
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) { p, ok := pages[id]; return p, ok }, nil).WithFiles(func() bool { return true }, nil)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	owner := map[string]string{"X-Telegram-Init-Data": sign(t, fakeToken, fields(42, time.Now()))}
	c := caller{t, srv.URL}
	for _, id := range []string{"answer", "annotate"} {
		if code, body := c.do("GET", "/api/files?id="+id+"&path=README.md", owner, ""); code != http.StatusOK || !strings.Contains(body["html"].(string), "Project") {
			t.Errorf("%s linked file: %d %v", id, code, body)
		}
		if code, _ := c.do("GET", "/api/files?id="+id, owner, ""); code != http.StatusNotFound {
			t.Errorf("%s page listed its project root: %d", id, code)
		}
	}
	if code, _ := c.do("GET", "/api/files?id=no-root&path=README.md", owner, ""); code != http.StatusNotFound {
		t.Errorf("page with no file root: %d", code)
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
		"p1": {Kind: KindProject, Title: "📁 repo", Thread: 7, Dir: root},
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
	if code, body := get("/api/files?id=p1&path=a.txt", owner); code != http.StatusOK || !strings.Contains(body["html"].(string), "hello") {
		t.Errorf("project file: %d %v", code, body)
	}
	if code, _ := get("/api/files?id=f1&path=../../etc/passwd", owner); code != http.StatusNotFound {
		t.Errorf("a path out of the folder: %d", code)
	}
	filesOn = false
	if code, _ := get("/api/files?id=f1&path=a.txt", owner); code != http.StatusNotFound {
		t.Errorf("the browser is off, yet: %d", code)
	}
}

// The changes view answers only the owner, only for a changes page, and
// only while it is on.
func TestChangesOnlyForTheOwner(t *testing.T) {
	root := changesRepo(t)
	write(t, filepath.Join(root, "main.go"), "package main\n\nfunc main() { println(2) }\n")
	pages := map[string]Page{
		"c1": {Kind: KindChanges, Title: "📝 repo", Thread: 7, Dir: root},
		"f1": {Kind: KindFiles, Title: "📂 repo", Thread: 7, Dir: root},
		"p1": {Kind: KindProject, Title: "📁 repo", Thread: 7, Dir: root},
	}
	on := true
	var turns []int64
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) { p, ok := pages[id]; return p, ok }, nil).
		WithChanges(func() bool { return on }, nil, func(p Page) TurnRange { turns = append(turns, p.Thread); return TurnRange{} })
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
	if code, _ := get("/api/changes?id=c1", other); code != http.StatusForbidden || len(turns) != 0 {
		t.Errorf("another user: %d", code)
	}
	code, body := get("/api/changes?id=c1", owner)
	if code != http.StatusOK || body["base"] != BaseHead || body["total"] != float64(1) || len(turns) != 1 || turns[0] != 7 {
		t.Fatalf("list: %d %v %v", code, body, turns)
	}
	if code, body := get("/api/changes?id=p1", owner); code != http.StatusOK || body["total"] != float64(1) {
		t.Errorf("project changes: %d %v", code, body)
	}
	if code, body := get("/api/changes?id=c1&base=head&path=main.go", owner); code != http.StatusOK || !strings.Contains(fmt.Sprint(body["html"]), "println(2)") {
		t.Errorf("main.go: %d %v", code, body)
	}
	for _, p := range []string{"/api/changes?id=f1", "/api/changes?id=c1&base=nope", "/api/changes?id=c1&base=turn"} {
		if code, _ := get(p, owner); code < 400 {
			t.Errorf("%s: %d", p, code)
		}
	}
	on = false
	if code, _ := get("/api/changes?id=c1", owner); code != http.StatusNotFound {
		t.Errorf("the view is off, yet: %d", code)
	}
}
