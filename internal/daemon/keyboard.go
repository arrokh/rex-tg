package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	"rex-tg/internal/inbound"
	"rex-tg/internal/miniapp"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// The persistent reply keyboard is the same in every topic and in the main
// chat (inbound.Buttons).
//
// With the Mini App on, each button is a web_app button: one tap opens its
// page. Its address carries a key (kv "kb_key"), because Telegram gives a
// Mini App that a keyboard button opens no signed init data. The key only
// shows; whatever acts stays signed (see package miniapp). Without the Mini
// App, a tap sends the button's label as the user's message, and
// inbound.RouteText reads it as its command, so a button is never typed into
// a pane.
// Telegram keeps the keyboard until another one replaces it. Whether a
// client keeps one keyboard per topic or one for the whole chat is not
// settled, so each place that should show it gets it, without the user doing
// anything: after an update that changes the buttons, the main chat and every
// open topic get one short note that carries it; a new topic gets it with its
// first message; and /help brings it back anywhere.

// keyboardVersion names the buttons. A new value posts the keyboard note to
// the main chat and every open topic once more, after an update that changes
// the buttons.
const keyboardVersion = "6" // Queue adds access to a topic's waiting messages

const (
	keyboardKey = "reply_keyboard" // kv: the version, key generation, and Mini App address that the chats have
	kbKeyKV     = "kb_key"         // kv: the key in the buttons' addresses
	kbGenKV     = "kb_gen"         // kv: how often the key was made new (/rekey)
)

const keyboardTopicNote = "⌨️ Buttons under the message box: 🎛 Panel (/rt), 🖥 Screen, 📁 Project, ⚙️ Config, ✍️ Annotate, 🧩 Templates, and 📥 Queue (/queue). " +
	"With the Mini App on, the page buttons open directly; Queue shows waiting messages in chat. Tap ⊞ in the message box to show or hide them."

const keyboardNote = keyboardTopicNote + " /help brings them back."

// tailnetHint ends the keyboard note when the pages open only inside the
// tailnet, so a phone without Tailscale does not wonder why they fail.
func (d *daemon) tailnetHint() string {
	if !d.miniAppOn() || !d.cfg.MiniApp.TailnetOnly {
		return ""
	}
	return " The pages open only inside your tailnet: keep Tailscale on on this phone."
}

const rekeyText = "🔑 New key for the buttons. The old buttons stop working; every topic got new ones."

const annotateText = "✍️ Annotate opens from its button under the message box. It needs the Mini App (<code>[miniapp] url</code>)."

// buttonViews maps a button's command to the page that its web_app button
// opens.
var buttonViews = map[string]string{
	"rt": miniapp.ViewPanel, "screen": miniapp.ViewScreen, "project": miniapp.ViewProject,
	"config": miniapp.ViewConfig, "annotate": miniapp.ViewAnnotate, "templates": miniapp.ViewTemplates,
}

// kbSnap is what the keyboard is made from. url is empty when the buttons
// are text buttons.
type kbSnap struct {
	key, gen, url string
}

// keyboardSource holds the key and address of the buttons. The sender reads
// it from its goroutine and the Mini App's server from HTTP goroutines, so it
// is an atomic value.
type keyboardSource struct {
	snap atomic.Pointer[kbSnap]
}

// miniURLHash is the short hash of the Mini App's address in the keyboard
// value; "-" for none.
func miniURLHash(u string) string {
	if u == "" {
		return "-"
	}
	h := sha256.Sum256([]byte(u))
	return hex.EncodeToString(h[:3])
}

// keyboardValue is the value of kv "reply_keyboard".
func keyboardValue(gen, miniURL string) string {
	return keyboardVersion + ":" + gen + ":" + miniURLHash(miniURL)
}

// KeyboardKV returns the key-value pair that marks the keyboard note as
// posted, for a start without the Mini App. Tests set it to start without
// the note.
func KeyboardKV() (key, value string) { return keyboardKey, keyboardValue("1", "") }

