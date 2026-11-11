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
	return m.MessageID, v, err
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
		s.notice(ctx, p)
		s.emit(Event{Kind: PartUnknown, ThreadID: p.ThreadID, OutboxID: p.OutboxID, Part: p.Part})
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
	ok, err := s.st.ClaimPart(ctx, p.OutboxID, p.Part)
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
		id, v, serr = s.sendText(ctx, p.ThreadID, p.ChatID, p.MessageThreadID, p.Content, p.Format == store.FormatHTML, silent, resendMarkup(p))
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
	default:
		serr, v = fmt.Errorf("unknown part format %q", p.Format), vFailed
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
		s.notice(ctx, p)
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
