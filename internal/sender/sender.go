// Package sender is the only component that writes to Telegram. It delivers
// outbox parts in order, keeps progress messages, and owns the shared rate
// budget and the priorities of design sections 9.1 to 9.3.
//
// One goroutine (Run) performs every request, one at a time. That keeps the
// rule "one request in flight per topic" trivially true and makes the budget
// exact. Message text and tokens are never logged.
package sender

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// Priority orders work when the rate budget is short. A lower value goes first.
type Priority int

// Priorities, from most to least important (design section 9.3).
const (
	// PriorityAnswer is for outbox answers and notices.
	PriorityAnswer Priority = iota
	// PriorityAlert is for permission prompts and questions.
	PriorityAlert
	// PriorityNewProgress is for new progress messages (Show, Repost).
	PriorityNewProgress
	// PriorityEdit is for progress edits and finishes.
	PriorityEdit
	// PriorityTyping is for "typing…" actions. They are dropped when the budget is short.
	PriorityTyping
)

// EventKind says what an Event reports.
type EventKind int

// Event kinds.
const (
	// ThreadGone: Telegram says the topic does not exist. The daemon closes it.
	ThreadGone EventKind = iota + 1
	// Blocked: the user blocked the bot. Sending stops until Run restarts.
	Blocked
	// Conflict: a call reported another poller for the same token.
	Conflict
	// PartUnknown: an outbox part may or may not have arrived.
	PartUnknown
)

// Event is a fact the daemon must act on.
type Event struct {
	Kind     EventKind
	ThreadID int64
	OutboxID int64
	Part     int
}

// ProgressKind is the operation of a ProgressOp.
type ProgressKind int

// Progress operations.
const (
	// Show posts the progress message (an Edit when it is already shown).
	Show ProgressKind = iota + 1
	// Edit changes the text of the progress message.
	Edit
	// Repost posts a new progress message, then deletes the old one.
	Repost
	// Finish edits the progress message a last time and forgets it.
	Finish
)

// ProgressOp is one change to a topic's progress message.
type ProgressOp struct {
	Kind ProgressKind
	HTML string
}

// Errors returned by Call.
var (
	// ErrBlocked means the bot is blocked by the user and sending is paused.
	ErrBlocked = errors.New("sender: bot is blocked")
	// ErrStopped means Run ended before the call could run.
	ErrStopped = errors.New("sender: stopped")
)

// Options configures a Sender.
type Options struct {
	// RatePerSecond is the refill rate of the budget (default 1).
	RatePerSecond float64
	// Burst is the size of the budget (default 5).
	Burst int
	// Now is the clock (default time.Now).
	Now func() time.Time
	// Sleep waits for d or until ctx ends (default: a real timer). Tests inject
	// a fake that advances their clock.
	Sleep func(ctx context.Context, d time.Duration) error
	// Logger receives diagnostics, never message text (nil discards).
	Logger *slog.Logger
}

const (
	maxAttempts  = 6
	backoffBase  = time.Second
	backoffCap   = 30 * time.Second
	idlePoll     = 2 * time.Second
	eventBufSize = 256
)

// Sender writes to Telegram.
type Sender struct {
	st    *store.Store
	tg    *telegram.Client
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
	log   *slog.Logger

	events chan Event
	wake   chan struct{}

	mu       sync.Mutex
	bucket   bucket
	jobs     []*job
	prog     map[int64][]*progOp
	seq      uint64
	blocked  bool
	stopped  bool
	finished map[int64]bool // topics whose progress row holds a finished summary
	gone     map[int64]bool
	served   map[int64]uint64
	// reactions holds the newest reaction per message. An older reaction
	// that has not been sent yet is skipped, so a message always ends with
	// the newest one, also when an older one waits for a retry.
	reactions map[[2]int64]uint64
	reactGen  uint64
}

