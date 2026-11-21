package store

import (
	"testing"
	"time"
)

func TestHistoricalConversationCanLinkToAnotherTopic(t *testing.T) {
	s := newStore(t)
	oldThread, oldConv := topic(t, s, 41)
	current := conv(t, s, "current")
	noErr(t, s.TakeOver(ctx, oldThread.ID, current.ID))
	if _, ok, err := s.ThreadByConversation(ctx, oldConv.ID); err != nil || ok {
		t.Fatalf("current-topic lookup returned historical session: ok=%v err=%v", ok, err)
	}
	if linked, ok, err := s.ThreadLinkByConversation(ctx, oldConv.ID); err != nil || !ok || linked.ID != oldThread.ID {
		t.Fatalf("history lookup = %+v, %v, %v", linked, ok, err)
	}
	newThread, err := s.LinkTopic(ctx, 100, 42, oldConv.ID, "new topic", t0)
	noErr(t, err)
	if newThread.CurrentConversationID != oldConv.ID || must(s.Thread(ctx, oldThread.ID)).CurrentConversationID != current.ID {
		t.Fatalf("old session link changed current topic: old=%+v new=%+v", must(s.Thread(ctx, oldThread.ID)), newThread)
	}
	noErr(t, s.CheckInvariants(ctx))
}

func TestHistoricalConversationCanAttachToAnotherTopic(t *testing.T) {
	s := newStore(t)
	oldThread, oldConv := topic(t, s, 51)
	current := conv(t, s, "current-attach")
	noErr(t, s.TakeOver(ctx, oldThread.ID, current.ID))
	targetThread, targetConv := topic(t, s, 52)
	noErr(t, s.SetThreadStatus(ctx, targetThread.ID, ThreadClosed))
	pane := must(s.UpsertPane(ctx, "local", "block:historical", "session:historical", "pane", t0))
	noErr(t, s.SetPaneOccupant(ctx, pane.ID, oldConv.ID, 501, "node", SourceHook))
	attached, err := s.AttachToTopic(ctx, pane.ID, targetThread.ChatID, targetThread.MessageThreadID, oldConv.ID, "ignored", t0.Add(time.Minute))
	noErr(t, err)
	if attached.ID != targetThread.ID || attached.CurrentConversationID != oldConv.ID || must(s.Thread(ctx, oldThread.ID)).CurrentConversationID != current.ID {
		t.Fatalf("attach moved the wrong topic: old=%+v target=%+v", must(s.Thread(ctx, oldThread.ID)), attached)
	}
	if must(s.Conversation(ctx, targetConv.ID)).ThreadID != targetThread.ID {
		t.Fatal("the previous target conversation lost its history link")
	}
	noErr(t, s.CheckInvariants(ctx))
}
