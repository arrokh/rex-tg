package miniapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeBookmarks struct {
	items     []BookmarkItem
	topics    []BookmarkTopic
	more      bool
	listErr   error
	listCalls int
	topicID   int64
	beforeID  int64
	limit     int
	detail    BookmarkDetail
	found     bool
	getErr    error
	gotID     int64
}

func (f *fakeBookmarks) List(_ context.Context, topicID, beforeID int64, limit int) ([]BookmarkItem, []BookmarkTopic, bool, error) {
	f.listCalls++
	f.topicID, f.beforeID, f.limit = topicID, beforeID, limit
	return f.items, f.topics, f.more, f.listErr
}

func (f *fakeBookmarks) Get(_ context.Context, id int64) (BookmarkDetail, bool, error) {
	f.gotID = id
	return f.detail, f.found, f.getErr
}

func bookmarkServer(t *testing.T, pages map[string]Page, repo Bookmarks) (*httptest.Server, caller, map[string]string) {
	t.Helper()
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) {
		p, ok := pages[id]
		return p, ok
	}, nil).WithBookmarks(repo)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, caller{t: t, url: srv.URL}, map[string]string{
		"X-Telegram-Init-Data": sign(t, fakeToken, fields(42, time.Now())),
	}
}

func TestBookmarksAPIRequiresOwnerAuthentication(t *testing.T) {
	repo := &fakeBookmarks{}
	_, c, _ := bookmarkServer(t, map[string]Page{"b": {Kind: KindBookmarks}}, repo)
	if code, _ := c.do("GET", "/api/bookmarks?id=b", nil, ""); code != http.StatusForbidden {
		t.Fatalf("unauthenticated list: %d", code)
	}
	if code, _ := c.do("GET", "/api/bookmark?id=b&bookmark=7", nil, ""); code != http.StatusForbidden {
		t.Fatalf("unauthenticated detail: %d", code)
	}
	if repo.listCalls != 0 || repo.gotID != 0 {
		t.Fatalf("unauthenticated request reached repository: %+v", repo)
	}
}

func TestBookmarksAPIRequiresBookmarksPage(t *testing.T) {
	repo := &fakeBookmarks{}
	_, c, owner := bookmarkServer(t, map[string]Page{"answer": {Kind: KindAnswer}}, repo)
	for _, path := range []string{
		"/api/bookmarks?id=answer",
		"/api/bookmark?id=answer&bookmark=7",
	} {
		if code, _ := c.do("GET", path, owner, ""); code != http.StatusNotFound {
			t.Errorf("%s: wrong page kind returned %d", path, code)
		}
	}
	if repo.listCalls != 0 || repo.gotID != 0 {
		t.Fatalf("wrong-kind request reached repository: %+v", repo)
	}
}

