package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"rex-tg/internal/format"
	"rex-tg/internal/inbound"
	"rex-tg/internal/miniapp"
	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// The keyboard's pages (Launch) and the Annotate page (Answers, SaveDraft,
// BotName). Current keyboard pages send notes through the page-bound action,
// with the current key and the owner's verified Tailscale login. The legacy
// web_app_data handler still accepts drafts from pages opened before this path.

var (
	_ miniapp.Launcher    = (*daemon)(nil)
	_ miniapp.Annotations = (*daemon)(nil)
)

const (
	// annotBase moves the ids of the queued rows of sent notes into a range
	// that real update ids and the /at rows (schedBase) never reach.
	annotBase = int64(1) << 51
	// annotDraftMaxAge is how long a draft may wait for its send.
	annotDraftMaxAge = time.Hour
	annotDraftPrefix = "annot_draft:"
	// annotMaxDrafts caps the drafts that wait at once: the page's key can
	// make drafts, and each holds up to 16 KB.
	annotMaxDrafts = 20

	annotNotSent = "✍️ These notes were not sent: the draft is gone. Open Annotate and send them again."
)

// annotDraft is notes that the page saved and the owner has not sent yet,
// for one topic and the session it showed.
type annotDraft struct {
	Thread int64  `json:"thread"`
	Conv   int64  `json:"conv"`
	Text   string `json:"text"`
	At     int64  `json:"at"`
}

func draftFresh(dr annotDraft, now time.Time) bool {
	age := now.Sub(time.Unix(dr.At, 0))
	return age >= 0 && age <= annotDraftMaxAge
}

// ---- Launch ----

// Launch returns the page that a keyboard button opens (any goroutine). It
// sends nothing to the chat.
func (d *daemon) Launch(ctx context.Context, view string, thread int64) miniapp.LaunchResult {
	fail := func(s string) miniapp.LaunchResult { return miniapp.LaunchResult{Error: s} }
	if !d.miniAppReady() {
		return fail("The Mini App is not ready.")
	}
	switch view {
	case miniapp.ViewPanel:
		id, err := d.livePage(miniapp.KindControl, 0, "main", "rex-tg", "")
		if err != nil {
			return fail("Could not open the control panel.")
		}
		return miniapp.LaunchResult{Page: id}
	case miniapp.ViewScreen, miniapp.ViewAnnotate, miniapp.ViewTemplates:
	case miniapp.ViewFiles:
		if !d.cfg.MiniApp.Files {
			return fail("The file browser is off ([miniapp] files).")
		}
	case miniapp.ViewChanges:
		if !d.cfg.MiniApp.Changes {
			return fail("The changes view is off ([miniapp] changes).")
		}
	default:
		return fail("Unknown page.")
	}
	var th store.Thread
	if thread != 0 {
		if t, err := d.st.Thread(ctx, thread); err == nil && t.Status != store.ThreadClosed {
			th = t
		}
	}
	if th.ID == 0 {
		return d.pickTopic(ctx)
	}
	switch view {
	case miniapp.ViewScreen:
		id, err := d.screenPage(th)
		if err != nil {
			return fail("Could not open the live screen.")
		}
		return miniapp.LaunchResult{Page: id}
	case miniapp.ViewAnnotate:
		// One page per topic and session: it shows only the answers of
		// the session the topic is in now, and names both.
		conv, err := d.st.Conversation(ctx, th.CurrentConversationID)
		if th.CurrentConversationID == 0 || err != nil {
			return fail("This topic has no agent session yet.")
		}
		title := "✍️ " + topicShownName(th) + " · " + sessionLabel(conv)
		id, err := d.livePageOf(miniapp.Page{Kind: miniapp.KindAnnotate, Title: title, Thread: th.ID, Conv: conv.ID}, fmt.Sprintf("conv:%d", conv.ID))
		if err != nil {
			return fail("Could not open Annotate.")
		}
		return miniapp.LaunchResult{Page: id}
	case miniapp.ViewTemplates:
		conv, err := d.st.Conversation(ctx, th.CurrentConversationID)
		if th.CurrentConversationID == 0 || err != nil {
			return fail("This topic has no agent session yet.")
		}
		title := "🧩 Templates · " + topicShownName(th)
		id, err := d.livePageOf(miniapp.Page{Kind: miniapp.KindTemplates, Title: title, Thread: th.ID, Conv: conv.ID}, fmt.Sprintf("conv:%d", conv.ID))
		if err != nil {
			return fail("Could not open Templates.")
		}
		return miniapp.LaunchResult{Page: id}
	}
	conv, err := d.st.Conversation(ctx, th.CurrentConversationID)
	if err != nil || conv.CWD == "" {
		return fail("This agent has no folder yet.")
	}
	gctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var id string
	if view == miniapp.ViewFiles {
		id, err = d.filesPage(gctx, th, conv.CWD)
	} else {
		top, ok := d.repoTop(gctx, conv.CWD)
		if !ok {
			return fail(d.tildePath(conv.CWD) + " is not in a git repository, so there are no changes to show.")
		}
		id, err = d.changesPage(th, top)
	}
	if err != nil {
		return fail("Could not open the page.")
	}
	return miniapp.LaunchResult{Page: id}
}

