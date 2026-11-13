package sender

import (
	"context"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

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
	p := telegram.SendMessageParams{ChatID: chatID, MessageThreadID: topicID, Text: text, DisableNotification: silent, ReplyMarkup: markup}
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
// error or bad request) nothing was posted, so the text is sent again as plain
// text, or as a .md document when it is longer than maxFallbackText.
func (s *Sender) sendRich(ctx context.Context, p store.Part, silent bool) (int64, verdict, error) {
	if err := s.token(ctx); err != nil {
		return 0, vUnknown, err
	}
	m, err := s.tg.SendRichMessage(ctx, p.ChatID, p.MessageThreadID, p.Content, silent, nil)
	switch telegram.Classify(err) {
	case telegram.ClassParseEntities, telegram.ClassBadRequest:
	default:
		return m.MessageID, s.judge(err, p.ThreadID), err
	}
	s.log.Warn("rich message rejected; falling back", "outbox", p.OutboxID, "part", p.Part, "err", err)
	if utf8.RuneCountInString(p.Content) <= maxFallbackText {
		return s.sendText(ctx, p.ThreadID, p.ChatID, p.MessageThreadID, p.Content, false, silent, nil)
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
	silent := p.Silent
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
		id, v, serr = s.sendText(ctx, p.ThreadID, p.ChatID, p.MessageThreadID, p.Content, p.Format == store.FormatHTML, silent, markup)
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
	j.run = func(ctx context.Context, j *job) jobResult { return s.checkPart(ctx, j, p.OutboxID, p.Part) }
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
func (s *Sender) checkPart(ctx context.Context, j *job, outboxID int64, part int) jobResult {
	bg := context.WithoutCancel(ctx)
	p, err := s.st.Part(ctx, outboxID, part)
	if err != nil || p.State != store.OutboxUnknown || p.Checked {
		return jobResult{} // gone, sent again by /resend, or checked already
	}
	if !p.HasFloor || s.sentTooOften(ctx, outboxID) {
		s.giveUpCheck(bg, p)
		return jobResult{}
	}
	text := fmt.Sprintf("⏳ Telegram did not confirm part %d of reply #%d. Checking whether it arrived…", p.Part+1, p.OutboxID)
	id, v, err := s.sendText(ctx, p.ThreadID, p.ChatID, p.MessageThreadID, text, true, true, nil)
	switch v {
	case vOK:
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
	th := store.Thread{ID: p.ThreadID, ChatID: p.ChatID, MessageThreadID: p.MessageThreadID}
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
