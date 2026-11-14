package sender

import (
	"context"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"rex-tg/internal/format"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// verdict is what the sender does about the result of one request.
type verdict int

const (
	vOK verdict = iota
	vParse
	vRetry // 429 or not sent: the budget is paused; nothing was posted
	vUnknown
	vGone    // topic deleted
	vBlocked // bot blocked
	vNotModified
	vEditGone
	vDeleteGone
	vFailed
)

// judge maps an error to a verdict and applies the shared side effects:
// pausing the budget, announcing a gone topic, and stopping when blocked.
func (s *Sender) judge(err error, threadID int64) verdict {
	c := telegram.Classify(err)
	switch c {
	case telegram.ClassNotSent:
		s.netDown()
		return vRetry
	case telegram.ClassUnknownOutcome:
	default:
		s.netUp() // Telegram answered
	}
	switch c {
	case telegram.ClassNone:
		return vOK
	case telegram.ClassRateLimited:
		s.pauseFor(err)
		return vRetry
	case telegram.ClassParseEntities:
		return vParse
	case telegram.ClassNotModified:
		return vNotModified
	case telegram.ClassEditTargetGone:
		return vEditGone
	case telegram.ClassDeleteTargetGone:
		return vDeleteGone
	case telegram.ClassThreadNotFound:
		if threadID != 0 { // reactions and other calls have no topic to report
			s.threadGone(threadID)
		}
		return vGone
	case telegram.ClassBlocked:
		s.block(threadID)
		return vBlocked
	case telegram.ClassConflict:
		s.emit(Event{Kind: Conflict, ThreadID: threadID})
		return vFailed
	case telegram.ClassUnknownOutcome:
		return vUnknown
	default:
		return vFailed
	}
}

// sendText sends one text message. HTML that Telegram cannot parse is sent
// again as plain text; that is safe because nothing was posted.
func (s *Sender) sendText(ctx context.Context, threadID, chatID, topicID int64, text string, html, silent bool, markup *telegram.InlineKeyboardMarkup) (int64, verdict, error) {
	return s.sendTextKeys(ctx, threadID, chatID, topicID, text, html, silent, markup, nil)
}

// sendTextKeys is sendText with a reply keyboard (kb), sent when markup is
// nil.
func (s *Sender) sendTextKeys(ctx context.Context, threadID, chatID, topicID int64, text string, html, silent bool, markup *telegram.InlineKeyboardMarkup, kb *telegram.ReplyKeyboardMarkup) (int64, verdict, error) {
	p := telegram.SendMessageParams{ChatID: chatID, MessageThreadID: topicID, Text: text, DisableNotification: silent, ReplyMarkup: markup, Keyboard: kb}
	if html {
		p.ParseMode = "HTML"
	}
	if err := s.token(ctx); err != nil {
		return 0, vUnknown, err
	}
	m, err := s.tg.SendMessage(ctx, p)
	v := s.judge(err, threadID)
	if v == vParse {
		p.ParseMode, p.Text = "", PlainFromHTML(text)
		if err := s.token(ctx); err != nil {
			return 0, vUnknown, err
		}
		m, err = s.tg.SendMessage(ctx, p)
		v = s.judge(err, threadID)
	}
	if v == vOK {
		s.noteMessage(chatID, m.MessageID)
	}
	return m.MessageID, v, err
}

// noteMessage records the number of a message that the bot sent. The daemon
// also records every number through the client's OnMessage, but the sender
// does not depend on that for its own messages.
func (s *Sender) noteMessage(chatID, messageID int64) {
	if messageID == 0 {
		return
	}
	if err := s.st.NoteMessage(context.Background(), chatID, messageID, s.now()); err != nil {
		s.log.Warn("message number not saved", "err", err)
	}
}

// maxFallbackText is the longest markdown sent as a plain message when a rich
// message is rejected; longer text goes out as a .md document.
const maxFallbackText = 4000

// sendRich sends markdown as a rich message. When Telegram answers 400 (parse
// error or bad request) nothing was posted, so the text is sent again in
// rex-tg's own format (Telegram HTML, which falls back to plain text when
// Telegram cannot parse it), or as a .md document when it does not fit one
// message.
func (s *Sender) sendRich(ctx context.Context, p store.Part, silent bool) (int64, verdict, error) {
	if err := s.token(ctx); err != nil {
		return 0, vUnknown, err
	}
	var markup *telegram.InlineKeyboardMarkup
	if s.markup != nil {
		markup = s.markup(p)
	}
	m, err := s.tg.SendRichMessage(ctx, p.ChatID, p.MessageThreadID, p.Content, silent, markup)
	switch telegram.Classify(err) {
	case telegram.ClassParseEntities, telegram.ClassBadRequest:
	default:
		return m.MessageID, s.judge(err, p.ThreadID), err
	}
	s.log.Warn("rich message rejected; falling back", "outbox", p.OutboxID, "part", p.Part, "err", err)
	if utf8.RuneCountInString(p.Content) <= maxFallbackText {
		if ms := format.Messages(p.Content, maxFallbackText, maxFallbackText); len(ms) == 1 {
			return s.sendText(ctx, p.ThreadID, p.ChatID, p.MessageThreadID, ms[0].HTML, true, silent, markup)
		}
		return s.sendText(ctx, p.ThreadID, p.ChatID, p.MessageThreadID, p.Content, false, silent, markup)
	}
	if err := s.token(ctx); err != nil {
		return 0, vUnknown, err
	}
	name := fmt.Sprintf("reply-%d.md", p.OutboxID)
	m, err = s.tg.SendDocument(ctx, p.ChatID, p.MessageThreadID, name, []byte(p.Content), "", silent)
	return m.MessageID, s.judge(err, p.ThreadID), err
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

var unescaper = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&amp;", "&")

// PlainFromHTML turns Telegram HTML into plain text: tags are removed and the
// entities &lt; &gt; &quot; &amp; are unescaped.
func PlainFromHTML(s string) string { return unescaper.Replace(tagRe.ReplaceAllString(s, "")) }

const unknownNoticePrefix = "notice:unknown:"

func noticeKey(outboxID int64, part int) string {
	return fmt.Sprintf(unknownNoticePrefix+"%d:%d", outboxID, part)
}

// ResendButtonData is the callback data of the "Send again" button on a
// "may not have arrived" notice; the daemon handles it like /resend.
func ResendButtonData(outboxID int64) string { return fmt.Sprintf("rs:%d", outboxID) }

// NoticeTarget returns the outbox id that a "may not have arrived" notice is
// about, from the notice's event key.
func NoticeTarget(key string) (int64, bool) {
	rest, ok := strings.CutPrefix(key, unknownNoticePrefix)
	if !ok {
		return 0, false
	}
	idText, _, _ := strings.Cut(rest, ":")
	id, err := strconv.ParseInt(idText, 10, 64)
	return id, err == nil
}

// resendMarkup is the "Send again" button of a "may not have arrived" notice,
// so the user does not have to type the id.
func resendMarkup(p store.Part) *telegram.InlineKeyboardMarkup {
	id, ok := NoticeTarget(p.Key)
	if !ok {
		return nil
	}
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{
		{Text: "🔁 Send again", CallbackData: ResendButtonData(id)},
	}}}
}