// sessionLabel names an agent session on the Annotate page: its title, or
// the start of its id.
func sessionLabel(c store.Conversation) string {
	if t := strings.TrimSpace(c.Title); t != "" {
		return format.Truncate(format.Redact(t), 40)
	}
	id := c.NativeID
	if len(id) > 8 {
		id = id[:8]
	}
	return "session " + id
}

// sessionKeyPrefix is how the event keys of a session's posts start:
// "<runtime>:<session id>", then ":" (agents.Key) or "@" (agents.FallbackKey).
func sessionKeyPrefix(c store.Conversation) string {
	return string(c.Runtime) + ":" + c.NativeID
}

// topicShownName is a topic's name as the panel shows it.
func topicShownName(th store.Thread) string {
	return format.Redact(strings.TrimPrefix(th.Name, offlineMark))
}

// pickTopic lists the topics to pick from, live ones first.
func (d *daemon) pickTopic(ctx context.Context) miniapp.LaunchResult {
	ths, err := d.st.Threads(ctx, store.ThreadLive, store.ThreadOffline)
	if err != nil || len(ths) == 0 {
		return miniapp.LaunchResult{Error: "There is no topic yet. Open an agent first."}
	}
	pick := make([]miniapp.TopicPick, 0, len(ths))
	for _, th := range ths {
		pick = append(pick, miniapp.TopicPick{Thread: th.ID, Name: topicShownName(th), Live: th.Status == store.ThreadLive})
	}
	slices.SortStableFunc(pick, func(a, b miniapp.TopicPick) int {
		switch {
		case a.Live && !b.Live:
			return -1
		case !a.Live && b.Live:
			return 1
		}
		return 0
	})
	return miniapp.LaunchResult{Pick: pick}
}

// ---- Annotations ----

// BotName is the bot's username (set before the Mini App starts).
func (d *daemon) BotName() string { return d.botUser }

// Answers returns the agent's answers in a topic, newest first (any
// goroutine): n older than the outbox item before (0: the newest), and
// whether older ones exist.
func (d *daemon) Answers(ctx context.Context, thread, conv, before int64, n int) ([]miniapp.AnswerItem, bool, error) {
	if n <= 0 || conv <= 0 {
		return nil, false, nil // a page from before sessions counted: nothing to show
	}
	c, err := d.st.Conversation(ctx, conv)
	if err != nil {
		return nil, false, err
	}
	if c.ThreadID != thread || c.NativeID == "" {
		return nil, false, nil // not this topic's session
	}
	session := sessionKeyPrefix(c)
	var items []miniapp.AnswerItem
	cursor := before
	for {
		rows, err := d.st.AnswersBefore(ctx, thread, cursor, 2*n+2, session)
		if err != nil {
			return nil, false, err
		}
		for _, r := range rows {
			cursor = r.ID
			md, plain, ok := answerSource(r, func(key string) (string, bool) { return d.answerPageText(thread, key) })
			if !ok {
				continue
			}
			items = append(items, miniapp.AnswerItem{ID: r.ID, At: r.CreatedAt, Markdown: md, Plain: plain})
			if len(items) > n {
				return items[:n], true, nil
			}
		}
		if len(rows) < 2*n+2 {
			return items, false, nil
		}
	}
}

// answerPageText is the whole text of a long answer, while its page exists.
func (d *daemon) answerPageText(thread int64, key string) (string, bool) {
	id, ok, err := d.st.Get(d.ctx, pageForKey(thread, miniapp.KindAnswer, key))
	if err != nil || !ok {
		return "", false
	}
	p, ok := loadPage(d.st, id)
	if !ok || p.Kind != miniapp.KindAnswer || strings.TrimSpace(p.Text) == "" {
		return "", false
	}
	return p.Text, true
}