// KeyboardKVFor is KeyboardKV for a start with the Mini App at miniURL.
func KeyboardKVFor(miniURL string) (key, value string) {
	return keyboardKey, keyboardValue("1", strings.TrimSpace(miniURL))
}

func (k *keyboardSource) value() string {
	s := k.snap.Load()
	if s == nil {
		return keyboardValue("1", "")
	}
	return keyboardValue(s.gen, s.url)
}

// keyboardURL is the address of a web_app button: <base>/app?k=<key>&go=<view>
// and &t=<store thread id> in a topic.
func keyboardURL(base, key, view string, thread int64) string {
	u := strings.TrimRight(base, "/") + "/app?k=" + url.QueryEscape(key) + "&go=" + url.QueryEscape(view)
	if thread != 0 {
		u += "&t=" + strconv.FormatInt(thread, 10)
	}
	return u
}

// keyboardFor builds the keyboard of a topic (thread is the store's thread id)
// or of the main chat (0).
func keyboardFor(s *kbSnap, thread int64) *telegram.ReplyKeyboardMarkup {
	var rows [][]telegram.KeyboardButton
	for i, b := range inbound.Buttons {
		if i%3 == 0 { // three buttons per row
			rows = append(rows, nil)
		}
		btn := telegram.KeyboardButton{Text: b.Label}
		if view, ok := buttonViews[b.Command]; ok && s != nil && s.url != "" {
			btn.WebApp = &telegram.WebAppInfo{URL: keyboardURL(s.url, s.key, view, thread)}
		}
		rows[len(rows)-1] = append(rows[len(rows)-1], btn)
	}
	return &telegram.ReplyKeyboardMarkup{Keyboard: rows, IsPersistent: true, ResizeKeyboard: true}
}

// forPart is the sender's Keyboard hook: a topic's first message, its
// keyboard note, its /help answer, and an attached topic's link card carry
// the keyboard. None is edited later.
func (k *keyboardSource) forPart(p store.Part) *telegram.ReplyKeyboardMarkup {
	if p.Kind != "notice" || p.Part != 0 {
		return nil
	}
	if strings.HasPrefix(p.Key, "intro:") || strings.HasPrefix(p.Key, "attach:") || strings.HasPrefix(p.Key, "keyboard:") ||
		(strings.HasPrefix(p.Key, "reply:") && strings.HasSuffix(p.Key, ":help")) {
		return keyboardFor(k.snap.Load(), p.ThreadID)
	}
	return nil
}

// mainKeyboard is the keyboard of the main chat.
func (d *daemon) mainKeyboard() *telegram.ReplyKeyboardMarkup {
	return keyboardFor(d.kb.snap.Load(), 0)
}

// initKeyboard loads the key, or makes one, and decides whether the buttons
// open the Mini App. It runs at the start, before showKeyboard and before the
// Mini App's server starts, so the first broadcast already carries web_app
// buttons when the Mini App is set up (the server comes up a moment later).
func (d *daemon) initKeyboard(ctx context.Context) {
	key, ok, _ := d.st.Get(ctx, kbKeyKV)
	gen, _, _ := d.st.Get(ctx, kbGenKV)
	if gen == "" {
		gen = "1"
	}
	if !ok || len(key) != 64 {
		key = newKey()
		if err := d.st.Set(ctx, kbKeyKV, key); err != nil {
			d.log.Warn("save the keyboard key", "err", err)
		}
	}
	if err := d.st.Set(ctx, kbGenKV, gen); err != nil {
		d.log.Warn("save the key generation", "err", err)
	}
	s := &kbSnap{key: key, gen: gen}
	if d.miniAppOn() && len(d.miniSecret) > 0 {
		s.url = strings.TrimSpace(d.cfg.MiniApp.URL)
	}
	d.kb.snap.Store(s)
}

