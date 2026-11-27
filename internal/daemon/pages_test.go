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
	oldScreen := save(miniapp.KindScreen, 8*24*time.Hour) // since its last use
	youngFiles := save(miniapp.KindFiles, 6*24*time.Hour)
	_ = st.Set(d.ctx, pageForKey(1, miniapp.KindDiff, "k"), oldDiff+"|1 file · +1 −0")
	clock = time.Unix(1_800_000_000, 0)
	d.prunePages(clock)
	for id, want := range map[string]bool{oldAnswer: false, youngAnswer: true, oldDiff: false, oldScreen: false, youngFiles: true} {
		if _, ok := loadPage(st, id); ok != want {
			t.Errorf("page %s kept = %v, want %v", id, ok, want)
		}
	}
	if _, ok, _ := st.Get(d.ctx, pageForKey(1, miniapp.KindDiff, "k")); ok {
		t.Error("the name of a gone page is left")
	}
}

// Mini App and Bookmark buttons each have their own row; quick replies come
// two to a row: on a phone, longer rows cut the labels.
func TestAnswerButtonsRows(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_ = st.Set(ctx, pageForKey(1, miniapp.KindAnswer, "k"), "a1")
	_ = st.Set(ctx, pageForKey(1, miniapp.KindDiff, "k"), "d1|2 files · +40 −12")
	m := answerButtons(st, []string{"Continue", "Yes", "Commit"}, "https://mac.example:8443", 1, "k", 42)
	var got []string
	for _, row := range m.InlineKeyboard {
		var labels []string
		for _, b := range row {
			labels = append(labels, b.Text)
		}
		got = append(got, strings.Join(labels, "|"))
	}
	want := []string{"📄 Whole answer", "📝 2 files · +40 −12", "🔖 Bookmark", "Continue|Yes", "Commit"}
	if strings.Join(got, " / ") != strings.Join(want, " / ") {
		t.Errorf("rows = %q, want %q", got, want)
	}
	if m.InlineKeyboard[2][0].CallbackData != "bm:42" {
		t.Errorf("bookmark callback = %q, want bm:42", m.InlineKeyboard[2][0].CallbackData)
	}
}

func TestLongAnswerDocumentGetsBookmarkButton(t *testing.T) {
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
	id, _, err := st.Enqueue(ctx, store.OutboxItem{ThreadID: th.ID, Key: "long:k", Kind: "answer", Parts: []store.PartContent{{Content: "# Complete answer", Format: store.FormatDocument, Filename: "answer.md"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkPartSent(ctx, id, 0, 500); err != nil {
		t.Fatal(err)
	}
	markup := answerMarkup(st, nil, "")(store.Part{
		OutboxID: id, Part: 0, ThreadID: th.ID, Key: "long:k", Kind: "answer",
	})
	if markup == nil || len(markup.InlineKeyboard) != 1 || markup.InlineKeyboard[0][0].CallbackData != fmt.Sprintf("%s:%d", bookmarkCallback, id) {
		t.Fatalf("long answer keyboard = %#v, want Bookmark button", markup)
	}
}

func TestBackfillAnswerGetsOnlyBookmarkButton(t *testing.T) {
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
	id, _, err := st.Enqueue(ctx, store.OutboxItem{ThreadID: th.ID, Key: "backfill:k", Kind: "backfill", Parts: []store.PartContent{{Content: "A missed answer", Format: store.FormatPlain}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkPartSent(ctx, id, 0, 501); err != nil {
		t.Fatal(err)
	}
	markup := answerMarkup(st, []string{"Yes"}, "https://mac.example:8443")(store.Part{
		OutboxID: id, Part: 0, ThreadID: th.ID, Key: "backfill:k", Kind: "backfill",
	})
	if markup == nil || len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 1 {
		t.Fatalf("backfill keyboard = %#v, want one Bookmark button", markup)
	}
	button := markup.InlineKeyboard[0][0]
	if button.Text != "🔖 Bookmark" || button.CallbackData != fmt.Sprintf("%s:%d", bookmarkCallback, id) {
		t.Errorf("backfill button = %#v", button)
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