// answerSource picks the text of an answer: the page's Markdown of a long
// answer, else its rich and Markdown document parts, else its HTML and plain
// parts as plain text. Images and failed parts do not count. ok is false when
// nothing is left.
func answerSource(r store.OutboxRow, page func(key string) (string, bool)) (md string, plain, ok bool) {
	if r.Kind == "answer" {
		if t, found := page(r.Key); found {
			return t, false, true
		}
	}
	var marks, plains []string
	for _, p := range r.Parts {
		if p.State == "failed" {
			continue
		}
		switch {
		case p.Format == store.FormatRich:
			marks = append(marks, p.Content)
		case p.Format == store.FormatDocument && strings.EqualFold(path.Ext(p.Filename), ".md"):
			marks = append(marks, p.Content)
		case p.Format == store.FormatHTML:
			plains = append(plains, sender.PlainFromHTML(p.Content))
		case p.Format == store.FormatPlain:
			plains = append(plains, p.Content)
		}
	}
	if t := strings.TrimSpace(strings.Join(marks, "\n\n")); t != "" {
		return t, false, true
	}
	if t := strings.TrimSpace(strings.Join(plains, "\n\n")); t != "" {
		return t, true, true
	}
	return "", false, false
}

// SaveDraft keeps notes until the owner sends them through Telegram (any
// goroutine). The draft alone sends nothing.
func (d *daemon) SaveDraft(ctx context.Context, thread, conv int64, text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", errors.New("no text")
	}
	th, err := d.st.Thread(ctx, thread)
	if err != nil || th.Status == store.ThreadClosed {
		return "", errors.New("no such topic")
	}
	if conv <= 0 || th.CurrentConversationID != conv {
		return "", errors.New("the topic is in another session")
	}
	if keys, err := d.st.KeysWithPrefix(ctx, annotDraftPrefix); err != nil {
		return "", err
	} else if len(keys) >= annotMaxDrafts {
		d.pruneDrafts(d.now())
		if keys, err = d.st.KeysWithPrefix(ctx, annotDraftPrefix); err != nil || len(keys) >= annotMaxDrafts {
			return "", errors.New("too many drafts wait")
		}
	}
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b[:])
	v, err := json.Marshal(annotDraft{Thread: thread, Conv: conv, Text: text, At: d.now().Unix()})
	if err != nil {
		return "", err
	}
	if err := d.st.Set(ctx, annotDraftPrefix+id, string(v)); err != nil {
		return "", err
	}
	return id, nil
}

// pruneDrafts drops the drafts older than an hour (housekeeping, a worker).
func (d *daemon) pruneDrafts(now time.Time) {
	keys, err := d.st.KeysWithPrefix(d.ctx, annotDraftPrefix)
	if err != nil {
		return
	}
	for _, k := range keys {
		v, ok, err := d.st.Get(d.ctx, k)
		if err != nil || !ok {
			continue
		}
		var dr annotDraft
		if json.Unmarshal([]byte(v), &dr) != nil || !draftFresh(dr, now) {
			_ = d.st.Delete(d.ctx, k)
		}
	}
}

// ---- Sending the notes ----

// annotateData is the data of the page's sendData.
type annotateData struct {
	V int    `json:"v"`
	A string `json:"a"`
	D string `json:"d"`
}

// parseAnnotateData reads the data that Annotate's "Send to agent" sends:
// {"v":1,"a":"annotate","d":"<draft id>"}.
func parseAnnotateData(s string) (draft string, ok bool) {
	var a annotateData
	if len(s) > 4096 || json.Unmarshal([]byte(s), &a) != nil {
		return "", false
	}
	if a.V != 1 || a.A != "annotate" || len(a.D) != 24 {
		return "", false
	}
	if _, err := hex.DecodeString(a.D); err != nil {
		return "", false
	}
	return a.D, true
}

// annotRowID is the update id of the queued row for a draft's text, or for
// the notes of an act (OpNotes). It comes from the first 12 hex digits of
// that id, so the same draft or act is queued at most once: a second try
// finds the row already there (InsertInbound).
func annotRowID(id string) int64 {
	b, _ := hex.DecodeString(id[:min(12, len(id))])
	var n int64
	for _, c := range b[:min(6, len(b))] {
		n = n<<8 | int64(c)
	}
	return annotBase + n
}

