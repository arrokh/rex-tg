package sender

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// verdict is what the sender does about the result of one request.
type verdict int

const (
	vOK verdict = iota
	vParse
	vRetry // 429: the budget is paused; nothing was posted
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
	switch telegram.Classify(err) {
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
		s.threadGone(threadID)
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
func (s *Sender) sendText(ctx context.Context, threadID, chatID, topicID int64, text string, html, silent bool) (int64, verdict, error) {
	p := telegram.SendMessageParams{ChatID: chatID, MessageThreadID: topicID, Text: text, DisableNotification: silent}
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

var tagRe = regexp.MustCompile(`<[^>]*>`)

var unescaper = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&amp;", "&")

// PlainFromHTML turns Telegram HTML into plain text: tags are removed and the
// entities &lt; &gt; &quot; &amp; are unescaped.
func PlainFromHTML(s string) string { return unescaper.Replace(tagRe.ReplaceAllString(s, "")) }

func noticeKey(outboxID int64, part int) string {
	return fmt.Sprintf("notice:unknown:%d:%d", outboxID, part)
}

// notice enqueues the "may not have arrived" message for a part, once.
func (s *Sender) notice(ctx context.Context, p store.Part) {
	text := fmt.Sprintf("⚠️ Part %d of reply #%d may not have arrived. /resend %d", p.Part+1, p.OutboxID, p.OutboxID)
	_, _, err := s.st.Enqueue(ctx, store.OutboxItem{
		ThreadID: p.ThreadID,
		Key:      noticeKey(p.OutboxID, p.Part),
		Kind:     "notice",
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
		id, v, serr = s.sendText(ctx, p.ThreadID, p.ChatID, p.MessageThreadID, p.Content, p.Format == store.FormatHTML, silent)
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
	return !errors.As(err, &api) && !errors.As(err, &unk)
}

func (s *Sender) finishPart(ctx context.Context, p store.Part, id int64, v verdict, err error) {
	var werr error
	switch v {
	case vOK:
		werr = s.st.MarkPartSent(ctx, p.OutboxID, p.Part, id)
	case vRetry:
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
		case vBlocked:
			reason = "bot blocked"
		default:
			if err != nil {
				reason = err.Error()
			}
		}
		werr = s.st.MarkPartFailed(ctx, p.OutboxID, p.Part, reason)
	}
	if werr != nil {
		s.log.Error("part state not saved", "outbox", p.OutboxID, "part", p.Part, "err", werr)
	}
}
