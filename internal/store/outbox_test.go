package store

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCursorRoundTrip(t *testing.T) {
	s := newStore(t)
	if _, ok, err := s.Cursor(ctx, "/t"); ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	c := Cursor{Transcript: "/t", DevIno: "1:2", Offset: 10, UpdatedAt: t0}
	noErr(t, s.SaveCursor(ctx, c))
	got, ok, _ := s.Cursor(ctx, "/t")
	if !ok || got.Offset != 10 || !got.LastEventAt.IsZero() || !got.UpdatedAt.Equal(t0) {
		t.Fatalf("%+v", got)
	}
	c.Offset, c.LastEventAt = 20, t0.Add(time.Second)
	noErr(t, s.SaveCursor(ctx, c))
	got, _, _ = s.Cursor(ctx, "/t")
	if got.Offset != 20 || !got.LastEventAt.Equal(t0.Add(time.Second)) {
		t.Fatalf("%+v", got)
	}
}

func TestCommitBatchDedupe(t *testing.T) {
	s := newStore(t)
	th, _ := topic(t, s, 1)
	items := []OutboxItem{item(th.ID, "k1", "a", "b"), item(th.ID, "k2", "c")}
	n, err := s.CommitBatch(ctx, Cursor{Transcript: "/t", DevIno: "d", Offset: 5, UpdatedAt: t0}, items)
	noErr(t, err)
	if n != 2 {
		t.Fatalf("inserted %d", n)
	}
	// Same lines again, plus one new, with a later cursor.
	items = append(items, item(th.ID, "k3", "d"))
	n, err = s.CommitBatch(ctx, Cursor{Transcript: "/t", DevIno: "d", Offset: 9, UpdatedAt: t0}, items)
	noErr(t, err)
	if n != 1 {
		t.Fatalf("inserted %d, want 1", n)
	}
	if c, _, _ := s.Cursor(ctx, "/t"); c.Offset != 9 {
		t.Fatalf("cursor %+v", c)
	}
	// k1 has exactly its two parts, not four.
	parts := must(queryParts(ctx, s.db, `WHERE o.event_key='k1' ORDER BY p.part`))
	if len(parts) != 2 || parts[0].Content != "a" || parts[1].Content != "b" {
		t.Fatalf("%+v", parts)
	}
}

func TestCommitBatchIsAtomic(t *testing.T) {
	s := newStore(t)
	th, _ := topic(t, s, 1)
	bad := item(th.ID, "bad", "x")
	bad.Parts[0].Format = "weird" // violates the CHECK
	_, err := s.CommitBatch(ctx, Cursor{Transcript: "/t", DevIno: "d", Offset: 5, UpdatedAt: t0},
		[]OutboxItem{item(th.ID, "good", "x"), bad})
	if err == nil {
		t.Fatal("want error")
	}
	if _, ok, _ := s.Cursor(ctx, "/t"); ok {
		t.Fatal("cursor saved despite failure")
	}
	if p := must(s.NextParts(ctx)); len(p) != 0 {
		t.Fatalf("rows left behind: %+v", p)
	}
}

