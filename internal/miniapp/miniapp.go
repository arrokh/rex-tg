// Package miniapp serves rex-tg's Telegram Mini App: pages that are too big
// for a chat message, such as an agent's long answer or the changes of a
// turn. The daemon keeps the pages; this package only checks who asks and
// renders them.
//
// Every request for a page must carry the Mini App's init data, which
// Telegram signs with the bot's token. Only the owner's signed requests get
// a page. The page shell itself holds no data.
package miniapp

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// Page is what the Mini App shows.
type Page struct {
	Kind  string    `json:"kind"`  // KindAnswer or KindDiff
	Title string    `json:"title"` // a short title
	Text  string    `json:"text"`  // Markdown (an answer) or a unified diff
	At    time.Time `json:"at"`
}

// Kinds of pages.
const (
	KindAnswer = "answer"
	KindDiff   = "diff"
)

// MaxAge is how old a signed request may be. Telegram signs the init data
// when the Mini App opens; a page that stays open longer asks again.
const MaxAge = 24 * time.Hour

// Server serves the Mini App.
type Server struct {
	secret []byte       // HMAC key from the bot token
	owner  func() int64 // the paired owner's user id (0: none)
	load   func(id string) (Page, bool)
	now    func() time.Time
	log    *slog.Logger
}

// New makes a server. botToken is used only to derive the key that checks
// Telegram's signature; it is not kept.
func New(botToken string, owner func() int64, load func(string) (Page, bool), log *slog.Logger) *Server {
	return NewWithSecret(Secret(botToken), owner, load, log)
}

// NewWithSecret makes a server from the key that Secret derives, so the
// caller need not keep the token.
func NewWithSecret(secret []byte, owner func() int64, load func(string) (Page, bool), log *slog.Logger) *Server {
	return &Server{secret: secret, owner: owner, load: load, now: time.Now, log: log}
}

// Secret is the key that checks a Mini App's init data: HMAC-SHA256 of the
// bot token with the key "WebAppData" (Telegram's rule).
func Secret(botToken string) []byte {
	m := hmac.New(sha256.New, []byte("WebAppData"))
	m.Write([]byte(botToken))
	return m.Sum(nil)
}

// Errors of CheckInitData.
var (
	ErrNoSignature  = errors.New("miniapp: no signature")
	ErrBadSignature = errors.New("miniapp: bad signature")
	ErrTooOld       = errors.New("miniapp: signed too long ago")
	ErrNoUser       = errors.New("miniapp: no user")
)

// CheckInitData checks Telegram's signature on a Mini App's init data and
// returns the user it was signed for.
func CheckInitData(raw string, secret []byte, now time.Time, maxAge time.Duration) (int64, error) {
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return 0, ErrBadSignature
	}
	got := vals.Get("hash")
	if got == "" {
		return 0, ErrNoSignature
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+"="+vals.Get(k))
	}
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(strings.Join(lines, "\n")))
	want := hex.EncodeToString(m.Sum(nil))
	if !hmac.Equal([]byte(got), []byte(want)) {
		return 0, ErrBadSignature
	}
	at, err := strconv.ParseInt(vals.Get("auth_date"), 10, 64)
	if err != nil || now.Sub(time.Unix(at, 0)) > maxAge {
		return 0, ErrTooOld
	}
	var u struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(vals.Get("user")), &u); err != nil || u.ID == 0 {
		return 0, ErrNoUser
	}
	return u.ID, nil
}

//go:embed app.html
var appHTML []byte

//go:embed app.js
var appJS []byte

// Handler serves /app (the page shell), /app.js, and /api/page?id=…
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /app", func(w http.ResponseWriter, r *http.Request) {
		s.headers(w, "text/html; charset=utf-8")
		_, _ = w.Write(appHTML)
	})
	mux.HandleFunc("GET /app.js", func(w http.ResponseWriter, r *http.Request) {
		s.headers(w, "text/javascript; charset=utf-8")
		_, _ = w.Write(appJS)
	})
	mux.HandleFunc("GET /api/page", s.page)
	return mux
}

func (s *Server) headers(w http.ResponseWriter, ctype string) {
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self' https://telegram.org; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'")
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	s.headers(w, "application/json")
	user, err := CheckInitData(r.Header.Get("X-Telegram-Init-Data"), s.secret, s.now(), MaxAge)
	owner := s.owner()
	if err != nil || owner == 0 || user != owner {
		if s.log != nil {
			s.log.Info("mini app: refused a request", "reason", refusal(err, owner))
		}
		http.Error(w, `{"error":"Open this page from its button in Telegram."}`, http.StatusForbidden)
		return
	}
	p, ok := s.load(r.URL.Query().Get("id"))
	if !ok {
		http.Error(w, `{"error":"This page is gone."}`, http.StatusNotFound)
		return
	}
	body := RenderAnswer(p.Text)
	if p.Kind == KindDiff {
		body = RenderDiff(p.Text)
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"kind": p.Kind, "title": p.Title, "html": body})
}

func refusal(err error, owner int64) string {
	switch {
	case err != nil:
		return err.Error()
	case owner == 0:
		return "not paired"
	}
	return "not the owner"
}

// md renders Markdown with GitHub's tables and strikethrough. Raw HTML in the
// text is left out and dangerous links are dropped (goldmark's defaults).
var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

// RenderAnswer renders an answer's Markdown to HTML.
func RenderAnswer(text string) string {
	var b bytes.Buffer
	if err := md.Convert([]byte(text), &b); err != nil {
		return "<pre>" + html.EscapeString(text) + "</pre>"
	}
	return b.String()
}

// RenderDiff renders a unified diff: a block per file, added and removed
// lines marked.
func RenderDiff(diff string) string {
	var b strings.Builder
	open := false
	for _, l := range strings.Split(strings.TrimRight(diff, "\n"), "\n") {
		if strings.HasPrefix(l, "diff --git ") {
			if open {
				b.WriteString("</div>")
			}
			name := strings.TrimPrefix(l, "diff --git ")
			if i := strings.Index(name, " b/"); i >= 0 {
				name = name[i+3:]
			}
			b.WriteString(`<div class="file"><div class="name">` + html.EscapeString(name) + `</div>`)
			open = true
			continue
		}
		class := "ctx"
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"), strings.HasPrefix(l, "index "),
			strings.HasPrefix(l, "new file"), strings.HasPrefix(l, "deleted file"), strings.HasPrefix(l, "similarity"),
			strings.HasPrefix(l, "rename "), strings.HasPrefix(l, "old mode"), strings.HasPrefix(l, "new mode"):
			class = "meta"
		case strings.HasPrefix(l, "@@"):
			class = "hunk"
		case strings.HasPrefix(l, "+"):
			class = "add"
		case strings.HasPrefix(l, "-"):
			class = "del"
		}
		b.WriteString(`<div class="` + class + `">` + html.EscapeString(l) + "</div>")
	}
	if open {
		b.WriteString("</div>")
	}
	return `<div class="diff">` + b.String() + "</div>"
}
