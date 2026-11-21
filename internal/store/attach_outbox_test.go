package store

import (
	"errors"
	"testing"
	"time"
)

func TestAttachToTopicDiscardsOldPendingOutput(t *testing.T) {
	s := newStore(t)
	oldThread, _ := topic(t, s, 31)
	rowID, inserted, err := s.Enqueue(ctx, OutboxItem{
		ThreadID: oldThread.ID, Key: "old-answer", Kind: "answer",
		Parts: []PartContent{{Content: "old session output", Format: FormatPlain}},
	})
	noErr(t, err)
	if !inserted {
		t.Fatal("outbox row not inserted")
	}
	noErr(t, s.SetThreadStatus(ctx, oldThread.ID, ThreadClosed))
	newConv := conv(t, s, "attach-new")
	pane := must(s.UpsertPane(ctx, "local", "block:attach-new", "session:attach-new", "pane", t0))
	noErr(t, s.SetPaneOccupant(ctx, pane.ID, newConv.ID, 321, "node", SourceHook))
	if _, err := s.AttachToTopic(ctx, pane.ID, oldThread.ChatID, oldThread.MessageThreadID, newConv.ID, "ignored", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	row := must(s.OutboxRow(ctx, rowID))
	if row.State != OutboxFailed || len(row.Parts) != 1 || row.Parts[0].State != OutboxFailed {
		t.Fatalf("old output was not discarded: %+v", row)
	}
	parts := must(s.NextParts(ctx))
	if len(parts) != 0 {
		t.Fatalf("old output became sendable: %+v", parts)
	}
}

func TestAttachToTopicWaitsForInFlightOutput(t *testing.T) {
	s := newStore(t)
	oldThread, _ := topic(t, s, 32)
	rowID, _, err := s.Enqueue(ctx, OutboxItem{
		ThreadID: oldThread.ID, Key: "in-flight", Kind: "answer",
		Parts: []PartContent{{Content: "possibly sending", Format: FormatPlain}},
	})
	noErr(t, err)
	if ok, err := s.ClaimPart(ctx, rowID, 0, -1); err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	noErr(t, s.SetThreadStatus(ctx, oldThread.ID, ThreadClosed))
	newConv := conv(t, s, "attach-busy")
	pane := must(s.UpsertPane(ctx, "local", "block:attach-busy", "session:attach-busy", "pane", t0))
	noErr(t, s.SetPaneOccupant(ctx, pane.ID, newConv.ID, 322, "node", SourceHook))
	if _, err := s.AttachToTopic(ctx, pane.ID, oldThread.ChatID, oldThread.MessageThreadID, newConv.ID, "ignored", t0.Add(time.Minute)); !errors.Is(err, ErrTopicOutboxSending) {
		t.Fatalf("attach error = %v, want ErrTopicOutboxSending", err)
	}
	if got := must(s.Thread(ctx, oldThread.ID)); got.Status != ThreadClosed {
		t.Fatalf("in-flight topic was reopened: %+v", got)
	}
}
