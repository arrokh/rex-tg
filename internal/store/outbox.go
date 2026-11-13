package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ---- Reading transcripts ----

// Cursor returns the saved read position of a transcript.
func (s *Store) Cursor(ctx context.Context, transcript string) (Cursor, bool, error) {
	var c Cursor
	var last, upd sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT transcript, dev_ino, offset, last_event_at, updated_at FROM cursor WHERE transcript=?`,
		transcript).Scan(&c.Transcript, &c.DevIno, &c.Offset, &last, &upd)
	if errors.Is(err, sql.ErrNoRows) {
		return Cursor{}, false, nil
	}
	if err != nil {
		return Cursor{}, false, fmt.Errorf("store: cursor: %w", err)
	}
	c.LastEventAt, c.UpdatedAt = fromMs(last), fromMs(upd)
	return c, true, nil
}

func saveCursor(ctx context.Context, q querier, c Cursor) error {
	_, err := q.ExecContext(ctx, `INSERT INTO cursor(transcript,dev_ino,offset,last_event_at,updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(transcript) DO UPDATE SET dev_ino=excluded.dev_ino, offset=excluded.offset,
		  last_event_at=excluded.last_event_at, updated_at=excluded.updated_at`,
		c.Transcript, c.DevIno, c.Offset, toMs(c.LastEventAt), toMsNN(c.UpdatedAt))
	if err != nil {
		return fmt.Errorf("store: save cursor: %w", err)
	}
	return nil
}

// SaveCursor stores a transcript's read position.
func (s *Store) SaveCursor(ctx context.Context, c Cursor) error { return saveCursor(ctx, s.db, c) }

// CommitBatch inserts the items (ignoring keys the topic already has) and saves
// the cursor in one transaction. It returns the number of new rows.
func (s *Store) CommitBatch(ctx context.Context, c Cursor, items []OutboxItem) (int, error) {
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
		return saveCursor(ctx, tx, c)
	})
	if err != nil {
		return 0, err
	}
	return inserted, nil
}

// ---- Outbox ----

func insertItem(ctx context.Context, tx *sql.Tx, it OutboxItem, now time.Time) (int64, bool, error) {
	if !it.Skip && len(it.Parts) == 0 {
		return 0, false, fmt.Errorf("store: outbox item %q has no parts", it.Key)
	}
	state := OutboxPending
	if it.Skip {
		state = OutboxSkipped
	}
	var body strings.Builder
	for i, p := range it.Parts {
		if i > 0 {
			body.WriteString("\n")
		}
		body.WriteString(p.Content)
	}
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO outbox(thread_id,event_key,kind,body,state,silent,created_at)
		VALUES(?,?,?,?,?,?,?)`, it.ThreadID, it.Key, it.Kind, body.String(), state, b2i(it.Silent), toMsNN(now))
	if err != nil {
		return 0, false, fmt.Errorf("store: insert outbox: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, false, err
	}
	if n == 0 {
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM outbox WHERE thread_id=? AND event_key=?`, it.ThreadID, it.Key).Scan(&id)
		if err != nil {
			return 0, false, fmt.Errorf("store: find outbox row: %w", err)
		}
		return id, false, nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, false, err
	}
	if it.Skip {
		return id, true, nil
	}
	for i, p := range it.Parts {
		_, err := tx.ExecContext(ctx, `INSERT INTO outbox_part(outbox_id,part,state,content,format,filename) VALUES(?,?,?,?,?,?)`,
			id, i, OutboxPending, p.Content, p.Format, nullStr(p.Filename))
		if err != nil {
			return 0, false, fmt.Errorf("store: insert outbox part: %w", err)
		}
	}
	return id, true, nil
}

// Enqueue inserts one outgoing message. When the topic already has the key it
// inserts nothing and returns the existing ID with inserted false.
func (s *Store) Enqueue(ctx context.Context, item OutboxItem) (int64, bool, error) {
	var id int64
	var ok bool
	now := s.now()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		id, ok, err = insertItem(ctx, tx, item, now)
		return err
	})
	if err != nil {
		return 0, false, err
	}
	return id, ok, nil
}

const partCols = `p.outbox_id, p.part, o.thread_id, t.chat_id, t.message_thread_id, o.kind, o.event_key,
	p.content, p.format, p.filename, o.silent, p.state, p.tg_message_id, p.floor_id, p.checked`

const partFrom = ` FROM outbox_part p JOIN outbox o ON o.id=p.outbox_id JOIN thread t ON t.id=o.thread_id `

func scanPart(r scanner) (Part, error) {
	var p Part
	var fn sql.NullString
	var silent int
	var tg, floor sql.NullInt64
	var checked int
	err := r.Scan(&p.OutboxID, &p.Part, &p.ThreadID, &p.ChatID, &p.MessageThreadID, &p.Kind, &p.Key,
		&p.Content, &p.Format, &fn, &silent, &p.State, &tg, &floor, &checked)
	if err != nil {
		return Part{}, err
	}
	p.Filename, p.Silent, p.TGMessageID = fn.String, silent != 0, tg.Int64
	p.Floor, p.HasFloor, p.Checked = floor.Int64, floor.Valid, checked != 0
	return p, nil
}

func queryParts(ctx context.Context, q querier, where string, args ...any) ([]Part, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+partCols+partFrom+where, args...)
	if err != nil {
		return nil, fmt.Errorf("store: query parts: %w", err)
	}
	defer rows.Close()
	var out []Part
	for rows.Next() {
		p, err := scanPart(rows)
		if err != nil {
			return nil, fmt.Errorf("store: query parts: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// NextParts returns at most one part per topic: the oldest pending part of a
// topic that has no part in flight (sending). Rows that are unknown, failed,
// skipped, or sent do not block later rows. Closed topics are left out.
func (s *Store) NextParts(ctx context.Context) ([]Part, error) {
	return queryParts(ctx, s.db, `WHERE p.state='pending' AND t.status<>'closed'
		AND NOT EXISTS (SELECT 1 FROM outbox_part p2 JOIN outbox o2 ON o2.id=p2.outbox_id
			WHERE o2.thread_id=o.thread_id AND p2.state='sending')
		AND NOT EXISTS (SELECT 1 FROM outbox_part p3 JOIN outbox o3 ON o3.id=p3.outbox_id
			WHERE o3.thread_id=o.thread_id AND p3.state='pending'
			  AND (o3.id<o.id OR (o3.id=o.id AND p3.part<p.part)))
		ORDER BY o.id, p.part`)
}

// recompute derives the row state from its parts.
func recompute(ctx context.Context, tx *sql.Tx, outboxID int64, now time.Time) error {
	counts := map[string]int{}
	rows, err := tx.QueryContext(ctx, `SELECT state, count(*) FROM outbox_part WHERE outbox_id=? GROUP BY state`, outboxID)
	if err != nil {
		return fmt.Errorf("store: recompute: %w", err)
	}
	total := 0
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			_ = rows.Close()
			return err
		}
		counts[st] = n
		total += n
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if total == 0 {
		return nil
	}
	state := OutboxPending
	switch {
	case counts[OutboxSent] == total:
		state = OutboxSent
	case counts[OutboxUnknown] > 0:
		state = OutboxUnknown
	case counts[OutboxFailed] > 0:
		state = OutboxFailed
	}
	_, err = tx.ExecContext(ctx, `UPDATE outbox SET state=?1,
		sent_at = CASE WHEN ?1='sent' THEN COALESCE(sent_at, ?2) ELSE sent_at END
		WHERE id=?3 AND state<>'skipped'`, state, toMsNN(now), outboxID)
	if err != nil {
		return fmt.Errorf("store: recompute: %w", err)
	}
	return nil
}

// setPart changes one part (from any state in `from`, or any when empty) and
// recomputes its row. It reports whether a part changed.
func (s *Store) setPart(ctx context.Context, outboxID int64, part int, set string, args []any, from string, rowExtra string, rowArgs ...any) (bool, error) {
	changed := false
	now := s.now()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		q := `UPDATE outbox_part SET ` + set + ` WHERE outbox_id=? AND part=?`
		a := append(append([]any{}, args...), outboxID, part)
		if from != "" {
			q += ` AND state=?`
			a = append(a, from)
		}
		res, err := tx.ExecContext(ctx, q, a...)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		changed = true
		if rowExtra != "" {
			if _, err := tx.ExecContext(ctx, rowExtra, append(append([]any{}, rowArgs...), outboxID)...); err != nil {
				return err
			}
		}
		return recompute(ctx, tx, outboxID, now)
	})
	if err != nil {
		return false, fmt.Errorf("store: update part: %w", err)
	}
	return changed, nil
}

func (s *Store) mustSetPart(ctx context.Context, outboxID int64, part int, set string, args []any, from, rowExtra string, rowArgs ...any) error {
	ok, err := s.setPart(ctx, outboxID, part, set, args, from, rowExtra, rowArgs...)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("store: part %d/%d: %w", outboxID, part, ErrNotFound)
	}
	return nil
}

// ClaimPart moves a part from pending to sending. It returns false when the
// part is not pending (someone else claimed it). floor is the newest message
// number known in the part's chat right before the send; a negative floor
// means none is known.
func (s *Store) ClaimPart(ctx context.Context, outboxID int64, part int, floor int64) (bool, error) {
	var f any
	if floor >= 0 {
		f = floor
	}
	return s.setPart(ctx, outboxID, part, `state='sending', floor_id=?, checked=0`, []any{f}, OutboxPending,
		`UPDATE outbox SET attempts=attempts+1 WHERE id=?`)
}

// MarkPartSent records that Telegram accepted a part.
func (s *Store) MarkPartSent(ctx context.Context, outboxID int64, part int, tgMessageID int64) error {
	return s.mustSetPart(ctx, outboxID, part, `state='sent', tg_message_id=?`, []any{nullInt(tgMessageID)}, "", "")
}

// ReleasePart moves a sending part back to pending.
func (s *Store) ReleasePart(ctx context.Context, outboxID int64, part int) error {
	return s.mustSetPart(ctx, outboxID, part, `state='pending'`, nil, OutboxSending, "")
}

// MarkPartUnknown records that a part may or may not have been sent.
func (s *Store) MarkPartUnknown(ctx context.Context, outboxID int64, part int) error {
	return s.mustSetPart(ctx, outboxID, part, `state='unknown'`, nil, "", "")
}

// MarkPartFailed records a permanent failure and its reason on the row.
func (s *Store) MarkPartFailed(ctx context.Context, outboxID int64, part int, reason string) error {
	return s.mustSetPart(ctx, outboxID, part, `state='failed'`, nil, "",
		`UPDATE outbox SET last_error=? WHERE id=?`, reason)
}

// NewestUnknown returns the newest item of a topic that has a part whose
// outcome is unknown, not counting notices. ok is false when there is none.
func (s *Store) NewestUnknown(ctx context.Context, threadID int64) (id int64, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT o.id FROM outbox o JOIN outbox_part p ON p.outbox_id=o.id
		WHERE o.thread_id=? AND p.state='unknown' AND o.kind NOT IN ('notice','part-notice')
		ORDER BY o.id DESC LIMIT 1`, threadID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("store: newest unknown: %w", err)
	}
	return id, true, nil
}

// ResendUnknown moves the unknown parts of a row back to pending and returns how many.
func (s *Store) ResendUnknown(ctx context.Context, outboxID int64) (int, error) {
	n := 0
	now := s.now()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE outbox_part SET state='pending', checked=0 WHERE outbox_id=? AND state='unknown'`, outboxID)
		if err != nil {
			return err
		}
		c, err := res.RowsAffected()
		if err != nil {
			return err
		}
		n = int(c)
		return recompute(ctx, tx, outboxID, now)
	})
	if err != nil {
		return 0, fmt.Errorf("store: resend unknown: %w", err)
	}
	return n, nil
}

// RecoverSending moves every sending part to unknown (at start) and returns them.
func (s *Store) RecoverSending(ctx context.Context) ([]Part, error) {
	var out []Part
	now := s.now()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		parts, err := queryParts(ctx, tx, `WHERE p.state='sending' ORDER BY p.outbox_id, p.part`)
		if err != nil {
			return err
		}
		seen := map[int64]bool{}
		for i := range parts {
			p := &parts[i]
			if _, err := tx.ExecContext(ctx, `UPDATE outbox_part SET state='unknown' WHERE outbox_id=? AND part=?`, p.OutboxID, p.Part); err != nil {
				return err
			}
			p.State = OutboxUnknown
			if !seen[p.OutboxID] {
				seen[p.OutboxID] = true
				if err := recompute(ctx, tx, p.OutboxID, now); err != nil {
					return err
				}
			}
		}
		out = parts
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: recover sending: %w", err)
	}
	return out, nil
}

// OutboxRow returns a row with its parts.
func (s *Store) OutboxRow(ctx context.Context, id int64) (OutboxRow, error) {
	var r OutboxRow
	var silent int
	var lastErr sql.NullString
	var created, sent sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, thread_id, event_key, kind, body, state, silent, attempts, last_error, created_at, sent_at
		FROM outbox WHERE id=?`, id).Scan(&r.ID, &r.ThreadID, &r.Key, &r.Kind, &r.Body, &r.State, &silent, &r.Attempts, &lastErr, &created, &sent)
	if err != nil {
		return OutboxRow{}, notFound(err, fmt.Sprintf("store: outbox row %d", id))
	}
	r.Silent, r.LastError, r.CreatedAt, r.SentAt = silent != 0, lastErr.String, fromMs(created), fromMs(sent)
	r.Parts, err = queryParts(ctx, s.db, `WHERE p.outbox_id=? ORDER BY p.part`, id)
	if err != nil {
		return OutboxRow{}, err
	}
	return r, nil
}

