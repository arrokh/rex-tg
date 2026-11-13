package daemon

import (
	"context"
	"fmt"
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

// sentText quotes what rex-tg sent in a message, by its kind: a rich answer
// and an answer as a .md file as plain text, HTML without tags, a code file
// as it is, and nothing for an image (its content is a file path).
func TestSentTextByKind(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	c, err := st.UpsertConversation(ctx, store.Conversation{Runtime: "claude", NativeID: "s1", Transcript: "/tmp/s1.jsonl", FirstSeenAt: time.Unix(1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	th, err := st.LinkTopic(ctx, 100, 7, c.ID, "topic", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	d := &daemon{ctx: ctx, st: st, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	cases := []struct {
		part store.PartContent
		want string
	}{
		{store.PartContent{Content: "My plan: **three PRs**.", Format: store.FormatRich}, "My plan: three PRs."},
		{store.PartContent{Content: "<b>Done</b> &amp; tested", Format: store.FormatHTML}, "Done & tested"},
		{store.PartContent{Content: "# Report\n\nAll **good**.", Format: store.FormatDocument, Filename: "answer.md"}, "Report\n\nAll good."},
		{store.PartContent{Content: "func main() {}\n", Format: store.FormatDocument, Filename: "code-1.go"}, "func main() {}\n"},
		{store.PartContent{Content: "/Users/x/shot.png", Format: store.FormatImage, Filename: "shot.png"}, ""},
	}
	for i, c := range cases {
		id, _, err := st.Enqueue(ctx, store.OutboxItem{ThreadID: th.ID, Key: fmt.Sprintf("k%d", i), Kind: "answer", Parts: []store.PartContent{c.part}})
		if err != nil {
			t.Fatal(err)
		}
		msg := int64(500 + i)
		if err := st.MarkPartSent(ctx, id, 0, msg); err != nil {
			t.Fatal(err)
		}
		if got := d.sentText(th.ID, msg); got != c.want {
			t.Errorf("%s %s: sentText = %q, want %q", c.part.Format, c.part.Filename, got, c.want)
		}
	}
	if got := d.sentText(th.ID, 999); got != "" {
		t.Errorf("a message rex-tg did not send: %q", got)
	}
}
