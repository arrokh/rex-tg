package miniapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// KindBookmarks is the global, cross-topic saved-message page.
const KindBookmarks = "bookmarks"

// BookmarkItem is one saved message in the list.
type BookmarkItem struct {
	ID      int64
	TopicID int64
	Topic   string
	Title   string
	Preview string
	At      time.Time
}

// BookmarkTopic is a source topic available to the bookmarks filter.
type BookmarkTopic struct {
	ID   int64
	Name string
}

// BookmarkDetail is the full saved message.
type BookmarkDetail struct {
	ID      int64
	TopicID int64
	Topic   string
	Title   string
	Text    string
	Plain   bool
	Kind    string
	At      time.Time
}

// Bookmarks reads saved messages for the owner's global bookmarks page.
// List uses topicID -1 for all topics and topicID 0 for the main chat; beforeID
// 0 starts at the newest saved message.
type Bookmarks interface {
	List(context.Context, int64, int64, int) ([]BookmarkItem, []BookmarkTopic, bool, error) // topicID, beforeID, limit
	Get(context.Context, int64) (BookmarkDetail, bool, error)
}

const (
	bookmarksPageSize        = 20
	bookmarksAllTopics int64 = -1
)

// WithBookmarks turns on the global saved-message page.
func (s *Server) WithBookmarks(b Bookmarks) *Server {
	s.bookmarks = b
	return s
}

type bookmarkListItem struct {
	ID      int64     `json:"id"`
	TopicID int64     `json:"topic_id"`
	Topic   string    `json:"topic"`
	Title   string    `json:"title"`
	Preview string    `json:"preview"`
	At      time.Time `json:"at"`
}

type bookmarkTopicOption struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type bookmarkListResponse struct {
	Items  []bookmarkListItem    `json:"items"`
	Topics []bookmarkTopicOption `json:"topics"`
	More   bool                  `json:"more"`
}

type bookmarkDetailResponse struct {
	ID      int64     `json:"id"`
	TopicID int64     `json:"topic_id"`
	Topic   string    `json:"topic"`
	Title   string    `json:"title"`
	Kind    string    `json:"kind"`
	At      time.Time `json:"at"`
	HTML    string    `json:"html"`
}

// bookmarksList answers one bounded, newest-first page of bookmarks.
func (s *Server) bookmarksList(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r) {
		return
	}
	q, ok := bookmarkQuery(w, r, "topic", "before")
	if !ok {
		return
	}
	if _, ok := s.loadKind(w, r, KindBookmarks); !ok {
		return
	}
	if s.bookmarks == nil {
		http.Error(w, `{"error":"Bookmarks are off."}`, http.StatusNotFound)
		return
	}

	topicID, beforeID := bookmarksAllTopics, int64(0)
	if values, exists := q["topic"]; exists {
		if topicID, ok = positiveOrZero(values[0]); !ok {
			http.Error(w, `{"error":"This topic filter is not valid."}`, http.StatusBadRequest)
			return
		}
	}
	if values, exists := q["before"]; exists {
		if beforeID, ok = positive(values[0]); !ok {
			http.Error(w, `{"error":"This page of bookmarks is not valid."}`, http.StatusBadRequest)
			return
		}
	}

	items, topics, more, err := s.bookmarks.List(r.Context(), topicID, beforeID, bookmarksPageSize)
	if err != nil {
		http.Error(w, `{"error":"Could not read bookmarks. Try again."}`, http.StatusServiceUnavailable)
		return
	}
	if len(items) > bookmarksPageSize {
		items = items[:bookmarksPageSize]
		more = true
	}
	out := bookmarkListResponse{
		Items:  make([]bookmarkListItem, 0, len(items)),
		Topics: make([]bookmarkTopicOption, 0, len(topics)),
		More:   more,
	}
	for _, item := range items {
		out.Items = append(out.Items, bookmarkListItem(item))
	}
	for _, topic := range topics {
		out.Topics = append(out.Topics, bookmarkTopicOption(topic))
	}
	_ = json.NewEncoder(w).Encode(out)
}

// bookmarkDetail answers the full text of one saved message.
func (s *Server) bookmarkDetail(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r) {
		return
	}
	q, ok := bookmarkQuery(w, r, "bookmark")
	if !ok {
		return
	}
	if _, ok := s.loadKind(w, r, KindBookmarks); !ok {
		return
	}
	if s.bookmarks == nil {
		http.Error(w, `{"error":"Bookmarks are off."}`, http.StatusNotFound)
		return
	}
	bookmarkID, ok := positive(q.Get("bookmark"))
	if !ok {
		http.Error(w, `{"error":"This bookmark is not valid."}`, http.StatusBadRequest)
		return
	}
	detail, found, err := s.bookmarks.Get(r.Context(), bookmarkID)
	if err != nil {
		http.Error(w, `{"error":"Could not read this bookmark. Try again."}`, http.StatusServiceUnavailable)
		return
	}
	if !found || detail.ID != bookmarkID {
		http.Error(w, `{"error":"This bookmark is no longer available."}`, http.StatusNotFound)
		return
	}
	body := RenderAnswer(detail.Text)
	if detail.Plain {
		body = RenderPlain(detail.Text)
	}
	_ = json.NewEncoder(w).Encode(bookmarkDetailResponse{
		ID: detail.ID, TopicID: detail.TopicID, Topic: detail.Topic, Title: detail.Title,
		Kind: detail.Kind, At: detail.At, HTML: body,
	})
}

// bookmarkQuery validates the page id and named query parameters before the
// page adapter is called. Repeated values are rejected rather than guessed.
func bookmarkQuery(w http.ResponseWriter, r *http.Request, names ...string) (url.Values, bool) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		http.Error(w, `{"error":"This bookmarks request is not valid."}`, http.StatusBadRequest)
		return nil, false
	}
	if len(q["id"]) != 1 || q.Get("id") == "" {
		http.Error(w, `{"error":"This bookmarks page is not valid."}`, http.StatusBadRequest)
		return nil, false
	}
	for _, name := range names {
		if len(q[name]) > 1 {
			http.Error(w, `{"error":"This bookmarks request is not valid."}`, http.StatusBadRequest)
			return nil, false
		}
	}
	return q, true
}

func positive(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n > 0
}

func positiveOrZero(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n >= 0
}
