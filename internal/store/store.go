// Package store is rex-tg's SQLite storage: the schema, its migrations, and
// every query the daemon runs. See docs/design.md section 10.
//
// Times are stored as Unix milliseconds; the zero time is stored as NULL where
// the column allows it. The store keeps one connection open, so a method never
// runs two queries at once.
package store

import (
	"context"
	"database/sql"
	_ "embed" // schema.sql
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"
)

//go:embed schema.sql
var schemaV1 string

// schemaV2 allows the "rich" part format. SQLite cannot alter a CHECK
// constraint, so the outbox_part table is rebuilt with all rows and its index.
const schemaV2 = `
CREATE TABLE outbox_part_new (
  outbox_id     INTEGER NOT NULL REFERENCES outbox (id),
  part          INTEGER NOT NULL,
  state         TEXT NOT NULL CHECK (state IN ('pending', 'sending', 'sent', 'unknown', 'failed')),
  content       TEXT NOT NULL,
  format        TEXT NOT NULL CHECK (format IN ('html', 'plain', 'document', 'rich')),
  filename      TEXT,
  tg_message_id INTEGER,
  PRIMARY KEY (outbox_id, part)
) STRICT;
INSERT INTO outbox_part_new (outbox_id, part, state, content, format, filename, tg_message_id)
  SELECT outbox_id, part, state, content, format, filename, tg_message_id FROM outbox_part;
DROP TABLE outbox_part;
ALTER TABLE outbox_part_new RENAME TO outbox_part;
CREATE INDEX outbox_part_state ON outbox_part (state);
`

// schemaV3 records the message numbers that rex-tg has seen in each chat
// (seen_message), and for each outbox part the newest number known when it
// was sent (floor_id), so that a part whose outcome is unknown can be checked
// (see Store.UnseenBetween). The table starts with the numbers that the
// database already holds. Where the time of a number is not known, it counts
// as seen now, so it never raises the floor of an older part. Unknown parts
// of the last two hours get a floor, so their check runs; older ones had
// their notice already and wait for /resend.
const schemaV3 = `
CREATE TABLE seen_message (
  chat_id    INTEGER NOT NULL,
  message_id INTEGER NOT NULL,
  seen_at    INTEGER NOT NULL,
  PRIMARY KEY (chat_id, message_id)
) STRICT, WITHOUT ROWID;
ALTER TABLE outbox_part ADD COLUMN floor_id INTEGER;
ALTER TABLE outbox_part ADD COLUMN checked INTEGER NOT NULL DEFAULT 0 CHECK (checked IN (0, 1));
INSERT OR IGNORE INTO seen_message (chat_id, message_id, seen_at)
  SELECT chat_id, tg_message_id, created_at FROM inbound WHERE tg_message_id IS NOT NULL;
INSERT OR IGNORE INTO seen_message (chat_id, message_id, seen_at)
  SELECT t.chat_id, p.tg_message_id, COALESCE(o.sent_at, CAST(strftime('%s','now') AS INTEGER) * 1000)
  FROM outbox_part p JOIN outbox o ON o.id = p.outbox_id JOIN thread t ON t.id = o.thread_id
  WHERE p.tg_message_id IS NOT NULL;
INSERT OR IGNORE INTO seen_message (chat_id, message_id, seen_at)
  SELECT t.chat_id, g.tg_message_id, COALESCE(g.edited_at, CAST(strftime('%s','now') AS INTEGER) * 1000)
  FROM progress g JOIN thread t ON t.id = g.thread_id WHERE g.tg_message_id IS NOT NULL;
INSERT OR IGNORE INTO seen_message (chat_id, message_id, seen_at)
  SELECT t.chat_id, a.tg_message_id, CAST(strftime('%s','now') AS INTEGER) * 1000
  FROM alert a JOIN thread t ON t.id = a.thread_id WHERE a.tg_message_id IS NOT NULL;
UPDATE outbox_part SET floor_id = (
  SELECT MAX(s.message_id) FROM seen_message s JOIN outbox o ON o.id = outbox_part.outbox_id
  JOIN thread t ON t.id = o.thread_id WHERE s.chat_id = t.chat_id AND s.seen_at <= o.created_at)
WHERE state = 'unknown' AND outbox_id IN (
  SELECT id FROM outbox WHERE created_at >= (CAST(strftime('%s','now') AS INTEGER) - 7200) * 1000);
UPDATE outbox_part SET checked = 1
WHERE state = 'unknown' AND (floor_id IS NULL OR outbox_id IN (SELECT id FROM outbox WHERE kind = 'part-notice'));
`

