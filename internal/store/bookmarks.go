package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const bookmarkCols = `id, chat_id, message_id, message_thread_id, source_topic_name,
	message_date, saved_at, title, full_text, text_format, content_kind, content_label`

func scanBookmark(r scanner) (Bookmark, error) {
	var b Bookmark
	var messageDate, savedAt int64
	if err := r.Scan(&b.ID, &b.ChatID, &b.MessageID, &b.MessageThreadID, &b.SourceTopicName,
		&messageDate, &savedAt, &b.Title, &b.FullText, &b.TextFormat, &b.ContentKind, &b.ContentLabel); err != nil {
		return Bookmark{}, err
	}
	b.MessageDate = fromMs(sql.NullInt64{Int64: messageDate, Valid: true})
	b.SavedAt = fromMs(sql.NullInt64{Int64: savedAt, Valid: true})
	return b, nil
}

func insertBookmark(ctx context.Context, q querier, b Bookmark, now time.Time) (bool, error) {
	if b.TextFormat == "" {
		b.TextFormat = BookmarkTextPlain
	}
	if b.TextFormat != BookmarkTextPlain && b.TextFormat != BookmarkTextMarkdown {
		return false, fmt.Errorf("store: invalid bookmark text format %q", b.TextFormat)
	}
	savedAt := b.SavedAt
	if savedAt.IsZero() {
		savedAt = now
	}
	res, err := q.ExecContext(ctx, `INSERT OR IGNORE INTO bookmark(`+bookmarkCols+`)
		VALUES(NULL,?,?,?,?,?,?,?,?,?,?,?)`, b.ChatID, b.MessageID, b.MessageThreadID, b.SourceTopicName,
		toMsNN(b.MessageDate), toMsNN(savedAt), b.Title, b.FullText, b.TextFormat, b.ContentKind, b.ContentLabel)
	if err != nil {
		return false, fmt.Errorf("store: insert bookmark: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// InsertBookmarkWithInbound atomically stores an inbound command or callback
// update and its bookmark. Each insert is idempotent independently: updateInserted
// reports a new update_id, and bookmarkCreated reports a new (chat_id,
// message_id) identity. A zero bookmark SavedAt uses the store clock.
func (s *Store) InsertBookmarkWithInbound(ctx context.Context, in Inbound, bookmark Bookmark) (updateInserted, bookmarkCreated bool, err error) {
	if in.ChatID != bookmark.ChatID {
		return false, false, fmt.Errorf("store: inbound chat %d does not match bookmark chat %d", in.ChatID, bookmark.ChatID)
	}
	now := s.now()
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		updateInserted, err = insertInbound(ctx, tx, in)
		if err != nil {
			return err
		}
		bookmarkCreated, err = insertBookmark(ctx, tx, bookmark, now)
		return err
	})
	if err != nil {
		return false, false, err
	}
	return updateInserted, bookmarkCreated, nil
}

// ListBookmarks returns a chat's bookmarks newest-first. sourceMessageThreadID
// filters by the source topic when non-nil (0 selects the main chat); beforeID
// selects rows whose bookmark ID is lower, and a non-positive value starts at
// the newest row. A non-positive limit returns an empty list.
func (s *Store) ListBookmarks(ctx context.Context, chatID int64, sourceMessageThreadID *int64, beforeID int64, limit int) ([]Bookmark, error) {
	if limit <= 0 {
		return []Bookmark{}, nil
	}
	query := `SELECT ` + bookmarkCols + ` FROM bookmark WHERE chat_id=?`
	args := []any{chatID}
	if sourceMessageThreadID != nil {
		query += ` AND message_thread_id=?`
		args = append(args, *sourceMessageThreadID)
	}
	if beforeID > 0 {
		query += ` AND id<?`
		args = append(args, beforeID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list bookmarks: %w", err)
	}
	defer rows.Close()
	var out []Bookmark
	for rows.Next() {
		b, err := scanBookmark(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list bookmarks: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BookmarkTopics lists distinct source topics represented in a chat's
// bookmarks, using the display name from the newest saved bookmark per topic.
func (s *Store) BookmarkTopics(ctx context.Context, chatID int64) ([]BookmarkTopic, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT b.message_thread_id, b.source_topic_name
		FROM bookmark b
		WHERE b.chat_id=? AND b.id=(
			SELECT MAX(newest.id) FROM bookmark newest
			WHERE newest.chat_id=b.chat_id AND newest.message_thread_id=b.message_thread_id
		)
		ORDER BY b.message_thread_id`, chatID)
	if err != nil {
		return nil, fmt.Errorf("store: bookmark topics: %w", err)
	}
	defer rows.Close()
	var out []BookmarkTopic
	for rows.Next() {
		var topic BookmarkTopic
		if err := rows.Scan(&topic.MessageThreadID, &topic.Name); err != nil {
			return nil, fmt.Errorf("store: bookmark topics: %w", err)
		}
		out = append(out, topic)
	}
	return out, rows.Err()
}

// Bookmark returns the saved message with this database ID.
func (s *Store) Bookmark(ctx context.Context, id int64) (Bookmark, error) {
	b, err := scanBookmark(s.db.QueryRowContext(ctx, `SELECT `+bookmarkCols+` FROM bookmark WHERE id=?`, id))
	if err != nil {
		return Bookmark{}, notFound(err, fmt.Sprintf("store: bookmark %d", id))
	}
	return b, nil
}
