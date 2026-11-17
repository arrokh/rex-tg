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
	// evWaitNotice reports the message number of a posted waiting notice.
	evWaitNotice struct {
		updateID, chat, msg int64
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
		ready            chan bool // buffered result: true once the intro is durable
	}
	// evQueueText asks the core to queue text for typing (media, voice, force).
	evQueueText struct {
		threadID, orig, tgMsg int64
		text, kind            string
		action                inbound.Action
		ctl                   *ctlSet   // a control command: its value counts once typed
		note                  *noteDone // a note to an alert: tell the user once queued
	}
	evFileRetryDone struct {
		updateID, callbackUpdateID int64
		thread                     store.Thread
		chatID, messageID          int64
		tgMessageID                int64
		file                       fileRetry
		path                       string
		err                        error
	}
	evOpenDone struct{ paneID int64 }
	evTailScan tailScan
	evRestart  struct{ conv int64 }
	// evTailEnded: a follower stopped on a read error (gen tells which one).
	evTailEnded struct {
		conv int64
		gen  int
	}
	// evNewSent: the question of a /new request was sent as message msg.
	evNewSent struct {
		token string
		msg   int64
	}
	// evNewFolders: the folders that a /new folder question offers.
	evNewFolders struct {
		token   string
		folders []string
	}
	// evNewDone: a /new request ended.
	evNewDone struct{ token string }
	// evNewWaiting: the agent of a /new request runs; answers takes the
	// presses of its startup question's buttons.
	evNewWaiting struct {
		token   string
		answers chan startupAnswer
	}
	// evNewReady: the agent that /new started reported its session.
	evNewReady  struct{ paneID, thread int64 }
	evAlertDone struct{ threadID int64 }
	// evAlertStale: an alert's prompt is no longer on the screen.
	evAlertStale struct{ threadID int64 }
	evFatal      struct{ err error }
)

// rexClient is the part of the Rex client that the daemon calls itself (the
// registry gets the full client). It is a *rex.Client, except in unit tests,
// which use an in-memory pane.
type rexClient interface {
	Whoami(ctx context.Context) (rex.Whoami, error)
	Events(ctx context.Context) (<-chan rex.Event, <-chan error)
	Process(ctx context.Context, session, block string) (rex.Process, error)
	Capture(ctx context.Context, session, block string) (string, error)
	CaptureVT(ctx context.Context, session, block string) (string, error)
	Paste(ctx context.Context, session, block, text string) error
	SendKeys(ctx context.Context, session, block string, keys ...string) error
	Sessions(ctx context.Context) ([]rex.Session, error)
	NewSession(ctx context.Context, label string, tab rex.Tab) (rex.Started, error)
	NewWindow(ctx context.Context, session string, tab rex.Tab) (rex.Started, error)
	Blocks(ctx context.Context, session string) ([]rex.Block, error)
	Windows(ctx context.Context, session string) ([]rex.Window, error)
	RenameWindow(ctx context.Context, session, window, label string) error
	RenameBlock(ctx context.Context, session, block, label string) error
	RenameSession(ctx context.Context, session, label string) error
}

var _ rexClient = (*rex.Client)(nil)

