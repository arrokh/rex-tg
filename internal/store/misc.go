package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ---- Progress messages ----

const progressCols = `thread_id, turn_key, state, tg_message_id, headline, rows, started_at, edited_at`

func scanProgress(r scanner) (Progress, error) {
	var p Progress
	var tg, started, edited sql.NullInt64
	var headline sql.NullString
	if err := r.Scan(&p.ThreadID, &p.TurnKey, &p.State, &tg, &headline, &p.Rows, &started, &edited); err != nil {
		return Progress{}, err
	}
	p.TGMessageID, p.Headline = tg.Int64, headline.String
	p.StartedAt, p.EditedAt = fromMs(started), fromMs(edited)
	return p, nil
}

// Progress returns a topic's live progress message.
func (s *Store) Progress(ctx context.Context, threadID int64) (Progress, bool, error) {
	p, err := scanProgress(s.db.QueryRowContext(ctx, `SELECT `+progressCols+` FROM progress WHERE thread_id=?`, threadID))
	if errors.Is(err, sql.ErrNoRows) {
		return Progress{}, false, nil
	}
	if err != nil {
		return Progress{}, false, fmt.Errorf("store: progress: %w", err)
	}
	return p, true, nil
}

// SaveProgress inserts or replaces a topic's progress message. Empty Rows is "[]".
func (s *Store) SaveProgress(ctx context.Context, p Progress) error {
	rows := p.Rows
	if rows == "" {
		rows = "[]"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO progress(`+progressCols+`) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(thread_id) DO UPDATE SET turn_key=excluded.turn_key, state=excluded.state,
		  tg_message_id=excluded.tg_message_id, headline=excluded.headline, rows=excluded.rows,
		  started_at=excluded.started_at, edited_at=excluded.edited_at`,
		p.ThreadID, p.TurnKey, p.State, nullInt(p.TGMessageID), nullStr(p.Headline), rows, toMsNN(p.StartedAt), toMs(p.EditedAt))
	if err != nil {
		return fmt.Errorf("store: save progress: %w", err)
	}
	return nil
}

// DeleteProgress removes a topic's progress message. A missing row is not an error.
func (s *Store) DeleteProgress(ctx context.Context, threadID int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM progress WHERE thread_id=?`, threadID); err != nil {
		return fmt.Errorf("store: delete progress: %w", err)
	}
	return nil
}

