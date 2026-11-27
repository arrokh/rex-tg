package store

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestOpenFreshAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "db")
	s, err := Open(path)
	noErr(t, err)
	v := must(s.version(ctx))
	if v != latestVersion {
		t.Fatalf("version = %d", v)
	}
	noErr(t, s.Set(ctx, "x", "1"))
	noErr(t, s.Close())

	for _, p := range []struct {
		path string
		mode os.FileMode
	}{{filepath.Dir(path), 0o700}, {path, 0o600}} {
		fi := must(os.Stat(p.path))
		if fi.Mode().Perm() != p.mode {
			t.Errorf("%s mode = %v, want %v", p.path, fi.Mode().Perm(), p.mode)
		}
	}

	s2, err := Open(path)
	noErr(t, err)
	defer s2.Close()
	if v, ok, _ := s2.Get(ctx, "x"); !ok || v != "1" {
		t.Fatalf("data lost on reopen: %q %v", v, ok)
	}
	matches, _ := filepath.Glob(path + ".bak-*")
	if len(matches) != 0 {
		t.Fatalf("unexpected backups %v", matches)
	}
}

func TestPragmas(t *testing.T) {
	s := newStore(t)
	for q, want := range map[string]string{
		"PRAGMA journal_mode": "wal",
		"PRAGMA foreign_keys": "1",
		"PRAGMA busy_timeout": "5000",
	} {
		var got string
		noErr(t, s.db.QueryRowContext(ctx, q).Scan(&got))
		if got != want {
			t.Errorf("%s = %s, want %s", q, got, want)
		}
	}
	if n := s.db.Stats().MaxOpenConnections; n != 1 {
		t.Errorf("max conns = %d", n)
	}
}

func TestMigrationBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := open(path, migrations[:1])
	noErr(t, err)
	noErr(t, s.Set(ctx, "keep", "me"))
	noErr(t, s.Close())

	next := append(append([]migration{}, migrations...), migration{version: 99, sql: `CREATE TABLE extra (x INTEGER) STRICT;`})
	s, err = open(path, next)
	noErr(t, err)
	if v := must(s.version(ctx)); v != 99 {
		t.Fatalf("version = %d", v)
	}
	noErr(t, s.Close())

	bak := path + ".bak-1"
	if _, err := os.Stat(bak); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	// The backup is a usable version-1 database with the old data.
	old, err := OpenReadOnly(bak)
	noErr(t, err)
	defer old.Close()
	if v, ok, _ := old.Get(ctx, "keep"); !ok || v != "me" {
		t.Fatalf("backup content: %q %v", v, ok)
	}
	if v := must(old.version(ctx)); v != 1 {
		t.Fatalf("backup version = %d", v)
	}
}

func TestFailedMigrationKeepsBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := open(path, migrations[:1])
	noErr(t, err)
	noErr(t, s.Close())
	bad := append(append([]migration{}, migrations[:1]...), migration{version: 2, sql: `NOT SQL;`})
	if _, err := open(path, bad); err == nil {
		t.Fatal("want error")
	}
	if _, err := os.Stat(path + ".bak-1"); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	s, err = Open(path) // still version 1, still opens
	noErr(t, err)
	s.Close()
}

func TestNewerSchemaIsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	noErr(t, err)
	noErr(t, s.Set(ctx, kvSchemaVersion, "99"))
	noErr(t, s.Close())
	_, err = Open(path)
	if !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	noErr(t, err)
	noErr(t, s.Set(ctx, "k", "v"))
	noErr(t, s.Close())

	ro, err := OpenReadOnly(path)
	noErr(t, err)
	defer ro.Close()
	if v, ok, _ := ro.Get(ctx, "k"); !ok || v != "v" {
		t.Fatalf("read: %q %v", v, ok)
	}
	if err := ro.Set(ctx, "k", "w"); err == nil {
		t.Fatal("write on read-only store succeeded")
	}
	if _, err := OpenReadOnly(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("read-only open of a missing file succeeded")
	}
}

func TestOpenReadOnlyDoesNotMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := open(path, migrations[:1])
	noErr(t, err)
	noErr(t, s.Close())
	ro, err := OpenReadOnly(path)
	noErr(t, err)
	defer ro.Close()
	if v := must(ro.version(ctx)); v != 1 {
		t.Fatal(v)
	}
}

func TestKV(t *testing.T) {
	s := newStore(t)
	if _, ok, err := s.Get(ctx, "a"); ok || err != nil {
		t.Fatalf("missing: %v %v", ok, err)
	}
	noErr(t, s.Set(ctx, "a", "1"))
	noErr(t, s.Set(ctx, "a", "2"))
	if v, ok, _ := s.Get(ctx, "a"); !ok || v != "2" {
		t.Fatalf("got %q %v", v, ok)
	}
	noErr(t, s.Delete(ctx, "a"))
	noErr(t, s.Delete(ctx, "a"))
	if _, ok, _ := s.Get(ctx, "a"); ok {
		t.Fatal("still there")
	}
}

