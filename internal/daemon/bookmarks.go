package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"rex-tg/internal/format"
	"rex-tg/internal/inbound"
	"rex-tg/internal/miniapp"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

const bookmarkCallback = "bm"

func isBookmarkSaveCommand(command string) bool {
	return command == "bookmark" || command == "b"
}

// miniAppBookmarks adapts durable, chat-scoped snapshots to the global page.
type miniAppBookmarks struct {
	st     *store.Store
	chatID func() int64
}

func (b miniAppBookmarks) List(ctx context.Context, topicID, beforeID int64, limit int) ([]miniapp.BookmarkItem, []miniapp.BookmarkTopic, bool, error) {
	chat := int64(0)
	if b.chatID != nil {
		chat = b.chatID()
	}
	if chat == 0 {
		return nil, nil, false, errors.New("bookmarks: owner chat is not paired")
	}
	if topicID < -1 {
		return nil, nil, false, errors.New("bookmarks: invalid topic filter")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var filter *int64
	if topicID >= 0 {
		filter = &topicID
	}
	rows, err := b.st.ListBookmarks(ctx, chat, filter, beforeID, limit+1)
	if err != nil {
		return nil, nil, false, err
	}
	topics, err := b.st.BookmarkTopics(ctx, chat)
	if err != nil {
		return nil, nil, false, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	items := make([]miniapp.BookmarkItem, 0, len(rows))
	for _, row := range rows {
		plain := row.FullText
		if row.TextFormat == store.BookmarkTextMarkdown {
			plain = format.Plain(plain)
		}
		preview := strings.Join(strings.Fields(plain), " ")
		items = append(items, miniapp.BookmarkItem{
			ID: row.ID, TopicID: row.MessageThreadID, Topic: bookmarkTopicName(row.MessageThreadID, row.SourceTopicName),
			Title: firstNonEmpty(row.Title, "Saved message"), Preview: format.Truncate(preview, 180), At: row.MessageDate,
		})
	}
	options := make([]miniapp.BookmarkTopic, 0, len(topics))
	for _, topic := range topics {
		options = append(options, miniapp.BookmarkTopic{
			ID: topic.MessageThreadID, Name: bookmarkTopicName(topic.MessageThreadID, topic.Name),
		})
	}
	return items, options, more, nil
}

func (b miniAppBookmarks) Get(ctx context.Context, id int64) (miniapp.BookmarkDetail, bool, error) {
	chat := int64(0)
	if b.chatID != nil {
		chat = b.chatID()
	}
	if chat == 0 {
		return miniapp.BookmarkDetail{}, false, errors.New("bookmarks: owner chat is not paired")
	}
	row, err := b.st.Bookmark(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return miniapp.BookmarkDetail{}, false, nil
	}
	if err != nil {
		return miniapp.BookmarkDetail{}, false, err
	}
	if row.ChatID != chat {
		return miniapp.BookmarkDetail{}, false, nil
	}
	return miniapp.BookmarkDetail{
		ID: row.ID, TopicID: row.MessageThreadID, Topic: bookmarkTopicName(row.MessageThreadID, row.SourceTopicName),
		Title: firstNonEmpty(row.Title, "Saved message"), Text: row.FullText,
		Plain: row.TextFormat != store.BookmarkTextMarkdown, Kind: row.ContentLabel, At: row.MessageDate,
	}, true, nil
}

func (b miniAppBookmarks) Delete(ctx context.Context, id int64) error {
	chat := int64(0)
	if b.chatID != nil {
		chat = b.chatID()
	}
	if chat == 0 {
		return errors.New("bookmarks: owner chat is not paired")
	}
	return b.st.DeleteBookmark(ctx, chat, id)
}

func bookmarkTopicName(topicID int64, name string) string {
	if topicID == 0 {
		return "Main chat"
	}
	if name != "" {
		return name
	}
	return fmt.Sprintf("Topic %d", topicID)
}

// bookmarksCommand opens the cross-topic page. It ignores the topic that
// carried the command because the page is global.
func (d *daemon) bookmarksCommand(chatID, threadID int64) {
	if !d.miniAppReady() {
		d.say(chatID, threadID, bookmarksText, nil)
		return
	}
	launched := d.Launch(d.ctx, miniapp.ViewBookmarks, 0)
	if launched.Page == "" {
		d.say(chatID, threadID, "🔖 Could not open Bookmarks. "+format.Escape(launched.Error), nil)
		return
	}
	markup := &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{{
		Text: "🔖 Open Bookmarks", WebApp: &telegram.WebAppInfo{URL: miniAppURL(miniBase(d.cfg), launched.Page)},
	}}}}
	d.say(chatID, threadID, "🔖 Open your saved messages.", markup)
}