// SentKind returns the kind of the outbox row of a topic that Telegram
// message msg is a part of. ok is false when rex-tg sent no such part there.
func (s *Store) SentKind(ctx context.Context, threadID, msg int64) (kind string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT o.kind FROM outbox_part p JOIN outbox o ON o.id = p.outbox_id
		WHERE o.thread_id=? AND p.tg_message_id=? LIMIT 1`, threadID, msg).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: sent kind: %w", err)
	}
	return kind, true, nil
}

// ResendPart moves one unknown part back to pending. It reports false when
// the part is not unknown any more (for example, /resend came first).
func (s *Store) ResendPart(ctx context.Context, outboxID int64, part int) (bool, error) {
	return s.setPart(ctx, outboxID, part, `state='pending', checked=0`, nil, OutboxUnknown, "")
}

// MarkPartChecked records that the arrival of an unknown part was checked,
// so it is not checked again; it waits for /resend.
func (s *Store) MarkPartChecked(ctx context.Context, outboxID int64, part int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE outbox_part SET checked=1 WHERE outbox_id=? AND part=?`, outboxID, part)
	if err != nil {
		return fmt.Errorf("store: mark part checked: %w", err)
	}
	return nil
}

// PartsToCheck returns the unknown parts whose arrival can still be
// checked: not checked yet, and with a known floor. Oldest first.
func (s *Store) PartsToCheck(ctx context.Context) ([]Part, error) {
	return queryParts(ctx, s.db, `WHERE p.state='unknown' AND p.checked=0 AND p.floor_id IS NOT NULL ORDER BY o.id, p.part`)
}