// latestVersion is the schema version of a new database.
var latestVersion = migrations[len(migrations)-1].version

func TestFreshDatabaseIsTheLatestVersion(t *testing.T) {
	s := newStore(t)
	if v := must(s.version(ctx)); v != latestVersion || v != 7 {
		t.Fatalf("version = %d", v)
	}
}

func TestMigrateV1ToV2KeepsOutbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := open(path, migrations[:1])
	noErr(t, err)
	s.now = func() time.Time { return t0 }
	th, _ := topic(t, s, 5)
	for i, f := range []string{FormatHTML, FormatPlain, FormatDocument} {
		_, _, err := s.Enqueue(ctx, OutboxItem{ThreadID: th.ID, Key: "k" + strconv.Itoa(i), Kind: "answer",
			Parts: []PartContent{{Content: "c" + strconv.Itoa(i), Format: f, Filename: "f.txt"}}})
		noErr(t, err)
	}
	// A v1 database refuses rich parts.
	if _, _, err := s.Enqueue(ctx, OutboxItem{ThreadID: th.ID, Key: "r", Kind: "answer",
		Parts: []PartContent{{Content: "x", Format: FormatRich}}}); err == nil {
		t.Fatal("v1 accepted a rich part")
	}
	noErr(t, s.Close())

	s, err = Open(path)
	noErr(t, err)
	defer s.Close()
	s.now = func() time.Time { return t0 }
	if v := must(s.version(ctx)); v != latestVersion {
		t.Fatalf("version = %d", v)
	}
	if _, err := os.Stat(path + ".bak-1"); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	var n int
	noErr(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM outbox_part WHERE tg_message_id IS NULL AND state='pending'`).Scan(&n))
	if n != 3 {
		t.Fatalf("rows after migration = %d, want 3", n)
	}
	var formats string
	noErr(t, s.db.QueryRowContext(ctx, `SELECT group_concat(format) FROM (SELECT format FROM outbox_part ORDER BY outbox_id)`).Scan(&formats))
	if formats != "html,plain,document" {
		t.Fatalf("formats = %q", formats)
	}
	noErr(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name='outbox_part_state'`).Scan(&n))
	if n != 1 {
		t.Fatal("index outbox_part_state missing")
	}
	if _, _, err := s.Enqueue(ctx, OutboxItem{ThreadID: th.ID, Key: "r", Kind: "answer",
		Parts: []PartContent{{Content: "**x**", Format: FormatRich}}}); err != nil {
		t.Fatalf("rich part after migration: %v", err)
	}
}

func TestInsertRichPart(t *testing.T) {
	s := newStore(t)
	th, _ := topic(t, s, 5)
	id, _, err := s.Enqueue(ctx, OutboxItem{ThreadID: th.ID, Key: "r", Kind: "answer",
		Parts: []PartContent{{Content: "# hi", Format: FormatRich}}})
	noErr(t, err)
	var f string
	noErr(t, s.db.QueryRowContext(ctx, `SELECT format FROM outbox_part WHERE outbox_id=?`, id).Scan(&f))
	if f != FormatRich {
		t.Fatalf("format = %q", f)
	}
}