// AllProgress lists every progress message, by topic.
func (s *Store) AllProgress(ctx context.Context) ([]Progress, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+progressCols+` FROM progress ORDER BY thread_id`)
	if err != nil {
		return nil, fmt.Errorf("store: all progress: %w", err)
	}
	defer rows.Close()
	var out []Progress
	for rows.Next() {
		p, err := scanProgress(rows)
		if err != nil {
			return nil, fmt.Errorf("store: all progress: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- Alerts ----

const alertCols = `id, thread_id, kind, screen_hash, options, tg_message_id, state, created_at`

func scanAlert(r scanner) (Alert, error) {
	var a Alert
	var opts string
	var tg, created sql.NullInt64
	if err := r.Scan(&a.ID, &a.ThreadID, &a.Kind, &a.ScreenHash, &opts, &tg, &a.State, &created); err != nil {
		return Alert{}, err
	}
	if err := json.Unmarshal([]byte(opts), &a.Options); err != nil {
		return Alert{}, fmt.Errorf("decode alert options: %w", err)
	}
	a.TGMessageID, a.CreatedAt = tg.Int64, fromMs(created)
	return a, nil
}

// CreateAlert stores an alert. An empty State is open.
func (s *Store) CreateAlert(ctx context.Context, a Alert) (int64, error) {
	opts, err := json.Marshal(a.Options)
	if err != nil {
		return 0, fmt.Errorf("store: encode alert options: %w", err)
	}
	if a.Options == nil {
		opts = []byte("[]")
	}
	state := a.State
	if state == "" {
		state = AlertOpen
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO alert(thread_id,kind,screen_hash,options,tg_message_id,state,created_at)
		VALUES(?,?,?,?,?,?,?)`, a.ThreadID, a.Kind, a.ScreenHash, string(opts), nullInt(a.TGMessageID), state, toMsNN(a.CreatedAt))
	if err != nil {
		return 0, fmt.Errorf("store: create alert: %w", err)
	}
	return res.LastInsertId()
}

// SetAlertMessage records the Telegram message of an alert.
func (s *Store) SetAlertMessage(ctx context.Context, id, tgMessageID int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE alert SET tg_message_id=? WHERE id=?`, nullInt(tgMessageID), id)
	if err != nil {
		return fmt.Errorf("store: set alert message: %w", err)
	}
	return affected(res, fmt.Sprintf("store: alert %d", id))
}

// SetAlertState changes an alert's state.
func (s *Store) SetAlertState(ctx context.Context, id int64, state string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE alert SET state=? WHERE id=?`, state, id)
	if err != nil {
		return fmt.Errorf("store: set alert state: %w", err)
	}
	return affected(res, fmt.Sprintf("store: alert %d", id))
}

// Alert returns an alert by ID.
func (s *Store) Alert(ctx context.Context, id int64) (Alert, error) {
	a, err := scanAlert(s.db.QueryRowContext(ctx, `SELECT `+alertCols+` FROM alert WHERE id=?`, id))
	if err != nil {
		return Alert{}, notFound(err, fmt.Sprintf("store: alert %d", id))
	}
	return a, nil
}

// OpenAlerts lists a topic's open alerts, oldest first.
func (s *Store) OpenAlerts(ctx context.Context, threadID int64) ([]Alert, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+alertCols+` FROM alert WHERE thread_id=? AND state='open' ORDER BY id`, threadID)
	if err != nil {
		return nil, fmt.Errorf("store: open alerts: %w", err)
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, fmt.Errorf("store: open alerts: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---- Incoming updates ----

const inboundCols = `update_id, chat_id, message_thread_id, tg_message_id, from_id, kind, body, body_hash,
	thread_id, state, reason, created_at, delivered_at, acked_event_key`

func scanInbound(r scanner) (Inbound, error) {
	var in Inbound
	var mt, tg, th, created, delivered sql.NullInt64
	var hash, reason, acked sql.NullString
	err := r.Scan(&in.UpdateID, &in.ChatID, &mt, &tg, &in.FromID, &in.Kind, &in.Body, &hash,
		&th, &in.State, &reason, &created, &delivered, &acked)
	if err != nil {
		return Inbound{}, err
	}
	in.MessageThreadID, in.TGMessageID, in.ThreadID = mt.Int64, tg.Int64, th.Int64
	in.BodyHash, in.Reason, in.AckedEventKey = hash.String, reason.String, acked.String
	in.CreatedAt, in.DeliveredAt = fromMs(created), fromMs(delivered)
	return in, nil
}

func queryInbound(ctx context.Context, q querier, where string, args ...any) ([]Inbound, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+inboundCols+` FROM inbound `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("store: query inbound: %w", err)
	}
	defer rows.Close()
	var out []Inbound
	for rows.Next() {
		in, err := scanInbound(rows)
		if err != nil {
			return nil, fmt.Errorf("store: query inbound: %w", err)
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// InsertInbound stores an update. It returns false when the update_id exists.
// An empty State is queued.
func (s *Store) InsertInbound(ctx context.Context, in Inbound) (bool, error) {
	state := in.State
	if state == "" {
		state = InboundQueued
	}
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO inbound(`+inboundCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.UpdateID, in.ChatID, nullInt(in.MessageThreadID), nullInt(in.TGMessageID), in.FromID, in.Kind, in.Body,
		nullStr(in.BodyHash), nullInt(in.ThreadID), state, nullStr(in.Reason), toMsNN(in.CreatedAt),
		toMs(in.DeliveredAt), nullStr(in.AckedEventKey))
	if err != nil {
		return false, fmt.Errorf("store: insert inbound: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// JoinInbound stores the row of a joined message and marks the queued updates
// it joins as done, in one transaction, so a crash cannot leave both the
// joined row and its parts to be typed. It returns false when the joined row
// exists already.
func (s *Store) JoinInbound(ctx context.Context, joined Inbound, parts []int64) (bool, error) {
	var inserted bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		state := joined.State
		if state == "" {
			state = InboundQueued
		}
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO inbound(`+inboundCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			joined.UpdateID, joined.ChatID, nullInt(joined.MessageThreadID), nullInt(joined.TGMessageID), joined.FromID,
			joined.Kind, joined.Body, nullStr(joined.BodyHash), nullInt(joined.ThreadID), state, nullStr(joined.Reason),
			toMsNN(joined.CreatedAt), toMs(joined.DeliveredAt), nullStr(joined.AckedEventKey))
		if err != nil {
			return fmt.Errorf("store: insert joined inbound: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		inserted = n > 0
		for _, id := range parts {
			if _, err := tx.ExecContext(ctx, `UPDATE inbound SET state='done', reason='joined' WHERE update_id=? AND state='queued'`, id); err != nil {
				return fmt.Errorf("store: mark joined inbound: %w", err)
			}
		}
		return nil
	})
	return inserted, err
}

// SetInboundState changes an update's state and reason. Moving to delivered
// also records the delivery time.
func (s *Store) SetInboundState(ctx context.Context, updateID int64, state, reason string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE inbound SET state=?1, reason=?2,
		delivered_at = CASE WHEN ?1='delivered' THEN ?3 ELSE delivered_at END WHERE update_id=?4`,
		state, nullStr(reason), toMs(at), updateID)
	if err != nil {
		return fmt.Errorf("store: set inbound state: %w", err)
	}
	return affected(res, fmt.Sprintf("store: inbound %d", updateID))
}

// QueuedInbound lists a topic's queued updates, oldest first.
func (s *Store) QueuedInbound(ctx context.Context, threadID int64) ([]Inbound, error) {
	return queryInbound(ctx, s.db, `WHERE thread_id=? AND state='queued' ORDER BY created_at, update_id`, threadID)
}

// RecoverTyping moves every typing update to unknown (at start) and returns
// them. The time is recorded in the reason.
func (s *Store) RecoverTyping(ctx context.Context, at time.Time) ([]Inbound, error) {
	var out []Inbound
	err := s.tx(ctx, func(tx *sql.Tx) error {
		ins, err := queryInbound(ctx, tx, `WHERE state='typing' ORDER BY update_id`)
		if err != nil {
			return err
		}
		reason := "daemon stopped while typing at " + at.UTC().Format(time.RFC3339)
		if _, err := tx.ExecContext(ctx, `UPDATE inbound SET state='unknown', reason=? WHERE state='typing'`, reason); err != nil {
			return err
		}
		for i := range ins {
			ins[i].State, ins[i].Reason = InboundUnknown, reason
		}
		out = ins
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: recover typing: %w", err)
	}
	return out, nil
}

// DeliveredSince lists a topic's delivered, not yet acked updates delivered at
// or after since, oldest first.
func (s *Store) DeliveredSince(ctx context.Context, threadID int64, since time.Time) ([]Inbound, error) {
	return queryInbound(ctx, s.db, `WHERE thread_id=? AND state='delivered' AND delivered_at>=? ORDER BY delivered_at, update_id`,
		threadID, toMsNN(since))
}

// AckInbound marks an update as confirmed by a transcript line.
func (s *Store) AckInbound(ctx context.Context, updateID int64, eventKey string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE inbound SET state='acked', acked_event_key=? WHERE update_id=?`, nullStr(eventKey), updateID)
	if err != nil {
		return fmt.Errorf("store: ack inbound: %w", err)
	}
	return affected(res, fmt.Sprintf("store: inbound %d", updateID))
}

// ---- Housekeeping ----

const retention = 30 * 24 * time.Hour

// Prune deletes old rows: sent and skipped outbox rows older than 30 days, all
// outbox rows of topics closed more than 30 days ago, and inbound and alert
// rows older than 30 days. Rows exactly 30 days old stay.
func (s *Store) Prune(ctx context.Context, now time.Time) error {
	cut := now.Add(-retention).UnixMilli()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		const doomed = `SELECT id FROM outbox WHERE (state IN ('sent','skipped') AND created_at<?1)
			OR thread_id IN (SELECT id FROM thread WHERE status='closed' AND closed_at IS NOT NULL AND closed_at<?1)`
		stmts := []string{
			`DELETE FROM outbox_part WHERE outbox_id IN (` + doomed + `)`,
			`DELETE FROM outbox WHERE id IN (` + doomed + `)`,
			`DELETE FROM inbound WHERE created_at<?1`,
			`DELETE FROM alert WHERE created_at<?1`,
		}
		for _, q := range stmts {
			if _, err := tx.ExecContext(ctx, q, cut); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: prune: %w", err)
	}
	return nil
}