// New returns a Sender. Call Run to start it.
func New(st *store.Store, tg *telegram.Client, opt Options) *Sender {
	if opt.RatePerSecond <= 0 {
		opt.RatePerSecond = 1
	}
	if opt.Burst <= 0 {
		opt.Burst = 5
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Sleep == nil {
		opt.Sleep = realSleep
	}
	if opt.Logger == nil {
		opt.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Sender{
		st: st, tg: tg, now: opt.Now, sleep: opt.Sleep, log: opt.Logger,
		events:    make(chan Event, eventBufSize),
		wake:      make(chan struct{}, 1),
		bucket:    newBucket(opt.RatePerSecond, opt.Burst, opt.Now()),
		prog:      map[int64][]*progOp{},
		gone:      map[int64]bool{},
		finished:  map[int64]bool{},
		served:    map[int64]uint64{},
		reactions: map[[2]int64]uint64{},
	}
}

func realSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Events returns the channel of events. If nobody reads it, events are dropped
// once its buffer is full.
func (s *Sender) Events() <-chan Event { return s.events }

func (s *Sender) emit(ev Event) {
	select {
	case s.events <- ev:
	default:
		s.log.Warn("sender event dropped", "kind", ev.Kind, "thread", ev.ThreadID)
	}
}

// Wake tells the sender that new outbox rows exist.
func (s *Sender) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// job is a one-off request: a Call, a reaction, typing, or a delete.
type job struct {
	prio      Priority
	seq       uint64
	notBefore time.Time
	attempts  int
	typing    bool
	run       func(ctx context.Context, j *job) jobResult
	done      chan error
}

type jobResult struct {
	retry bool
	delay time.Duration
	err   error
}

func (s *Sender) addJob(j *job) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return ErrStopped
	}
	if s.blocked {
		s.mu.Unlock()
		return ErrBlocked
	}
	s.seq++
	j.seq = s.seq
	s.jobs = append(s.jobs, j)
	s.mu.Unlock()
	s.Wake()
	return nil
}

// Call runs fn with the Telegram client once the budget allows, in the order of
// prio, and returns fn's error. A 429 answer pauses the budget and runs fn
// again. Use it for topics, alerts, and callback answers.
func (s *Sender) Call(prio Priority, fn func(ctx context.Context, tg *telegram.Client) error) error {
	j := &job{prio: prio, done: make(chan error, 1)}
	j.run = func(ctx context.Context, j *job) jobResult {
		if err := s.token(ctx); err != nil {
			return jobResult{err: err}
		}
		err := fn(ctx, s.tg)
		switch telegram.Classify(err) {
		case telegram.ClassRateLimited:
			s.pauseFor(err)
			if j.attempts++; j.attempts < maxAttempts {
				return jobResult{retry: true}
			}
		case telegram.ClassBlocked:
			s.block(0)
		case telegram.ClassConflict:
			s.emit(Event{Kind: Conflict})
		}
		return jobResult{err: err}
	}
	if err := s.addJob(j); err != nil {
		return err
	}
	return <-j.done
}

// React sets a reaction on a message. It waits for budget; errors are logged.
// A newer reaction for the same message replaces an older one that was not
// sent yet.
func (s *Sender) React(chatID, messageID int64, emoji string) {
	key := [2]int64{chatID, messageID}
	s.mu.Lock()
	s.reactGen++
	gen := s.reactGen
	s.reactions[key] = gen
	s.mu.Unlock()
	j := &job{prio: PriorityNewProgress}
	j.run = func(ctx context.Context, j *job) jobResult {
		if !s.reactionCurrent(key, gen) {
			return jobResult{} // a newer reaction for this message is queued
		}
		if err := s.token(ctx); err != nil {
			return jobResult{err: err}
		}
		if !s.reactionCurrent(key, gen) {
			return jobResult{}
		}
		err := s.tg.SetMessageReaction(ctx, chatID, messageID, emoji)
		if v := s.judge(err, 0); v == vRetry {
			return jobResult{retry: true}
		} else if v != vOK {
			s.log.Warn("reaction failed", "chat", chatID, "message", messageID, "verdict", v)
		}
		s.mu.Lock()
		if s.reactions[key] == gen {
			delete(s.reactions, key)
		}
		s.mu.Unlock()
		return jobResult{}
	}
	s.addJob(j)
}

// reactionCurrent reports whether gen is still the newest reaction for key.
func (s *Sender) reactionCurrent(key [2]int64, gen uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reactions[key] == gen
}