// bookmarkMessage saves the message that /bookmark or /b replies to. The
// inbound update and its snapshot commit together, so a restart cannot keep
// only one side of the action.
func (d *daemon) bookmarkMessage(u telegram.Update, m *telegram.Message, threadID int64) {
	r := inbound.RouteText(m.Text, m.MessageThreadID != 0, d.botUser)
	if r.Args != "" {
		if d.recorded(u, m, "command", m.Text, threadID, store.InboundDone) {
			d.bookmarkMessageReply(u, m, threadID, "Usage: reply to a message with /bookmark or /b.")
		}
		return
	}
	target := m.ReplyToMessage
	if target == nil {
		if d.recorded(u, m, "command", m.Text, threadID, store.InboundDone) {
			d.bookmarkMessageReply(u, m, threadID, "Reply to a message with /bookmark or /b to save it.")
		}
		return
	}
	bookmark, _, err := d.bookmarkForMessage(d.ctx, m.Chat.ID, m.MessageThreadID, target, nil)
	if err != nil {
		d.storeErr = err
		return
	}
	in := store.Inbound{
		UpdateID: u.UpdateID, ChatID: m.Chat.ID, MessageThreadID: m.MessageThreadID, TGMessageID: m.MessageID,
		FromID: d.ownerUser, Kind: "command", Body: m.Text, State: store.InboundDone, ThreadID: threadID, CreatedAt: d.now(),
	}
	inserted, created, err := d.st.InsertBookmarkWithInbound(d.ctx, in, bookmark)
	if err != nil {
		d.storeErr = fmt.Errorf("save bookmark update %d: %w", u.UpdateID, err)
		return
	}
	if !inserted {
		return
	}
	if threadID > 0 {
		d.markBelow(threadID)
	}
	msg := "🔖 Saved to Bookmarks · " + format.Escape(bookmarkTopicName(bookmark.MessageThreadID, bookmark.SourceTopicName)) + "."
	if !created {
		msg = "🔖 This message is already in Bookmarks."
	}
	d.bookmarkMessageReply(u, m, threadID, msg)
}

func (d *daemon) bookmarkMessageReply(u telegram.Update, m *telegram.Message, threadID int64, text string) {
	if threadID > 0 {
		if th, err := d.st.Thread(d.ctx, threadID); err == nil {
			d.replyTopic(th, fmt.Sprintf("%d:bookmark", u.UpdateID), text)
			return
		}
	}
	d.say(m.Chat.ID, m.MessageThreadID, text, nil)
}

// bookmarkButton saves an agent answer from its inline Bookmark button.
func (d *daemon) bookmarkButton(u telegram.Update, cb *telegram.CallbackQuery, outboxID int64) {
	m := cb.Message
	if m == nil {
		d.answerCallback(cb.ID, "This answer is no longer available.", true)
		return
	}
	var row *store.OutboxRow
	got, err := d.st.OutboxRow(d.ctx, outboxID)
	if err == nil {
		if got.Kind != "answer" && got.Kind != "backfill" {
			d.answerCallback(cb.ID, "This bookmark button is out of date.", true)
			return
		}
		matched := false
		for _, part := range got.Parts {
			if part.TGMessageID == m.MessageID && part.ChatID == m.Chat.ID && part.State == store.OutboxSent {
				matched = true
				break
			}
		}
		if !matched {
			d.answerCallback(cb.ID, "This bookmark button is out of date.", true)
			return
		}
		row = &got
	} else if !errors.Is(err, store.ErrNotFound) {
		d.storeErr = fmt.Errorf("read bookmark answer %d: %w", outboxID, err)
		d.answerCallback(cb.ID, "Could not save this bookmark. Try again.", true)
		return
	}
	bookmark, sourceThread, err := d.bookmarkForMessage(d.ctx, m.Chat.ID, m.MessageThreadID, m, row)
	if err != nil {
		d.storeErr = fmt.Errorf("prepare bookmark answer %d: %w", outboxID, err)
		d.answerCallback(cb.ID, "Could not save this bookmark. Try again.", true)
		return
	}
	in := store.Inbound{
		UpdateID: u.UpdateID, ChatID: m.Chat.ID, MessageThreadID: m.MessageThreadID, TGMessageID: m.MessageID,
		FromID: cb.From.ID, Kind: "callback", Body: cb.Data, State: store.InboundDone, ThreadID: sourceThread, CreatedAt: d.now(),
	}
	inserted, created, err := d.st.InsertBookmarkWithInbound(d.ctx, in, bookmark)
	if err != nil {
		d.storeErr = fmt.Errorf("save bookmark callback %d: %w", u.UpdateID, err)
		d.answerCallback(cb.ID, "Could not save this bookmark. Try again.", true)
		return
	}
	if !inserted || !created {
		d.answerCallback(cb.ID, "Already in Bookmarks.", false)
		return
	}
	d.answerCallback(cb.ID, "Saved to Bookmarks.", false)
}

