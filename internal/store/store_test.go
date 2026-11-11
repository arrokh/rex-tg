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
	if v != 2 {
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

	next := append(append([]migration{}, migrations...), migration{version: 3, sql: `CREATE TABLE extra (x INTEGER) STRICT;`})
	s, err = open(path, next)
	noErr(t, err)
	if v := must(s.version(ctx)); v != 3 {
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

func TestFreshDatabaseIsVersion2(t *testing.T) {
	s := newStore(t)
	if v := must(s.version(ctx)); v != 2 {
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
	if v := must(s.version(ctx)); v != 2 {
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
