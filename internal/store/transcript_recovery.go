package store

import (
	"context"
	"database/sql"
	"fmt"
)

// CommitTranscriptBatch stores transcript output and its cursor together. When
// snapshotKey is non-empty, the tracker snapshot is committed in the same
// transaction as the outbox rows and cursor, so a restart cannot observe a
// cursor ahead of the tracker state that produced it.
func (s *Store) CommitTranscriptBatch(ctx context.Context, c Cursor, items []OutboxItem, snapshotKey, snapshot string) (int, error) {
	if (snapshotKey == "") != (snapshot == "") {
		return 0, fmt.Errorf("store: transcript snapshot key and value must both be set or empty")
	}

	inserted := 0
	now := s.now()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		for _, it := range items {
			_, ok, err := insertItem(ctx, tx, it, now)
			if err != nil {
				return err
			}
			if ok {
				inserted++
			}
		}
		if err := saveCursor(ctx, tx, c); err != nil {
			return err
		}
		if snapshotKey != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO kv(key,value) VALUES(?,?)
				ON CONFLICT(key) DO UPDATE SET value=excluded.value`, snapshotKey, snapshot); err != nil {
				return fmt.Errorf("store: save transcript snapshot: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return inserted, nil
}
