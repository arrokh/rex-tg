package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/agents/claude"
	"rex-tg/internal/agents/codex"
	"rex-tg/internal/agents/pi"
	"rex-tg/internal/config"
	"rex-tg/internal/inbound"
	"rex-tg/internal/registry"
	"rex-tg/internal/rex"
	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/tail"
	"rex-tg/internal/telegram"
	"rex-tg/internal/turn"
)

const (
	tickInterval = 250 * time.Millisecond
	prepInterval = 500 * time.Millisecond
	// tailerGrace is how long a tailer of a stopped session keeps reading, so
	// the last lines written before the agent exited still arrive.
	tailerGrace = 2 * time.Second
)

// Core events. Workers and producers send them with emit; only the core loop
// handles them.
type (
	evUpdates struct {
		ups  []telegram.Update
		next *int64 // set before done is closed: the offset to poll from
		done chan struct{}
	}
	evBatch struct {
		conv int64
		gen  int
		b    tail.Batch
	}
	evChanges struct{ ch []registry.Change }
	evSender  struct{ e sender.Event }
	// evPrepared is the result of the readiness and safety checks for the
	// oldest queued message of a topic.
	evPrepared struct {
		thread int64
		in     store.Inbound
		res    prepResult
	}
	// evTyped reports the end of a paste and Enter.
	evTyped struct {
		thread int64
		in     store.Inbound
		err    error
	}
	// evTopicCreated reports a new Telegram topic that the core must link.
	evTopicCreated struct {
		chatID, threadID int64
		name             string
		convID           int64
		title            string
	}
	// evQueueText asks the core to queue text for typing (media, voice, force).
	evQueueText struct {
		threadID, orig, tgMsg int64
		text, kind            string
		action                inbound.Action
	}
	evOpenDone  struct{ paneID int64 }
	evTailScan  tailScan
	evRestart   struct{ conv int64 }
	evAlertDone struct{ threadID int64 }
	evFatal     struct{ err error }
)

// daemon is the wiring. Fields marked "core" are touched only by the core
// goroutine; the others are safe for concurrent use.
type daemon struct {
	cfg      config.Config
	home     string
	log      *slog.Logger
	now      func() time.Time
	st       *store.Store
	tg       *telegram.Client
	rx       *rex.Client
	reg      *registry.Registry
	snd      *sender.Sender
	adapters map[agents.Runtime]agents.Adapter

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	events chan any
	regq   *jobQueue

	paused    atomic.Bool
	answering sync.Map // alert ids whose answer is being sent
	vimStuck  sync.Map // pane id -> the pi-vim label that showed no reaction to a key
	botUser   string   // set before the workers start
	scanQueue atomic.Bool

	// core
	ownerUser, ownerChat int64
	tailers              map[int64]*tailer       // by conversation
	pending              map[int64]pendingTail   // tailers waiting for their first scan, by conversation
	trackers             map[int64]*turn.Tracker // by thread
	snaps                map[int64]string        // last saved tracker snapshot
	joiner               *inbound.Joiner
	joining              map[int64]int64   // update id -> Telegram message id, while in the joiner
	extra                map[int64][]int64 // joined row -> Telegram messages that get its reactions too
	actions              map[int64]inbound.Action
	dirty                map[int64]bool // threads that may have queued messages
	busy                 map[int64]bool // threads with a delivery in progress
	nextPrep             map[int64]time.Time
	delivering           map[int64]*delivery
	waitNoticed          map[int64]bool
	vimNoticed           map[int64]bool // inbound rows told that pi-vim did not react
	alertBusy            map[int64]bool
	opening              map[int64]bool // panes whose topic is being created
	lastThread           int64
	lastScan, lastHouse  time.Time
	lastNames            time.Time // the last check of topic names, see syncNames
	gen                  int
	unknown              unknownCounts
	fatal                error
	storeErr             error // set while handling an update that could not be stored
}

