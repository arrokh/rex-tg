package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/format"
	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
	"rex-tg/internal/turn"
)

// Drafts (design section 8.1a). While an agent writes, its topic shows the
// text as a Telegram draft (sendMessageDraft): a preview of about 30 seconds
// that never becomes a message. A real message from the bot, such as the
// answer, ends it. So a draft can never be a second copy of an answer, and
// no draft needs the at-most-once rules of the outbox.
//
// The text comes from two places. pi's extension writes the message that pi
// is writing to a small file in cfg.LiveDir(), so a pi draft grows word by
// word. Claude Code and Codex write only complete blocks to their transcripts,
// so their draft shows the latest commentary block (turn.Tracker.Streaming).
//
// The sender sends one request at a time, drafts last. A draft is closed in
// the core before the answer's outbox row exists, and the request checks that
// at the moment it is sent, so no draft can come after the answer.

const (
	// maxDraftRunes is how much of the end of the text a draft shows.
	maxDraftRunes = 3000
	// liveMaxAge is how old a live file of pi may be and still count: an
	// extension that stopped leaves its file behind.
	liveMaxAge = 30 * time.Second
	// maxDraftRefusals is how many refused drafts in a row turn drafts off.
	maxDraftRefusals = 3
	// draftRefreshFor is how long after its last change a draft is still
	// refreshed; then it may fade.
	draftRefreshFor = 2 * time.Minute
	// liveStale is the age at which the daemon removes a live file that pi
	// left behind (pi was killed).
	liveStale = 10 * time.Minute
)

// errDraftClosed reports that the turn's answer came before the draft left.
var errDraftClosed = errors.New("draft closed")

// draft is the draft of one turn in a topic (core, except closed).
type draft struct {
	chat, topic int64
	id          int64  // the draft_id: non-zero, one per turn
	src         string // the text that want was rendered from
	want        string // the HTML to show
	shown       string // the HTML that Telegram has
	sentAt      time.Time
	changedAt   time.Time   // when want last changed
	busy        bool        // a request is on its way
	closed      atomic.Bool // the turn's answer is out: no more drafts
}

// evDraftSent reports the end of a draft request.
type evDraftSent struct {
	thread int64
	d      *draft
	html   string
	err    error
}

// liveText is what pi's extension last wrote for a topic.
type liveText struct {
	text string
	at   time.Time
}

// evLive reports a change in the live folder: a session's text, or gone.
type evLive struct {
	session string
	text    string
	at      time.Time
	gone    bool
}

// streamDraft updates the draft of a topic from its tracker (core). It runs
// at every tick.
func (d *daemon) streamDraft(th store.Thread, tr *turn.Tracker, now time.Time) {
	if !d.cfg.Display.Stream || d.draftsOff {
		return
	}
	text, ok := tr.Streaming()
	if !ok || th.Status != store.ThreadLive {
		d.closeDraft(th.ID)
		return
	}
	live := false
	if lt, has := d.liveText[th.ID]; has && now.Sub(lt.at) < liveMaxAge {
		text, live = lt.text, true
	}
	dr := d.drafts[th.ID]
	if dr != nil && dr.src == text {
		d.pushDraft(th.ID, dr, now) // unchanged: only the refresh may be due
		return
	}
	src := text
	if live {
		// pi's text may end inside a word, such as a key that is half
		// written and too short to be found yet: hold that word back.
		text = format.Redact(wholeWords(text))
	}
	html := draftHTML(text)
	if html == "" {
		return
	}
	if dr == nil {
		dr = &draft{chat: th.ChatID, topic: th.MessageThreadID, id: newDraftID()}
		d.drafts[th.ID] = dr
	}
	if html != dr.want {
		dr.changedAt = now
	}
	dr.src, dr.want = src, html
	d.pushDraft(th.ID, dr, now)
}

// wholeWords cuts text that pi is still writing after its last whole word.
func wholeWords(s string) string {
	if s == "" || strings.ContainsAny(s[len(s)-1:], " \t\n") {
		return s
	}
	if i := strings.LastIndexAny(s, " \t\n"); i >= 0 {
		return s[:i]
	}
	return "" // one word so far
}

// newDraftID returns a non-zero draft_id. A new turn gets a new one, so its
// draft does not animate out of the last turn's.
func newDraftID() int64 { return rand.Int64N(1<<53-1) + 1 }

// draftHTML renders the end of an agent's text for a draft, or "".
func draftHTML(md string) string {
	md = strings.TrimSpace(md)
	if md == "" {
		return ""
	}
	if r := []rune(md); len(r) > maxDraftRunes {
		cut := string(r[len(r)-maxDraftRunes:])
		// Start at a line, so a cut never starts inside a Markdown mark.
		if i := strings.IndexByte(cut, '\n'); i >= 0 && i < len(cut)-1 {
			cut = cut[i+1:]
		}
		md = "…\n" + cut
	}
	parts := format.Messages(md, partLimit, partLimit)
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1].HTML
}