// Typing shows "typing…" in a topic. It is dropped when no budget is free.
func (s *Sender) Typing(chatID, threadID int64) {
	s.mu.Lock()
	if s.blocked || !s.bucket.available(s.now()) {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	j := &job{prio: PriorityTyping, typing: true}
	j.run = func(ctx context.Context, j *job) jobResult {
		if err := s.token(ctx); err != nil {
			return jobResult{err: err}
		}
		err := s.tg.SendChatAction(ctx, chatID, threadID, "typing")
		if v := s.judge(err, 0); v != vOK {
			s.log.Debug("typing failed", "thread", threadID, "verdict", v)
		}
		return jobResult{}
	}
	s.addJob(j)
}

// Run sends until ctx ends. A blocked bot pauses sending until Run is called
// again.
func (s *Sender) Run(ctx context.Context) error {
	s.mu.Lock()
	s.blocked = false
	s.stopped = false
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.stopped = true
		s.mu.Unlock()
		s.drain(ErrStopped)
	}()

	for ctx.Err() == nil {
		w := s.pick(ctx)
		switch w.kind {
		case wBlocked:
			s.drain(ErrBlocked)
			s.idle(ctx)
		case wNone:
			if w.until.IsZero() {
				s.idle(ctx)
			} else {
				s.sleepOrWake(ctx, w.until.Sub(s.now()))
			}
		default:
			s.mu.Lock()
			wait := s.bucket.wait(s.now())
			s.mu.Unlock()
			if wait > 0 {
				if w.kind == wJob && w.job.typing {
					s.removeJob(w.job)
					continue
				}
				s.sleepOrWake(ctx, wait)
				continue
			}
			s.execute(ctx, w)
		}
	}
	return nil
}

func (s *Sender) idle(ctx context.Context) {
	t := time.NewTimer(idlePoll)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-s.wake:
	case <-t.C:
	}
}

// sleepOrWake sleeps for d, but returns early when new work is signalled.
func (s *Sender) sleepOrWake(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		_ = s.sleep(cctx, d)
		close(done)
	}()
	select {
	case <-done:
	case <-s.wake:
	case <-ctx.Done():
	}
	cancel()
	<-done
}

// token waits until the budget has a request to spend, and spends it.
func (s *Sender) token(ctx context.Context) error {
	for {
		s.mu.Lock()
		w := s.bucket.reserve(s.now())
		s.mu.Unlock()
		if w <= 0 {
			return nil
		}
		if err := s.sleep(ctx, w); err != nil {
			return err
		}
	}
}

func (s *Sender) pauseBucket(d time.Duration) {
	s.mu.Lock()
	s.bucket.pause(s.now(), d)
	s.mu.Unlock()
}

// pauseFor pauses the budget for the retry_after of a 429 error.
func (s *Sender) pauseFor(err error) {
	d := time.Second
	var api *telegram.APIError
	if errors.As(err, &api) && api.RetryAfter > 0 {
		d = api.RetryAfter
	}
	s.pauseBucket(d)
}

// block pauses all sending until Run restarts.
func (s *Sender) block(threadID int64) {
	s.mu.Lock()
	already := s.blocked
	s.blocked = true
	s.mu.Unlock()
	if !already {
		s.emit(Event{Kind: Blocked, ThreadID: threadID})
	}
}

func (s *Sender) threadGone(threadID int64) {
	s.mu.Lock()
	first := !s.gone[threadID]
	s.gone[threadID] = true
	delete(s.prog, threadID)
	s.mu.Unlock()
	if first {
		s.emit(Event{Kind: ThreadGone, ThreadID: threadID})
	}
}

// drain fails every queued job with err and forgets queued progress work.
func (s *Sender) drain(err error) {
	s.mu.Lock()
	jobs := s.jobs
	s.jobs = nil
	if err == ErrBlocked {
		s.prog = map[int64][]*progOp{}
	}
	s.mu.Unlock()
	for _, j := range jobs {
		if j.done != nil {
			j.done <- err
		}
	}
}