// isPartNotice reports a notice: the daemon's own (kind "notice"), or one
// that the sender wrote about another part, also one stored before
// partNoticeKind existed. Notices get no notices of their own.
func isPartNotice(p store.Part) bool {
	return p.Kind == "notice" || p.Kind == partNoticeKind ||
		strings.HasPrefix(p.Key, unknownNoticePrefix) || strings.HasPrefix(p.Key, "notice:failed:")
}

// partNoticeKind is the outbox kind of the sender's own notices about a part.
// Such a notice never gets a notice of its own, so a network outage cannot
// make them pile up.
const partNoticeKind = "part-notice"

// notice enqueues the "may not have arrived" message for a part, once.
func (s *Sender) notice(ctx context.Context, p store.Part) {
	text := fmt.Sprintf("⚠️ Part %d of reply #%d may not have arrived.", p.Part+1, p.OutboxID)
	s.enqueuePartNotice(ctx, p, noticeKey(p.OutboxID, p.Part), text)
}

// failNotice enqueues the "could not be sent" message for a part that
// Telegram refused for good, once.
func (s *Sender) failNotice(ctx context.Context, p store.Part, reason string) {
	text := fmt.Sprintf("⚠️ Part %d of reply #%d could not be sent: %s", p.Part+1, p.OutboxID, html.EscapeString(reason))
	s.enqueuePartNotice(ctx, p, fmt.Sprintf("notice:failed:%d:%d", p.OutboxID, p.Part), text)
}

