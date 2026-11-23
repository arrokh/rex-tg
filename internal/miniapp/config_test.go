package miniapp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeConfigurator struct {
	thread, conv int64
}

func (f *fakeConfigurator) State(_ context.Context, thread, conv int64) (ConfigState, error) {
	f.thread, f.conv = thread, conv
	return ConfigState{Controls: []ConfigItem{{Name: "model", Title: "Model", Free: true}}}, nil
}

func TestConfigPageBindsReadAndWriteToItsSession(t *testing.T) {
	pages := map[string]Page{
		"cfg":     {Kind: KindConfig, Title: "Config", Thread: 71, Conv: 82},
		"project": {Kind: KindProject, Title: "Project", Thread: 73, Conv: 84, Dir: "/repo"},
		"answer":  {Kind: KindAnswer, Title: "Answer", Thread: 71, Conv: 82},
	}
	fc, ctl := &fakeConfigurator{}, &fakeControl{}
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) { p, ok := pages[id]; return p, ok }, nil).
		WithConfig(fc).WithControl(ctl, "")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	call := func(method, path, initData string, body []byte) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
		req.Header.Set("X-Telegram-Init-Data", initData)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var got map[string]any
		_ = json.NewDecoder(res.Body).Decode(&got)
		return res.StatusCode, got
	}
	owner := sign(t, fakeToken, fields(42, time.Now()))
	other := sign(t, fakeToken, fields(7, time.Now()))
	stale := sign(t, fakeToken, fields(42, time.Now().Add(-2*time.Hour)))
	if code, _ := call("GET", "/api/config?id=cfg", other, nil); code != http.StatusForbidden {
		t.Fatalf("another user read config: %d", code)
	}
	if code, got := call("GET", "/api/config?id=cfg", owner, nil); code != http.StatusOK || len(got["controls"].([]any)) != 1 || fc.thread != 71 || fc.conv != 82 {
		t.Fatalf("config state: %d %v, bound to %d/%d", code, got, fc.thread, fc.conv)
	}
	body := []byte(`{"act":"0123456789abcdef","name":"model","value":"provider/model"}`)
	if code, _ := call("POST", "/api/config/do?id=answer", owner, body); code != http.StatusNotFound {
		t.Fatalf("wrong page kind accepted config action: %d", code)
	}
	if len(ctl.did) != 0 {
		t.Fatal("wrong page action reached the daemon")
	}
	if code, _ := call("POST", "/api/config/do?id=cfg", stale, body); code != http.StatusForbidden || len(ctl.did) != 0 {
		t.Fatalf("stale signed action reached the daemon: %d", code)
	}
	if code, got := call("POST", "/api/config/do?id=cfg", other, body); code != http.StatusForbidden || len(ctl.did) != 0 {
		t.Fatalf("another user config action: %d %v", code, got)
	}
	if code, got := call("POST", "/api/config/do?id=cfg", owner, body); code != http.StatusOK || got["ok"] != true || len(ctl.did) != 1 {
		t.Fatalf("config action: %d %v, actions %v", code, got, ctl.did)
	}
	a := ctl.did[0]
	if a.Op != OpConfig || a.Thread != 71 || a.Conv != 82 || a.Name != "model" || a.Value != "provider/model" {
		t.Errorf("legacy action not bound to page: %+v", a)
	}
	batch := []byte(`{"act":"1123456789abcdef","changes":[{"name":"model","value":"provider/model"},{"name":"effort","value":"high"}]}`)
	if code, got := call("POST", "/api/config/do?id=cfg", owner, batch); code != http.StatusOK || got["ok"] != true || len(ctl.did) != 2 {
		t.Fatalf("combined config action: %d %v; actions %v", code, got, ctl.did)
	}
	changes := ctl.did[1].Changes
	if len(changes) != 2 || changes[0].Name != "model" || changes[1].Name != "effort" {
		t.Errorf("combined changes not bound to page: %+v", ctl.did[1])
	}
	if code, got := call("GET", "/api/config?id=project", owner, nil); code != http.StatusOK || fc.thread != 73 || fc.conv != 84 {
		t.Fatalf("project Config state: %d %v, bound to %d/%d", code, got, fc.thread, fc.conv)
	}
	if code, got := call("POST", "/api/config/do?id=project", owner, body); code != http.StatusOK || len(ctl.did) != 3 {
		t.Fatalf("project Config action: %d %v; actions %v", code, got, ctl.did)
	}
	if got := ctl.did[2]; got.Thread != 73 || got.Conv != 84 {
		t.Errorf("project Config action not bound to its session: %+v", got)
	}
}