// webAppData handles a message that carries a Mini App's data, from the
// owner (core). The text of the draft goes to the agent like /at's text, at
// most once, and is never lost without a word:
//   - the text is queued first, as the row annotRowID(draft): a second try
//     (the update fetched again after a stop) finds the row and adds none;
//   - then the update is recorded, and the draft deleted;
//   - a draft that is gone but whose row exists was sent: only the note
//     (at most once, by its key) follows.
//
// The message itself is never typed.
func (d *daemon) webAppData(u telegram.Update, m *telegram.Message) {
	id, ok := parseAnnotateData(m.WebAppData.Data)
	if !ok {
		if d.recorded(u, m, "other", "", 0, store.InboundDone) {
			d.log.Info("web app data dropped: not annotate data", "update", u.UpdateID)
		}
		return
	}
	row := annotRowID(id)
	var dr annotDraft
	found := false
	v, has, err := d.st.Get(d.ctx, annotDraftPrefix+id)
	if err != nil {
		d.storeErr = err
		return // fetched again
	}
	if has {
		found = json.Unmarshal([]byte(v), &dr) == nil
	}
	if !found {
		// Sent before a stop? Then its row is there, and only the note may
		// be missing (the note's key keeps it to one).
		if in, ok, err := d.st.InboundByID(d.ctx, -row); err == nil && ok {
			if _, err := d.record(u, m, "annotate", id, in.ThreadID, store.InboundDone); err != nil {
				d.storeErr = err
				return
			}
			d.annotSent(in.ThreadID, id, in.Body)
			return
		}
		if d.recorded(u, m, "annotate", id, 0, store.InboundDone) {
			d.annotNotSent(u, m, store.Thread{}, false)
		}
		return
	}
	th, err := d.st.Thread(d.ctx, dr.Thread)
	known := err == nil && th.Status != store.ThreadClosed
	if !known || !draftFresh(dr, d.now()) {
		if d.recorded(u, m, "annotate", id, dr.Thread, store.InboundDone) {
			_ = d.st.Delete(d.ctx, annotDraftPrefix+id)
			d.annotNotSent(u, m, th, known)
		}
		return
	}
	// The notes go only to the agent session that the page showed, and only
	// from its own topic: a keyboard that Telegram shares across topics
	// may have opened another topic's page.
	if why := d.annotWrongPlace(m, th, dr); why != "" {
		if d.recorded(u, m, "annotate", id, dr.Thread, store.InboundDone) {
			_ = d.st.Delete(d.ctx, annotDraftPrefix+id)
			d.log.Info("annotate: notes not sent", "thread", th.ID, "why", why)
			d.say(m.Chat.ID, m.MessageThreadID, why, nil)
		}
		return
	}
	if !d.queueText(th.ID, row, 0, dr.Text, "text", inbound.ActText) {
		return // the store failed: the update is fetched again
	}
	// Recorded now, or already by a run that stopped before the draft was
	// deleted: either way the row is queued once, and the rest is finished.
	inserted, err := d.record(u, m, "annotate", id, th.ID, store.InboundDone)
	if err != nil {
		d.storeErr = err
		return // fetched again; the row is there already
	}
	if inserted {
		d.markBelow(th.ID)
	}
	_ = d.st.Delete(d.ctx, annotDraftPrefix+id)
	d.annotSent(th.ID, id, dr.Text)
}

// annotWrongPlace says why notes must not go to th's agent, or "": the
// topic is in another session than the page showed, or the notes came from
// another agent's topic. Notes sent from the main chat (where the buttons
// ask which topic) are fine.
func (d *daemon) annotWrongPlace(m *telegram.Message, th store.Thread, dr annotDraft) string {
	if dr.Conv <= 0 || th.CurrentConversationID != dr.Conv {
		return "✍️ Not sent: the agent in " + format.Escape(topicShownName(th)) + " is in another session now. Open ✍️ Annotate again; it shows the new session."
	}
	if m.MessageThreadID != 0 {
		if from, ok, err := d.st.ThreadByTopic(d.ctx, m.Chat.ID, m.MessageThreadID); err == nil && ok && from.ID != th.ID && from.Status != store.ThreadClosed {
			return "✍️ Not sent: these notes are for " + format.Escape(topicShownName(th)) + ", but they came from this topic. Send /help here for this topic's buttons, then open ✍️ Annotate again."
		}
	}
	return ""
}

// annotSent posts the silent note that the notes went to the agent, once per
// draft.
func (d *daemon) annotSent(thread int64, draft, text string) {
	d.enqueue(store.OutboxItem{
		ThreadID: thread, Key: "annot:" + draft, Kind: "notice", Silent: true,
		Parts: []store.PartContent{{Content: annotSentNote(text), Format: store.FormatHTML}},
	})
}

// annotNotSent says that the notes were not sent: in the draft's topic when
// it is known, else where the data came from.
func (d *daemon) annotNotSent(u telegram.Update, m *telegram.Message, th store.Thread, known bool) {
	if known {
		d.notice(th.ID, fmt.Sprintf("annot-gone:%d", u.UpdateID), annotNotSent)
		return
	}
	d.say(m.Chat.ID, m.MessageThreadID, annotNotSent, nil)
}

// annotSentNote is the silent note in the topic: the text in an expandable
// quote, cut to fit one message.
func annotSentNote(text string) string {
	const head = "✍️ Notes sent to the agent\n<blockquote expandable>"
	const tail = "</blockquote>"
	room := 4096 - len(head) - len(tail)
	body := format.Escape(text)
	if len(body) <= room {
		return head + body + tail
	}
	r := []rune(text)
	n := min(len(r), room)
	for n > 0 {
		body = format.Escape(string(r[:n])) + "…"
		if len(body) <= room {
			break
		}
		n = n * 9 / 10
	}
	return head + body + tail
}
