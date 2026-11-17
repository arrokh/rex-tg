// Package miniapp serves rex-tg's Telegram Mini App: pages that are too big
// for a chat message, such as an agent's long answer or the changes of a
// turn, the live screen of a topic's pane, the files of its folder, the
// changes of its repository, and the control panel (control.go). The
// daemon keeps the pages and reads the screens; this package checks who
// asks and renders them.
//
// Every request for a page must carry the Mini App's init data, which
// Telegram signs with the bot's token. Only the owner's signed requests get
// a page. The page shell itself holds no data.
package miniapp

import (
	"bytes"
	"context"
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
	Kind   string `json:"kind"`             // KindAnswer, KindDiff, KindScreen, KindFiles, KindChanges or KindControl
	Title  string `json:"title"`            // a short title
	Text   string `json:"text"`             // Markdown (an answer) or a unified diff
	Thread int64  `json:"thread,omitempty"` // the topic of a screen, files, changes, annotate or answer page (0 on answer pages made before Annotate)
	Dir    string `json:"dir,omitempty"`    // the folder of a files or changes page
	// Conv is the agent's session (conversation) of an Annotate or answer
	// page: the page shows only that session's answers, and its notes go
	// to the agent only while the topic is still in that session.
	Conv int64     `json:"conv,omitempty"`
	At   time.Time `json:"at"`
}

// Kinds of pages.
const (
	KindAnswer = "answer"
	KindDiff   = "diff"
	KindScreen = "screen" // the live screen of a topic's pane
	KindFiles  = "files"  // the files of a folder
	// KindChanges is the changes of a repository, read when the page asks.
	KindChanges = "changes"
)

// Screen is what the live screen shows: the pane's text, or why there is
// none (Error).
type Screen struct {
	Text  string `json:"text,omitempty"`
	VT    string `json:"vt,omitempty"` // the same lines with their colors (SGR sequences only); "" for none
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
	secret  []byte       // HMAC key from the bot token
	owner   func() int64 // the paired owner's user id (0: none)
	load    func(id string) (Page, bool)
	screen  func(Page) Screen    // nil: no live screen
	files   func() bool          // whether the file browser is on (nil: off)
	changes func() bool          // whether the changes view is on (nil: off)
	turn    func(Page) TurnRange // the last turn of a changes page's topic (nil: none)
	redact  func(string) string  // cleans a file's text before it is shown
	control Control              // the control panel (nil: off)
	home    string               // where the control panel's folder explorer starts
	launch  Launcher             // the keyboard buttons' pages (nil: off)
	keyOK   func(string) bool    // checks the keyboard's key (nil: no key works)
	annot   Annotations          // the Annotate page (nil: off)
	tailnet bool                 // refuse a request that did not come from the tailnet
	logins  Logins               // whose tailnet devices may use the key (nil: any)
	reads   reads                // folder and file reads that may wait for macOS (slowread.go)
	wait    time.Duration        // how long a request waits for such a read
	now     func() time.Time
	log     *slog.Logger
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
	return &Server{secret: secret, owner: owner, load: load, wait: slowRead, now: time.Now, log: log}
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

// WithChanges turns on the changes view while on() is true. redact cleans
// a diff before it is shown. turn returns the last turn of a page's topic
// (nil: no BaseTurn).
func (s *Server) WithChanges(on func() bool, redact func(string) string, turn func(Page) TurnRange) *Server {
	s.changes, s.redact, s.turn = on, redact, turn
	return s
}

// WithLaunch turns on the keyboard buttons' pages (launch.go). keyOK checks
// the key that a button's address carries; the key authorizes reads and
// validated control-panel actions.
func (s *Server) WithLaunch(l Launcher, keyOK func(string) bool) *Server {
	s.launch, s.keyOK = l, keyOK
	return s
}

// WithAnnotate turns on the Annotate page (annotate.go).
func (s *Server) WithAnnotate(a Annotations) *Server {
	s.annot = a
	return s
}

// WithTailnetOnly makes the server answer only requests that came through
// "tailscale serve" from a device of the tailnet: they carry the
// Tailscale-User-Login header, which Tailscale sets itself and removes when
// a client sends it. Funnel (public) requests carry Tailscale-Funnel-Request
// and no identity, so they are refused.
func (s *Server) WithTailnetOnly(on bool) *Server {
	s.tailnet = on
	return s
}

// Logins decides which Tailscale logins may open pages with the buttons'
// key, so that in a shared tailnet another person's device cannot use a key
// that leaked (the daemon keeps them). Used with WithTailnetOnly.
type Logins interface {
	// Owner notes a login whose request carried the owner's own Telegram
	// signature: that login is the owner's.
	Owner(login string)
	// KeyLoginOK tells whether a request with key may come from login. It
	// checks the key again, together with the logins, so a /rekey between
	// the two checks cannot let an old key teach a login.
	KeyLoginOK(key, login string) bool
}

// WithLogins limits the key to the owner's Tailscale logins.
func (s *Server) WithLogins(l Logins) *Server {
	s.logins = l
	return s
}

// tailnetLogin is the Tailscale login that tailscale serve put on r.
func tailnetLogin(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("Tailscale-User-Login"))
}

