package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrateV6ToV7PreservesPartsAndAddsFullText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := open(path, migrations[:6])
	noErr(t, err)
	s.now = func() time.Time { return t0 }
	th, _ := topic(t, s, 50)
	oldID, _, err := s.Enqueue(ctx, OutboxItem{ThreadID: th.ID, Key: "legacy", Kind: "answer",
		Parts: []PartContent{{Content: "legacy part", Format: FormatHTML}}})
	noErr(t, err)
	noErr(t, s.Close())

	s, err = Open(path)
	noErr(t, err)
	defer s.Close()
	if version := must(s.version(ctx)); version != 7 {
		t.Fatalf("schema version = %d, want 7", version)
	}
	row := must(s.OutboxRow(ctx, oldID))
	if row.FullText != "" || len(row.Parts) != 1 || row.Parts[0].Content != "legacy part" {
		t.Fatalf("legacy outbox row after migration = %+v", row)
	}

	fullText := "# complete redacted answer\n\n**full body**"
	id, _, err := s.Enqueue(ctx, OutboxItem{ThreadID: th.ID, Key: "new", Kind: "answer", FullText: fullText,
		Parts: []PartContent{{Content: "sent piece", Format: FormatHTML}}})
	noErr(t, err)
	if row := must(s.OutboxRow(ctx, id)); row.FullText != fullText || len(row.Parts) != 1 {
		t.Fatalf("new outbox row = %+v", row)
	}
	answers := must(s.AnswersBefore(ctx, th.ID, 0, 10, ""))
	if len(answers) != 2 || answers[0].FullText != fullText || answers[1].FullText != "" {
		t.Fatalf("answer history full text = %+v", answers)
	}
}

func TestInsertBookmarkWithInboundIsAtomicAndIdempotent(t *testing.T) {
	s := newStore(t)
	bookmark := Bookmark{
		ChatID: 100, MessageID: 700, MessageThreadID: 12, SourceTopicName: "topic",
		MessageDate: t0.Add(-time.Minute), SavedAt: t0, Title: "Saved title",
		FullText: "**saved text**", TextFormat: BookmarkTextMarkdown, ContentKind: "text", ContentLabel: "Markdown",
	}
	inbound := Inbound{UpdateID: 1, ChatID: 100, MessageThreadID: 12, TGMessageID: 701,
		FromID: 5, Kind: "callback", Body: "save:700", CreatedAt: t0}
	if updateInserted, bookmarkCreated, err := s.InsertBookmarkWithInbound(ctx, inbound, bookmark); err != nil || !updateInserted || !bookmarkCreated {
		t.Fatalf("first insert = %v, %v, %v; want true, true, nil", updateInserted, bookmarkCreated, err)
	}
	if updateInserted, bookmarkCreated, err := s.InsertBookmarkWithInbound(ctx, inbound, bookmark); err != nil || updateInserted || bookmarkCreated {
		t.Fatalf("repeat insert = %v, %v, %v; want false, false, nil", updateInserted, bookmarkCreated, err)
	}

	// Update and bookmark deduplication are independent.
	newBookmark := bookmark
	newBookmark.MessageID = 702
	if updateInserted, bookmarkCreated, err := s.InsertBookmarkWithInbound(ctx, inbound, newBookmark); err != nil || updateInserted || !bookmarkCreated {
		t.Fatalf("duplicate update insert = %v, %v, %v; want false, true, nil", updateInserted, bookmarkCreated, err)
	}
	newInbound := inbound
	newInbound.UpdateID = 2
	duplicateTarget := bookmark
	duplicateTarget.Title = "must not replace the first snapshot"
	if updateInserted, bookmarkCreated, err := s.InsertBookmarkWithInbound(ctx, newInbound, duplicateTarget); err != nil || !updateInserted || bookmarkCreated {
		t.Fatalf("duplicate target insert = %v, %v, %v; want true, false, nil", updateInserted, bookmarkCreated, err)
	}

	// An invalid snapshot fails the transaction, so the inbound update is not
	// left behind as a successfully handled command without its bookmark.
	badInbound := inbound
	badInbound.UpdateID = 3
	badBookmark := bookmark
	badBookmark.MessageID = 703
	badBookmark.TextFormat = "html"
	if _, _, err := s.InsertBookmarkWithInbound(ctx, badInbound, badBookmark); err == nil {
		t.Fatal("invalid text format accepted")
	}
	if _, found, err := s.InboundByID(ctx, badInbound.UpdateID); err != nil || found {
		t.Fatalf("inbound from rolled-back transaction found=%v err=%v", found, err)
	}
	if got, err := s.Bookmark(ctx, 1); err != nil || got.Title != bookmark.Title || !got.SavedAt.Equal(t0) {
		t.Fatalf("bookmark snapshot = %+v, err=%v", got, err)
	}
}