func newDaemon(cfg config.Config, home string, log *slog.Logger, now func() time.Time, st *store.Store,
	tg *telegram.Client, rx *rex.Client, snd *sender.Sender, procStart func(int) (time.Time, error)) *daemon {
	adapters := map[agents.Runtime]agents.Adapter{
		agents.Pi: pi.New(), agents.Claude: claude.New(), agents.Codex: codex.New(),
	}
	d := &daemon{
		cfg: cfg, home: home, log: log, now: now, st: st, tg: tg, rx: rx, snd: snd, adapters: adapters,
		events:      make(chan any, 256),
		regq:        newJobQueue(),
		tailers:     map[int64]*tailer{},
		pending:     map[int64]pendingTail{},
		trackers:    map[int64]*turn.Tracker{},
		snaps:       map[int64]string{},
		joining:     map[int64]int64{},
		extra:       map[int64][]int64{},
		actions:     map[int64]inbound.Action{},
		dirty:       map[int64]bool{},
		busy:        map[int64]bool{},
		nextPrep:    map[int64]time.Time{},
		delivering:  map[int64]*delivery{},
		waitNoticed: map[int64]bool{},
		vimNoticed:  map[int64]bool{},
		alertBusy:   map[int64]bool{},
		opening:     map[int64]bool{},
	}
	jw := cfg.InputSettings.JoinWindow.Duration
	if jw <= 0 {
		jw = 300 * time.Millisecond
	}
	jl := cfg.InputSettings.JoinWindowLong.Duration
	if jl <= 0 {
		jl = 1500 * time.Millisecond
	}
	d.joiner = inbound.NewJoiner(jw, jl, 4000)
	d.reg = registry.New(st, rx, registry.Options{
		Home: home,
		Rules: registry.Rules{
			PiArgv0: cfg.Agents.PiArgv0, ClaudeArgv0: cfg.Agents.ClaudeArgv0,
			ClaudeExecGlob: cfg.Agents.ClaudeExecGlob, CodexNames: cfg.Agents.CodexNames,
		},
		Adapters:        adapters,
		Now:             now,
		Logger:          log,
		ProcStart:       procStart,
		BusyAfterTyping: cfg.InputSettings.BusyAfterTyping.Duration,
	})
	return d
}

// spawn runs f in a goroutine that run waits for.
func (d *daemon) spawn(f func()) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		f()
	}()
}

// emit sends an event to the core loop. It gives up when the daemon stops.
func (d *daemon) emit(ev any) {
	select {
	case d.events <- ev:
	case <-d.ctx.Done():
	}
}

func (d *daemon) run(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	d.ctx, d.cancel = ctx, cancel
	defer func() {
		cancel()
		d.stopAllTailers()
		d.flushUnknown(context.Background())
		d.wg.Wait()
	}()

	if err := d.startup(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	d.spawn(func() {
		if err := d.snd.Run(ctx); err != nil && ctx.Err() == nil {
			d.log.Error("sender stopped", "err", err)
			d.emit(evFatal{fmt.Errorf("daemon: sender: %w", err)})
		}
	})
	d.spawn(func() { d.forwardSenderEvents(ctx) })
	d.spawn(func() { d.registryWorker(ctx) })
	d.spawn(func() { d.rexEventLoop(ctx) })
	d.spawn(func() { d.spoolLoop(ctx) })
	d.spawn(func() { d.pollLoop(ctx) })
	return d.loop(ctx)
}

func (d *daemon) loop(ctx context.Context) error {
	tick := time.NewTicker(tickInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return d.fatal
		case ev := <-d.events:
			d.handle(ev)
		case <-tick.C:
			d.tick()
		}
		if d.fatal != nil {
			return d.fatal
		}
	}
}

func (d *daemon) handle(ev any) {
	switch e := ev.(type) {
	case evUpdates:
		d.onUpdates(e)
	case evBatch:
		d.onBatch(e)
	case evChanges:
		d.onChanges(e.ch)
	case evSender:
		d.onSenderEvent(e.e)
	case evPrepared:
		d.onPrepared(e)
	case evTyped:
		d.onTyped(e)
	case evTopicCreated:
		d.onTopicCreated(e)
	case evQueueText:
		d.queueText(e.threadID, e.orig, e.tgMsg, e.text, e.kind, e.action)
	case evTailScan:
		d.onTailScan(e)
	case evRestart:
		d.restartTailer(e.conv)
	case evOpenDone:
		delete(d.opening, e.paneID)
	case evAlertDone:
		d.alertDone(e.threadID)
	case evFatal:
		if d.fatal == nil {
			d.fatal = e.err
		}
	}
}