// Version 3 starts the table of seen message numbers with the numbers that
// the database holds, and gives an unknown part of the last two hours the
// floor that its check needs: the newest number seen when its item was
// made. Numbers whose time is not known never raise that floor. Older
// unknown parts had their notice already: they are not checked.
func TestMigrateV2ToV3SeedsMessageNumbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := open(path, migrations[:2])
	noErr(t, err)
	base := time.Now().Add(-10 * time.Minute)
	s.now = func() time.Time { return base }
	th, _ := topic(t, s, 5)
	at := func(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }
	for i, sec := range []int{10, 20} { // user messages 11 and 12
		_, err := s.InsertInbound(ctx, Inbound{UpdateID: int64(100 + i), ChatID: th.ChatID, TGMessageID: int64(11 + i),
			FromID: 1, Kind: "text", Body: "hi", State: InboundDone, CreatedAt: at(sec)})
		noErr(t, err)
	}
	// (A version 2 part is set without ClaimPart, which needs version 3.)
	s.now = func() time.Time { return at(25) }
	sent, _, err := s.Enqueue(ctx, OutboxItem{ThreadID: th.ID, Key: "sent", Kind: "answer", Parts: []PartContent{{Content: "a", Format: FormatHTML}}})
	noErr(t, err)
	noErr(t, s.MarkPartSent(ctx, sent, 0, 13)) // sent at 25 s
	s.now = func() time.Time { return at(30) }
	lost, _, err := s.Enqueue(ctx, OutboxItem{ThreadID: th.ID, Key: "lost", Kind: "answer", Parts: []PartContent{{Content: "b", Format: FormatHTML}}})
	noErr(t, err)
	noErr(t, s.MarkPartUnknown(ctx, lost, 0))
	note, _, err := s.Enqueue(ctx, OutboxItem{ThreadID: th.ID, Key: "notice:unknown:2:0", Kind: "part-notice", Parts: []PartContent{{Content: "c", Format: FormatHTML}}})
	noErr(t, err)
	noErr(t, s.MarkPartUnknown(ctx, note, 0))
	s.now = func() time.Time { return base.Add(-3 * time.Hour) }
	old, _, err := s.Enqueue(ctx, OutboxItem{ThreadID: th.ID, Key: "old", Kind: "answer", Parts: []PartContent{{Content: "d", Format: FormatHTML}}})
	noErr(t, err)
	noErr(t, s.MarkPartUnknown(ctx, old, 0))
	// An alert's time is not known: it must not raise the floor.
	_, err = s.db.ExecContext(ctx, `INSERT INTO alert(thread_id,kind,screen_hash,options,tg_message_id,state,created_at) VALUES(?,?,?,?,?,?,?)`,
		th.ID, "permission", "h", "[]", 14, "open", toMsNN(at(1)))
	noErr(t, err)
	noErr(t, s.Close())

	s, err = Open(path)
	noErr(t, err)
	defer s.Close()
	if n := must(s.UnseenBetween(ctx, th.ChatID, 10, 15)); n != 0 {
		t.Fatalf("numbers 11 to 14 not all seen: %d unseen", n)
	}
	p := must(s.Part(ctx, lost, 0))
	if !p.HasFloor || p.Floor != 13 || p.Checked {
		t.Fatalf("lost part: floor %d (known %v), checked %v; want 13, unchecked", p.Floor, p.HasFloor, p.Checked)
	}
	if p := must(s.Part(ctx, note, 0)); !p.Checked {
		t.Fatal("an old notice about a part must not be checked")
	}
	if p := must(s.Part(ctx, old, 0)); p.HasFloor || !p.Checked {
		t.Fatalf("a part from three hours ago: %+v; want no floor, checked", p)
	}
	if ps := must(s.PartsToCheck(ctx)); len(ps) != 1 || ps[0].OutboxID != lost {
		t.Fatalf("parts to check: %+v", ps)
	}
}

// Version 4 allows image parts and keeps every column of every part,
// including the floor and the checked flag of version 3.
func TestMigrateV3ToV4KeepsParts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := open(path, migrations[:3])
	noErr(t, err)
	s.now = func() time.Time { return t0 }
	th, _ := topic(t, s, 5)
	if _, _, err := s.Enqueue(ctx, OutboxItem{ThreadID: th.ID, Key: "i", Kind: "image",
		Parts: []PartContent{{Content: "/x.png", Format: FormatImage, Filename: "x.png"}}}); err == nil {
		t.Fatal("v3 accepted an image part")
	}
	unk, _, err := s.Enqueue(ctx, OutboxItem{ThreadID: th.ID, Key: "u", Kind: "answer",
		Parts: []PartContent{{Content: "a", Format: FormatHTML}, {Content: "b", Format: FormatDocument, Filename: "b.md"}}})
	noErr(t, err)
	ok, err := s.ClaimPart(ctx, unk, 0, 42)
	noErr(t, err)
	if !ok {
		t.Fatal("claim failed")
	}
	noErr(t, s.MarkPartUnknown(ctx, unk, 0))
	noErr(t, s.MarkPartChecked(ctx, unk, 0))
	noErr(t, s.MarkPartSent(ctx, unk, 1, 77))
	noErr(t, s.Close())

	s, err = Open(path)
	noErr(t, err)
	defer s.Close()
	s.now = func() time.Time { return t0 }
	if v := must(s.version(ctx)); v != latestVersion {
		t.Fatalf("version = %d", v)
	}
	if _, err := os.Stat(path + ".bak-3"); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	p0 := must(s.Part(ctx, unk, 0))
	if p0.State != OutboxUnknown || !p0.HasFloor || p0.Floor != 42 || !p0.Checked || p0.Content != "a" {
		t.Fatalf("part 0 after migration: %+v", p0)
	}
	p1 := must(s.Part(ctx, unk, 1))
	if p1.State != OutboxSent || p1.Format != FormatDocument || p1.Filename != "b.md" || p1.HasFloor {
		t.Fatalf("part 1 after migration: %+v", p1)
	}
	var n int
	noErr(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name='outbox_part_state'`).Scan(&n))
	if n != 1 {
		t.Fatal("index outbox_part_state missing")
	}
	id, _, err := s.Enqueue(ctx, OutboxItem{ThreadID: th.ID, Key: "i", Kind: "image",
		Parts: []PartContent{{Content: "/x.png", Format: FormatImage, Filename: "x.png"}}})
	noErr(t, err)
	if p := must(s.Part(ctx, id, 0)); p.Format != FormatImage || p.Content != "/x.png" {
		t.Fatalf("image part: %+v", p)
	}
}