func TestBookmarksListFilteringAndPaginationContract(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 20, 0, 0, time.UTC)
	items := make([]BookmarkItem, 22)
	for i := range items {
		items[i] = BookmarkItem{ID: int64(100 - i), TopicID: 9, Topic: "API", Title: "Answer", Preview: "Excerpt", At: at}
	}
	repo := &fakeBookmarks{
		items: items, topics: []BookmarkTopic{{ID: 9, Name: "API"}, {ID: 0, Name: "Main chat"}},
	}
	_, c, owner := bookmarkServer(t, map[string]Page{"b": {Kind: KindBookmarks, Title: "Bookmarks"}}, repo)
	code, body := c.do("GET", "/api/bookmarks?id=b&topic=9&before=80", owner, "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, body)
	}
	if repo.listCalls != 1 || repo.topicID != 9 || repo.beforeID != 80 || repo.limit != bookmarksPageSize {
		t.Fatalf("List args = calls:%d topic:%d before:%d limit:%d", repo.listCalls, repo.topicID, repo.beforeID, repo.limit)
	}
	gotItems, ok := body["items"].([]any)
	if !ok || len(gotItems) != bookmarksPageSize || body["more"] != true {
		t.Fatalf("list page is not bounded with more set: %v", body)
	}
	first := gotItems[0].(map[string]any)
	if first["id"] != float64(100) || first["topic_id"] != float64(9) || first["topic"] != "API" || first["preview"] != "Excerpt" {
		t.Fatalf("list item fields: %v", first)
	}
	gotTopics, ok := body["topics"].([]any)
	if !ok || len(gotTopics) != 2 || gotTopics[1].(map[string]any)["id"] != float64(0) || gotTopics[1].(map[string]any)["name"] != "Main chat" {
		t.Fatalf("topic options: %v", body["topics"])
	}

	code, body = c.do("GET", "/api/bookmarks?id=b", owner, "")
	if code != http.StatusOK || repo.topicID != bookmarksAllTopics || repo.beforeID != 0 {
		t.Fatalf("all-topics sentinel: code=%d args=(%d,%d) body=%v", code, repo.topicID, repo.beforeID, body)
	}
	code, body = c.do("GET", "/api/bookmarks?id=b&topic=0", owner, "")
	if code != http.StatusOK || repo.topicID != 0 {
		t.Fatalf("main-chat filter: code=%d topic=%d body=%v", code, repo.topicID, body)
	}
}

func TestBookmarksValidateFilterAndCursor(t *testing.T) {
	repo := &fakeBookmarks{}
	_, c, owner := bookmarkServer(t, map[string]Page{"b": {Kind: KindBookmarks}}, repo)
	for _, path := range []string{
		"/api/bookmarks?id=b&topic=-1",
		"/api/bookmarks?id=b&topic=abc",
		"/api/bookmarks?id=b&before=0",
		"/api/bookmarks?id=b&before=1&before=2",
		"/api/bookmark?id=b&bookmark=0",
	} {
		if code, _ := c.do("GET", path, owner, ""); code != http.StatusBadRequest {
			t.Errorf("%s: invalid query returned %d", path, code)
		}
	}
	if repo.listCalls != 0 || repo.gotID != 0 {
		t.Fatalf("invalid query reached repository: %+v", repo)
	}
}

func TestBookmarkDetailRendersMarkdownAndEscapesPlainText(t *testing.T) {
	pages := map[string]Page{"b": {Kind: KindBookmarks, Title: "Bookmarks"}}
	t.Run("markdown", func(t *testing.T) {
		repo := &fakeBookmarks{found: true, detail: BookmarkDetail{
			ID: 7, TopicID: 9, Topic: "API", Title: "Markdown", Text: "**bold** <script>alert(1)</script>", At: time.Now(),
		}}
		_, c, owner := bookmarkServer(t, pages, repo)
		code, body := c.do("GET", "/api/bookmark?id=b&bookmark=7", owner, "")
		html, _ := body["html"].(string)
		if code != http.StatusOK || repo.gotID != 7 || !strings.Contains(html, "<strong>bold</strong>") || strings.Contains(html, "<script>") {
			t.Fatalf("markdown detail: %d %v", code, body)
		}
	})
	t.Run("plain", func(t *testing.T) {
		repo := &fakeBookmarks{found: true, detail: BookmarkDetail{
			ID: 8, Title: "Plain", Text: "**not markdown** <script>unsafe</script>\nnext line", Plain: true,
		}}
		_, c, owner := bookmarkServer(t, pages, repo)
		code, body := c.do("GET", "/api/bookmark?id=b&bookmark=8", owner, "")
		html, _ := body["html"].(string)
		if code != http.StatusOK || repo.gotID != 8 || strings.Contains(html, "<strong>") || strings.Contains(html, "<script>") || !strings.Contains(html, "**not markdown** &lt;script&gt;unsafe&lt;/script&gt;") || !strings.Contains(html, "<br>next line") {
			t.Fatalf("plain detail was not escaped literally: %d %v", code, body)
		}
	})
}
