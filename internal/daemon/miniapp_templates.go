package daemon

import (
	"context"
	"errors"

	"rex-tg/internal/miniapp"
	"rex-tg/internal/store"
)

type miniAppTemplates struct{ st *store.Store }

func (m miniAppTemplates) List(ctx context.Context) ([]miniapp.Template, error) {
	stored, err := m.st.Templates(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]miniapp.Template, 0, len(stored))
	for _, t := range stored {
		items = append(items, miniapp.Template{ID: t.ID, Name: t.Name, Text: t.Text, Updated: t.Updated})
	}
	return items, nil
}

func (m miniAppTemplates) Save(ctx context.Context, t miniapp.Template) error {
	err := m.st.SaveTemplate(ctx, store.Template{ID: t.ID, Name: t.Name, Text: t.Text, Updated: t.Updated})
	if errors.Is(err, store.ErrTemplateLimit) {
		return miniapp.ErrTemplateLimit
	}
	if errors.Is(err, store.ErrTemplateDeleted) {
		return miniapp.ErrTemplateDeleted
	}
	return err
}

func (m miniAppTemplates) Import(ctx context.Context, items []miniapp.Template) (int, []string, error) {
	stored := make([]store.Template, 0, len(items))
	for _, t := range items {
		stored = append(stored, store.Template{ID: t.ID, Name: t.Name, Text: t.Text, Updated: t.Updated})
	}
	return m.st.ImportTemplates(ctx, stored)
}

func (m miniAppTemplates) Reorder(ctx context.Context, expected, ids []string) error {
	err := m.st.ReorderTemplates(ctx, expected, ids)
	if errors.Is(err, store.ErrTemplateOrderChanged) {
		return miniapp.ErrTemplateOrderChanged
	}
	return err
}

func (m miniAppTemplates) Delete(ctx context.Context, id string) error {
	return m.st.DeleteTemplate(ctx, id)
}
