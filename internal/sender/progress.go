package sender

import (
	"context"
	"time"

	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// progOp is a queued progress operation.
type progOp struct {
	kind      ProgressKind
	html      string
	seq       uint64
	attempts  int
	notBefore time.Time
}

// Progress queues a change to a topic's progress message. Operations of a topic
// run in order, but a pending edit merges into the operation before it: the
// latest HTML wins.
func (s *Sender) Progress(threadID int64, op ProgressOp) {
	s.mu.Lock()
	if s.blocked || s.gone[threadID] {
		s.mu.Unlock()
		return
	}
	q := s.prog[threadID]
	var last *progOp
	if len(q) > 0 {
		last = q[len(q)-1]
	}
	switch op.Kind {
	case Edit:
		if last != nil {
			if last.kind != Finish {
				last.html = op.HTML
			}
			s.mu.Unlock()
			return
		}
	case Finish:
		if last != nil && last.kind == Edit {
			q = q[:len(q)-1]
		}
	case Show, Repost:
	default:
		s.mu.Unlock()
		return
	}
	s.seq++
	s.prog[threadID] = append(q, &progOp{kind: op.Kind, html: op.HTML, seq: s.seq})
	s.mu.Unlock()
	s.Wake()
}

// requeue puts op back at the front of its topic's queue after a delay.
// Edits behind it are merged into it, and a stale edit is dropped.
func (s *Sender) requeue(tid int64, op *progOp, delay time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op.notBefore = s.now().Add(delay)
	q := s.prog[tid]
	if op.kind == Edit && len(q) > 0 && (q[0].kind == Edit || q[0].kind == Finish) {
		return
	}
	if op.kind != Edit {
		for len(q) > 0 && q[0].kind == Edit {
			op.html = q[0].html
			q = q[1:]
		}
	}
	s.prog[tid] = append([]*progOp{op}, q...)
}

func (s *Sender) runProgress(ctx context.Context, tid int64, op *progOp) {
	bg := context.WithoutCancel(ctx)
	th, err := s.st.Thread(ctx, tid)
	if err != nil {
		s.log.Warn("progress: topic not found", "thread", tid, "err", err)
		return
	}
	row, has, err := s.st.Progress(ctx, tid)
	if err != nil {
		s.log.Warn("progress: read failed", "thread", tid, "err", err)
		return
	}
	// A row stuck in "sending" means a send of unknown outcome.
	if has && row.State == store.ProgressSending {
		row.State = store.ProgressUnknown
		_ = s.st.SaveProgress(bg, row)
	}
	pr := &progRun{s: s, ctx: ctx, bg: bg, th: th, row: row, has: has, op: op}
	switch op.kind {
	case Show:
		pr.show()
	case Edit:
		pr.edit()
	case Repost:
		pr.repost()
	case Finish:
		pr.finish()
	}
}

type progRun struct {
	s   *Sender
	ctx context.Context
	bg  context.Context
	th  store.Thread
	row store.Progress
	has bool
	op  *progOp
}

func (p *progRun) save(row store.Progress) {
	if err := p.s.st.SaveProgress(p.bg, row); err != nil {
		p.s.log.Error("progress: save failed", "thread", p.th.ID, "err", err)
	}
}

func (p *progRun) drop() {
	if err := p.s.st.DeleteProgress(p.bg, p.th.ID); err != nil {
		p.s.log.Error("progress: delete failed", "thread", p.th.ID, "err", err)
	}
}

func (p *progRun) retry(delay time.Duration) { p.s.requeue(p.th.ID, p.op, delay) }

// backoffOrDrop retries an idempotent request later; it reports false when the
// attempts ran out.
func (p *progRun) backoffOrDrop() bool {
	p.op.attempts++
	if p.op.attempts >= maxAttempts {
		p.s.log.Warn("progress: giving up", "thread", p.th.ID, "kind", p.op.kind)
		return false
	}
	p.retry(backoff(p.op.attempts - 1))
	return true
}

func (p *progRun) show() {
	if p.has {
		switch p.row.State {
		case store.ProgressShown:
			p.edit()
			return
		case store.ProgressUnknown, store.ProgressSending:
			return // never retried within the turn
		}
	}
	p.post(p.op.html, 0)
}

// post sends a new progress message and stores it. keepOld is the message ID
// of the message that a repost replaces (0 for a plain Show).
func (p *progRun) post(html string, keepOld int64) {
	now := p.s.now()
	row := store.Progress{ThreadID: p.th.ID, State: store.ProgressSending, StartedAt: now}
	if p.has {
		row.TurnKey, row.Headline, row.Rows = p.row.TurnKey, p.row.Headline, p.row.Rows
		if !p.row.StartedAt.IsZero() {
			row.StartedAt = p.row.StartedAt
		}
	}
	if keepOld == 0 {
		p.save(row)
	}
	id, v, _ := p.s.sendText(p.ctx, p.th.ID, p.th.ChatID, p.th.MessageThreadID, html, true, true)
	switch v {
	case vOK:
		row.State, row.TGMessageID, row.EditedAt = store.ProgressShown, id, p.s.now()
		p.save(row)
		if keepOld != 0 {
			p.s.deleteMessage(p.th, keepOld)
		}
	case vUnknown:
		row.State, row.TGMessageID = store.ProgressUnknown, keepOld
		p.save(row)
	case vRetry:
		if keepOld == 0 {
			if p.has {
				p.save(p.row)
			} else {
				p.drop()
			}
		}
		p.retry(0)
	case vBlocked:
		if keepOld == 0 {
			p.drop()
		}
	default: // topic gone or a clear refusal: no progress message this turn
		if keepOld == 0 {
			p.drop()
		}
	}
}

func (p *progRun) edit() {
	if !p.has {
		return
	}
	switch p.row.State {
	case store.ProgressShown:
	case store.ProgressWaiting:
		p.post(p.op.html, 0)
		return
	default:
		return
	}
	v := p.editText(p.op.html)
	switch v {
	case vOK, vNotModified:
		p.row.EditedAt = p.s.now()
		p.save(p.row)
	case vEditGone:
		p.drop()
		p.has = false
		p.post(p.op.html, 0)
	case vRetry:
		p.retry(0)
	case vUnknown:
		p.backoffOrDrop()
	case vGone:
		p.drop()
	}
}

// editText edits the shown message, falling back to plain text when Telegram
// cannot parse the HTML.
func (p *progRun) editText(html string) verdict {
	e := telegram.EditMessageTextParams{ChatID: p.th.ChatID, MessageID: p.row.TGMessageID, Text: html, ParseMode: "HTML"}
	if p.s.token(p.ctx) != nil {
		return vUnknown
	}
	_, err := p.s.tg.EditMessageText(p.ctx, e)
	v := p.s.judge(err, p.th.ID)
	if v == vParse {
		e.ParseMode, e.Text = "", PlainFromHTML(html)
		if p.s.token(p.ctx) != nil {
			return vUnknown
		}
		_, err = p.s.tg.EditMessageText(p.ctx, e)
		v = p.s.judge(err, p.th.ID)
	}
	return v
}

func (p *progRun) repost() {
	if !p.has || p.row.State == store.ProgressWaiting {
		p.post(p.op.html, 0)
		return
	}
	if p.row.State != store.ProgressShown {
		return
	}
	p.post(p.op.html, p.row.TGMessageID)
}

func (p *progRun) finish() {
	if !p.has {
		return
	}
	if p.row.State != store.ProgressShown {
		p.drop()
		return
	}
	switch p.editText(p.op.html) {
	case vRetry:
		p.retry(0)
	case vUnknown:
		if !p.backoffOrDrop() {
			p.drop()
		}
	case vBlocked:
		// Sending is paused; the row stays for the next run.
	default:
		p.drop()
	}
}

// deleteMessage deletes a message as an idempotent job, retried with backoff.
func (s *Sender) deleteMessage(th store.Thread, messageID int64) {
	j := &job{prio: PriorityEdit}
	j.run = func(ctx context.Context, j *job) jobResult {
		if err := s.token(ctx); err != nil {
			return jobResult{}
		}
		switch s.judge(s.tg.DeleteMessage(ctx, th.ChatID, messageID), th.ID) {
		case vRetry:
			return jobResult{retry: true}
		case vUnknown:
			if j.attempts++; j.attempts < maxAttempts {
				return jobResult{retry: true, delay: backoff(j.attempts - 1)}
			}
			s.log.Warn("delete: giving up", "thread", th.ID)
		case vFailed:
			s.log.Warn("delete refused", "thread", th.ID)
		}
		return jobResult{}
	}
	s.addJob(j)
}
