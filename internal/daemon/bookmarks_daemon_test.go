package daemon_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/format"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

func TestBookmarkShortcutSavesReplyAndDeduplicates(t *testing.T) {
	e, _, thread := piTopicCfg(t, "pi-bookmark", func(*env) {})
	targetID := e.say(thread, "Remember this message.")
	e.tg.PushReply(chatID, thread, ownerID, targetID, "", "/b")
	e.waitText(thread, "Saved to Bookmarks")

	st := e.peek()
	rows, err := st.ListBookmarks(context.Background(), chatID, nil, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("bookmarks = %d, want 1: %+v", len(rows), rows)
	}
	b := rows[0]
	if b.MessageID != targetID || b.MessageThreadID != thread || b.FullText != "Remember this message." || b.TextFormat != store.BookmarkTextPlain {
		t.Fatalf("bookmark snapshot = %+v", b)
	}
	if b.SourceTopicName == "" || b.SavedAt.IsZero() {
		t.Fatalf("bookmark lacks source snapshot or saved time: %+v", b)
	}

	e.tg.PushReply(chatID, thread, ownerID, targetID, "", "/bookmark")
	e.waitText(thread, "already in Bookmarks")
	rows, err = st.ListBookmarks(context.Background(), chatID, nil, 0, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("duplicate save created a second bookmark: rows=%+v err=%v", rows, err)
	}
}

func TestBookmarkShortcutPreservesTelegramTextLink(t *testing.T) {
	e, _, thread := piTopicCfg(t, "pi-bookmark-link", func(*env) {})
	const label = "Read the guide"
	const url = "https://example.test/guide"
	e.tg.PushUpdate(telegram.Update{Message: &telegram.Message{
		MessageID: 9701, MessageThreadID: thread, IsTopicMessage: true,
		From: &telegram.User{ID: ownerID}, Chat: telegram.Chat{ID: chatID, Type: "private"},
		Text: "/bookmark",
		ReplyToMessage: &telegram.Message{
			MessageID: 9700, MessageThreadID: thread, IsTopicMessage: true,
			Chat: telegram.Chat{ID: chatID, Type: "private"}, Text: label,
			Entities: []telegram.MessageEntity{{Type: "text_link", Offset: 0, Length: len(label), URL: url}},
		},
	}})
	e.waitText(thread, "Saved to Bookmarks")

	rows, err := e.peek().ListBookmarks(context.Background(), chatID, nil, 0, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("bookmarks = %+v, err=%v", rows, err)
	}
	if rows[0].FullText != "["+label+"](<"+url+">)" || rows[0].TextFormat != store.BookmarkTextMarkdown {
		t.Fatalf("bookmark lost Telegram text link: text=%q format=%q", rows[0].FullText, rows[0].TextFormat)
	}
	gotHTML, wantHTML := format.RenderHTML(rows[0].FullText), format.RenderHTML("["+label+"]("+url+")")
	if gotHTML != wantHTML {
		t.Fatalf("bookmark link render = %q, want Whole answer render %q", gotHTML, wantHTML)
	}
	if gotPreview, wantPreview := format.Plain(rows[0].FullText), label+" ("+url+")"; gotPreview != wantPreview {
		t.Fatalf("bookmark preview = %q, want Whole answer preview %q", gotPreview, wantPreview)
	}
}

func TestBookmarkReplyKeepsMainChatSourceFromTopic(t *testing.T) {
	e, _, thread := piTopicCfg(t, "pi-bookmark-main-source", func(*env) {})
	targetID := e.say(0, "Main chat message.")
	e.tg.PushReply(chatID, thread, ownerID, targetID, "", "/bookmark")
	e.waitText(thread, "Saved to Bookmarks")

	rows, err := e.peek().ListBookmarks(context.Background(), chatID, nil, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].MessageThreadID != 0 || rows[0].SourceTopicName != "Main chat" {
		t.Fatalf("bookmark source = %+v, want Main chat", rows)
	}
}

func TestBookmarkButtonStoresWholeRedactedAnswer(t *testing.T) {
	e, p, thread := piTopicCfg(t, "pi-bookmark-answer", func(*env) {})
	answer := "# Plan\n\nThe complete **answer** remains available.\n\nLast detail."
	p.User("write a plan")
	time.Sleep(10 * time.Millisecond)
	p.Final(answer)
	message := e.waitText(thread, "The complete")
	data := buttonData(message, "Bookmark")
	if !strings.HasPrefix(data, "bm:") {
		t.Fatalf("answer has no Bookmark callback: %+v", message.Markup)
	}
	e.pressButton(message, data)
	e.waitFor("saved answer callback", func() bool {
		for _, text := range e.callbackTexts() {
			if text == "Saved to Bookmarks." {
				return true
			}
		}
		return false
	})
	e.pressButton(message, data)
	e.waitFor("duplicate bookmark callback", func() bool {
		texts := e.callbackTexts()
		return len(texts) >= 2 && texts[len(texts)-1] == "Already in Bookmarks."
	})

	st := e.peek()
	rows, err := st.ListBookmarks(context.Background(), chatID, nil, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("bookmarks = %d, want 1: %+v", len(rows), rows)
	}
	b := rows[0]
	if b.MessageID != message.ID || b.MessageThreadID != thread || b.FullText != answer || b.TextFormat != store.BookmarkTextMarkdown {
		t.Fatalf("full answer bookmark = %+v", b)
	}
}

func TestBookmarksCommandOpensGlobalPage(t *testing.T) {
	e, _, thread := piTopicCfg(t, "pi-bookmarks-page", func(e *env) {
		e.cfg.MiniApp.URL = "https://mac.example.ts.net:8443"
	})
	e.say(thread, "/bookmarks")
	m := e.waitText(thread, "Open your saved messages")
	url := webAppURL(e, thread, "Open Bookmarks")
	if !strings.HasPrefix(url, "https://mac.example.ts.net:8443/app?p=") {
		t.Fatalf("Bookmarks did not open a global page: %q; message=%+v", url, m.Markup)
	}
}
