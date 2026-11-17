package store

import (
	"reflect"
	"testing"
	"time"
)

func TestProgress(t *testing.T) {
	s := newStore(t)
	th1, _ := topic(t, s, 1)
	th2, _ := topic(t, s, 2)
	if _, ok, err := s.Progress(ctx, th1.ID); ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	p := Progress{ThreadID: th1.ID, TurnKey: "t1", State: ProgressWaiting, StartedAt: t0}
	noErr(t, s.SaveProgress(ctx, p))
	got, ok, _ := s.Progress(ctx, th1.ID)
	if !ok || got.Rows != "[]" || got.TGMessageID != 0 || !got.EditedAt.IsZero() || !got.StartedAt.Equal(t0) {
		t.Fatalf("%+v", got)
	}
	p.State, p.TGMessageID, p.Headline, p.Rows, p.EditedAt = ProgressShown, 77, "Working", `["a","b"]`, t0.Add(time.Second)
	noErr(t, s.SaveProgress(ctx, p))
	got, _, _ = s.Progress(ctx, th1.ID)
	if !reflect.DeepEqual(got, p) {
		t.Fatalf("got %+v want %+v", got, p)
	}
	noErr(t, s.SaveProgress(ctx, Progress{ThreadID: th2.ID, TurnKey: "t2", State: ProgressUnknown, StartedAt: t0}))
	if all := must(s.AllProgress(ctx)); len(all) != 2 || all[0].ThreadID != th1.ID {
		t.Fatalf("%+v", all)
	}
	if err := s.SaveProgress(ctx, Progress{ThreadID: th1.ID, TurnKey: "t", State: ProgressShown, Rows: "not json", StartedAt: t0}); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	noErr(t, s.DeleteProgress(ctx, th1.ID))
	noErr(t, s.DeleteProgress(ctx, th1.ID))
	if all := must(s.AllProgress(ctx)); len(all) != 1 {
		t.Fatalf("%+v", all)
	}
}

func TestAlerts(t *testing.T) {
	s := newStore(t)
	th, _ := topic(t, s, 1)
	opts := []AlertOption{{Label: "Yes", Keys: []string{"y", "Enter"}}, {Label: "No", Keys: []string{"n"}}}
	id := must(s.CreateAlert(ctx, Alert{ThreadID: th.ID, Kind: AlertPermission, ScreenHash: "h1", Options: opts, CreatedAt: t0}))
	id2 := must(s.CreateAlert(ctx, Alert{ThreadID: th.ID, Kind: AlertQuestion, ScreenHash: "h2", CreatedAt: t0}))
	a := must(s.Alert(ctx, id))
	if a.State != AlertOpen || !reflect.DeepEqual(a.Options, opts) || a.TGMessageID != 0 || !a.CreatedAt.Equal(t0) {
		t.Fatalf("%+v", a)
	}
	noErr(t, s.SetAlertMessage(ctx, id, 900))
	if a := must(s.Alert(ctx, id)); a.TGMessageID != 900 {
		t.Fatalf("%+v", a)
	}
	if open := must(s.OpenAlerts(ctx, th.ID)); len(open) != 2 || open[0].ID != id || open[1].ID != id2 {
		t.Fatalf("%+v", open)
	}
	noErr(t, s.SetAlertState(ctx, id, AlertAnswered))
	if open := must(s.OpenAlerts(ctx, th.ID)); len(open) != 1 || open[0].ID != id2 {
		t.Fatalf("%+v", open)
	}
	if len(must(s.Alert(ctx, id2)).Options) != 0 {
		t.Fatal("nil options should decode empty")
	}
	if err := s.SetAlertState(ctx, id, "bogus"); err == nil {
		t.Fatal("bad state accepted")
	}
}

