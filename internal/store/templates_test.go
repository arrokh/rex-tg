package store

import (
	"errors"
	"fmt"
	"path/filepath"
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

func TestMigrateV4ToV5AddsTemplateTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := open(path, migrations[:4])
	noErr(t, err)
	noErr(t, s.Close())

	s, err = Open(path)
	noErr(t, err)
	defer s.Close()
	if v := must(s.version(ctx)); v != 5 {
		t.Fatalf("version = %d", v)
	}
	if err := s.SaveTemplate(ctx, testTemplate(0)); err != nil {
		t.Fatalf("template table missing after migration: %v", err)
	}
}
