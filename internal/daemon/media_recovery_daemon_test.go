package daemon_test

import (
	"context"
	"testing"
	"time"

	"rex-tg/internal/store"
)

func TestInterruptedVoiceWorkIsReportedAfterRestart(t *testing.T) {
	e, _, topic := piTopic(t, "pi-interrupted-voice")
	ctx := context.Background()
	th, ok, err := e.peek().ThreadByTopic(ctx, chatID, topic)
	if err != nil || !ok {
		t.Fatalf("thread lookup: %v, %v", ok, err)
	}
	e.stop()
	st, err := store.Open(e.cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.InsertInbound(ctx, store.Inbound{
		UpdateID: 9801, ChatID: chatID, MessageThreadID: topic, TGMessageID: 9802,
		FromID: ownerID, ThreadID: th.ID, Kind: "voice", State: store.InboundDone,
		Body: `{"v":1,"processing":"voice"}`, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	e.start()
	e.waitText(topic, "Voice note processing was interrupted")
	if len(e.pastes()) != 0 {
		t.Fatal("interrupted voice input was typed automatically")
	}
	e.stop()
	e.start()
	e.waitFor("the next restart", func() bool { return len(e.tg.Calls("getMe")) >= 3 })
	if n := e.countText(topic, "Voice note processing was interrupted"); n != 1 {
		t.Fatalf("interruption notice sent %d times, want once", n)
	}
}
