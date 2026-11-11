package store

import (
	"context"
	"testing"
)

func TestUnknownParts(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	conv, err := s.UpsertConversation(ctx, Conversation{Runtime: "claude", NativeID: "s", Transcript: "/t"})
	if err != nil {
		t.Fatal(err)
	}
	th, err := s.LinkTopic(ctx, 1, 10, conv.ID, "n", s.now())
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, k := range []string{"a", "b", "c"} {
		id, _, err := s.Enqueue(ctx, OutboxItem{ThreadID: th.ID, Key: k, Kind: "final", Parts: []PartContent{{Content: k, Format: FormatPlain}}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for _, i := range []int{2, 0} {
		if err := s.MarkPartUnknown(ctx, ids[i], 0); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.UnknownParts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].OutboxID != ids[0] || got[1].OutboxID != ids[2] || got[0].ThreadID != th.ID || got[0].ChatID != 1 {
		t.Fatalf("got %+v", got)
	}
}
