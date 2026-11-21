package daemon

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/registry"
	"rex-tg/internal/store"
)

func TestAttachableViewsFiltersAndOrdersAgents(t *testing.T) {
	views := []registry.AgentView{
		{Pane: store.Pane{ID: 1, ConversationID: 1, State: "idle"}, Runtime: agents.Pi, Conversation: &store.Conversation{ID: 1}},
		{Pane: store.Pane{ID: 2, ConversationID: 2, State: "blocked"}, Runtime: agents.Claude, Conversation: &store.Conversation{ID: 2}},
		{Pane: store.Pane{ID: 3, ConversationID: 3}, Runtime: agents.Codex, Conversation: &store.Conversation{ID: 3}, Thread: &store.Thread{Status: store.ThreadLive}},
		{Pane: store.Pane{ID: 4, ConversationID: 0}, Runtime: agents.Pi},
		{Pane: store.Pane{ID: 5, ConversationID: 5, GoneAt: time.Now()}, Runtime: agents.Pi, Conversation: &store.Conversation{ID: 5}},
	}
	got := attachableViews(views)
	if len(got) != 2 || got[0].Pane.ID != 2 || got[1].Pane.ID != 1 {
		t.Fatalf("attachable views = %+v", got)
	}
}

func TestBotCommandMenuHidesRequestedCommands(t *testing.T) {
	hidden := map[string]bool{
		"agents": true, "new": true, "topics": true, "pause": true, "unpause": true,
		"annotate": true, "templates": true, "screen": true, "files": true, "changes": true,
		"model": true, "effort": true, "mode": true, "verbose": true, "reasoning": true,
		"resend": true, "at": true, "interrupt": true, "key": true,
	}
	foundEsc := false
	for _, cmd := range BotCommands() {
		if hidden[cmd.Command] {
			t.Errorf("/%s is still in the Telegram command menu", cmd.Command)
		}
		foundEsc = foundEsc || cmd.Command == "esc"
	}
	if !foundEsc {
		t.Fatal("/esc is missing from the Telegram command menu")
	}
}

func TestRenderAttachablePaginates(t *testing.T) {
	views := make([]registry.AgentView, 20)
	for i := range views {
		id := int64(i + 1)
		views[i] = registry.AgentView{
			Pane:    store.Pane{ID: id, ConversationID: id, Label: fmt.Sprintf("pane-%02d", i)},
			Runtime: agents.Pi, Conversation: &store.Conversation{ID: id, CWD: "/work/project"},
		}
	}
	text, markup := renderAttachable(views, 18)
	if !strings.Contains(text, "20 available") || len(markup.InlineKeyboard) != 3 {
		t.Fatalf("last page = %q, markup=%+v", text, markup)
	}
	if b := markup.InlineKeyboard[2][0]; b.Text != "‹ Previous" || b.CallbackData != "ap:0" {
		t.Fatalf("previous-page button = %+v", b)
	}
}