func TestEnqueue(t *testing.T) {
	s := newStore(t)
	th, _ := topic(t, s, 1)
	it := item(th.ID, "k", "hello")
	it.Silent = true
	id, ok, err := s.Enqueue(ctx, it)
	noErr(t, err)
	if !ok {
		t.Fatal("not inserted")
	}
	id2, ok, err := s.Enqueue(ctx, it)
	noErr(t, err)
	if ok || id2 != id {
		t.Fatalf("second: %d %v", id2, ok)
	}
	row := must(s.OutboxRow(ctx, id))
	if row.State != OutboxPending || !row.Silent || row.Kind != "answer" || len(row.Parts) != 1 || !row.CreatedAt.Equal(t0) {
		t.Fatalf("%+v", row)
	}
	p := row.Parts[0]
	if p.ChatID != 100 || p.MessageThreadID != 1 || p.ThreadID != th.ID || !p.Silent || p.Format != FormatHTML {
		t.Fatalf("part routing: %+v", p)
	}

	// Skip: skipped, no parts, never sent.
	skip := item(th.ID, "echo", "mine")
	skip.Skip = true
	sid, ok, err := s.Enqueue(ctx, skip)
	noErr(t, err)
	if !ok {
		t.Fatal("skip not inserted")
	}
	srow := must(s.OutboxRow(ctx, sid))
	if srow.State != OutboxSkipped || len(srow.Parts) != 0 {
		t.Fatalf("%+v", srow)
	}
	// A document part keeps its filename.
	did, _, err := s.Enqueue(ctx, OutboxItem{ThreadID: th.ID, Key: "doc", Kind: "block",
		Parts: []PartContent{{Content: "data", Format: FormatDocument, Filename: "out.txt"}}})
	noErr(t, err)
	if r := must(s.OutboxRow(ctx, did)); r.Parts[0].Filename != "out.txt" || r.Parts[0].Format != FormatDocument {
		t.Fatalf("%+v", r.Parts[0])
	}
	if _, _, err := s.Enqueue(ctx, OutboxItem{ThreadID: th.ID, Key: "empty", Kind: "answer"}); err == nil {
		t.Fatal("item without parts accepted")
	}
	if _, err := s.OutboxRow(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestNextPartsOrderingAcrossTopics(t *testing.T) {
	s := newStore(t)
	th1, _ := topic(t, s, 1)
	th2, _ := topic(t, s, 2)
	a := must(mustID(s.Enqueue(ctx, item(th1.ID, "a", "a0", "a1"))))
	b := must(mustID(s.Enqueue(ctx, item(th2.ID, "b", "b0"))))
	c := must(mustID(s.Enqueue(ctx, item(th1.ID, "c", "c0"))))
	_ = c

	got := must(s.NextParts(ctx))
	if len(got) != 2 || got[0].OutboxID != a || got[0].Part != 0 || got[1].OutboxID != b {
		t.Fatalf("%+v", got)
	}
	// Claiming a0 blocks topic 1 entirely; topic 2 still shows.
	if ok := must(s.ClaimPart(ctx, a, 0, -1)); !ok {
		t.Fatal("claim failed")
	}
	got = must(s.NextParts(ctx))
	if len(got) != 1 || got[0].OutboxID != b {
		t.Fatalf("%+v", got)
	}
	noErr(t, s.MarkPartSent(ctx, a, 0, 501))
	got = must(s.NextParts(ctx))
	if len(got) != 2 || got[0].OutboxID != a || got[0].Part != 1 {
		t.Fatalf("a1 must come before c0: %+v", got)
	}
	if r := must(s.OutboxRow(ctx, a)); r.State != OutboxPending || r.Parts[0].TGMessageID != 501 {
		t.Fatalf("%+v", r)
	}
	// Closed topics are not sent to.
	noErr(t, s.SetThreadStatus(ctx, th2.ID, ThreadClosed))
	got = must(s.NextParts(ctx))
	if len(got) != 1 || got[0].ThreadID != th1.ID {
		t.Fatalf("%+v", got)
	}
}

func mustID(id int64, ok bool, err error) (int64, error) { return id, err }

func TestNonPendingRowsDoNotBlock(t *testing.T) {
	s := newStore(t)
	th, _ := topic(t, s, 1)
	ids := map[string]int64{}
	for _, k := range []string{"unk", "fail", "skip", "sent", "next"} {
		it := item(th.ID, k, k)
		it.Skip = k == "skip"
		ids[k] = must(mustID(s.Enqueue(ctx, it)))
	}
	noErr(t, s.MarkPartUnknown(ctx, ids["unk"], 0))
	noErr(t, s.MarkPartFailed(ctx, ids["fail"], 0, "boom"))
	noErr(t, s.MarkPartSent(ctx, ids["sent"], 0, 1))
	got := must(s.NextParts(ctx))
	if len(got) != 1 || got[0].OutboxID != ids["next"] {
		t.Fatalf("%+v", got)
	}
	if r := must(s.OutboxRow(ctx, ids["fail"])); r.State != OutboxFailed || r.LastError != "boom" {
		t.Fatalf("%+v", r)
	}
	// Quote characters in a reason are stored verbatim.
	noErr(t, s.MarkPartFailed(ctx, ids["fail"], 0, "it's \"bad\""))
	if r := must(s.OutboxRow(ctx, ids["fail"])); r.LastError != "it's \"bad\"" {
		t.Fatalf("%q", r.LastError)
	}
}

func TestClaimRace(t *testing.T) {
	s := newStore(t)
	th, _ := topic(t, s, 1)
	id := must(mustID(s.Enqueue(ctx, item(th.ID, "k", "x"))))
	var wg sync.WaitGroup
	wins := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.ClaimPart(ctx, id, 0, -1)
			if err != nil {
				t.Error(err)
			}
			wins <- ok
		}()
	}
	wg.Wait()
	close(wins)
	n := 0
	for w := range wins {
		if w {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d claims won", n)
	}
	if r := must(s.OutboxRow(ctx, id)); r.Attempts != 1 || r.Parts[0].State != OutboxSending {
		t.Fatalf("%+v", r)
	}
	// Second sequential claim loses.
	if ok := must(s.ClaimPart(ctx, id, 0, -1)); ok {
		t.Fatal("claimed twice")
	}
}

func TestRowStateFromParts(t *testing.T) {
	s := newStore(t)
	th, _ := topic(t, s, 1)
	id := must(mustID(s.Enqueue(ctx, item(th.ID, "k", "a", "b", "c"))))
	state := func() string { return must(s.OutboxRow(ctx, id)).State }

	noErr(t, s.MarkPartSent(ctx, id, 0, 1))
	if state() != OutboxPending {
		t.Fatal(state())
	}
	noErr(t, s.MarkPartFailed(ctx, id, 1, "x"))
	if state() != OutboxFailed {
		t.Fatal(state())
	}
	noErr(t, s.MarkPartUnknown(ctx, id, 2))
	if state() != OutboxUnknown { // unknown wins over failed
		t.Fatal(state())
	}
	n := must(s.ResendUnknown(ctx, id))
	if n != 1 {
		t.Fatalf("resent %d", n)
	}
	if state() != OutboxFailed {
		t.Fatal(state())
	}
	if p := must(s.NextParts(ctx)); len(p) != 1 || p[0].Part != 2 {
		t.Fatalf("%+v", p)
	}
	noErr(t, s.MarkPartSent(ctx, id, 1, 2))
	noErr(t, s.MarkPartSent(ctx, id, 2, 3))
	row := must(s.OutboxRow(ctx, id))
	if row.State != OutboxSent || !row.SentAt.Equal(t0) {
		t.Fatalf("%+v", row)
	}
	if n := must(s.ResendUnknown(ctx, id)); n != 0 {
		t.Fatalf("resent %d", n)
	}
	if err := s.MarkPartSent(ctx, id, 9, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestReleasePart(t *testing.T) {
	s := newStore(t)
	th, _ := topic(t, s, 1)
	id := must(mustID(s.Enqueue(ctx, item(th.ID, "k", "a"))))
	if err := s.ReleasePart(ctx, id, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("release of a pending part: %v", err)
	}
	must(s.ClaimPart(ctx, id, 0, -1))
	noErr(t, s.ReleasePart(ctx, id, 0))
	if p := must(s.NextParts(ctx)); len(p) != 1 {
		t.Fatalf("%+v", p)
	}
}

func TestRecoverSending(t *testing.T) {
	s := newStore(t)
	th1, _ := topic(t, s, 1)
	th2, _ := topic(t, s, 2)
	a := must(mustID(s.Enqueue(ctx, item(th1.ID, "a", "a0", "a1"))))
	b := must(mustID(s.Enqueue(ctx, item(th2.ID, "b", "b0"))))
	must(s.ClaimPart(ctx, a, 0, -1))
	must(s.ClaimPart(ctx, b, 0, -1))
	parts := must(s.RecoverSending(ctx))
	if len(parts) != 2 || parts[0].State != OutboxUnknown || parts[0].OutboxID != a || parts[1].OutboxID != b {
		t.Fatalf("%+v", parts)
	}
	if r := must(s.OutboxRow(ctx, a)); r.State != OutboxUnknown || r.Parts[0].State != OutboxUnknown || r.Parts[1].State != OutboxPending {
		t.Fatalf("%+v", r)
	}
	if again := must(s.RecoverSending(ctx)); len(again) != 0 {
		t.Fatalf("%+v", again)
	}
	// Sending continues at the next part; the unknown part is not re-sent.
	if p := must(s.NextParts(ctx)); len(p) != 1 || p[0].OutboxID != a || p[0].Part != 1 {
		t.Fatalf("%+v", p)
	}
}

func TestPrune(t *testing.T) {
	s := newStore(t)
	day := 24 * time.Hour
	now := t0.Add(100 * day)
	thOpen, _ := topic(t, s, 1)
	thClosed, _ := topic(t, s, 2)
	thRecent, _ := topic(t, s, 3)

	enqueue := func(th int64, key string, age time.Duration, finish func(id int64)) int64 {
		s.now = func() time.Time { return now.Add(-age) }
		id := must(mustID(s.Enqueue(ctx, item(th, key, "x"))))
		if finish != nil {
			finish(id)
		}
		return id
	}
	sent := func(id int64) { noErr(t, s.MarkPartSent(ctx, id, 0, 1)) }
	oldSent := enqueue(thOpen.ID, "old-sent", 30*day+time.Millisecond, sent)
	edgeSent := enqueue(thOpen.ID, "edge-sent", 30*day, sent)
	oldPending := enqueue(thOpen.ID, "old-pending", 40*day, nil)
	oldFailed := enqueue(thOpen.ID, "old-failed", 40*day, func(id int64) { noErr(t, s.MarkPartFailed(ctx, id, 0, "x")) })
	skipIt := item(thOpen.ID, "old-skip", "x")
	skipIt.Skip = true
	s.now = func() time.Time { return now.Add(-31 * day) }
	oldSkip := must(mustID(s.Enqueue(ctx, skipIt)))

	// Closed topic rows go when the topic was closed more than 30 days ago.
	closedRow := enqueue(thClosed.ID, "closed-row", 1*day, nil)
	s.now = func() time.Time { return now.Add(-31 * day) }
	noErr(t, s.SetThreadStatus(ctx, thClosed.ID, ThreadClosed))
	// A recently closed topic keeps its rows.
	recentRow := enqueue(thRecent.ID, "recent-row", 1*day, nil)
	s.now = func() time.Time { return now.Add(-29 * day) }
	noErr(t, s.SetThreadStatus(ctx, thRecent.ID, ThreadClosed))

	// Inbound and alerts.
	for id, age := range map[int64]time.Duration{1: 31 * day, 2: 29 * day} {
		_, err := s.InsertInbound(ctx, Inbound{UpdateID: id, ChatID: 1, FromID: 1, Kind: "text", Body: "b", CreatedAt: now.Add(-age)})
		noErr(t, err)
	}
	oldAlert := must(s.CreateAlert(ctx, Alert{ThreadID: thOpen.ID, Kind: AlertPermission, ScreenHash: "h", CreatedAt: now.Add(-31 * day)}))
	newAlert := must(s.CreateAlert(ctx, Alert{ThreadID: thOpen.ID, Kind: AlertPermission, ScreenHash: "h", CreatedAt: now.Add(-29 * day)}))

	noErr(t, s.Prune(ctx, now))

	exists := func(id int64) bool { _, err := s.OutboxRow(ctx, id); return err == nil }
	for name, tc := range map[string]struct {
		id   int64
		want bool
	}{
		"old sent": {oldSent, false}, "edge sent": {edgeSent, true}, "old pending": {oldPending, true},
		"old failed": {oldFailed, true}, "old skipped": {oldSkip, false}, "closed topic row": {closedRow, false},
		"recently closed topic row": {recentRow, true},
	} {
		if exists(tc.id) != tc.want {
			t.Errorf("%s: exists = %v, want %v", name, !tc.want, tc.want)
		}
	}
	var n int
	noErr(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM outbox_part WHERE outbox_id=?`, oldSent).Scan(&n))
	if n != 0 {
		t.Errorf("parts of a pruned row remain: %d", n)
	}
	if _, err := s.Alert(ctx, oldAlert); !errors.Is(err, ErrNotFound) {
		t.Errorf("old alert: %v", err)
	}
	if _, err := s.Alert(ctx, newAlert); err != nil {
		t.Errorf("new alert: %v", err)
	}
	var inbound int
	noErr(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM inbound`).Scan(&inbound))
	if inbound != 1 {
		t.Errorf("inbound rows = %d, want 1", inbound)
	}
}

// Message numbers: a part's floor is kept from its claim, and a gap of
// numbers that rex-tg has not seen is counted.
func TestSeenMessagesAndPartChecks(t *testing.T) {
	s := newStore(t)
	th, _ := topic(t, s, 1)
	if _, ok := must2(s.MaxMessage(ctx, th.ChatID)); ok {
		t.Fatal("a new chat has a newest message")
	}
	for _, id := range []int64{5, 6, 8, 6} {
		noErr(t, s.NoteMessage(ctx, th.ChatID, id, t0))
	}
	noErr(t, s.NoteMessage(ctx, th.ChatID+1, 7, t0)) // another chat
	if id, ok := must2(s.MaxMessage(ctx, th.ChatID)); !ok || id != 8 {
		t.Fatalf("newest = %d %v", id, ok)
	}
	for _, c := range []struct{ lo, hi, want int64 }{{4, 9, 1}, {5, 8, 1}, {6, 8, 1}, {8, 9, 0}, {8, 8, 0}, {5, 7, 0}} {
		if got := must(s.UnseenBetween(ctx, th.ChatID, c.lo, c.hi)); got != c.want {
			t.Errorf("unseen (%d, %d) = %d, want %d", c.lo, c.hi, got, c.want)
		}
	}

	id, _, err := s.Enqueue(ctx, OutboxItem{ThreadID: th.ID, Key: "k", Kind: "answer", Parts: []PartContent{{Content: "x", Format: FormatHTML}}})
	noErr(t, err)
	must(s.ClaimPart(ctx, id, 0, 8))
	noErr(t, s.MarkPartUnknown(ctx, id, 0))
	if p := must(s.Part(ctx, id, 0)); !p.HasFloor || p.Floor != 8 || p.Checked || p.State != OutboxUnknown {
		t.Fatalf("part after claim: %+v", p)
	}
	if ps := must(s.PartsToCheck(ctx)); len(ps) != 1 {
		t.Fatalf("parts to check: %+v", ps)
	}
	noErr(t, s.MarkPartChecked(ctx, id, 0))
	if ps := must(s.PartsToCheck(ctx)); len(ps) != 0 {
		t.Fatalf("a checked part is checked again: %+v", ps)
	}
	if ok := must(s.ResendPart(ctx, id, 0)); !ok {
		t.Fatal("resend part: not unknown")
	}
	if p := must(s.Part(ctx, id, 0)); p.State != OutboxPending || p.Checked {
		t.Fatalf("part after resend: %+v", p)
	}
	if ok := must(s.ResendPart(ctx, id, 0)); ok {
		t.Fatal("a pending part was moved again")
	}
	must(s.ClaimPart(ctx, id, 0, -1))
	if p := must(s.Part(ctx, id, 0)); p.HasFloor {
		t.Fatalf("a claim without a known floor kept the old one: %+v", p)
	}
}

func must2[T, U any](a T, b U, err error) (T, U) {
	if err != nil {
		panic(err)
	}
	return a, b
}