// daemon is the wiring. Fields marked "core" are touched only by the core
// goroutine; the others are safe for concurrent use.
type daemon struct {
	lastHold      atomic.Pointer[telegram.Hold] // a long Telegram wait whose end note is due (hold.go)
	holdMu        sync.Mutex                    // orders noteHold and checkHoldEnd
	tabMu         sync.Mutex                    // serializes the choice of tab names (syncTab), and guards the two below
	tabSessions   []rex.Session                 // the Rex sessions, cached for syncTab
	tabSessionsAt time.Time
	cfg           config.Config
	timing        Timing // fixed waits, with defaults filled in
	home          string
	log           *slog.Logger
	now           func() time.Time
	st            *store.Store
	tg            *telegram.Client
	rx            rexClient
	reg           *registry.Registry
	snd           *sender.Sender
	adapters      map[agents.Runtime]agents.Adapter
	rules         registry.Rules // how a process is told to be an agent
	// hasProgram reports whether a program that /new can start (cux) is
	// installed; see installed.
	hasProgram func(name string) bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	events chan any
	regq   *jobQueue
	// editTopicFn replaces the Telegram call that sets a topic's name (tests).
	editTopicFn func(th store.Thread, name string) error

	paused     atomic.Bool
	answering  sync.Map      // alert ids whose answer is being sent
	alertEdits sync.Map      // alert id -> *sync.Mutex, see alertLock
	polled     *atomic.Int64 // when a getUpdates call last finished, unix ns
	// below holds the topics where a message came below the progress
	// message from outside the turn (any goroutine); tick hands them to
	// the trackers (core).
	belowMu sync.Mutex
	below   map[int64]bool
	// promptChecks are the sessions whose transcript went on while their
	// pane may be blocked, by when to read the screen (checkPromptGone).
	promptChecks map[int64]time.Time
	// blockedSince holds the blocked panes' sessions, by when they were
	// noted blocked, and recheckAt when their screen was last asked for
	// (tickPromptChecks).
	blockedSince  map[int64]time.Time
	recheckAt     map[int64]time.Time
	goneReads     goneConfirm        // the first read without a prompt, until a second confirms it
	turnTrees     map[int64]turnTree // the repository at each running turn's start, by session
	miniSecret    []byte             // the key that checks the Mini App's signed requests
	miniUp        atomic.Bool        // the Mini App's server listens
	kb            *keyboardSource    // the reply keyboard's key and address (keyboard.go)
	logins        *miniLogins        // the Tailscale logins that may use the key (logins.go)
	screenMu      sync.Mutex
	screenShots   map[int64]*screenRead // the last live-screen read of each topic (the map under screenMu)
	liveMu        sync.Mutex            // held while a live page is found or made
	blockedScanAt time.Time
	renamedBack   time.Time  // the last rename back of the session for /new (under tabMu)
	vimStuck      sync.Map   // pane id -> vimStuckMark: the vim label that showed no reaction to a key
	newMu         sync.Mutex // held while /new finds or makes a session, see startTab
	botUser       string     // set before the workers start
	scanQueue     atomic.Bool
	ctl           controlStore // latest setting values per session, see controls.go

	// core
	ownerUser, ownerChat int64
	tailers              map[int64]*tailer       // by conversation
	pending              map[int64]pendingTail   // tailers waiting for their first scan, by conversation
	trackers             map[int64]*turn.Tracker // by thread
	snaps                map[int64]string        // last saved tracker snapshot
	shown                map[int64]*shownTurn    // images the agent looked at in the running turn, by thread
	joiner               *inbound.Joiner
	joining              map[int64]int64   // update id -> Telegram message id, while in the joiner
	extra                map[int64][]int64 // joined row -> Telegram messages that get its reactions too
	actions              map[int64]inbound.Action
	dirty                map[int64]bool // threads that may have queued messages
	busy                 map[int64]bool // threads with a delivery in progress
	nextPrep             map[int64]time.Time
	delivering           map[int64]*delivery
	waitNoticed          map[int64]bool
	waitTexts            map[int64]string        // the text of each posted waiting notice
	macNotes             map[int64]*macNote      // the Mac hold's notice, by thread
	drafts               map[int64]*draft        // the draft of a running turn, by thread
	deadlines            map[int64]alertDeadline // the time limit of an open alert, by alert
	follows              map[int64]alertFollow   // a look for the next question, by thread
	alertWanted          map[int64]string        // an alert asked for while one was being made, by thread
	liveText             map[int64]liveText      // what pi's extension last wrote, by thread
	draftsOff            bool                    // Telegram refused drafts: none until the next start
	draftRefused         int                     // drafts refused in a row
	draftWorked          bool                    // a draft went through since the start (logged once)
	self                 *selfWatch              // the daemon's own binary, for the restart after an update
	board                *board                  // the live /agents message
	boardLoaded          bool                    // board was read from the store
	reminders            map[int64]*reminder     // reminders of open alerts, by alert
	lastRemind           time.Time               // the last look for due reminders
	sched                map[int64]schedule      // scheduled messages (/at), by id
	schedLoaded          bool                    // sched was read from the store
	lastSched            time.Time               // the last look for due schedules
	remindSwept          bool                    // stale reminders of closed alerts were dropped
	macSwept             bool                    // with MacNotice off, holds from before the start were cleared
	ctlPending           map[int64]ctlSet        // queued control commands, by row id
	vimNoticed           map[int64]bool          // inbound rows told that pi-vim did not react
	mediaRetryBusy       map[int64]bool          // media downloads started by retry buttons
	mediaSlots           chan struct{}           // bounds concurrent downloads and transcriptions
	alertBusy            map[int64]bool
	opening              map[int64]bool      // panes whose topic is being created
	newFlows             map[string]*newFlow // /new requests, by the token that their buttons carry
	newSeq               int64               // numbers the /new requests
	lastThread           int64
	lastScan, lastHouse  time.Time
	lastNames            time.Time // the last check of topic names, see syncNames
	gen                  int
	unknown              unknownCounts
	fatal                error
	storeErr             error // set while handling an update that could not be stored
}