// bookmarkForMessage snapshots visible text and source-topic metadata without
// linking the bookmark to a topic, conversation, inbound row, or outbox row.
func (d *daemon) bookmarkForMessage(ctx context.Context, chatID, fallbackTopicID int64, target *telegram.Message, row *store.OutboxRow) (store.Bookmark, int64, error) {
	if target == nil || target.MessageID <= 0 {
		return store.Bookmark{}, 0, errors.New("bookmark: target message is missing")
	}
	topicID := target.MessageThreadID
	if topicID == 0 && target.IsTopicMessage {
		topicID = fallbackTopicID
	}
	if topicID == 0 && row != nil && row.ThreadID > 0 {
		if th, err := d.st.Thread(ctx, row.ThreadID); err == nil {
			topicID = th.MessageThreadID
		}
	}
	threadID := int64(0)
	topicName := "Main chat"
	if topicID != 0 {
		topicName = fmt.Sprintf("Topic %d", topicID)
		th, ok, err := d.st.ThreadByTopic(ctx, chatID, topicID)
		if err != nil {
			return store.Bookmark{}, 0, err
		}
		if ok {
			threadID = th.ID
			topicName = topicShownName(th)
		}
	}
	if row == nil && threadID != 0 {
		got, ok, err := d.st.OutboxRowBySentMessage(ctx, threadID, target.MessageID)
		if err != nil {
			return store.Bookmark{}, 0, err
		}
		if ok {
			row = &got
		}
	}
	text, textFormat, contentKind, contentLabel := bookmarkMessageContent(target)
	if row != nil && (row.Kind == "answer" || row.Kind == "backfill") {
		if row.FullText != "" {
			text, textFormat = row.FullText, store.BookmarkTextMarkdown
		} else if md, plain, ok := answerSource(*row, func(key string) (string, bool) {
			return d.answerPageText(row.ThreadID, key)
		}); ok {
			text = md
			if plain {
				textFormat = store.BookmarkTextPlain
			} else {
				textFormat = store.BookmarkTextMarkdown
			}
		}
		contentKind, contentLabel = "answer", "Agent answer"
	}
	text = format.Redact(text)
	contentLabel = format.Redact(contentLabel)
	if strings.TrimSpace(text) == "" {
		text = contentLabel
		textFormat = store.BookmarkTextPlain
	}
	titleText := text
	if textFormat == store.BookmarkTextMarkdown {
		titleText = format.Plain(titleText)
	}
	title := strings.Join(strings.Fields(titleText), " ")
	if title == "" {
		title = contentLabel
	}
	title = format.Truncate(title, 100)
	date := d.now()
	if target.Date > 0 {
		date = time.Unix(target.Date, 0)
	}
	return store.Bookmark{
		ChatID: chatID, MessageID: target.MessageID, MessageThreadID: topicID, SourceTopicName: format.Redact(topicName),
		MessageDate: date, SavedAt: d.now(), Title: title, FullText: text, TextFormat: textFormat,
		ContentKind: contentKind, ContentLabel: contentLabel,
	}, threadID, nil
}