// fromTailnet tells whether r came through tailscale serve from a user's
// device in the tailnet.
func fromTailnet(r *http.Request) bool {
	return r.Header.Get("Tailscale-Funnel-Request") == "" && strings.TrimSpace(r.Header.Get("Tailscale-User-Login")) != ""
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

// xterm.js draws the live screen as a terminal (third_party/README.md).
var (
	//go:embed third_party/xterm-5.5.0.js
	xtermJS []byte
	//go:embed third_party/xterm-5.5.0.css
	xtermCSS []byte
)

// Handler serves /app (the page shell), /app.js, the terminal library,
// /api/page?id=…,
// /api/screen?id=…, /api/files?id=…&path=… and
// /api/changes?id=…&base=…[&path=…&new=1], and the control panel's
// /api/control?id=…, /api/control/folders?id=…&path=…[&hidden=1] and
// POST /api/control/do?id=…
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
	// The terminal library: versioned names, so they can be kept long.
	mux.HandleFunc("GET /xterm-5.5.0.js", func(w http.ResponseWriter, r *http.Request) {
		s.headers(w, "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
		_, _ = w.Write(xtermJS)
	})
	mux.HandleFunc("GET /xterm-5.5.0.css", func(w http.ResponseWriter, r *http.Request) {
		s.headers(w, "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
		_, _ = w.Write(xtermCSS)
	})
	mux.HandleFunc("GET /api/page", s.page)
	mux.HandleFunc("GET /api/screen", s.liveScreen)
	mux.HandleFunc("GET /api/files", s.fileView)
	mux.HandleFunc("GET /api/changes", s.changesView)
	mux.HandleFunc("GET /api/control", s.controlState)
	mux.HandleFunc("GET /api/control/folders", s.controlFolders)
	mux.HandleFunc("POST /api/control/do", s.controlDo)
	mux.HandleFunc("GET /api/launch", s.launchPage)
	mux.HandleFunc("GET /api/annotate", s.annotateList)
	mux.HandleFunc("POST /api/annotate/draft", s.annotateDraft)
	mux.HandleFunc("POST /api/annotate/send", s.annotateSend)
	if !s.tailnet {
		return mux
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !fromTailnet(r) {
			s.noteRefusal("not from the tailnet")
			if strings.HasPrefix(r.URL.Path, "/api/") {
				s.headers(w, "application/json")
				http.Error(w, `{"error":"The Mini App opens only inside your tailnet. Turn on Tailscale on this device."}`, http.StatusForbidden)
				return
			}
			// The page itself: Telegram shows this instead of raw JSON.
			s.headers(w, "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write(notInTailnetHTML)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// notInTailnetHTML is the page for a device outside the tailnet.
var notInTailnetHTML = []byte(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><title>rex-tg</title>
<style>body{font:17px/1.45 -apple-system,system-ui,sans-serif;margin:0;padding:28px 20px;color:#1c1c1e;background:#fff}
@media (prefers-color-scheme:dark){body{color:#f2f2f7;background:#1c1c1e}}h1{font-size:22px;margin:0 0 12px}</style></head>
<body><h1>Turn on Tailscale</h1><p>rex-tg's pages open only inside your tailnet. Turn on Tailscale on this device, then open the page again.</p></body></html>
`)

func (s *Server) headers(w http.ResponseWriter, ctype string) {
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self' https://telegram.org; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'")
}

// allowed checks that a request is the owner's: signed by Telegram, or
// carrying the keyboard's key. The key reads pages and can submit validated
// control-panel actions; it cannot send Annotate notes directly. It answers
// a refused request itself.
func (s *Server) allowed(w http.ResponseWriter, r *http.Request) bool {
	s.headers(w, "application/json")
	if r.Header.Get("X-Telegram-Init-Data") == "" && r.Header.Get("X-Rex-Key") != "" {
		if !s.keyed(r) {
			s.noteRefusal("wrong key")
			http.Error(w, `{"error":"This button is old. Send /help in the chat for new buttons."}`, http.StatusForbidden)
			return false
		}
		if s.tailnet && s.logins != nil && !s.logins.KeyLoginOK(r.Header.Get("X-Rex-Key"), tailnetLogin(r)) {
			s.noteRefusal("the key from another Tailscale login")
			http.Error(w, `{"error":"These buttons open only on your own Tailscale account. Open them on your phone, or send /rekey in the chat to start over."}`, http.StatusForbidden)
			return false
		}
		return true
	}
	user, err := CheckInitData(r.Header.Get("X-Telegram-Init-Data"), s.secret, s.now(), MaxAge)
	owner := s.owner()
	if err != nil || owner == 0 || user != owner {
		s.noteRefusal(refusal(err, owner))
		http.Error(w, `{"error":"Open this page from its button in Telegram."}`, http.StatusForbidden)
		return false
	}
	if s.tailnet && s.logins != nil {
		s.logins.Owner(tailnetLogin(r)) // Telegram vouches that this is the owner
	}
	return true
}

// keyed tells whether r carries the keyboard's key and no init data: a page
// that a keyboard button opened.
func (s *Server) keyed(r *http.Request) bool {
	k := r.Header.Get("X-Rex-Key")
	return k != "" && r.Header.Get("X-Telegram-Init-Data") == "" && s.keyOK != nil && s.owner() != 0 && s.keyOK(k)
}

// noteRefusal counts a refused request; the address may be reachable by
// others, so refusals are logged at most once a minute.
func (s *Server) noteRefusal(reason string) {
	s.refused.Add(1)
	if now := s.now().UnixNano(); s.log != nil && now-s.refusedAt.Load() >= int64(time.Minute) {
		s.refusedAt.Store(now)
		s.log.Info("mini app: refused requests", "count", s.refused.Swap(0), "last_reason", reason)
	}
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
	case KindScreen, KindFiles, KindChanges, KindControl, KindAnnotate: // the page asks for its content itself
	default:
		body = RenderAnswer(p.Text)
	}
	at := ""
	if !p.At.IsZero() {
		at = p.At.Local().Format("Jan 2 15:04")
	}
	out := map[string]any{"kind": p.Kind, "title": p.Title, "html": body, "at": at, "thread": p.Thread}
	if s.annot != nil && (p.Kind == KindAnswer || p.Kind == KindAnnotate) {
		out["bot"] = s.annot.BotName() // for t.me links (Annotate's "Put in message box")
	}
	_ = json.NewEncoder(w).Encode(out)
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

// filesKey is the key of a file browser read in Server.reads.
func filesKey(dir, rel string) string {
	return "files\x00" + dir + "\x00" + rel
}

// fileView answers a folder or file of a files page. The read may wait for
// macOS (slowread.go).
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
	rel := r.URL.Query().Get("path")
	// The read may wait for macOS (slowread.go), so it does not stop when
	// this request ends: a later request for the same path joins it.
	ctx := context.WithoutCancel(r.Context())
	got, err := s.reads.do(filesKey(p.Dir, rel), s.wait, func() (any, error) {
		return Browse(ctx, p.Dir, rel, s.redact)
	})
	if errors.Is(err, errSlowRead) {
		http.Error(w, slowReadText("this file or folder"), http.StatusServiceUnavailable)
		return
	}
	if errors.Is(err, errReadLimit) {
		http.Error(w, readLimitText("file or folder"), http.StatusServiceUnavailable)
		return
	}
	v, _ := got.(View)
	if err != nil {
		http.Error(w, `{"error":"This file is not there, or it is hidden."}`, http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// changesView answers the list of a changes page (base: BaseHead,
// BaseBranch, or "" to choose), or with path the change of one file (new=1:
// the new file that git does not track).
func (s *Server) changesView(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r) {
		return
	}
	p, ok := s.loadKind(w, r, KindChanges)
	if !ok {
		return
	}
	if s.changes == nil || !s.changes() {
		http.Error(w, `{"error":"The changes view is off."}`, http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	base := q.Get("base")
	var turn TurnRange
	if s.turn != nil {
		turn = s.turn(p)
	}
	if base != "" && base != BaseHead && base != BaseBranch && base != BaseTurn {
		http.Error(w, `{"error":"No such base."}`, http.StatusBadRequest)
		return
	}
	if rel := q.Get("path"); rel != "" {
		if base == "" {
			base = BaseHead
		}
		fc, err := ChangeOf(r.Context(), p.Dir, rel, base, q.Get("new") == "1", turn, s.redact)
		if err != nil {
			http.Error(w, `{"error":"This file has no changes now, or it is hidden."}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(fc)
		return
	}
	l, err := ListChanges(r.Context(), p.Dir, base, turn)
	switch {
	case errors.Is(err, ErrNotRepo):
		http.Error(w, `{"error":"This folder is not in a git repository now."}`, http.StatusNotFound)
		return
	case errors.Is(err, ErrNoBase) && base == BaseTurn:
		http.Error(w, `{"error":"No turn of the agent in this repository was kept yet."}`, http.StatusNotFound)
		return
	case errors.Is(err, ErrNoBase):
		http.Error(w, `{"error":"This repository has no main branch to compare with."}`, http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, `{"error":"Could not read the changes: git failed or took too long."}`, http.StatusInternalServerError)
		return
	}
	l.At = s.now().Format("15:04:05")
	_ = json.NewEncoder(w).Encode(l)
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
