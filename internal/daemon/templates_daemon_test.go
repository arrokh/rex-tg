package daemon_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// A keyboard template uses the topic and session on its page; retrying the
// same action queues the text only once.
func TestKeyboardTemplateSendsDirectlyOnce(t *testing.T) {
	addr := freeAddr(t)
	e, _, thread := piTopicCfg(t, "pi-key-template", func(e *env) {
		e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
		e.cfg.MiniApp.Listen = addr
	})
	storeThread := storeThreadID(t, e)
	key, ok, err := e.peek().Get(t.Context(), "kb_key")
	if err != nil || !ok {
		t.Fatalf("keyboard key: %q %v", key, err)
	}
	var launch map[string]any
	e.waitFor("the Templates Mini App", func() bool {
		code, body := miniKeyCall(t, addr, http.MethodGet, fmt.Sprintf("/api/launch?go=templates&t=%d", storeThread), key, "")
		if code != http.StatusOK || body["page"] == nil {
			return false
		}
		launch = body
		return true
	})
	pageID := launch["page"].(string)
	code, page := miniKeyCall(t, addr, http.MethodGet, "/api/page?id="+pageID, key, "")
	if code != http.StatusOK || page["can_send_templates"] != true || page["thread"] != float64(storeThread) {
		t.Fatalf("templates page: %d %v", code, page)
	}
	body := `{"act":"0123456789abcdef0123456789abcdef","text":"Please inspect the latest change."}`
	send := func() {
		code, result := miniKeyCall(t, addr, http.MethodPost, "/api/templates/send?id="+pageID, key, body)
		if code != http.StatusOK || result["ok"] != true {
			t.Fatalf("send: %d %v", code, result)
		}
	}
	send()
	send()
	e.waitFor("the template message to reach the agent", func() bool { return len(e.pastes()) == 1 })
	if got := e.pastes()[0]; !strings.Contains(got, "Please inspect the latest change.") {
		t.Errorf("pasted %q", got)
	}
	e.waitText(thread, "Template sent to the agent")
	if n := len(e.pastes()); n != 1 {
		t.Errorf("pastes = %d, want one", n)
	}
}
