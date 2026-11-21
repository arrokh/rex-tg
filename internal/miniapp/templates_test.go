package miniapp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTemplateSendIsBoundToPageAndOwner(t *testing.T) {
	pages := map[string]Page{
		"tpl":   {Kind: KindTemplates, Title: "Templates", Thread: 7, Conv: 5},
		"old":   {Kind: KindTemplates, Title: "Templates", Thread: 7},
		"other": {Kind: KindAnswer, Title: "Answer", Thread: 7, Conv: 5},
		"panel": {Kind: KindControl, Title: "Panel"},
	}
	fc := &fakeControl{}
	fl := &fakeLogins{allow: map[string]bool{"me@example.com": true}}
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) { p, ok := pages[id]; return p, ok }, nil).
		WithControl(fc, t.TempDir()).
		WithLaunch(&fakeLauncher{}, func(k string) bool { return k == testKey }).
		WithTailnetOnly(true).WithLogins(fl)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := caller{t, srv.URL}
	key := map[string]string{"X-Rex-Key": testKey, "Tailscale-User-Login": "me@example.com"}
	if code, page := c.do("GET", "/api/page?id=tpl", key, ""); code != http.StatusOK || page["can_send_templates"] != true {
		t.Fatalf("templates page: %d %v", code, page)
	}
	body := `{"act":"0123456789abcdef0123456789abcdef","text":"Please review this."}`
	post := map[string]string{"X-Rex-Key": testKey, "Tailscale-User-Login": "me@example.com", "Content-Type": "application/json"}
	if code, result := c.do("POST", "/api/templates/send?id=tpl", post, body); code != http.StatusOK || result["ok"] != true {
		t.Fatalf("send: %d %v", code, result)
	}
	if len(fc.did) != 1 {
		t.Fatalf("actions: %+v", fc.did)
	}
	a := fc.did[0]
	if a.Op != OpTemplateMessage || a.Thread != 7 || a.Conv != 5 || a.Text != "Please review this." {
		t.Errorf("action not bound to page: %+v", a)
	}
	for _, tc := range []struct {
		id, body string
		code     int
	}{
		{"old", body, http.StatusBadRequest},
		{"other", body, http.StatusNotFound},
		{"tpl", `{"act":"0123456789abcdef0123456789abcdef","text":"x","thread":8}`, http.StatusBadRequest},
		{"tpl", `{"act":"0123456789abcdef0123456789abcdef","text":"x"} {}`, http.StatusBadRequest},
	} {
		if code, _ := c.do("POST", "/api/templates/send?id="+tc.id, post, tc.body); code != tc.code {
			t.Errorf("%s %.40s: %d, want %d", tc.id, tc.body, code, tc.code)
		}
	}
	stranger := map[string]string{"X-Rex-Key": testKey, "Tailscale-User-Login": "other@example.com", "Content-Type": "application/json"}
	if code, _ := c.do("POST", "/api/templates/send?id=tpl", stranger, body); code != http.StatusForbidden {
		t.Errorf("another Tailscale login: %d", code)
	}
	oldSigned := map[string]string{"X-Telegram-Init-Data": sign(t, fakeToken, fields(42, time.Now().Add(-2*time.Hour))), "Content-Type": "application/json"}
	if code, _ := c.do("POST", "/api/templates/send?id=tpl", oldSigned, body); code != http.StatusForbidden {
		t.Errorf("stale signed data: %d", code)
	}
	if code, _ := c.do("POST", "/api/control/do?id=panel", post, `{"op":"template_message","act":"1123456789abcdef0123456789abcdef","thread":7,"conv":5,"text":"x"}`); code != http.StatusBadRequest {
		t.Errorf("generic control endpoint accepted a template send: %d", code)
	}
	if len(fc.did) != 1 {
		t.Errorf("rejected requests ran actions: %+v", fc.did)
	}
}

func TestTemplateSendRequiresJSONAndValidText(t *testing.T) {
	pages := map[string]Page{"tpl": {Kind: KindTemplates, Title: "Templates", Thread: 7, Conv: 5}}
	fc := &fakeControl{}
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) { p, ok := pages[id]; return p, ok }, nil).
		WithControl(fc, t.TempDir())
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := caller{t, srv.URL}
	signed := map[string]string{"X-Telegram-Init-Data": sign(t, fakeToken, fields(42, time.Now())), "Content-Type": "application/json"}
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"act":"0123456789abcdef0123456789abcdef","text":"  "}`, http.StatusBadRequest},
		{`{"act":"bad","text":"x"}`, http.StatusBadRequest},
		{`{"act":"0123456789abcdef0123456789abcdef","text":"x","conv":99}`, http.StatusBadRequest},
		{`{"act":"0123456789abcdef0123456789abcdef","text":"` + strings.Repeat("x", maxDraftBytes+1) + `"}`, http.StatusBadRequest},
		{`{"act":"0123456789abcdef0123456789abcdef","text":"x"}` + strings.Repeat(" ", 4*maxDraftBytes) + `x`, http.StatusBadRequest},
	} {
		if code, _ := c.do("POST", "/api/templates/send?id=tpl", signed, tc.body); code != tc.code {
			t.Errorf("%.45s: %d, want %d", tc.body, code, tc.code)
		}
	}
	if code, _ := c.do("POST", "/api/templates/send?id=tpl", map[string]string{"X-Telegram-Init-Data": sign(t, fakeToken, fields(42, time.Now()))}, `{"act":"0123456789abcdef0123456789abcdef","text":"x"}`); code != http.StatusUnsupportedMediaType {
		t.Errorf("non-JSON body: %d", code)
	}
	if len(fc.did) != 0 {
		t.Errorf("invalid sends ran actions: %+v", fc.did)
	}
}
