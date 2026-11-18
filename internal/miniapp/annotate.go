package miniapp

import (
	"encoding/json"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The Annotate page shows the agent's answers in a topic, newest first, ten
// at a time. The owner adds notes to parts of them; the page joins the notes
// into one message. Current pages submit through a one-time, page-bound action.
// A keyboard page needs its current key and the owner's verified Tailscale
// login; a signed page needs fresh Telegram init data. A draft alone sends
// nothing. Legacy web_app_data drafts remain supported for old open pages.

// annotatePage is how many answers one request returns.
const annotatePage = 10

// maxDraftBytes is the longest draft.
const maxDraftBytes = 16000

// AnnotateList is one request's answers.
type AnnotateList struct {
	Title string         `json:"title"`
	Bot   string         `json:"bot"` // the bot's username, for t.me links
	Items []AnnotateItem `json:"items"`
	More  bool           `json:"more"` // older answers exist
}

// AnnotateItem is one answer, rendered.
type AnnotateItem struct {
	ID   int64  `json:"id"`
	At   string `json:"at"` // "Jan 2 15:04"
	HTML string `json:"html"`
}

// launchPage answers a keyboard button: the page to open (launch.go).
func (s *Server) launchPage(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r) {
		return
	}
	if s.launch == nil {
		http.Error(w, `{"error":"The buttons' pages are off."}`, http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	view := q.Get("go")
	switch view {
	case ViewPanel, ViewScreen, ViewFiles, ViewChanges, ViewAnnotate:
	default:
		http.Error(w, `{"error":"This button is not known."}`, http.StatusBadRequest)
		return
	}
	var thread int64
	if t := q.Get("t"); t != "" {
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil || n <= 0 {
			http.Error(w, `{"error":"This button is not known."}`, http.StatusBadRequest)
			return
		}
		thread = n
	}
	_ = json.NewEncoder(w).Encode(s.launch.Launch(r.Context(), view, thread))
}

// annotateList answers the answers of an Annotate page.
func (s *Server) annotateList(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r) {
		return
	}
	p, ok := s.loadKind(w, r, KindAnnotate)
	if !ok {
		return
	}
	if s.annot == nil {
		http.Error(w, `{"error":"Annotate is off."}`, http.StatusNotFound)
		return
	}
	var before int64
	if b := r.URL.Query().Get("before"); b != "" {
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil || n <= 0 {
			http.Error(w, `{"error":"This list is not valid."}`, http.StatusBadRequest)
			return
		}
		before = n
	}
	items, more, err := s.annot.Answers(r.Context(), p.Thread, p.Conv, before, annotatePage)
	if err != nil {
		http.Error(w, `{"error":"Could not read the answers. Try again."}`, http.StatusServiceUnavailable)
		return
	}
	out := AnnotateList{Title: p.Title, Bot: s.annot.BotName(), Items: make([]AnnotateItem, 0, len(items)), More: more}
	for _, it := range items {
		body := ""
		if it.Plain {
			body = RenderPlain(it.Markdown)
		} else {
			body = RenderAnswer(it.Markdown)
		}
		out.Items = append(out.Items, AnnotateItem{ID: it.ID, At: it.At.Local().Format("Jan 2 15:04"), HTML: body})
	}
	_ = json.NewEncoder(w).Encode(out)
}

// annotateDraft keeps a draft for an older Annotate page. It sends nothing:
// that page then gives the draft's id to Telegram through sendData.
func (s *Server) annotateDraft(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r) {
		return
	}
	p, ok := s.loadKind(w, r, KindAnnotate)
	if !ok {
		return
	}
	if s.annot == nil {
		http.Error(w, `{"error":"Annotate is off."}`, http.StatusNotFound)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, `{"error":"Send JSON."}`, http.StatusUnsupportedMediaType)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 4*maxDraftBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, `{"error":"These notes are not valid."}`, http.StatusBadRequest)
		return
	}
	switch {
	case strings.TrimSpace(body.Text) == "":
		http.Error(w, `{"error":"Write a note first."}`, http.StatusBadRequest)
		return
	case len(body.Text) > maxDraftBytes || !utf8.ValidString(body.Text) || strings.ContainsRune(body.Text, 0):
		http.Error(w, `{"error":"These notes are too long. Send fewer, or shorter ones."}`, http.StatusBadRequest)
		return
	}
	id, err := s.annot.SaveDraft(r.Context(), p.Thread, p.Conv, body.Text)
	if err != nil {
		http.Error(w, `{"error":"Could not keep the notes. Try again."}`, http.StatusServiceUnavailable)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"draft": id})
}

// annotateSend sends the notes of an Annotate or answer page to its topic's
// agent, with an action id so retries do not send twice. A signed page needs
// fresh Telegram init data. A keyboard page needs the current key and the
// owner's verified Tailscale login (keyCanSendNotes).
func (s *Server) annotateSend(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r) {
		return
	}
	keyed := s.keyed(r)
	if keyed && !s.keyCanSendNotes(r) {
		http.Error(w, `{"error":"Sending notes requires your verified Tailscale login."}`, http.StatusForbidden)
		return
	}
	p, ok := s.loadKind(w, r, "")
	if !ok {
		return
	}
	if (p.Kind != KindAnnotate && p.Kind != KindAnswer) || p.Thread <= 0 || p.Conv <= 0 {
		http.Error(w, `{"error":"This page is from an older version of rex-tg and cannot send notes. Use Copy."}`, http.StatusBadRequest)
		return
	}
	if s.control == nil {
		http.Error(w, `{"error":"Sending notes is off."}`, http.StatusNotFound)
		return
	}
	if !keyed {
		if _, err := CheckInitData(r.Header.Get("X-Telegram-Init-Data"), s.secret, s.now(), ActMaxAge); err != nil {
			http.Error(w, `{"error":"This page was opened more than an hour ago. Close it and open it again."}`, http.StatusForbidden)
			return
		}
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, `{"error":"Send JSON."}`, http.StatusUnsupportedMediaType)
		return
	}
	var body struct {
		Act  string `json:"act"`
		Text string `json:"text"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 4*maxDraftBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, `{"error":"These notes are not valid."}`, http.StatusBadRequest)
		return
	}
	a := ControlAction{Op: OpNotes, Act: body.Act, Thread: p.Thread, Conv: p.Conv, Text: body.Text}
	if err := a.Check(); err != nil {
		b, _ := json.Marshal(map[string]string{"error": capital(err.Error()) + "."})
		http.Error(w, string(b), http.StatusBadRequest)
		return
	}
	_ = json.NewEncoder(w).Encode(s.control.Do(r.Context(), a))
}

// RenderPlain renders plain text as HTML paragraphs: a blank line starts a
// paragraph, and a line break stays one.
func RenderPlain(text string) string {
	var b strings.Builder
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		para = strings.Trim(para, "\n")
		if strings.TrimSpace(para) == "" {
			continue
		}
		b.WriteString("<p>")
		b.WriteString(strings.ReplaceAll(html.EscapeString(para), "\n", "<br>"))
		b.WriteString("</p>\n")
	}
	return b.String()
}