func TestBookmarkListPaginationAndTopics(t *testing.T) {
	s := newStore(t)
	cases := []struct {
		updateID int64
		msgID    int64
		threadID int64
		name     string
	}{
		{1, 101, 0, "main"},
		{2, 102, 11, "topic old"},
		{3, 103, 22, "other"},
		{4, 104, 11, "topic renamed"},
	}
	for _, tc := range cases {
		in := Inbound{UpdateID: tc.updateID, ChatID: 100, FromID: 1, Kind: "command", Body: "/save", CreatedAt: t0}
		b := Bookmark{ChatID: 100, MessageID: tc.msgID, MessageThreadID: tc.threadID, SourceTopicName: tc.name,
			MessageDate: t0.Add(time.Duration(tc.msgID) * time.Second), SavedAt: t0.Add(time.Duration(tc.updateID) * time.Second),
			Title: tc.name, FullText: tc.name, ContentKind: "text", ContentLabel: "Text"}
		if _, _, err := s.InsertBookmarkWithInbound(ctx, in, b); err != nil {
			t.Fatal(err)
		}
	}

	all := must(s.ListBookmarks(ctx, 100, nil, 0, 2))
	if len(all) != 2 || all[0].MessageID != 104 || all[1].MessageID != 103 {
		t.Fatalf("first page = %+v", all)
	}
	next := must(s.ListBookmarks(ctx, 100, nil, all[1].ID, 2))
	if len(next) != 2 || next[0].MessageID != 102 || next[1].MessageID != 101 {
		t.Fatalf("second page = %+v", next)
	}
	topicID := int64(11)
	filtered := must(s.ListBookmarks(ctx, 100, &topicID, 0, 10))
	if len(filtered) != 2 || filtered[0].MessageID != 104 || filtered[1].MessageID != 102 {
		t.Fatalf("topic-filtered bookmarks = %+v", filtered)
	}
	mainID := int64(0)
	main := must(s.ListBookmarks(ctx, 100, &mainID, 0, 10))
	if len(main) != 1 || main[0].MessageID != 101 {
		t.Fatalf("main-chat bookmarks = %+v", main)
	}

	topics := must(s.BookmarkTopics(ctx, 100))
	if len(topics) != 3 || topics[0] != (BookmarkTopic{MessageThreadID: 0, Name: "main"}) ||
		topics[1] != (BookmarkTopic{MessageThreadID: 11, Name: "topic renamed"}) ||
		topics[2] != (BookmarkTopic{MessageThreadID: 22, Name: "other"}) {
		t.Fatalf("bookmark topics = %+v", topics)
	}
	if _, err := s.Bookmark(ctx, all[0].ID); err != nil {
		t.Fatalf("get bookmark: %v", err)
	}
	if _, err := s.Bookmark(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing bookmark error = %v", err)
	}
}

func TestOutboxRowBySentMessage(t *testing.T) {
	s := newStore(t)
	th, _ := topic(t, s, 55)
	fullText := "**complete response**"
	id, _, err := s.Enqueue(ctx, OutboxItem{ThreadID: th.ID, Key: "answer", Kind: "answer", FullText: fullText,
		Parts: []PartContent{{Content: "part one", Format: FormatHTML}, {Content: "part two", Format: FormatHTML}}})
	noErr(t, err)
	noErr(t, s.MarkPartSent(ctx, id, 1, 889))
	row, found, err := s.OutboxRowBySentMessage(ctx, th.ID, 889)
	if err != nil || !found || row.ID != id || row.FullText != fullText || len(row.Parts) != 2 {
		t.Fatalf("sent message lookup = %+v, %v, %v", row, found, err)
	}
	if _, found, err := s.OutboxRowBySentMessage(ctx, th.ID+100, 889); err != nil || found {
		t.Fatalf("wrong topic lookup found=%v err=%v", found, err)
	}
}

func TestBookmarksHaveNoForeignKeysOrRetentionCoupling(t *testing.T) {
	s := newStore(t)
	th, conv := topic(t, s, 66)
	old := t0.Add(-31 * 24 * time.Hour)
	in := Inbound{UpdateID: 1, ChatID: th.ChatID, FromID: 1, Kind: "command", Body: "/save", CreatedAt: old}
	b := Bookmark{ChatID: th.ChatID, MessageID: 999, MessageThreadID: th.MessageThreadID,
		SourceTopicName: th.Name, MessageDate: old, SavedAt: old, Title: "durable", FullText: "full snapshot",
		TextFormat: BookmarkTextPlain, ContentKind: "text", ContentLabel: "Text"}
	if _, created, err := s.InsertBookmarkWithInbound(ctx, in, b); err != nil || !created {
		t.Fatalf("insert bookmark = %v, %v", created, err)
	}
	var foreignKeys int
	noErr(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_list('bookmark')`).Scan(&foreignKeys))
	if foreignKeys != 0 {
		t.Fatalf("bookmark has %d foreign keys", foreignKeys)
	}

	// Remove the source database entities. Bookmarks do not point to them.
	_, err := s.db.ExecContext(ctx, `UPDATE conversation SET thread_id=NULL WHERE id=?`, conv.ID)
	noErr(t, err)
	_, err = s.db.ExecContext(ctx, `DELETE FROM thread WHERE id=?`, th.ID)
	noErr(t, err)
	_, err = s.db.ExecContext(ctx, `DELETE FROM conversation WHERE id=?`, conv.ID)
	noErr(t, err)
	noErr(t, s.Prune(ctx, t0))

	bookmarks := must(s.ListBookmarks(ctx, th.ChatID, nil, 0, 10))
	if len(bookmarks) != 1 || bookmarks[0].MessageID != b.MessageID || bookmarks[0].FullText != b.FullText {
		t.Fatalf("bookmark lost after source deletion and retention: %+v", bookmarks)
	}
	if _, found, err := s.InboundByID(ctx, in.UpdateID); err != nil || found {
		t.Fatalf("old inbound found=%v err=%v; want retention cleanup", found, err)
	}
}