// tick runs every 250 ms: timers of the turn trackers, the joiner, queued
// messages, the scan, tailers waiting to stop, and housekeeping.
func (d *daemon) tick() {
	now := d.now()
	for tid, tr := range d.trackers {
		th, err := d.st.Thread(d.ctx, tid)
		if err != nil || th.Status == store.ThreadClosed {
			continue
		}
		d.perform(th, tr.Tick(now))
		d.saveSnapshot(tid, tr)
	}
	for _, g := range d.joiner.Due(now) {
		d.joined(g)
	}
	d.pump(now)
	for conv, tl := range d.tailers {
		if !tl.stopAt.IsZero() && !now.Before(tl.stopAt) {
			d.stopTailer(conv, false)
		}
	}
	if iv := d.cfg.Rex.ScanInterval.Duration; iv > 0 && now.Sub(d.lastScan) >= iv {
		d.lastScan = now
		d.requestScan()
	}
	d.maybeFlushUnknown(now)
	if now.Sub(d.lastHouse) >= 24*time.Hour {
		d.lastHouse = now
		d.spawn(func() { d.housekeeping(now) })
	}
	if now.Sub(d.lastNames) >= nameSyncInterval {
		d.lastNames = now
		d.syncNames()
	}
}

func (d *daemon) onSenderEvent(e sender.Event) {
	switch e.Kind {
	case sender.ThreadGone:
		d.log.Warn("topic is gone; closing it", "thread", e.ThreadID)
		d.closeThread(e.ThreadID)
	case sender.Blocked:
		d.log.Error("the bot is blocked by the user; stopping")
		d.fatal = errors.New("daemon: the bot was blocked by the user (403)")
	case sender.Conflict:
		d.log.Error("another program polls with the same token; stopping")
		d.fatal = errors.New("daemon: another program polls with the same bot token (409 Conflict)")
	}
}

func (d *daemon) forwardSenderEvents(ctx context.Context) {
	ch := d.snd.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			d.emit(evSender{e})
		}
	}
}

// closeThread closes a topic in the store and stops everything that serves it.
func (d *daemon) closeThread(threadID int64) {
	th, err := d.st.Thread(d.ctx, threadID)
	if err != nil {
		return
	}
	if th.Status != store.ThreadClosed {
		if err := d.st.SetThreadStatus(d.ctx, threadID, store.ThreadClosed); err != nil {
			d.log.Warn("close topic", "thread", threadID, "err", err)
		}
	}
	for conv, tl := range d.tailers {
		if tl.thread == threadID {
			d.stopTailer(conv, false)
		}
	}
	for conv, p := range d.pending {
		if p.thread == threadID {
			delete(d.pending, conv)
		}
	}
	d.closeAlerts(th, store.AlertGone)
	// Messages that still wait for this topic can never be typed.
	if qs, err := d.st.QueuedInbound(d.ctx, threadID); err == nil {
		for _, q := range qs {
			if err := d.st.SetInboundState(d.ctx, q.UpdateID, store.InboundRefused, "topic closed", d.now()); err != nil {
				d.log.Warn("close topic: refuse queued message", "err", err)
			}
			delete(d.actions, q.UpdateID)
			delete(d.waitNoticed, q.UpdateID)
			delete(d.vimNoticed, q.UpdateID)
			delete(d.extra, q.UpdateID)
		}
	}
	delete(d.trackers, threadID)
	delete(d.snaps, threadID)
	delete(d.dirty, threadID)
	delete(d.busy, threadID)
	delete(d.nextPrep, threadID)
	delete(d.alertBusy, threadID)
	delete(d.delivering, threadID)
}

// --- small helpers shared by the files ---

