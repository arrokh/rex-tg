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
	echo, steered := f.d.echo(f.th, event, time.Now())
	if !echo || steered {
		t.Fatalf("replayed ordinary prompt = echo %v, steered %v; want echo only", echo, steered)
	}
	event.Key = "pi:synthetic:another-prompt"
	if echo, _ := f.d.echo(f.th, event, time.Now()); echo {
		t.Fatal("another event reused an acknowledged input with the same text")
	}
}

func TestAcknowledgedSteeredPromptKeepsItsStateOnReplay(t *testing.T) {
	f := newRenameFixture(t, false, false)
	text := "the steered request"
	_, err := f.d.st.InsertInbound(f.d.ctx, store.Inbound{
		UpdateID: 92, ChatID: f.th.ChatID, FromID: 7, ThreadID: f.th.ID,
		Kind: "text", Body: text, BodyHash: inbound.Hash(text), State: store.InboundAcked,
		Reason: store.InboundReasonSteered, CreatedAt: time.Now().Add(-time.Hour),
		DeliveredAt: time.Now().Add(-time.Hour), AckedEventKey: "pi:synthetic:steered-prompt",
	})
	if err != nil {
		t.Fatal(err)
	}
	event := agents.Event{Key: "pi:synthetic:steered-prompt", Kind: agents.UserPrompt, Text: text}
	if echo, steered := f.d.echo(f.th, event, time.Now()); !echo || !steered {
		t.Fatalf("replayed steered prompt = echo %v, steered %v; want both", echo, steered)
	}
}