// Part returns one outbox part.
func (s *Store) Part(ctx context.Context, outboxID int64, part int) (Part, error) {
	ps, err := queryParts(ctx, s.db, `WHERE p.outbox_id=? AND p.part=?`, outboxID, part)
	if err != nil {
		return Part{}, err
	}
	if len(ps) == 0 {
		return Part{}, fmt.Errorf("store: part %d/%d: %w", outboxID, part, ErrNotFound)
	}
	return ps[0], nil
}

// ---- Message numbers ----

// NoteMessage records that a message with this number exists in a chat:
// one that rex-tg sent, or one that an update brought.
func (s *Store) NoteMessage(ctx context.Context, chatID, messageID int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO seen_message(chat_id,message_id,seen_at) VALUES(?,?,?)`,
		chatID, messageID, toMsNN(at))
	if err != nil {
		return fmt.Errorf("store: note message: %w", err)
	}
	return nil
}

// MaxMessage returns the newest message number seen in a chat; ok is false
// when none was seen.
func (s *Store) MaxMessage(ctx context.Context, chatID int64) (id int64, ok bool, err error) {
	var v sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(message_id) FROM seen_message WHERE chat_id=?`, chatID).Scan(&v); err != nil {
		return 0, false, fmt.Errorf("store: newest message: %w", err)
	}
	return v.Int64, v.Valid, nil
}

// UnseenBetween counts the message numbers strictly between lo and hi that
// rex-tg has not seen in a chat. Telegram numbers a private chat's messages
// in order, both ways, so a part sent after lo that arrived has a number
// there: when every number there is seen, the part did not arrive.
func (s *Store) UnseenBetween(ctx context.Context, chatID, lo, hi int64) (int64, error) {
	if hi-lo <= 1 {
		return 0, nil
	}
	var seen int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM seen_message WHERE chat_id=? AND message_id>? AND message_id<?`,
		chatID, lo, hi).Scan(&seen)
	if err != nil {
		return 0, fmt.Errorf("store: unseen messages: %w", err)
	}
	return hi - lo - 1 - seen, nil
}

// UnknownParts returns every outbox part in state unknown, oldest first.
func (s *Store) UnknownParts(ctx context.Context) ([]Part, error) {
	return queryParts(ctx, s.db, `WHERE p.state='unknown' ORDER BY o.id, p.part`)
}