func (s *Sender) enqueuePartNotice(ctx context.Context, p store.Part, key, text string) {
	if isPartNotice(p) {
		return
	}
	_, _, err := s.st.Enqueue(ctx, store.OutboxItem{
		ThreadID: p.ThreadID,
		Key:      key,
		Kind:     partNoticeKind,
		Parts:    []store.PartContent{{Content: text, Format: store.FormatHTML}},
	})
	if err != nil {
		s.log.Error("notice not stored", "outbox", p.OutboxID, "part", p.Part, "err", err)
		return
	}
	s.Wake()
}

// Recover turns parts left in "sending" by a crash into unknown parts, each
// with one notice. Call it before Run.
func (s *Sender) Recover(ctx context.Context) error {
	parts, err := s.st.RecoverSending(ctx)
	if err != nil {
		return fmt.Errorf("sender: recover outbox: %w", err)
	}
	for _, p := range parts {
		if !p.HasFloor {
			s.notice(ctx, p) // claimed without a known floor: it cannot be checked
		}
		s.emit(Event{Kind: PartUnknown, ThreadID: p.ThreadID, OutboxID: p.OutboxID, Part: p.Part})
	}
	// Parts left in "sending" are unknown now, and the checks that were
	// waiting when the daemon stopped are lost: check them all again.
	check, err := s.st.PartsToCheck(ctx)
	if err != nil {
		return fmt.Errorf("sender: recover checks: %w", err)
	}
	for _, p := range check {
		s.unknownPart(ctx, p)
	}
	s.sweepCheckNotes(ctx, check)
	// A progress message caught in "sending" may exist; it is never sent again.
	all, err := s.st.AllProgress(ctx)
	if err != nil {
		return fmt.Errorf("sender: recover progress: %w", err)
	}
	for _, pr := range all {
		if pr.State == store.ProgressSending {
			pr.State = store.ProgressUnknown
			if err := s.st.SaveProgress(ctx, pr); err != nil {
				return fmt.Errorf("sender: recover progress: %w", err)
			}
		}
	}
	return nil
}