func (d *daemon) kvInt(key string) int64 {
	v, ok, err := d.st.Get(d.ctx, key)
	if err != nil || !ok {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func (d *daemon) kvSetInt(key string, n int64) {
	if err := d.st.Set(d.ctx, key, strconv.FormatInt(n, 10)); err != nil {
		d.log.Warn("save value", "key", key, "err", err)
	}
}

// nextN returns 1, 2, 3, ... for a key. It gives the keys of dividers and
// notices a stable number that survives restarts.
func (d *daemon) nextN(key string) int64 {
	n := d.kvInt(key) + 1
	d.kvSetInt(key, n)
	return n
}

// enqueue puts an item in the outbox and wakes the sender.
func (d *daemon) enqueue(it store.OutboxItem) {
	if len(it.Parts) == 0 && !it.Skip {
		return
	}
	if _, _, err := d.st.Enqueue(d.ctx, it); err != nil {
		d.log.Warn("enqueue", "thread", it.ThreadID, "kind", it.Kind, "err", err)
		return
	}
	d.snd.Wake()
}

// notice enqueues one HTML message for a topic. The key must be built from
// stable inputs.
func (d *daemon) notice(threadID int64, key, html string) {
	d.enqueue(store.OutboxItem{
		ThreadID: threadID, Key: key, Kind: "notice",
		Parts: []store.PartContent{{Content: html, Format: store.FormatHTML}},
	})
}

// say sends an HTML message right away through the sender (not the outbox).
// It is used for the main chat, buttons, and answers about closed topics.
func (d *daemon) say(chatID, threadID int64, html string, markup *telegram.InlineKeyboardMarkup) {
	d.spawn(func() { _, _ = d.sendNow(chatID, threadID, html, markup) })
}

func (d *daemon) sendNow(chatID, threadID int64, html string, markup *telegram.InlineKeyboardMarkup) (telegram.Message, error) {
	var msg telegram.Message
	err := d.snd.Call(sender.PriorityAnswer, func(ctx context.Context, tg *telegram.Client) error {
		m, err := tg.SendMessage(ctx, telegram.SendMessageParams{
			ChatID: chatID, MessageThreadID: threadID, Text: html, ParseMode: "HTML", ReplyMarkup: markup,
		})
		msg = m
		return err
	})
	if err != nil && d.ctx.Err() == nil {
		d.log.Warn("send message", "chat", chatID, "thread", threadID, "err", err)
	}
	return msg, err
}

// answerCallback answers a button press in a worker.
func (d *daemon) answerCallback(id, text string, alert bool) {
	d.spawn(func() {
		err := d.snd.Call(sender.PriorityAlert, func(ctx context.Context, tg *telegram.Client) error {
			return tg.AnswerCallbackQuery(ctx, id, text, alert)
		})
		if err != nil && d.ctx.Err() == nil {
			d.log.Debug("answer callback", "err", err)
		}
	})
}

// --- the job queue of the registry worker ---

type regJob struct {
	run  func(ctx context.Context) ([]registry.Change, error)
	done chan error // optional, buffered
}

type jobQueue struct {
	mu    sync.Mutex
	items []regJob
	sig   chan struct{}
}

func newJobQueue() *jobQueue { return &jobQueue{sig: make(chan struct{}, 1)} }

func (q *jobQueue) push(j regJob) {
	q.mu.Lock()
	q.items = append(q.items, j)
	q.mu.Unlock()
	select {
	case q.sig <- struct{}{}:
	default:
	}
}

func (q *jobQueue) pop(ctx context.Context) (regJob, bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			j := q.items[0]
			q.items = q.items[1:]
			q.mu.Unlock()
			return j, true
		}
		q.mu.Unlock()
		select {
		case <-q.sig:
		case <-ctx.Done():
			return regJob{}, false
		}
	}
}

// registryWorker runs the registry's calls one at a time, in the order they
// were queued, and hands the changes to the core loop.
func (d *daemon) registryWorker(ctx context.Context) {
	for {
		j, ok := d.regq.pop(ctx)
		if !ok {
			return
		}
		changes, err := j.run(ctx)
		if err != nil && ctx.Err() == nil {
			d.log.Warn("registry", "err", err)
		}
		if len(changes) > 0 {
			d.emit(evChanges{changes})
		}
		if j.done != nil {
			j.done <- err
		}
	}
}

// requestScan queues one scan, unless one is already waiting.
func (d *daemon) requestScan() {
	if !d.scanQueue.CompareAndSwap(false, true) {
		return
	}
	d.regq.push(regJob{run: func(ctx context.Context) ([]registry.Change, error) {
		d.scanQueue.Store(false)
		return d.reg.Scan(ctx)
	}})
}

// scanAndWait queues a scan and waits for its result (used by /agents).
func (d *daemon) scanAndWait(ctx context.Context) error {
	done := make(chan error, 1)
	d.regq.push(regJob{run: func(ctx context.Context) ([]registry.Change, error) { return d.reg.Scan(ctx) }, done: done})
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// signal sends a transcript state signal to the registry.
func (d *daemon) signal(conv int64, sig registry.Signal) {
	d.regq.push(regJob{run: func(ctx context.Context) ([]registry.Change, error) {
		return d.reg.TranscriptSignal(ctx, conv, sig)
	}})
}
