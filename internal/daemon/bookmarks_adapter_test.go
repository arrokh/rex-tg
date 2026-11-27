package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"rex-tg/internal/miniapp"
	"rex-tg/internal/store"
)

func TestMiniAppBookmarksMapsGlobalAndMainChatFilters(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	base := store.Bookmark{
		ChatID: 100, MessageThreadID: 7, SourceTopicName: "API", MessageDate: time.Unix(100, 0),
		SavedAt: time.Unix(200, 0), Title: "Saved answer", FullText: "# Full answer\n\n**all details**",
		TextFormat: store.BookmarkTextMarkdown, ContentKind: "answer", ContentLabel: "Agent answer",
	}
	for i, topicID := range []int64{0, 7, 7} {
		b := base
		b.MessageID = int64(500 + i)
		b.MessageThreadID = topicID
		if topicID == 0 {
			b.SourceTopicName = "Main chat"
			b.Title = "Main message"
			b.FullText = "Main body"
			b.TextFormat = store.BookmarkTextPlain
		}
		in := store.Inbound{UpdateID: int64(i + 1), ChatID: 100, Kind: "command", CreatedAt: time.Unix(200, 0)}
		if _, _, err := st.InsertBookmarkWithInbound(ctx, in, b); err != nil {
			t.Fatal(err)
		}
	}
	repo := miniAppBookmarks{st: st, chatID: func() int64 { return 100 }}
	all, topics, more, err := repo.List(ctx, -1, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || !more || all[0].Preview != "Full answer all details" {
		t.Fatalf("first page = %+v, more=%v", all, more)
	}
	if len(topics) != 2 || topics[0] != (miniapp.BookmarkTopic{ID: 0, Name: "Main chat"}) || topics[1] != (miniapp.BookmarkTopic{ID: 7, Name: "API"}) {
		t.Fatalf("topic options = %+v", topics)
	}
	main, _, more, err := repo.List(ctx, 0, 0, 10)
	if err != nil || len(main) != 1 || main[0].Topic != "Main chat" || more {
		t.Fatalf("main-chat filter = %+v, more=%v, err=%v", main, more, err)
	}
	detail, found, err := repo.Get(ctx, all[0].ID)
	if err != nil || !found || detail.Text != base.FullText || detail.Plain || detail.Topic != "API" {
		t.Fatalf("detail = %+v, found=%v, err=%v", detail, found, err)
	}
	if _, found, err := repo.Get(ctx, 999); err != nil || found {
		t.Fatalf("missing detail found=%v err=%v", found, err)
	}
}