// sendPart claims one part, sends it, and records the outcome.
func (s *Sender) sendPart(ctx context.Context, p store.Part) {
	bg := context.WithoutCancel(ctx)
	// The newest message number known before the send: if the part arrives,
	// its number is higher (see checkPart).
	floor := int64(-1)
	if id, _, err := s.st.MaxMessage(ctx, p.ChatID); err == nil {
		floor = id // 0 when none is known yet
	}
	ok, err := s.st.ClaimPart(ctx, p.OutboxID, p.Part, floor)
	if err != nil {
		s.log.Warn("claim failed", "outbox", p.OutboxID, "part", p.Part, "err", err)
		s.sleepOrWake(ctx, idlePoll/4)
		return
	}
	if !ok {
		return
	}
	// The messages after the first of one reply come without sound, as when
	// a person sends a long reply in a few messages.
	silent := p.Silent || p.Part > 0
	if th, err := s.st.Thread(ctx, p.ThreadID); err == nil {
		silent = silent || th.Muted
	}

	var (
		id   int64
		v    verdict
		serr error
	)
	switch p.Format {
	case store.FormatHTML, store.FormatPlain:
		markup := resendMarkup(p)
		if markup == nil && s.markup != nil {
			markup = s.markup(p)
		}
		var kb *telegram.ReplyKeyboardMarkup
		if markup == nil && s.keyboard != nil {
			kb = s.keyboard(p)
		}
		id, v, serr = s.sendTextKeys(ctx, p.ThreadID, p.ChatID, p.MessageThreadID, p.Content, p.Format == store.FormatHTML, silent, markup, kb)
	case store.FormatRich:
		id, v, serr = s.sendRich(ctx, p, silent)
	case store.FormatDocument:
		if serr = s.token(ctx); serr != nil {
			v = vUnknown
			break
		}
		var m telegram.Message
		m, serr = s.tg.SendDocument(ctx, p.ChatID, p.MessageThreadID, p.Filename, []byte(p.Content), "", silent)
		id, v = m.MessageID, s.judge(serr, p.ThreadID)
	case store.FormatImage:
		id, v, serr = s.sendImage(ctx, p, silent)
	default:
		serr, v = fmt.Errorf("unknown part format %q", p.Format), vFailed
	}
	if v == vOK {
		s.noteMessage(p.ChatID, id) // every format; a text was noted already
	}
	if v == vUnknown && errors.Is(serr, context.Canceled) && ctx.Err() != nil && id == 0 && isTokenWait(serr) {
		_ = s.st.ReleasePart(bg, p.OutboxID, p.Part)
		return
	}
	s.finishPart(bg, p, id, v, serr)
}

// isTokenWait reports a context error from waiting for budget, which means no
// request was made.
func isTokenWait(err error) bool {
	var api *telegram.APIError
	var unk *telegram.UnknownOutcomeError
	var ns *telegram.NotSentError
	return !errors.As(err, &api) && !errors.As(err, &unk) && !errors.As(err, &ns)
}

func (s *Sender) finishPart(ctx context.Context, p store.Part, id int64, v verdict, err error) {
	var werr error
	switch v {
	case vOK:
		werr = s.st.MarkPartSent(ctx, p.OutboxID, p.Part, id)
	case vRetry, vBlocked:
		// Nothing was delivered: keep the part for after the user unblocks the bot.
		werr = s.st.ReleasePart(ctx, p.OutboxID, p.Part)
	case vUnknown:
		werr = s.st.MarkPartUnknown(ctx, p.OutboxID, p.Part)
		s.unknownPart(ctx, p)
		s.emit(Event{Kind: PartUnknown, ThreadID: p.ThreadID, OutboxID: p.OutboxID, Part: p.Part})
	default:
		reason := "failed"
		switch v {
		case vGone:
			reason = "topic not found"
		default:
			if err != nil {
				reason = err.Error()
			}
		}
		werr = s.st.MarkPartFailed(ctx, p.OutboxID, p.Part, reason)
		if v != vGone {
			s.failNotice(ctx, p, reason) // a lost answer must not be silent
		}
	}
	if werr != nil {
		s.log.Error("part state not saved", "outbox", p.OutboxID, "part", p.Part, "err", werr)
	}
}

// maxAutoSends is how many times a part is sent before an unknown outcome
// leads to the notice instead of a check: each check that finds the part
// missing sends it again.
const maxAutoSends = 5

// isAboutAPart reports the sender's own notice about another part, also one
// stored before partNoticeKind existed.
func isAboutAPart(p store.Part) bool {
	return p.Kind == partNoticeKind || strings.HasPrefix(p.Key, unknownNoticePrefix) || strings.HasPrefix(p.Key, "notice:failed:")
}