func newDaemon(cfg config.Config, home string, log *slog.Logger, now func() time.Time, st *store.Store,
	tg *telegram.Client, rx *rex.Client, snd *sender.Sender, procStart func(int) (time.Time, error), timing Timing) *daemon {
	adapters := map[agents.Runtime]agents.Adapter{
		agents.Pi: pi.New(), agents.Claude: claude.New(), agents.Codex: codex.New(),
	}
	d := &daemon{
		cfg: cfg, timing: timing, home: home, log: log, now: now, st: st, tg: tg, rx: rx, snd: snd, adapters: adapters,
		polled:         &atomic.Int64{}, // Run shares its own with the sender
		kb:             &keyboardSource{},
		logins:         &miniLogins{},
		below:          map[int64]bool{},
		promptChecks:   map[int64]time.Time{},
		blockedSince:   map[int64]time.Time{},
		recheckAt:      map[int64]time.Time{},
		goneReads:      goneConfirm{},
		turnTrees:      map[int64]turnTree{},
		events:         make(chan any, 256),
		regq:           newJobQueue(),
		tailers:        map[int64]*tailer{},
		pending:        map[int64]pendingTail{},
		trackers:       map[int64]*turn.Tracker{},
		snaps:          map[int64]string{},
		shown:          map[int64]*shownTurn{},
		joining:        map[int64]int64{},
		extra:          map[int64][]int64{},
		actions:        map[int64]inbound.Action{},
		dirty:          map[int64]bool{},
		busy:           map[int64]bool{},
		nextPrep:       map[int64]time.Time{},
		delivering:     map[int64]*delivery{},
		waitNoticed:    map[int64]bool{},
		waitTexts:      map[int64]string{},
		macNotes:       map[int64]*macNote{},
		drafts:         map[int64]*draft{},
		deadlines:      map[int64]alertDeadline{},
		follows:        map[int64]alertFollow{},
		alertWanted:    map[int64]string{},
		liveText:       map[int64]liveText{},
		reminders:      map[int64]*reminder{},
		sched:          map[int64]schedule{},
		ctlPending:     map[int64]ctlSet{},
		vimNoticed:     map[int64]bool{},
		mediaRetryBusy: map[int64]bool{},
		mediaSlots:     make(chan struct{}, maxMediaWorkers),
		alertBusy:      map[int64]bool{},
		opening:        map[int64]bool{},
		newFlows:       map[string]*newFlow{},
	}
	d.logins.d = d
	d.hasProgram = func(name string) bool { return installed(home, name) }
	jw := cfg.InputSettings.JoinWindow.Duration
	if jw <= 0 {
		jw = 300 * time.Millisecond
	}
	jl := cfg.InputSettings.JoinWindowLong.Duration
	if jl <= 0 {
		jl = 1500 * time.Millisecond
	}
	d.joiner = inbound.NewJoiner(jw, jl, 4000)
	d.rules = registry.Rules{
		PiArgv0: cfg.Agents.PiArgv0, ClaudeArgv0: cfg.Agents.ClaudeArgv0,
		ClaudeExecGlob: cfg.Agents.ClaudeExecGlob, CodexNames: cfg.Agents.CodexNames,
	}
	d.reg = registry.New(st, rx, registry.Options{
		Home:            home,
		Rules:           d.rules,
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
	if d.miniAppOn() && len(d.miniSecret) > 0 {
		d.spawn(func() { d.serveMiniApp(ctx) })
	}
	if d.cfg.Display.Stream {
		d.spawn(func() { d.liveLoop(ctx) })
	}
	return d.loop(ctx)
}

func (d *daemon) loop(ctx context.Context) error {
	tick := time.NewTicker(d.timing.Tick)
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
	case evWaitNotice:
		d.onWaitNotice(e)
	case evMacSent:
		d.onMacSent(e)
	case evDraftSent:
		d.onDraftSent(e)
	case evAlertDeadline:
		d.deadlines[e.alert] = e.d
	case evPromptRead:
		d.onPromptRead(e)
	case evTurnTree:
		d.onTurnTree(e)
	case evTurnChanges:
		d.onTurnChanges(e)
	case evTurnRange:
		d.onTurnRange(e)
	case evLater:
		e.f()
	case evRename:
		d.onRename(e)
	case evAlertFollow:
		d.onAlertFollow(e)
	case evLive:
		d.onLive(e)
	case evBoard:
		d.onBoard(e)
	case evReminder:
		d.onReminder(e)
	case evTopicCreated:
		d.onTopicCreated(e)
	case evQueueText:
		if e.ctl != nil {
			d.keepPending(-e.orig, *e.ctl) // the row's id; kept before the row, so a stop between loses neither
		}
		queued := d.queueText(e.threadID, e.orig, e.tgMsg, e.text, e.kind, e.action)
		if queued && (e.kind == "file" || e.kind == "voice") {
			d.clearFileRetry(e.orig)
		}
		if !queued && e.ctl != nil {
			d.controlTyped(-e.orig, false)
		}
		if e.note != nil {
			d.noteQueued(e.threadID, e.note, queued)
		}
	case evFileRetryDone:
		d.fileRetryDone(e)
	case evTailScan:
		d.onTailScan(e)
	case evRestart:
		d.restartTailer(e.conv)
	case evTailEnded:
		// Only the follower that ended: a newer one may run already. One that
		// was about to stop (its session ended) is not started again.
		if tl, ok := d.tailers[e.conv]; ok && tl.gen == e.gen {
			delete(d.tailers, e.conv)
			tl.cancel()
			if tl.stopAt.IsZero() {
				d.restartTailer(e.conv)
			}
		}
	case evOpenDone:
		delete(d.opening, e.paneID)
	case evNewSent:
		if f, ok := d.newFlows[e.token]; ok && f.msg == 0 {
			f.msg = e.msg
		}
	case evNewFolders:
		if f, ok := d.newFlows[e.token]; ok && f.step == newPickFolder {
			f.folders = e.folders
		}
	case evNewDone:
		if f, ok := d.newFlows[e.token]; ok {
			d.dropAnswers(f)
		}
		delete(d.newFlows, e.token)
	case evNewWaiting:
		if f, ok := d.newFlows[e.token]; ok {
			f.step, f.answers = newWaiting, e.answers
		}
	case evNewReady:
		d.openNewTopic(e.paneID, e.thread)
	case evAlertDone:
		d.alertDone(e.threadID)
	case evAlertStale:
		d.reopenAlert(e.threadID)
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
	d.belowMu.Lock()
	for tid := range d.below {
		if tr := d.trackers[tid]; tr != nil {
			tr.MessageBelow()
		}
		delete(d.below, tid)
	}
	d.belowMu.Unlock()
	for tid, tr := range d.trackers {
		th, err := d.st.Thread(d.ctx, tid)
		if err != nil || th.Status == store.ThreadClosed {
			d.closeDraft(tid)
			continue
		}
		d.perform(th, tr.Tick(now))
		d.saveSnapshot(tid, tr)
		d.streamDraft(th, tr, now)
	}
	for _, g := range d.joiner.Due(now) {
		d.joined(g)
	}
	d.pump(now)
	d.checkHoldEnd()
	for conv, tl := range d.tailers {
		if !tl.stopAt.IsZero() && !now.Before(tl.stopAt) {
			d.stopTailer(conv, false)
		}
	}
	if iv := d.cfg.Rex.ScanInterval.Duration; iv > 0 && now.Sub(d.lastScan) >= iv {
		d.lastScan = now
		d.requestScan()
	}
	d.tickFollows(now)
	d.tickPromptChecks(now)
	d.expireNewFlows(now)
	d.maybeFlushUnknown(now)
	if now.Sub(d.lastHouse) >= 24*time.Hour {
		d.lastHouse = now
		d.spawn(func() { d.housekeeping(now) })
	}
	if now.Sub(d.lastNames) >= nameSyncInterval {
		d.lastNames = now
		d.syncNames()
		d.spawn(d.keepSessionName)
	}
	d.tickBoard(now)
	d.tickReminders(now)
	d.tickSchedules(now)
	d.sweepMacNotices()
	d.checkUpdate(now)
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
	if dl := d.delivering[threadID]; dl != nil && dl.cancel != nil {
		dl.cancel()
	}
	d.forgetMac(threadID)
	if err := d.st.Delete(d.ctx, lastTurnKey(threadID)); err != nil {
		d.log.Warn("close topic: last turn", "err", err)
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
	d.dropSchedules(threadID)
	// Messages that still wait for this topic can never be typed.
	if qs, err := d.st.QueuedInbound(d.ctx, threadID); err == nil {
		for _, q := range qs {
			if err := d.st.SetInboundState(d.ctx, q.UpdateID, store.InboundRefused, "topic closed", d.now()); err != nil {
				d.log.Warn("close topic: refuse queued message", "err", err)
			}
			d.dropWaitNotice(q.UpdateID)
			d.controlTyped(q.UpdateID, false)
			delete(d.actions, q.UpdateID)
			delete(d.waitNoticed, q.UpdateID)
			delete(d.vimNoticed, q.UpdateID)
			delete(d.extra, q.UpdateID)
		}
	}
	delete(d.trackers, threadID)
	delete(d.snaps, threadID)
	delete(d.shown, threadID)
	delete(d.dirty, threadID)
	delete(d.busy, threadID)
	delete(d.nextPrep, threadID)
	delete(d.alertBusy, threadID)
	delete(d.alertWanted, threadID)
	delete(d.follows, threadID)
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

// markBelow notes that a message goes below the topic's progress message, so
// the progress message moves down (any goroutine).
func (d *daemon) markBelow(threadID int64) {
	if threadID == 0 {
		return
	}
	d.belowMu.Lock()
	d.below[threadID] = true
	d.belowMu.Unlock()
}

// enqueue puts an item in the outbox and wakes the sender. It reports whether
// the row is durable.
func (d *daemon) enqueue(it store.OutboxItem) bool {
	if len(it.Parts) == 0 && !it.Skip {
		return false
	}
	if !it.Skip {
		d.markBelow(it.ThreadID)
	}
	if _, _, err := d.st.Enqueue(d.ctx, it); err != nil {
		d.log.Warn("enqueue", "thread", it.ThreadID, "kind", it.Kind, "err", err)
		return false
	}
	d.snd.Wake()
	return true
}

// notice enqueues one HTML message for a topic. The key must be built from
// stable inputs.
func (d *daemon) notice(threadID int64, key, html string) {
	d.enqueue(store.OutboxItem{
		ThreadID: threadID, Key: key, Kind: "notice", Silent: true, // only answers and prompts make a sound
		Parts: []store.PartContent{{Content: html, Format: store.FormatHTML}},
	})
}

// say sends an HTML message right away through the sender (not the outbox).
// It is used for the main chat, buttons, and answers about closed topics.
func (d *daemon) say(chatID, threadID int64, html string, markup *telegram.InlineKeyboardMarkup) {
	d.spawn(func() { _, _ = d.sendNow(chatID, threadID, html, markup) })
}

func (d *daemon) sendNow(chatID, threadID int64, html string, markup *telegram.InlineKeyboardMarkup) (telegram.Message, error) {
	return d.sendMsg(chatID, threadID, html, markup, true)
}

// sendPrompt is sendNow for a prompt that waits for the user: it makes a
// sound.
func (d *daemon) sendPrompt(chatID, threadID int64, html string, markup *telegram.InlineKeyboardMarkup) (telegram.Message, error) {
	return d.sendMsg(chatID, threadID, html, markup, false)
}

func (d *daemon) sendMsg(chatID, threadID int64, html string, markup *telegram.InlineKeyboardMarkup, silent bool) (telegram.Message, error) {
	return d.sendMsgKeys(chatID, threadID, html, markup, nil, silent)
}

// sendMsgKeys is sendMsg with a reply keyboard (kb), sent when markup is nil.
func (d *daemon) sendMsgKeys(chatID, threadID int64, html string, markup *telegram.InlineKeyboardMarkup, kb *telegram.ReplyKeyboardMarkup, silent bool) (telegram.Message, error) {
	var msg telegram.Message
	err := d.snd.Call(sender.PriorityAnswer, func(ctx context.Context, tg *telegram.Client) error {
		m, err := tg.SendMessage(ctx, telegram.SendMessageParams{
			ChatID: chatID, MessageThreadID: threadID, Text: html, ParseMode: "HTML", ReplyMarkup: markup, Keyboard: kb,
			DisableNotification: silent, // only answers and prompts make a sound
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
		switch {
		case err == nil || ctx.Err() != nil:
		case rex.IsGone(err):
			d.log.Debug("registry: the pane or session is gone", "err", err) // closed meanwhile
		default:
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