func bookmarkMessageContent(m *telegram.Message) (text, textFormat, kind, label string) {
	kind, label = "text", "Message"
	switch {
	case len(m.Photo) > 0:
		kind, label = "photo", "Photo"
	case m.Document != nil:
		kind, label = "document", "File"
		if m.Document.FileName != "" {
			label += ": " + m.Document.FileName
		}
	case m.Voice != nil:
		kind, label = "voice", "Voice message"
	case m.Audio != nil:
		kind, label = "audio", "Audio"
		if m.Audio.FileName != "" {
			label += ": " + m.Audio.FileName
		}
	case m.Video != nil:
		kind, label = "video", "Video"
		if m.Video.FileName != "" {
			label += ": " + m.Video.FileName
		}
	case m.Sticker != nil:
		kind, label = "sticker", "Sticker"
		if m.Sticker.Emoji != "" {
			label += " " + m.Sticker.Emoji
		}
	case m.ForumTopicCreated != nil:
		kind, label = "topic", "Topic created"
	}
	rawText := m.Text
	entities := m.Entities
	if strings.TrimSpace(rawText) == "" {
		rawText = m.Caption
		entities = m.CaptionEntities
	}
	if strings.TrimSpace(rawText) == "" {
		return label, store.BookmarkTextPlain, kind, label
	}
	if linked, ok := bookmarkTextLinks(rawText, entities); ok {
		return strings.TrimSpace(linked), store.BookmarkTextMarkdown, kind, label
	}
	return strings.TrimSpace(rawText), store.BookmarkTextPlain, kind, label
}

type bookmarkTextLink struct {
	start, end int
	url        string
	autolink   bool
}

// bookmarkTextLinks preserves Telegram link entities as the same safe Markdown
// links used by Whole answer. Other text is escaped so literal punctuation
// cannot become Markdown when the snapshot switches from plain text.
func bookmarkTextLinks(text string, entities []telegram.MessageEntity) (string, bool) {
	links := make([]bookmarkTextLink, 0, len(entities))
	for _, entity := range entities {
		if entity.Type != "text_link" && entity.Type != "url" && entity.Type != "email" {
			continue
		}
		start, end, ok := bookmarkUTF16Range(text, entity.Offset, entity.Length)
		if !ok {
			continue
		}
		rawURL := entity.URL
		autolink := entity.Type == "url"
		switch entity.Type {
		case "url":
			rawURL = text[start:end]
		case "email":
			rawURL = "mailto:" + text[start:end]
		}
		linkURL, ok := bookmarkLinkURL(rawURL)
		if !ok {
			continue
		}
		links = append(links, bookmarkTextLink{start: start, end: end, url: linkURL, autolink: autolink})
	}
	if len(links) == 0 {
		return text, false
	}
	sort.SliceStable(links, func(i, j int) bool {
		if links[i].start == links[j].start {
			return links[i].end > links[j].end
		}
		return links[i].start < links[j].start
	})

	var b strings.Builder
	last, added := 0, false
	for _, link := range links {
		if link.start < last {
			continue
		}
		b.WriteString(escapeBookmarkMarkdownText(text[last:link.start]))
		if link.autolink {
			b.WriteByte('<')
			b.WriteString(link.url)
			b.WriteByte('>')
		} else {
			b.WriteByte('[')
			b.WriteString(escapeBookmarkMarkdownText(text[link.start:link.end]))
			b.WriteString("](<")
			b.WriteString(link.url)
			b.WriteString(">)")
		}
		last, added = link.end, true
	}
	if !added {
		return text, false
	}
	b.WriteString(escapeBookmarkMarkdownText(text[last:]))
	return b.String(), true
}

// bookmarkUTF16Range maps Telegram's UTF-16 entity span to UTF-8 byte offsets.
// It refuses ranges that split a surrogate pair or fall outside the text.
func bookmarkUTF16Range(text string, offset, length int) (int, int, bool) {
	total := 0
	for _, r := range text {
		total += utf16.RuneLen(r)
	}
	if offset < 0 || length <= 0 || offset > total || length > total-offset {
		return 0, 0, false
	}
	start, ok := bookmarkUTF16ByteOffset(text, offset)
	if !ok {
		return 0, 0, false
	}
	end, ok := bookmarkUTF16ByteOffset(text, offset+length)
	if !ok || start >= end {
		return 0, 0, false
	}
	return start, end, true
}

func bookmarkUTF16ByteOffset(text string, offset int) (int, bool) {
	units := 0
	for byteOffset, r := range text {
		if units == offset {
			return byteOffset, true
		}
		runeUnits := utf16.RuneLen(r)
		if offset < units+runeUnits {
			return 0, false
		}
		units += runeUnits
	}
	return len(text), units == offset
}

// bookmarkLinkURL matches the schemes allowed by the shared Markdown renderer.
func bookmarkLinkURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Host == "" {
			return "", false
		}
	case "mailto":
		if u.Opaque == "" && u.Path == "" {
			return "", false
		}
	default:
		return "", false
	}
	return u.String(), true
}

// escapeBookmarkMarkdownText keeps non-link text literal inside a Markdown snapshot.
func escapeBookmarkMarkdownText(text string) string {
	const punctuation = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if strings.ContainsRune(punctuation, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