// unknownPart deals with a part whose outcome is unknown: it checks later
// whether the part arrived (checkPart). The sender's own notices about other
// parts are never checked, so a network outage cannot make them pile up.
func (s *Sender) unknownPart(ctx context.Context, p store.Part) {
	if isAboutAPart(p) {
		if err := s.st.MarkPartChecked(ctx, p.OutboxID, p.Part); err != nil {
			s.log.Warn("part check not saved", "outbox", p.OutboxID, "part", p.Part, "err", err)
		}
		return
	}
	j := &job{prio: PriorityAnswer, notBefore: s.now().Add(s.checkDelay)}
	c := &partCheck{}
	j.run = func(ctx context.Context, j *job) jobResult { return s.checkPart(ctx, j, c, p.OutboxID, p.Part) }
	if err := s.addJob(j); err != nil {
		s.log.Warn("part check not started", "outbox", p.OutboxID, "part", p.Part, "err", err)
	}
}

// checkPart finds out whether a part whose outcome is unknown arrived
// (design section 9.1). Telegram numbers the messages of a private chat in
// order, both ways. So it posts a short note in the part's topic and looks
// at the numbers between the newest one known before the part was sent
// (the part's floor) and the note's: had the part arrived, its number would
// be there. When every number there is a message that rex-tg knows, the
// part did not arrive: it is sent again, and the note goes. Otherwise the
// note becomes the "may not have arrived" notice with its Send again button.
//
// It runs once the network works again and CheckDelay after the outcome. By
// then the request cannot arrive late: its connection was closed, and the
// client's connections drop unsent data when they close.
//
// The numbers of the user's messages come with getUpdates. A message that
// the user sent just before the note may not be fetched yet, and its number
// would look like the part's. So the numbers are compared only once a
// getUpdates call finished after the note (PolledSince): the job comes back
// every half second for that, and gives up with the notice after
// maxPollWait.
func (s *Sender) checkPart(ctx context.Context, j *job, c *partCheck, outboxID int64, part int) jobResult {
	bg := context.WithoutCancel(ctx)
	p, err := s.st.Part(ctx, outboxID, part)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		if ctx.Err() != nil {
			return jobResult{}
		}
		return jobResult{retry: true, delay: time.Second} // the store failed: look again
	}
	if c.noteID == 0 {
		// A note posted before a restart: its wait for a poll starts again
		// now, so a long stop does not use it up before Telegram answers.
		if c.load(bg, s.st, outboxID, part) {
			c.waitFrom = s.now()
		}
	}
	if err == nil {
		c.place(p)
	}
	if err != nil || p.State != store.OutboxUnknown || p.Checked {
		c.drop(bg, s, outboxID, part)
		return jobResult{} // gone, sent again by /resend, or checked already
	}
	if c.noteID != 0 {
		return s.judgePart(ctx, bg, p, c)
	}
	if !p.HasFloor || s.sentTooOften(ctx, outboxID) {
		s.giveUpCheck(bg, p)
		return jobResult{}
	}
	text := fmt.Sprintf("⏳ Telegram did not confirm part %d of reply #%d. Checking whether it arrived…", p.Part+1, p.OutboxID)
	id, v, err := s.sendText(ctx, p.ThreadID, p.ChatID, p.MessageThreadID, text, true, true, nil)
	switch v {
	case vOK:
		// The time after the answer: a poll that started before it may not
		// have fetched every message older than the note (sendText can wait
		// for the rate budget first).
		c.noteID, c.noteAt, c.chat, c.thread = id, s.now(), p.ChatID, p.ThreadID
		c.waitFrom = c.noteAt
		if err := c.save(bg, s.st, outboxID, part); err != nil {
			s.log.Warn("part check: note not saved", "outbox", outboxID, "part", part, "err", err)
		}
	case vRetry: // nothing was posted: the network or the rate limit
		j.attempts++
		return jobResult{retry: true, delay: backoff(j.attempts - 1)}
	case vUnknown:
		if ctx.Err() != nil && isTokenWait(err) {
			return jobResult{} // stopping; the next start checks again
		}
		// The note may have arrived: its number is then unknown too, and
		// the next try finds a gap. After a few tries, the notice comes.
		if j.attempts++; j.attempts < 3 {
			return jobResult{retry: true, delay: s.checkDelay}
		}
		s.giveUpCheck(bg, p)
		return jobResult{}
	case vGone, vBlocked:
		_ = s.st.MarkPartChecked(bg, p.OutboxID, p.Part)
		return jobResult{}
	default:
		s.giveUpCheck(bg, p)
		return jobResult{}
	}
	return s.judgePart(ctx, bg, p, c)
}

