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
