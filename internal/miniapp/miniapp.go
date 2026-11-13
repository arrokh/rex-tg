// Package miniapp serves rex-tg's Telegram Mini App: pages that are too big
// for a chat message, such as an agent's long answer or the changes of a
// turn, the live screen of a topic's pane, and the files of its folder. The
// daemon keeps the pages and reads the screens; this package checks who
// asks and renders them.
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
	"sync/atomic"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// Page is what the Mini App shows.
type Page struct {
	Kind   string    `json:"kind"`             // KindAnswer, KindDiff, KindScreen or KindFiles
	Title  string    `json:"title"`            // a short title
	Text   string    `json:"text"`             // Markdown (an answer) or a unified diff
	Thread int64     `json:"thread,omitempty"` // the topic of a screen or files page
	Dir    string    `json:"dir,omitempty"`    // the folder of a files page
	At     time.Time `json:"at"`
}

// Kinds of pages.
const (
	KindAnswer = "answer"
	KindDiff   = "diff"
	KindScreen = "screen" // the live screen of a topic's pane
	KindFiles  = "files"  // the files of a folder
)

// Screen is what the live screen shows: the pane's text, or why there is
// none (Error).
type Screen struct {
	Text  string `json:"text,omitempty"`
	At    string `json:"at,omitempty"` // when it was read, "15:04:05"
	Error string `json:"error,omitempty"`
}

// maxSkew is how far in the future a signature's time may be (clocks differ).
const maxSkew = 5 * time.Minute

// MaxAge is how old a signed request may be. Telegram signs the init data
// when the Mini App opens; a page that stays open longer asks again.
const MaxAge = 24 * time.Hour

// Server serves the Mini App.
type Server struct {
	secret []byte       // HMAC key from the bot token
	owner  func() int64 // the paired owner's user id (0: none)
	load   func(id string) (Page, bool)
	screen func(Page) Screen   // nil: no live screen
	files  func() bool         // whether the file browser is on (nil: off)
	redact func(string) string // cleans a file's text before it is shown
	now    func() time.Time
	log    *slog.Logger
	// refusedAt is when a refusal was last logged: the address is public,
	// so refusals are logged at most once a minute.
	refusedAt atomic.Int64
	refused   atomic.Int64 // refusals since that log line
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

// WithScreen turns on the live screen: read returns the screen of a screen
// page's topic. It is called for each refresh of an open page.
func (s *Server) WithScreen(read func(Page) Screen) *Server {
	s.screen = read
	return s
}

// WithFiles turns on the file browser while on() is true. redact cleans a
// file's text before it is shown.
func (s *Server) WithFiles(on func() bool, redact func(string) string) *Server {
	s.files, s.redact = on, redact
	return s
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
	if err != nil || now.Sub(time.Unix(at, 0)) > maxAge || time.Unix(at, 0).Sub(now) > maxSkew {
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

// Handler serves /app (the page shell), /app.js, /api/page?id=…,
// /api/screen?id=… and /api/files?id=…&path=…
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
	mux.HandleFunc("GET /api/screen", s.liveScreen)
	mux.HandleFunc("GET /api/files", s.fileView)
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

// allowed checks that a request is the owner's, signed by Telegram. It
// answers a refused request itself.
func (s *Server) allowed(w http.ResponseWriter, r *http.Request) bool {
	s.headers(w, "application/json")
	user, err := CheckInitData(r.Header.Get("X-Telegram-Init-Data"), s.secret, s.now(), MaxAge)
	owner := s.owner()
	if err != nil || owner == 0 || user != owner {
		s.refused.Add(1)
		if now := s.now().UnixNano(); s.log != nil && now-s.refusedAt.Load() >= int64(time.Minute) {
			s.refusedAt.Store(now)
			s.log.Info("mini app: refused requests", "count", s.refused.Swap(0), "last_reason", refusal(err, owner))
		}
		http.Error(w, `{"error":"Open this page from its button in Telegram."}`, http.StatusForbidden)
		return false
	}
	return true
}

// loadKind loads the page of the request's id. With kind set, the page must
// be of that kind. It answers a missing page itself.
func (s *Server) loadKind(w http.ResponseWriter, r *http.Request, kind string) (Page, bool) {
	p, ok := s.load(r.URL.Query().Get("id"))
	if !ok || (kind != "" && p.Kind != kind) {
		http.Error(w, `{"error":"This page is gone."}`, http.StatusNotFound)
		return Page{}, false
	}
	return p, true
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r) {
		return
	}
	p, ok := s.loadKind(w, r, "")
	if !ok {
		return
	}
	var body string
	switch p.Kind {
	case KindDiff:
		body = RenderDiff(p.Text)
	case KindScreen, KindFiles: // the page asks for its content itself
	default:
		body = RenderAnswer(p.Text)
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"kind": p.Kind, "title": p.Title, "html": body})
}

// liveScreen answers a refresh of a live screen.
func (s *Server) liveScreen(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r) {
		return
	}
	p, ok := s.loadKind(w, r, KindScreen)
	if !ok {
		return
	}
	sc := Screen{Error: "The live screen is off."}
	if s.screen != nil {
		sc = s.screen(p)
	}
	_ = json.NewEncoder(w).Encode(sc)
}

// fileView answers a folder or file of a files page.
func (s *Server) fileView(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r) {
		return
	}
	p, ok := s.loadKind(w, r, KindFiles)
	if !ok {
		return
	}
	if s.files == nil || !s.files() {
		http.Error(w, `{"error":"The file browser is off."}`, http.StatusNotFound)
		return
	}
	v, err := Browse(r.Context(), p.Dir, r.URL.Query().Get("path"), s.redact)
	if err != nil {
		http.Error(w, `{"error":"This file is not there, or it is hidden."}`, http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(v)
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
