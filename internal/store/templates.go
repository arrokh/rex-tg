package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// Template is one reusable message saved by the paired owner.
type Template struct {
	ID      string
	Name    string
	Text    string
	Updated int64 // Unix milliseconds
}

const MaxTemplates = 50

// ErrTemplateLimit means a new template would exceed MaxTemplates.
var ErrTemplateLimit = errors.New("store: template limit reached")

// ErrTemplateDeleted means a stale client tried to restore a deleted template.
var ErrTemplateDeleted = errors.New("store: template was deleted")

// ErrTemplateOrderChanged means the submitted IDs no longer match the shared list.
var ErrTemplateOrderChanged = errors.New("store: template list changed")

// Templates returns saved templates in their shared manual order.
func (s *Store) Templates(ctx context.Context) ([]Template, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, text, updated_at FROM template ORDER BY position, updated_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list templates: %w", err)
	}
	defer rows.Close()
	var out []Template
	for rows.Next() {
		var t Template
		if err := rows.Scan(&t.ID, &t.Name, &t.Text, &t.Updated); err != nil {
			return nil, fmt.Errorf("store: list templates: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list templates: %w", err)
	}
	return out, nil
}

// SaveTemplate inserts or updates one template, without exceeding the limit.
func (s *Store) SaveTemplate(ctx context.Context, t Template) error {
	if err := checkTemplate(t); err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		var deleted int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM template_deleted WHERE id=?`, t.ID).Scan(&deleted); err != nil {
			return err
		}
		if deleted != 0 {
			return ErrTemplateDeleted
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM template WHERE id=?`, t.ID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM template`).Scan(&count); err != nil {
				return err
			}
			if count >= MaxTemplates {
				return ErrTemplateLimit
			}
			if _, err := tx.ExecContext(ctx, `UPDATE template SET position=position+1`); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO template(id, name, text, updated_at, position) VALUES(?, ?, ?, ?, 0)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, text=excluded.text, updated_at=excluded.updated_at`,
			t.ID, t.Name, t.Text, t.Updated)
		return err
	})
}

// ImportTemplates adds legacy device templates that are not already stored.
// It never overwrites a newer shared copy and skips entries after the limit.
func (s *Store) ImportTemplates(ctx context.Context, templates []Template) (int, []string, error) {
	for _, t := range templates {
		if err := checkTemplate(t); err != nil {
			return 0, nil, err
		}
	}
	imported := 0
	deleted := make([]string, 0)
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM template`).Scan(&count); err != nil {
			return err
		}
		var position int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), -1) + 1 FROM template`).Scan(&position); err != nil {
			return err
		}
		seen := make(map[string]bool, len(templates))
		for _, t := range templates {
			if seen[t.ID] {
				continue
			}
			seen[t.ID] = true
			var exists int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM template WHERE id=?`, t.ID).Scan(&exists); err != nil {
				return err
			}
			if exists != 0 {
				continue
			}
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM template_deleted WHERE id=?`, t.ID).Scan(&exists); err != nil {
				return err
			}
			if exists != 0 {
				deleted = append(deleted, t.ID)
				continue
			}
			if count >= MaxTemplates {
				continue
			}
			res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO template(id, name, text, updated_at, position) VALUES(?, ?, ?, ?, ?)`,
				t.ID, t.Name, t.Text, t.Updated, position)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n != 0 {
				count++
				imported++
				position++
			}
		}
		return nil
	})
	if err != nil {
		return 0, nil, fmt.Errorf("store: import templates: %w", err)
	}
	return imported, deleted, nil
}

// ReorderTemplates applies an exact permutation if the expected order is current.
func (s *Store) ReorderTemplates(ctx context.Context, expected, ids []string) error {
	if len(expected) > MaxTemplates || len(ids) > MaxTemplates || len(expected) != len(ids) {
		return errors.New("store: invalid template order")
	}
	seenExpected := make(map[string]bool, len(expected))
	for _, id := range expected {
		if !validTemplateID(id) || seenExpected[id] {
			return errors.New("store: invalid template order")
		}
		seenExpected[id] = true
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !validTemplateID(id) || seen[id] {
			return errors.New("store: invalid template order")
		}
		seen[id] = true
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM template ORDER BY position, updated_at DESC, id`)
		if err != nil {
			return err
		}
		current := make([]string, 0, len(ids))
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			current = append(current, id)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if !slices.Equal(current, expected) || len(ids) != len(current) {
			return ErrTemplateOrderChanged
		}
		for _, id := range ids {
			if !seenExpected[id] {
				return ErrTemplateOrderChanged
			}
		}
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE template SET position=? WHERE id=?`, i, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteTemplate removes one template. Deleting a missing id is harmless.
func (s *Store) DeleteTemplate(ctx context.Context, id string) error {
	if !validTemplateID(id) {
		return errors.New("store: invalid template id")
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO template_deleted(id, deleted_at) VALUES(?, ?)`, id, s.now().UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM template WHERE id=?`, id); err != nil {
			return err
		}
		return nil
	})
}

func checkTemplate(t Template) error {
	if !validTemplateID(t.ID) || strings.TrimSpace(t.Name) == "" || utf8.RuneCountInString(t.Name) > 80 ||
		strings.TrimSpace(t.Text) == "" || len([]byte(t.Text)) > 16000 || t.Updated <= 0 {
		return errors.New("store: invalid template")
	}
	return nil
}

func validTemplateID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