// newKey makes a key: 32 random bytes as hex.
func newKey() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("daemon: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// keyOK checks a key from a button's address, in constant time.
func (d *daemon) keyOK(k string) bool {
	s := d.kb.snap.Load()
	if s == nil || k == "" || len(k) != len(s.key) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(k), []byte(s.key)) == 1
}

// rekey makes a new key; the old one stops working at once. Every chat gets
// the new buttons (core). It returns false, and keeps the old key, when the
// store does not keep the new one.
func (d *daemon) rekey(ctx context.Context) bool {
	old := d.kb.snap.Load()
	if old == nil {
		return false
	}
	gen := "1"
	if n, err := strconv.Atoi(old.gen); err == nil {
		gen = strconv.Itoa(n + 1)
	}
	key := newKey()
	// The new key counts only once it is kept: else a restart would bring
	// the old key back, which /rekey promised to stop.
	if err := d.st.Set(ctx, kbKeyKV, key); err != nil {
		d.log.Warn("save the keyboard key", "err", err)
		return false
	}
	if err := d.st.Set(ctx, kbGenKV, gen); err != nil {
		d.log.Warn("save the key generation", "err", err)
	}
	d.kb.snap.Store(&kbSnap{key: key, gen: gen, url: old.url})
	d.logins.forget() // a new key starts the learned Tailscale logins over
	d.broadcastKeyboard(ctx)
	return true
}

// sayKeys is say with the reply keyboard: a linked topic's own (a main
// command such as /start may come from one), else the main chat's.
// threadID is Telegram's message_thread_id.
func (d *daemon) sayKeys(chatID, threadID int64, html string) {
	kb := d.mainKeyboard()
	if threadID != 0 {
		if th, ok, err := d.st.ThreadByTopic(d.ctx, chatID, threadID); err == nil && ok && th.Status != store.ThreadClosed {
			kb = keyboardFor(d.kb.snap.Load(), th.ID)
		}
	}
	d.spawn(func() { _, _ = d.sendMsgKeys(chatID, threadID, html, nil, kb, true) })
}

// showKeyboard posts the keyboard note to the main chat and to every open
// topic the first time this version of the buttons runs, so the buttons show
// everywhere without the user doing anything.
func (d *daemon) showKeyboard(ctx context.Context) {
	if d.ownerChat == 0 {
		return
	}
	if v, ok, _ := d.st.Get(ctx, keyboardKey); ok && v == d.kb.value() {
		return
	}
	d.broadcastKeyboard(ctx)
}

// broadcastKeyboard posts the note to the main chat and every open topic.
// Each topic's note goes through the outbox under "keyboard:<value>": at most
// once per topic and value, also after a crash. The main chat's note is
// marked as posted before it is sent, so it is never sent twice; /help shows
// the buttons again.
func (d *daemon) broadcastKeyboard(ctx context.Context) {
	if d.ownerChat == 0 {
		return
	}
	value := d.kb.value()
	threads, err := d.st.Threads(ctx)
	if err != nil {
		d.log.Warn("keyboard: list topics", "err", err)
	}
	for _, th := range threads {
		if th.Status == store.ThreadClosed {
			continue
		}
		d.enqueue(store.OutboxItem{
			ThreadID: th.ID, Key: "keyboard:" + value, Kind: "notice", Silent: true,
			Parts: []store.PartContent{{Content: keyboardTopicNote + d.tailnetHint(), Format: store.FormatHTML}},
		})
	}
	if err := d.st.Set(ctx, keyboardKey, value); err != nil {
		d.log.Warn("save keyboard version", "err", err)
		return
	}
	d.sayKeys(d.ownerChat, 0, keyboardNote+d.tailnetHint())
}

// keyboardShown records that the owner's chat has the current keyboard, so
// that the next start does not post the note.
func (d *daemon) keyboardShown(ctx context.Context) {
	if err := d.st.Set(ctx, keyboardKey, d.kb.value()); err != nil {
		d.log.Warn("save keyboard version", "err", err)
	}
}