// pushDraft sends a draft's text when it changed and the last one is old
// enough, and sends it again before Telegram lets it fade (core). One request
// per draft runs at a time.
func (d *daemon) pushDraft(thread int64, dr *draft, now time.Time) {
	if dr.busy || dr.closed.Load() {
		return
	}
	every := d.timing.Draft // tests only
	if every <= 0 {
		every = d.cfg.Display.StreamInterval.Duration
	}
	changed := dr.want != dr.shown && now.Sub(dr.sentAt) >= every
	fading := dr.shown != "" && now.Sub(dr.sentAt) >= d.timing.DraftKeep && now.Sub(dr.changedAt) < draftRefreshFor
	if !changed && !fading {
		return
	}
	dr.busy, dr.sentAt = true, now
	html, chat, topic, id := dr.want, dr.chat, dr.topic, dr.id
	d.spawn(func() {
		err := d.snd.Call(sender.PriorityDraft, func(ctx context.Context, tg *telegram.Client) error {
			if dr.closed.Load() {
				return errDraftClosed
			}
			return tg.SendMessageDraft(ctx, chat, topic, id, html, "HTML")
		})
		d.emit(evDraftSent{thread: thread, d: dr, html: html, err: err})
	})
}

// onDraftSent takes the result of a draft request (core). A draft that
// failed stays as it is: the next change, at most one per Draft interval,
// tries again. A deleted topic is found by the other sends.
func (d *daemon) onDraftSent(e evDraftSent) {
	dr := e.d
	dr.busy = false
	switch {
	case e.err == nil:
		dr.shown = e.html
		d.draftRefused = 0
		if !d.draftWorked {
			d.draftWorked = true
			d.log.Info("drafts: Telegram took the first draft", "thread", e.thread)
		}
	case errors.Is(e.err, errDraftClosed), d.ctx.Err() != nil:
	case telegram.Classify(e.err) == telegram.ClassBadRequest:
		// Telegram may not take drafts here (an older Bot API, or a chat that
		// does not allow them). After a few refusals in a row, stop trying
		// until the next start.
		d.draftRefused++
		if d.draftRefused >= maxDraftRefusals && !d.draftsOff {
			d.draftsOff = true
			d.log.Warn("drafts are off until the next start", "err", e.err)
		}
	default:
		d.log.Debug("draft", "thread", e.thread, "err", e.err) // the next change tries again
	}
}

// closeDraft ends the draft of a topic (core): its answer is out, or the
// turn is held or gone. A request on its way checks this before it is sent.
func (d *daemon) closeDraft(thread int64) {
	if dr := d.drafts[thread]; dr != nil {
		dr.closed.Store(true)
		delete(d.drafts, thread)
	}
}

// liveLoop watches the live folder of pi's extension and reports each change
// (worker). A file holds the text of the message that a pi session is
// writing; the extension removes it when the message is complete.
func (d *daemon) liveLoop(ctx context.Context) {
	dir := d.cfg.LiveDir()
	type file struct {
		mod     time.Time
		session string
	}
	seen := map[string]file{} // by file name: what was read last
	t := time.NewTicker(d.timing.LiveCheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			d.log.Debug("live folder", "err", err)
		}
		now := map[string]bool{}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".json") || e.IsDir() {
				continue
			}
			now[name] = true
			info, err := e.Info()
			if err != nil || info.ModTime().Equal(seen[name].mod) {
				continue
			}
			if time.Since(info.ModTime()) > liveStale {
				_ = os.Remove(filepath.Join(dir, name)) // pi stopped without removing it
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			var f struct {
				SessionID string `json:"session_id"`
				Text      string `json:"text"`
			}
			if json.Unmarshal(b, &f) != nil || f.SessionID == "" {
				continue
			}
			seen[name] = file{mod: info.ModTime(), session: f.SessionID}
			d.emit(evLive{session: f.SessionID, text: f.Text, at: info.ModTime()})
		}
		for name, f := range seen {
			if !now[name] {
				delete(seen, name)
				d.emit(evLive{session: f.session, gone: true})
			}
		}
	}
}

// onLive keeps the text that pi is writing for its topic (core).
func (d *daemon) onLive(e evLive) {
	conv, ok, err := d.st.ConversationByNative(d.ctx, agents.Pi, e.session)
	if err != nil || !ok {
		return
	}
	th, ok, err := d.st.ThreadByConversation(d.ctx, conv.ID)
	if err != nil || !ok {
		return
	}
	if e.gone {
		delete(d.liveText, th.ID)
		return
	}
	d.liveText[th.ID] = liveText{text: e.text, at: e.at}
}