func (s *Sender) removeJob(j *job) {
	s.mu.Lock()
	for i, x := range s.jobs {
		if x == j {
			s.jobs = append(s.jobs[:i:i], s.jobs[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
}

type workKind int

const (
	wNone workKind = iota
	wBlocked
	wPart
	wJob
	wProg
)

type work struct {
	kind  workKind
	part  store.Part
	job   *job
	tid   int64
	until time.Time // wNone: the earliest time deferred work becomes ready
}

// pick chooses the most important ready work.
func (s *Sender) pick(ctx context.Context) work {
	s.mu.Lock()
	blocked := s.blocked
	s.mu.Unlock()
	if blocked {
		return work{kind: wBlocked}
	}
	parts, err := s.st.NextParts(ctx)
	if err != nil && ctx.Err() == nil {
		s.log.Warn("outbox read failed", "err", err)
	}
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()
	var best work
	bestPrio, bestSeq := Priority(1<<30), uint64(0)
	var until time.Time
	deferTo := func(t time.Time) {
		if until.IsZero() || t.Before(until) {
			until = t
		}
	}
	consider := func(w work, prio Priority, seq uint64) {
		if best.kind == wNone || prio < bestPrio || (prio == bestPrio && seq < bestSeq) {
			best, bestPrio, bestSeq = w, prio, seq
		}
	}
	if len(parts) > 0 {
		p := parts[0]
		for _, q := range parts[1:] {
			if s.served[q.ThreadID] < s.served[p.ThreadID] {
				p = q
			}
		}
		consider(work{kind: wPart, part: p}, PriorityAnswer, 0)
	}
	for _, j := range s.jobs {
		if j.notBefore.After(now) {
			deferTo(j.notBefore)
			continue
		}
		consider(work{kind: wJob, job: j}, j.prio, j.seq)
	}
	for tid, q := range s.prog {
		if len(q) == 0 {
			continue
		}
		op := q[0]
		if op.notBefore.After(now) {
			deferTo(op.notBefore)
			continue
		}
		prio := PriorityEdit
		if op.kind == Show || op.kind == Repost {
			prio = PriorityNewProgress
		}
		consider(work{kind: wProg, tid: tid}, prio, op.seq)
	}
	if best.kind == wNone {
		best.until = until
		return best
	}
	// Do not spend the budget on work when a deferred item is due sooner than
	// the budget refills; that only matters for ordering, so keep it simple.
	if best.kind == wPart {
		s.seq++
		s.served[best.part.ThreadID] = s.seq
	}
	return best
}

func (s *Sender) execute(ctx context.Context, w work) {
	switch w.kind {
	case wPart:
		s.sendPart(ctx, w.part)
	case wJob:
		s.removeJob(w.job)
		res := w.job.run(ctx, w.job)
		if res.retry {
			w.job.notBefore = s.now().Add(res.delay)
			s.mu.Lock()
			s.jobs = append(s.jobs, w.job)
			s.mu.Unlock()
			return
		}
		if w.job.done != nil {
			w.job.done <- res.err
		}
	case wProg:
		s.mu.Lock()
		q := s.prog[w.tid]
		var op *progOp
		if len(q) > 0 {
			op = q[0]
			s.prog[w.tid] = q[1:]
			if len(q) == 1 {
				delete(s.prog, w.tid)
			}
		}
		s.mu.Unlock()
		if op != nil {
			s.runProgress(ctx, w.tid, op)
		}
	}
}

func backoff(attempts int) time.Duration {
	d := backoffBase << min(attempts, 10)
	return min(d, backoffCap)
}

// bucket is a token bucket with a pause for 429 answers.
type bucket struct {
	rate, burst, tokens float64
	last, pausedUntil   time.Time
}

func newBucket(rate float64, burst int, now time.Time) bucket {
	return bucket{rate: rate, burst: float64(burst), tokens: float64(burst), last: now}
}

func (b *bucket) refill(now time.Time) {
	if now.After(b.last) {
		b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
		b.last = now
	}
}

// wait returns how long until one request may run (0 = now). It spends nothing.
func (b *bucket) wait(now time.Time) time.Duration {
	b.refill(now)
	if now.Before(b.pausedUntil) {
		return b.pausedUntil.Sub(now)
	}
	if b.tokens >= 1-1e-9 {
		return 0
	}
	return time.Duration((1 - b.tokens) / b.rate * float64(time.Second))
}

func (b *bucket) available(now time.Time) bool { return b.wait(now) == 0 }

// reserve spends one request and returns 0, or returns the wait and spends nothing.
func (b *bucket) reserve(now time.Time) time.Duration {
	if w := b.wait(now); w > 0 {
		return w
	}
	b.tokens = max(0, b.tokens-1)
	return 0
}

func (b *bucket) pause(now time.Time, d time.Duration) {
	if until := now.Add(d); until.After(b.pausedUntil) {
		b.pausedUntil = until
	}
}
