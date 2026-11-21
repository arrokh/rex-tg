package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// discardPendingForReattach prevents unsent output from an old conversation
// from being delivered after its topic is attached to another agent. A part
// already being sent must finish first; its result may be externally visible.
func discardPendingForReattach(ctx context.Context, tx *sql.Tx, threadID int64, now time.Time) error {
	var sending int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM outbox_part p JOIN outbox o ON o.id=p.outbox_id
		WHERE o.thread_id=? AND p.state=?`, threadID, OutboxSending).Scan(&sending); err != nil {
		return fmt.Errorf("store: check messages in flight: %w", err)
	}
	if sending > 0 {
		return ErrTopicOutboxSending
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT o.id FROM outbox_part p JOIN outbox o ON o.id=p.outbox_id
		WHERE o.thread_id=? AND p.state=?`, threadID, OutboxPending)
	if err != nil {
		return fmt.Errorf("store: find pending messages: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fmt.Errorf("store: scan pending message: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("store: close pending messages: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE outbox_part SET state=? WHERE state=? AND outbox_id IN
		(SELECT id FROM outbox WHERE thread_id=?)`, OutboxFailed, OutboxPending, threadID); err != nil {
		return fmt.Errorf("store: discard pending messages: %w", err)
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE outbox SET last_error='topic reattached before delivery' WHERE id=?`, id); err != nil {
			return fmt.Errorf("store: mark discarded message: %w", err)
		}
		if err := recompute(ctx, tx, id, now); err != nil {
			return err
		}
	}
	return nil
}
