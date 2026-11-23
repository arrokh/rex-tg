package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
)

func testTemplate(i int) Template {
	return Template{ID: fmt.Sprintf("%032x", i+1), Name: fmt.Sprintf("template %d", i), Text: fmt.Sprintf("message %d", i), Updated: int64(i + 1)}
}

func TestTemplatesPersistUpdateImportAndDelete(t *testing.T) {
	s := newStore(t)
	first := testTemplate(0)
	noErr(t, s.SaveTemplate(ctx, first))
	updated := first
	updated.Name, updated.Text, updated.Updated = "updated", "new body", 99
	noErr(t, s.SaveTemplate(ctx, updated))

	legacy := testTemplate(1)
	imported, deleted, err := s.ImportTemplates(ctx, []Template{first, legacy})
	noErr(t, err)
	if len(deleted) != 0 {
		t.Fatalf("deleted = %v, want none", deleted)
	}
	if imported != 1 {
		t.Fatalf("imported = %d, want 1", imported)
	}
	got := must(s.Templates(ctx))
	if len(got) != 2 || got[0] != updated || got[1] != legacy {
		t.Fatalf("templates = %+v", got)
	}
	noErr(t, s.DeleteTemplate(ctx, first.ID))
	noErr(t, s.DeleteTemplate(ctx, first.ID))
	got = must(s.Templates(ctx))
	if len(got) != 1 || got[0] != legacy {
		t.Fatalf("after delete = %+v", got)
	}
	if imported, deleted, err := s.ImportTemplates(ctx, []Template{first}); err != nil || imported != 0 || len(deleted) != 1 || deleted[0] != first.ID {
		t.Fatalf("deleted template import = %d, %v, %v", imported, deleted, err)
	}
	if err := s.SaveTemplate(ctx, first); !errors.Is(err, ErrTemplateDeleted) {
		t.Fatalf("saving deleted template = %v", err)
	}
}

func TestTemplateOrderPersistsAndNewTemplatesGoFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	noErr(t, err)
	first, second, third := testTemplate(0), testTemplate(1), testTemplate(2)
	noErr(t, s.SaveTemplate(ctx, first))
	noErr(t, s.SaveTemplate(ctx, second))
	noErr(t, s.SaveTemplate(ctx, third))
	want := []string{third.ID, second.ID, first.ID}
	assertTemplateOrder(t, must(s.Templates(ctx)), want)

	// Editing content changes updated_at, but does not undo a manual order.
	second.Name, second.Updated = "edited", 100
	noErr(t, s.SaveTemplate(ctx, second))
	assertTemplateOrder(t, must(s.Templates(ctx)), want)

	expected := append([]string(nil), want...)
	want = []string{first.ID, third.ID, second.ID}
	noErr(t, s.ReorderTemplates(ctx, expected, want))
	assertTemplateOrder(t, must(s.Templates(ctx)), want)
	newItem := testTemplate(3)
	noErr(t, s.SaveTemplate(ctx, newItem))
	want = append([]string{newItem.ID}, want...)
	assertTemplateOrder(t, must(s.Templates(ctx)), want)
	noErr(t, s.Close())

	s, err = Open(path)
	noErr(t, err)
	defer s.Close()
	assertTemplateOrder(t, must(s.Templates(ctx)), want)
}

func TestReorderTemplatesRequiresExactCurrentIDs(t *testing.T) {
	s := newStore(t)
	first, second := testTemplate(0), testTemplate(1)
	noErr(t, s.SaveTemplate(ctx, first))
	noErr(t, s.SaveTemplate(ctx, second))
	before := must(s.Templates(ctx))
	current := []string{before[0].ID, before[1].ID}
	for _, ids := range [][]string{
		{first.ID},
		{first.ID, first.ID},
		{first.ID, testTemplate(2).ID},
		{"bad", second.ID},
	} {
		if err := s.ReorderTemplates(ctx, current, ids); err == nil {
			t.Errorf("ReorderTemplates(%v) succeeded", ids)
		}
		assertTemplateOrder(t, must(s.Templates(ctx)), current)
	}
	if err := s.ReorderTemplates(ctx, []string{first.ID, second.ID}, current); !errors.Is(err, ErrTemplateOrderChanged) {
		t.Errorf("stale order error = %v, want ErrTemplateOrderChanged", err)
	}
	assertTemplateOrder(t, must(s.Templates(ctx)), current)
}

func TestMigrateV5ToV6BackfillsTemplateOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := open(path, migrations[:5])
	noErr(t, err)
	for _, row := range []struct {
		id      string
		name    string
		updated int64
	}{
		{"00000000000000000000000000000001", "old", 100},
		{"00000000000000000000000000000002", "new", 200},
		{"00000000000000000000000000000003", "tie", 200},
	} {
		_, err := s.db.ExecContext(ctx, `INSERT INTO template(id,name,text,updated_at) VALUES(?,?,?,?)`, row.id, row.name, "text", row.updated)
		noErr(t, err)
	}
	noErr(t, s.Close())

	s, err = Open(path)
	noErr(t, err)
	defer s.Close()
	if v := must(s.version(ctx)); v != latestVersion {
		t.Fatalf("version = %d, want %d", v, latestVersion)
	}
	got := must(s.Templates(ctx))
	want := []string{"00000000000000000000000000000002", "00000000000000000000000000000003", "00000000000000000000000000000001"}
	assertTemplateOrder(t, got, want)
}

func assertTemplateOrder(t *testing.T, templates []Template, want []string) {
	t.Helper()
	got := make([]string, len(templates))
	for i, item := range templates {
		got[i] = item.ID
	}
	if !slices.Equal(got, want) {
		t.Fatalf("template order = %v, want %v", got, want)
	}
}

func TestTemplatesEnforceLimit(t *testing.T) {
	s := newStore(t)
	legacy := make([]Template, MaxTemplates+1)
	for i := range legacy {
		legacy[i] = testTemplate(i)
	}
	imported, deleted, err := s.ImportTemplates(ctx, legacy)
	noErr(t, err)
	if len(deleted) != 0 {
		t.Fatalf("deleted = %v, want none", deleted)
	}
	if imported != MaxTemplates {
		t.Fatalf("imported = %d, want %d", imported, MaxTemplates)
	}
	if got := len(must(s.Templates(ctx))); got != MaxTemplates {
		t.Fatalf("stored %d templates, want %d", got, MaxTemplates)
	}
	if err := s.SaveTemplate(ctx, testTemplate(MaxTemplates)); !errors.Is(err, ErrTemplateLimit) {
		t.Fatalf("save beyond limit error = %v", err)
	}
}

func TestMigrateV4ToLatestAddsTemplateTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := open(path, migrations[:4])
	noErr(t, err)
	noErr(t, s.Close())

	s, err = Open(path)
	noErr(t, err)
	defer s.Close()
	if v := must(s.version(ctx)); v != latestVersion {
		t.Fatalf("version = %d, want %d", v, latestVersion)
	}
	if err := s.SaveTemplate(ctx, testTemplate(0)); err != nil {
		t.Fatalf("template table missing after migration: %v", err)
	}
}