func TestInboundTransitions(t *testing.T) {
	s := newStore(t)
	th, _ := topic(t, s, 1)
	other, _ := topic(t, s, 2)
	mk := func(id int64, thread int64, created time.Time) Inbound {
		return Inbound{UpdateID: id, ChatID: 100, MessageThreadID: 1, TGMessageID: id * 10, FromID: 7, Kind: "text",
			Body: "hi", BodyHash: "hash", ThreadID: thread, CreatedAt: created}
	}
	for _, in := range []Inbound{mk(3, th.ID, t0.Add(2*time.Second)), mk(1, th.ID, t0), mk(2, other.ID, t0), mk(4, th.ID, t0.Add(time.Second))} {
		if ok := must(s.InsertInbound(ctx, in)); !ok {
			t.Fatalf("update %d not inserted", in.UpdateID)
		}
	}
	if ok := must(s.InsertInbound(ctx, mk(1, th.ID, t0))); ok {
		t.Fatal("duplicate update_id inserted")
	}
	q := must(s.QueuedInbound(ctx, th.ID))
	if len(q) != 3 || q[0].UpdateID != 1 || q[1].UpdateID != 4 || q[2].UpdateID != 3 {
		t.Fatalf("queue order: %+v", q)
	}
	if q[0].State != InboundQueued || q[0].BodyHash != "hash" || q[0].TGMessageID != 10 || q[0].MessageThreadID != 1 {
		t.Fatalf("%+v", q[0])
	}

	noErr(t, s.SetInboundState(ctx, 1, InboundTyping, "", t0))
	noErr(t, s.SetInboundState(ctx, 4, InboundTyping, "", t0))
	if in, ok := must2(s.InboundByID(ctx, 4)); !ok || in.State != InboundTyping || in.ThreadID != th.ID {
		t.Fatalf("InboundByID(4) = %+v, %v", in, ok)
	}
	noErr(t, s.SetInboundBody(ctx, 4, `{"retry":"file"}`))
	if in, ok := must2(s.InboundByID(ctx, 4)); !ok || in.Body != `{"retry":"file"}` {
		t.Fatalf("updated inbound body = %+v, %v", in, ok)
	}
	noErr(t, s.SetInboundBody(ctx, 4, ""))
	if err := s.SetInboundBody(ctx, 99, "x"); err == nil {
		t.Fatal("setting the body of a missing update succeeded")
	}
	if _, ok := must2(s.InboundByID(ctx, 99)); ok {
		t.Fatal("InboundByID found a missing update")
	}
	if q := must(s.QueuedInbound(ctx, th.ID)); len(q) != 1 || q[0].UpdateID != 3 {
		t.Fatalf("%+v", q)
	}
	// Delivered and acked.
	d1 := t0.Add(10 * time.Second)
	noErr(t, s.SetInboundState(ctx, 1, InboundDelivered, "", d1))
	noErr(t, s.SetInboundState(ctx, 3, InboundRefused, "agent busy", t0))
	got := must(s.DeliveredSince(ctx, th.ID, t0))
	if len(got) != 1 || got[0].UpdateID != 1 || !got[0].DeliveredAt.Equal(d1) {
		t.Fatalf("%+v", got)
	}
	if got := must(s.DeliveredSince(ctx, th.ID, d1.Add(time.Millisecond))); len(got) != 0 {
		t.Fatalf("since filter: %+v", got)
	}
	if got := must(s.DeliveredSince(ctx, other.ID, t0)); len(got) != 0 {
		t.Fatalf("topic filter: %+v", got)
	}
	noErr(t, s.AckInbound(ctx, 1, "claude:s:u1"))
	if got := must(s.DeliveredSince(ctx, th.ID, t0)); len(got) != 0 {
		t.Fatalf("acked row still delivered: %+v", got)
	}
	acked := must(queryInbound(ctx, s.db, `WHERE update_id=1`))[0]
	if acked.State != InboundAcked || acked.AckedEventKey != "claude:s:u1" {
		t.Fatalf("%+v", acked)
	}
	refused := must(queryInbound(ctx, s.db, `WHERE update_id=3`))[0]
	if refused.State != InboundRefused || refused.Reason != "agent busy" {
		t.Fatalf("%+v", refused)
	}
	if err := s.AckInbound(ctx, 999, "k"); err == nil {
		t.Fatal("ack of a missing update succeeded")
	}
	if err := s.SetInboundState(ctx, 1, "bogus", "", t0); err == nil {
		t.Fatal("bad state accepted")
	}
}

func TestRecoverTyping(t *testing.T) {
	s := newStore(t)
	th, _ := topic(t, s, 1)
	for id, st := range map[int64]string{1: InboundTyping, 2: InboundQueued, 3: InboundTyping, 4: InboundDelivered} {
		_, err := s.InsertInbound(ctx, Inbound{UpdateID: id, ChatID: 1, FromID: 1, Kind: "text", Body: "b", ThreadID: th.ID, State: st, CreatedAt: t0})
		noErr(t, err)
	}
	got := must(s.RecoverTyping(ctx, t0))
	if len(got) != 2 || got[0].UpdateID != 1 || got[1].UpdateID != 3 || got[0].State != InboundUnknown {
		t.Fatalf("%+v", got)
	}
	var states []string
	for _, in := range must(queryInbound(ctx, s.db, `ORDER BY update_id`)) {
		states = append(states, in.State)
	}
	want := []string{InboundUnknown, InboundQueued, InboundUnknown, InboundDelivered}
	if !reflect.DeepEqual(states, want) {
		t.Fatalf("states %v want %v", states, want)
	}
	if again := must(s.RecoverTyping(ctx, t0)); len(again) != 0 {
		t.Fatalf("%+v", again)
	}
}

// The joined row and the end of its parts are one change: after it, only the
// joined row waits to be typed.
func TestJoinInbound(t *testing.T) {
	s := newStore(t)
	th, _ := topic(t, s, 1)
	for _, id := range []int64{1, 2} {
		in := Inbound{UpdateID: id, ChatID: 100, MessageThreadID: 1, FromID: 7, Kind: "text", Body: "part", ThreadID: th.ID, CreatedAt: t0}
		if ok := must(s.InsertInbound(ctx, in)); !ok {
			t.Fatalf("update %d not inserted", id)
		}
	}
	joined := Inbound{UpdateID: -1, ChatID: 100, MessageThreadID: 1, FromID: 7, Kind: "text", Body: "part\n\npart", ThreadID: th.ID, CreatedAt: t0}
	if ok := must(s.JoinInbound(ctx, joined, []int64{1, 2})); !ok {
		t.Fatal("joined row not inserted")
	}
	q := must(s.QueuedInbound(ctx, th.ID))
	if len(q) != 1 || q[0].UpdateID != -1 || q[0].Body != "part\n\npart" {
		t.Fatalf("queue = %+v, want only the joined row", q)
	}
	for _, id := range []int64{1, 2} {
		rows := must(queryInbound(ctx, s.db, `WHERE update_id=?`, id))
		if len(rows) != 1 || rows[0].State != InboundDone || rows[0].Reason != "joined" {
			t.Fatalf("update %d = %+v, want done (joined)", id, rows)
		}
	}
	if ok := must(s.JoinInbound(ctx, joined, []int64{1, 2})); ok {
		t.Fatal("the joined row was inserted twice")
	}
}
