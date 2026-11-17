package store

import (
	"testing"
	"time"
)

func TestCommitTranscriptBatchRollsBackOutboxCursorAndSnapshotTogether(t *testing.T) {
	s := newStore(t)
	th, _ := topic(t, s, 1)
	old := Cursor{Transcript: "/transcript", DevIno: "dev", Offset: 10, LastEventAt: t0, UpdatedAt: t0}
	noErr(t, s.SaveCursor(ctx, old))
	noErr(t, s.Set(ctx, "turn:1", "old snapshot"))

	bad := item(th.ID, "bad", "new output")
	bad.Parts[0].Format = "invalid"
	if _, err := s.CommitTranscriptBatch(ctx,
		Cursor{Transcript: old.Transcript, DevIno: "dev", Offset: 20, UpdatedAt: t0.Add(time.Second)},
		[]OutboxItem{bad}, "turn:1", "new snapshot"); err == nil {
		t.Fatal("want the invalid outbox part to fail the transaction")
	}

	got, ok, err := s.Cursor(ctx, old.Transcript)
	noErr(t, err)
	if !ok || got.Offset != old.Offset {
		t.Fatalf("cursor after rollback = %+v, %v; want offset %d", got, ok, old.Offset)
	}
	if value, ok, err := s.Get(ctx, "turn:1"); err != nil || !ok || value != "old snapshot" {
		t.Fatalf("snapshot after rollback = %q, %v, %v", value, ok, err)
	}
	if _, found, err := s.OutboxIDByKey(ctx, th.ID, "bad"); err != nil || found {
		t.Fatalf("outbox after rollback: found %v, err %v", found, err)
	}

	_, err = s.db.ExecContext(ctx, `CREATE TRIGGER fail_turn_snapshot BEFORE UPDATE ON kv
		WHEN OLD.key='turn:1' BEGIN SELECT RAISE(ABORT, 'forced snapshot failure'); END`)
	noErr(t, err)
	good := item(th.ID, "good", "new output")
	if _, err := s.CommitTranscriptBatch(ctx,
		Cursor{Transcript: old.Transcript, DevIno: "dev", Offset: 20, UpdatedAt: t0.Add(time.Second)},
		[]OutboxItem{good}, "turn:1", "new snapshot"); err == nil {
		t.Fatal("want the snapshot trigger to fail the transaction")
	}
	_, err = s.db.ExecContext(ctx, `DROP TRIGGER fail_turn_snapshot`)
	noErr(t, err)
	got, ok, err = s.Cursor(ctx, old.Transcript)
	noErr(t, err)
	if !ok || got.Offset != old.Offset {
		t.Fatalf("cursor after snapshot failure = %+v, %v", got, ok)
	}
	if value, ok, err := s.Get(ctx, "turn:1"); err != nil || !ok || value != "old snapshot" {
		t.Fatalf("snapshot after snapshot failure = %q, %v, %v", value, ok, err)
	}
	if _, found, err := s.OutboxIDByKey(ctx, th.ID, "good"); err != nil || found {
		t.Fatalf("outbox after snapshot failure: found %v, err %v", found, err)
	}

	n, err := s.CommitTranscriptBatch(ctx,
		Cursor{Transcript: old.Transcript, DevIno: "dev", Offset: 20, UpdatedAt: t0.Add(time.Second)},
		[]OutboxItem{good}, "turn:1", "new snapshot")
	noErr(t, err)
	if n != 1 {
		t.Fatalf("inserted %d rows, want 1", n)
	}
	got, ok, err = s.Cursor(ctx, old.Transcript)
	noErr(t, err)
	if !ok || got.Offset != 20 {
		t.Fatalf("committed cursor = %+v, %v", got, ok)
	}
	if value, ok, err := s.Get(ctx, "turn:1"); err != nil || !ok || value != "new snapshot" {
		t.Fatalf("committed snapshot = %q, %v, %v", value, ok, err)
	}
	id, found, err := s.OutboxIDByKey(ctx, th.ID, "good")
	noErr(t, err)
	if !found {
		t.Fatal("committed outbox row is missing")
	}
	if row := must(s.OutboxRow(ctx, id)); row.Key != "good" {
		t.Fatalf("committed outbox = %+v", row)
	}
}
