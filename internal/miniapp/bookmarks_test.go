package miniapp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeBookmarks struct {
	items       []BookmarkItem
	topics      []BookmarkTopic
	more        bool
	listErr     error
	listCalls   int
	topicID     int64
	beforeID    int64
	limit       int
	detail      BookmarkDetail
	found       bool
	getErr      error
	gotID       int64
	deleteCalls int
	deleteID    int64
	deleteErr   error
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

func (f *fakeBookmarks) Delete(_ context.Context, id int64) error {
	f.deleteCalls++
	f.deleteID = id
	return f.deleteErr
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
	if code, _ := c.do("POST", "/api/bookmark?id=b&bookmark=7", map[string]string{"Content-Type": "application/json"}, `{"op":"delete"}`); code != http.StatusForbidden {
		t.Fatalf("unauthenticated delete: %d", code)
	}
	if repo.listCalls != 0 || repo.gotID != 0 || repo.deleteCalls != 0 {
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

func TestBookmarkDeleteRequiresOwnerAndBookmarksPageAndIsRepeatable(t *testing.T) {
	repo := &fakeBookmarks{}
	pages := map[string]Page{
		"b":      {Kind: KindBookmarks, Title: "Bookmarks"},
		"answer": {Kind: KindAnswer, Title: "Answer"},
	}
	_, c, owner := bookmarkServer(t, pages, repo)
	path := "/api/bookmark?id=b&bookmark=7"
	jsonOwner := map[string]string{"X-Telegram-Init-Data": owner["X-Telegram-Init-Data"], "Content-Type": "application/json"}

	if code, _ := c.do("POST", "/api/bookmark?id=answer&bookmark=7", jsonOwner, `{"op":"delete"}`); code != http.StatusNotFound {
		t.Fatalf("wrong page kind delete: %d", code)
	}
	if code, _ := c.do("POST", path, jsonOwner, `{"op":"delete","extra":true}`); code != http.StatusBadRequest {
		t.Fatalf("unknown delete field: %d", code)
	}
	if code, _ := c.do("POST", "/api/bookmark?id=b&bookmark=0", jsonOwner, `{"op":"delete"}`); code != http.StatusBadRequest {
		t.Fatalf("invalid bookmark id: %d", code)
	}
	if repo.deleteCalls != 0 {
		t.Fatalf("invalid delete reached repository: %+v", repo)
	}

	for i := 0; i < 2; i++ {
		code, body := c.do("POST", path, jsonOwner, `{"op":"delete"}`)
		if code != http.StatusOK || body["deleted"] != true {
			t.Fatalf("delete attempt %d: %d %v", i+1, code, body)
		}
	}
	if repo.deleteCalls != 2 || repo.deleteID != 7 {
		t.Fatalf("delete repository calls = %d id=%d", repo.deleteCalls, repo.deleteID)
	}

	old := sign(t, fakeToken, fields(42, time.Now().Add(-2*time.Hour)))
	if code, _ := c.do("POST", path, map[string]string{"X-Telegram-Init-Data": old, "Content-Type": "application/json"}, `{"op":"delete"}`); code != http.StatusForbidden {
		t.Fatalf("stale signed delete: %d", code)
	}
	if repo.deleteCalls != 2 {
		t.Fatalf("stale signed delete reached repository: %+v", repo)
	}

	repo.deleteErr = errors.New("synthetic storage failure")
	if code, body := c.do("POST", path, jsonOwner, `{"op":"delete"}`); code != http.StatusServiceUnavailable || body["error"] != "Could not remove this bookmark. Try again." {
		t.Fatalf("storage failure response: %d %v", code, body)
	}
	if repo.deleteCalls != 3 {
		t.Fatalf("delete error call count = %d", repo.deleteCalls)
	}
}

func TestBookmarkDeleteUsesVerifiedKeyboardKey(t *testing.T) {
	repo := &fakeBookmarks{}
	pages := map[string]Page{"b": {Kind: KindBookmarks}}
	logins := &fakeLogins{allow: map[string]bool{"owner@example.test": true}}
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) {
		p, ok := pages[id]
		return p, ok
	}, nil).
		WithBookmarks(repo).
		WithLaunch(&fakeLauncher{}, func(key string) bool { return key == testKey }).
		WithTailnetOnly(true).
		WithLogins(logins)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c := caller{t: t, url: srv.URL}
	path := "/api/bookmark?id=b&bookmark=7"
	body := `{"op":"delete"}`
	stranger := map[string]string{"X-Rex-Key": testKey, "Tailscale-User-Login": "other@example.test", "Content-Type": "application/json"}
	if code, _ := c.do("POST", path, stranger, body); code != http.StatusForbidden || repo.deleteCalls != 0 {
		t.Fatalf("keyboard key from another login: %d, delete calls %d", code, repo.deleteCalls)
	}
	owner := map[string]string{"X-Rex-Key": testKey, "Tailscale-User-Login": "owner@example.test", "Content-Type": "application/json"}
	if code, result := c.do("POST", path, owner, body); code != http.StatusOK || result["deleted"] != true || repo.deleteID != 7 {
		t.Fatalf("verified owner keyboard delete: %d %v id=%d", code, result, repo.deleteID)
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

func TestBookmarkDetailsRenderLinksLikeWholeAnswers(t *testing.T) {
	text := "Read [the guide](https://docs.example.test/guide) and [unsafe](javascript:alert(1))."
	plainURL := "Read https://docs.example.test/plain."
	repo := &fakeBookmarks{found: true, detail: BookmarkDetail{ID: 7, Text: text}}
	pages := map[string]Page{
		"b":           {Kind: KindBookmarks},
		"whole":       {Kind: KindAnswer, Text: text},
		"whole-plain": {Kind: KindAnswer, Text: plainURL},
	}
	_, c, owner := bookmarkServer(t, pages, repo)
	code, whole := c.do("GET", "/api/page?id=whole", owner, "")
	wholeHTML, _ := whole["html"].(string)
	if code != http.StatusOK || !strings.Contains(wholeHTML, `<a href="https://docs.example.test/guide">`) || strings.Contains(wholeHTML, `href="javascript:`) {
		t.Fatalf("Whole answer did not linkify the synthetic URL: %d %v", code, whole)
	}

	code, markdown := c.do("GET", "/api/bookmark?id=b&bookmark=7", owner, "")
	markdownHTML, _ := markdown["html"].(string)
	if code != http.StatusOK || markdownHTML != wholeHTML {
		t.Fatalf("Markdown bookmark links differ from Whole answer: %d %v, whole=%q", code, markdown, wholeHTML)
	}

	repo.detail = BookmarkDetail{ID: 7, Text: plainURL, Plain: true}
	code, plain := c.do("GET", "/api/bookmark?id=b&bookmark=7", owner, "")
	plainHTML, _ := plain["html"].(string)
	wholeCode, whole := c.do("GET", "/api/page?id=whole-plain", owner, "")
	wholePlainHTML, _ := whole["html"].(string)
	if code != http.StatusOK || wholeCode != http.StatusOK || plainHTML != wholePlainHTML {
		t.Fatalf("plain bookmark link differs from Whole answer: bookmark=%d %v, whole=%d %v", code, plain, wholeCode, whole)
	}

	repo.detail = BookmarkDetail{ID: 7, Text: "<script>unsafe</script> https://docs.example.test/plain. javascript:alert(1)", Plain: true}
	code, plain = c.do("GET", "/api/bookmark?id=b&bookmark=7", owner, "")
	plainHTML, _ = plain["html"].(string)
	if code != http.StatusOK || !strings.Contains(plainHTML, `<a href="https://docs.example.test/plain">https://docs.example.test/plain</a>.`) ||
		strings.Contains(plainHTML, `href="javascript:`) || strings.Contains(plainHTML, "<script>") || !strings.Contains(plainHTML, "&lt;script&gt;unsafe&lt;/script&gt;") {
		t.Fatalf("plain bookmark did not safely link HTTP(S) URLs: %d %v", code, plain)
	}
}

func TestBookmarkDetailAlreadyRemovedDoesNotReturnSavedText(t *testing.T) {
	repo := &fakeBookmarks{detail: BookmarkDetail{ID: 7, Text: "stale synthetic content"}, found: false}
	_, c, owner := bookmarkServer(t, map[string]Page{"b": {Kind: KindBookmarks}}, repo)
	code, body := c.do("GET", "/api/bookmark?id=b&bookmark=7", owner, "")
	if code != http.StatusNotFound || body["html"] != nil || body["error"] != "This bookmark is no longer available." {
		t.Fatalf("missing bookmark returned stale detail: %d %v", code, body)
	}
}

func TestBookmarkDetailUIConfirmsDeleteAndWiresSafeLinks(t *testing.T) {
	js := string(appJS)
	for _, want := range []string{
		`wireSafeWebLinks(content);`,
		`function safeWebURL(raw)`,
		`tg.openLink(url)`,
		`window.open(url, "_blank", "noopener,noreferrer")`,
		`message: "Remove “" + title + "” from " + source + "? Only the saved copy will be removed; the original Telegram message will not be deleted."`,
		`ok: "Delete bookmark", danger: true`,
		`button("Delete bookmark", "btn danger wide", "Delete bookmark: " + title)`,
		`actionStatus.setAttribute("aria-live", "polite")`,
		`actionStatus.textContent = "Removing bookmark…"`,
		`remove.disabled = true`,
		`fetchList(true, "Bookmark deleted. Refreshing the list…", "Bookmark deleted.")`,
		`api("api/bookmark?id=" + encodeURIComponent(id) + "&bookmark=" + encodeURIComponent(String(bookmarkID)), { op: "delete" })`,
		`remove.textContent = "Removing…"`,
		`actionStatus.setAttribute("role", "alert")`,
		`if (!hasKeep && keep !== "" && keepLabel)`,
		`filter.focus({ preventScroll: true })`,
		`window.scrollTo(0, y)`,
		`fetchList(true, "This bookmark was removed elsewhere. Refreshing the list…", "This bookmark was removed elsewhere.")`,
		`wireFileMentions(page, "");`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks bookmark behavior %q", want)
		}
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