// partCheck is the state of one part's check across the job's runs: the
// note, once it is posted. It is kept in the store too (kv
// "check_note:<outbox>:<part>"), so a check cut by a stop goes on with the
// same note after the start, instead of leaving it and posting another.
type partCheck struct {
	noteID       int64
	noteAt       time.Time // when the note was posted: the poll must start later
	chat, thread int64     // where the note is, to delete it when the part is gone
	waitFrom     time.Time // the start of the wait for that poll (maxPollWait)
}

func checkNoteKey(outboxID int64, part int) string {
	return fmt.Sprintf("check_note:%d:%d", outboxID, part)
}

func (c *partCheck) save(ctx context.Context, st *store.Store, outboxID int64, part int) error {
	return st.Set(ctx, checkNoteKey(outboxID, part), fmt.Sprintf("%d:%d:%d:%d", c.noteID, c.noteAt.UnixNano(), c.chat, c.thread))
}

// place fills in where the note is from its part, for a row that does not
// hold it.
func (c *partCheck) place(p store.Part) {
	if c.chat == 0 {
		c.chat, c.thread = p.ChatID, p.ThreadID
	}
}

// drop deletes the note, by the number and chat kept with it, and forgets
// the check.
func (c *partCheck) drop(ctx context.Context, s *Sender, outboxID int64, part int) {
	if c.noteID != 0 && c.chat != 0 {
		s.deleteMessage(store.Thread{ID: c.thread, ChatID: c.chat}, c.noteID)
	}
	c.forget(ctx, s.st, outboxID, part)
}

// sweepCheckNotes deletes the notes of checks that cannot go on: their part
// left the "unknown" state while rex-tg was stopped (/resend), or is gone.
// The parts still to check (check) keep their notes. A note's message
// number was stored when it was posted, so the delete hits only that note.
func (s *Sender) sweepCheckNotes(ctx context.Context, check []store.Part) {
	keys, err := s.st.KeysWithPrefix(ctx, "check_note:")
	if err != nil {
		s.log.Warn("part check: list notes", "err", err)
		return
	}
	live := map[string]bool{}
	for _, p := range check {
		live[checkNoteKey(p.OutboxID, p.Part)] = true
	}
	for _, k := range keys {
		var outboxID int64
		var part int
		if live[k] {
			continue
		}
		if _, err := fmt.Sscanf(k, "check_note:%d:%d", &outboxID, &part); err != nil {
			_ = s.st.Delete(ctx, k)
			continue
		}
		c := &partCheck{}
		c.load(ctx, s.st, outboxID, part)
		p, err := s.st.Part(ctx, outboxID, part)
		switch {
		case err == nil:
			c.place(p)
		case !errors.Is(err, store.ErrNotFound):
			continue // the store failed: look again at the next start
		}
		c.drop(ctx, s, outboxID, part)
	}
}

func (c *partCheck) load(ctx context.Context, st *store.Store, outboxID int64, part int) bool {
	v, ok, err := st.Get(ctx, checkNoteKey(outboxID, part))
	if err != nil || !ok {
		return false
	}
	var id, at, chat, thread int64
	// "id:at:chat:thread"; a row of v0.1.11 has only "id:at".
	if n, _ := fmt.Sscanf(v, "%d:%d:%d:%d", &id, &at, &chat, &thread); n >= 2 && id != 0 {
		c.noteID, c.noteAt, c.chat, c.thread = id, time.Unix(0, at), chat, thread
		c.waitFrom = c.noteAt
		return true
	}
	return false
}

