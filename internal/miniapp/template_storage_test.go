package miniapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

type memoryTemplateRepo struct {
	items   map[string]Template
	deleted map[string]bool
	saveErr error
}

func (m *memoryTemplateRepo) List(context.Context) ([]Template, error) {
	out := make([]Template, 0, len(m.items))
	for _, t := range m.items {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated > out[j].Updated })
	return out, nil
}
func (m *memoryTemplateRepo) Save(_ context.Context, t Template) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	if _, ok := m.items[t.ID]; !ok && len(m.items) >= maxTemplateCount {
		return ErrTemplateLimit
	}
	m.items[t.ID] = t
	return nil
}
func (m *memoryTemplateRepo) Import(_ context.Context, items []Template) (int, []string, error) {
	n := 0
	var deleted []string
	for _, t := range items {
		if m.deleted[t.ID] {
			deleted = append(deleted, t.ID)
			continue
		}
		if _, ok := m.items[t.ID]; ok || len(m.items) >= maxTemplateCount {
			continue
		}
		m.items[t.ID] = t
		n++
	}
	return n, deleted, nil
}
func (m *memoryTemplateRepo) Delete(_ context.Context, id string) error {
	delete(m.items, id)
	if m.deleted == nil {
		m.deleted = map[string]bool{}
	}
	m.deleted[id] = true
	return nil
}

func TestTemplateStorageAPIIsOwnerOnlyAndValidatesActions(t *testing.T) {
	pages := map[string]Page{
		"tpl":   {Kind: KindTemplates, Title: "Templates", Thread: 7, Conv: 5},
		"other": {Kind: KindAnswer, Title: "Answer"},
	}
	repo := &memoryTemplateRepo{items: map[string]Template{}}
	logins := &fakeLogins{allow: map[string]bool{"me@example.com": true}}
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) { p, ok := pages[id]; return p, ok }, nil).
		WithTemplates(repo).WithLaunch(&fakeLauncher{}, func(k string) bool { return k == testKey }).
		WithTailnetOnly(true).WithLogins(logins)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := caller{t, srv.URL}
	owner := map[string]string{"X-Rex-Key": testKey, "Tailscale-User-Login": "me@example.com"}
	if code, got := c.do("GET", "/api/templates?id=tpl", owner, ""); code != http.StatusOK || got["templates"] == nil {
		t.Fatalf("list: %d %v", code, got)
	}
	body := `{"op":"save","id":"0123456789abcdef0123456789abcdef","name":"First","text":"Message"}`
	post := map[string]string{"X-Rex-Key": testKey, "Tailscale-User-Login": "me@example.com", "Content-Type": "application/json"}
	if code, got := c.do("POST", "/api/templates?id=tpl", post, body); code != http.StatusOK || len(got["templates"].([]any)) != 1 {
		t.Fatalf("save: %d %v", code, got)
	}
	if code, _ := c.do("POST", "/api/templates?id=other", post, body); code != http.StatusNotFound {
		t.Errorf("wrong page kind: %d", code)
	}
	stranger := map[string]string{"X-Rex-Key": testKey, "Tailscale-User-Login": "other@example.com", "Content-Type": "application/json"}
	if code, _ := c.do("POST", "/api/templates?id=tpl", stranger, body); code != http.StatusForbidden {
		t.Errorf("stranger write: %d", code)
	}
	for _, invalid := range []string{
		`{"op":"save","id":"bad","name":"x","text":"x"}`,
		`{"op":"save","id":"1123456789abcdef0123456789abcdef","name":" ","text":"x"}`,
		`{"op":"save","id":"1123456789abcdef0123456789abcdef","name":"x","text":"x","thread":9}`,
	} {
		if code, _ := c.do("POST", "/api/templates?id=tpl", post, invalid); code != http.StatusBadRequest {
			t.Errorf("invalid action %s: %d", invalid, code)
		}
	}
	importBody := `{"op":"import","templates":[{"id":"0123456789abcdef0123456789abcdef","name":"Old local","text":"old copy","updated":1},{"id":"1123456789abcdef0123456789abcdef","name":"Second","text":"Message two","updated":2}]}`
	if code, got := c.do("POST", "/api/templates?id=tpl", post, importBody); code != http.StatusOK || len(got["templates"].([]any)) != 2 || repo.items["0123456789abcdef0123456789abcdef"].Text != "Message" {
		t.Fatalf("import: %d %v, repo %+v", code, got, repo.items)
	}
	if code, _ := c.do("POST", "/api/templates?id=tpl", post, `{"op":"delete","id":"0123456789abcdef0123456789abcdef"}`); code != http.StatusOK || len(repo.items) != 1 {
		t.Errorf("delete: %d, items %+v", code, repo.items)
	}
	staleCopy := `{"op":"import","templates":[{"id":"0123456789abcdef0123456789abcdef","name":"Old local","text":"stale copy","updated":1}]}`
	if code, got := c.do("POST", "/api/templates?id=tpl", post, staleCopy); code != http.StatusOK || len(got["deleted"].([]any)) != 1 || got["deleted"].([]any)[0] != "0123456789abcdef0123456789abcdef" {
		t.Errorf("tombstoned import: %d %v", code, got)
	}
	repo.saveErr = ErrTemplateDeleted
	if code, _ := c.do("POST", "/api/templates?id=tpl", post, `{"op":"save","id":"2123456789abcdef0123456789abcdef","name":"Stale","text":"stale copy"}`); code != http.StatusConflict {
		t.Errorf("stale deleted template save: %d", code)
	}
	repo.saveErr = nil
	oldSigned := map[string]string{"X-Telegram-Init-Data": sign(t, fakeToken, fields(42, time.Now().Add(-2*time.Hour))), "Content-Type": "application/json"}
	if code, _ := c.do("POST", "/api/templates?id=tpl", oldSigned, body); code != http.StatusForbidden {
		t.Errorf("stale signed write: %d", code)
	}
}
