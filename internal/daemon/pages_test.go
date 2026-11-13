package daemon

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/miniapp"
	"rex-tg/internal/store"
)

// The cleanup reads the small index, drops pages past their age (changes
// sooner than answers), keeps at most maxPages, and forgets the names of
// pages that are gone.
func TestPrunePages(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	clock := time.Unix(1_800_000_000, 0)
	d := &daemon{ctx: context.Background(), st: st, now: func() time.Time { return clock }, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	save := func(kind string, age time.Duration) string {
		clock = time.Unix(1_800_000_000, 0).Add(-age)
		id, err := d.savePage(kind, "t", "x")
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	oldAnswer := save(miniapp.KindAnswer, 31*24*time.Hour)
	youngAnswer := save(miniapp.KindAnswer, 20*24*time.Hour)
	oldDiff := save(miniapp.KindDiff, 15*24*time.Hour)
	_ = st.Set(d.ctx, pageForKey(1, miniapp.KindDiff, "k"), oldDiff+"|1 file · +1 −0")
	clock = time.Unix(1_800_000_000, 0)
	d.prunePages(clock)
	for id, want := range map[string]bool{oldAnswer: false, youngAnswer: true, oldDiff: false} {
		if _, ok := loadPage(st, id); ok != want {
			t.Errorf("page %s kept = %v, want %v", id, ok, want)
		}
	}
	if _, ok, _ := st.Get(d.ctx, pageForKey(1, miniapp.KindDiff, "k")); ok {
		t.Error("the name of a gone page is left")
	}
}

// Each Mini App button has its own row, then the quick replies come two to
// a row: on a phone, longer rows cut the labels.
func TestAnswerButtonsRows(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_ = st.Set(ctx, pageForKey(1, miniapp.KindAnswer, "k"), "a1")
	_ = st.Set(ctx, pageForKey(1, miniapp.KindDiff, "k"), "d1|2 files · +40 −12")
	m := answerButtons(st, []string{"Continue", "Yes", "Commit"}, "https://mac.example:8443", 1, "k")
	var got []string
	for _, row := range m.InlineKeyboard {
		var labels []string
		for _, b := range row {
			labels = append(labels, b.Text)
		}
		got = append(got, strings.Join(labels, "|"))
	}
	want := []string{"📄 Whole answer", "📝 2 files · +40 −12", "Continue|Yes", "Commit"}
	if strings.Join(got, " / ") != strings.Join(want, " / ") {
		t.Errorf("rows = %q, want %q", got, want)
	}
}
