package daemon

import (
	"testing"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/inbound"
	"rex-tg/internal/store"
)

func TestAcknowledgedPromptKeepsItsTelegramOriginOnReplay(t *testing.T) {
	f := newRenameFixture(t, false, false)
	text := "the delivered request"
	_, err := f.d.st.InsertInbound(f.d.ctx, store.Inbound{
		UpdateID: 91, ChatID: f.th.ChatID, FromID: 7, ThreadID: f.th.ID,
		Kind: "text", Body: text, BodyHash: inbound.Hash(text), State: store.InboundAcked,
		CreatedAt: time.Now().Add(-time.Hour), DeliveredAt: time.Now().Add(-time.Hour), AckedEventKey: "pi:synthetic:prompt",
	})
	if err != nil {
		t.Fatal(err)
	}
	event := agents.Event{Key: "pi:synthetic:prompt", Kind: agents.UserPrompt, Text: text}
	if !f.d.echo(f.th, event, time.Now()) {
		t.Fatal("replayed acknowledged prompt lost its Telegram origin")
	}
	event.Key = "pi:synthetic:another-prompt"
	if f.d.echo(f.th, event, time.Now()) {
		t.Fatal("another event reused an acknowledged input with the same text")
	}
}