// ErrNotFound is returned (wrapped) when a row does not exist.
var ErrNotFound = errors.New("store: not found")

// ErrNewerSchema is returned (wrapped) when the database was written by a newer
// version of rex-tg than this code.
var ErrNewerSchema = errors.New("store: database schema is newer than this program")

// ErrTopicTaken is returned (wrapped) when a conversation already belongs to
// another topic that is not closed.
var ErrTopicTaken = errors.New("store: conversation already has a topic")

const kvSchemaVersion = "schema_version"

type migration struct {
	version int
	sql     string
}

// migrations is the numbered list of schema steps. Append only.
var migrations = []migration{
	{version: 1, sql: schemaV1},
	{version: 2, sql: schemaV2},
	{version: 3, sql: schemaV3},
}

// Store is an open database.
type Store struct {
	db  *sql.DB
	ro  bool
	now func() time.Time // used where an API has no time parameter
}

// Open opens (and creates, if needed) the database at path and applies
// migrations. The parent folder gets mode 0700 and the file mode 0600.
func Open(path string) (*Store, error) {
	return open(path, migrations)
}

func open(path string, migs []migration) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: create folder: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: create file: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("store: create file: %w", err)
	}
	s, err := connect(path, false)
	if err != nil {
		return nil, err
	}
	if err := s.migrate(context.Background(), path, migs); err != nil {
		_ = s.db.Close()
		return nil, err
	}
	return s, nil
}

// OpenReadOnly opens an existing database without migrating it.
func OpenReadOnly(path string) (*Store, error) {
	return connect(path, true)
}

func connect(path string, ro bool) (*Store, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	if ro {
		q.Set("mode", "ro")
	} else {
		q.Add("_pragma", "journal_mode(WAL)")
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	return &Store{db: db, ro: ro, now: time.Now}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) version(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='kv'`).Scan(&n)
	if err != nil || n == 0 {
		return 0, err
	}
	var v string
	err = s.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key=?`, kvSchemaVersion).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(v)
}

func (s *Store) migrate(ctx context.Context, path string, migs []migration) error {
	cur, err := s.version(ctx)
	if err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	latest := migs[len(migs)-1].version
	if cur > latest {
		return fmt.Errorf("%w (database %d, program %d)", ErrNewerSchema, cur, latest)
	}
	if cur == latest {
		return nil
	}
	if cur > 0 {
		if _, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			return fmt.Errorf("store: checkpoint before backup: %w", err)
		}
		if err := copyFile(path, path+".bak-"+strconv.Itoa(cur)); err != nil {
			return fmt.Errorf("store: backup before migration: %w", err)
		}
	}
	for _, m := range migs {
		if m.version <= cur {
			continue
		}
		err := s.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO kv(key,value) VALUES(?,?)
				ON CONFLICT(key) DO UPDATE SET value=excluded.value`, kvSchemaVersion, strconv.Itoa(m.version))
			return err
		})
		if err != nil {
			return fmt.Errorf("store: migrate to version %d: %w", m.version, err)
		}
	}
	return nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// tx runs fn in a transaction. fn must use only the given tx.
func (s *Store) tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

type querier interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

type scanner interface{ Scan(dest ...any) error }

// Time and null helpers.

func toMs(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

func toMsNN(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMs(n sql.NullInt64) time.Time {
	if !n.Valid || n.Int64 == 0 {
		return time.Time{}
	}
	return time.UnixMilli(n.Int64).UTC()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func affected(res sql.Result, what string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return nil
}

func notFound(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// Get returns the value of a small key, if set.
func (s *Store) Get(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: get %q: %w", key, err)
	}
	return v, true, nil
}

// Set stores a small value.
func (s *Store) Set(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO kv(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("store: set %q: %w", key, err)
	}
	return nil
}

// Delete removes a small value. A missing key is not an error.
func (s *Store) Delete(ctx context.Context, key string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM kv WHERE key=?`, key); err != nil {
		return fmt.Errorf("store: delete %q: %w", key, err)
	}
	return nil
}