func (c *partCheck) forget(ctx context.Context, st *store.Store, outboxID int64, part int) {
	_ = st.Delete(ctx, checkNoteKey(outboxID, part))
}

// maxPollWait is how long the check waits for a getUpdates call after its
// note; when Telegram does not answer that long, the notice comes.
const maxPollWait = 3 * time.Minute

// judgePart compares the numbers once the user's messages up to the note are
// fetched (see checkPart).
func (s *Sender) judgePart(ctx, bg context.Context, p store.Part, c *partCheck) jobResult {
	th := store.Thread{ID: p.ThreadID, ChatID: p.ChatID, MessageThreadID: p.MessageThreadID}
	id := c.noteID
	if s.polled != nil && !s.polled(c.noteAt) {
		if ctx.Err() != nil {
			return jobResult{} // stopping: the next start goes on with this note
		}
		if s.now().Sub(c.waitFrom) < maxPollWait {
			return jobResult{retry: true, delay: 500 * time.Millisecond}
		}
		c.forget(bg, s.st, p.OutboxID, p.Part)
		s.log.Info("part check: no getUpdates after the note; asking", "outbox", p.OutboxID, "part", p.Part)
		if err := s.st.MarkPartChecked(bg, p.OutboxID, p.Part); err != nil {
			s.log.Warn("part check not saved", "outbox", p.OutboxID, "part", p.Part, "err", err)
		}
		s.deleteMessage(th, id)
		s.notice(bg, p)
		return jobResult{}
	}
	c.forget(bg, s.st, p.OutboxID, p.Part) // judged now, one way or the other
	unseen, err := s.st.UnseenBetween(bg, p.ChatID, p.Floor, id)
	if err == nil && unseen == 0 {
		if ok, err := s.st.ResendPart(bg, p.OutboxID, p.Part); err != nil {
			s.log.Error("part not sent again", "outbox", p.OutboxID, "part", p.Part, "err", err)
		} else if ok {
			s.log.Info("part did not arrive; sending it again", "outbox", p.OutboxID, "part", p.Part)
		}
		s.deleteMessage(th, id)
		s.Wake()
		return jobResult{}
	}
	s.log.Info("part may have arrived; asking before sending again", "outbox", p.OutboxID, "part", p.Part, "unseen", unseen)
	if err := s.st.MarkPartChecked(bg, p.OutboxID, p.Part); err != nil {
		s.log.Warn("part check not saved", "outbox", p.OutboxID, "part", p.Part, "err", err)
	}
	notice := fmt.Sprintf("⚠️ Part %d of reply #%d may not have arrived.", p.Part+1, p.OutboxID)
	markup := &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{
		{Text: "🔁 Send again", CallbackData: ResendButtonData(p.OutboxID)},
	}}}
	if err := s.token(ctx); err == nil {
		_, err := s.tg.EditMessageText(ctx, telegram.EditMessageTextParams{
			ChatID: p.ChatID, MessageID: id, Text: notice, ParseMode: "HTML", ReplyMarkup: markup,
		})
		if v := s.judge(err, p.ThreadID); v == vOK || v == vNotModified {
			return jobResult{}
		}
	}
	s.notice(bg, p) // the note could not become the notice: post the notice
	return jobResult{}
}

// giveUpCheck ends the check of a part without an answer: the part gets the
// "may not have arrived" notice and waits for /resend.
func (s *Sender) giveUpCheck(ctx context.Context, p store.Part) {
	if err := s.st.MarkPartChecked(ctx, p.OutboxID, p.Part); err != nil {
		s.log.Warn("part check not saved", "outbox", p.OutboxID, "part", p.Part, "err", err)
	}
	s.notice(ctx, p)
}

// sentTooOften reports a row that was sent maxAutoSends times or more.
func (s *Sender) sentTooOften(ctx context.Context, outboxID int64) bool {
	row, err := s.st.OutboxRow(ctx, outboxID)
	return err == nil && row.Attempts >= maxAutoSends
}
