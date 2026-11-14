package daemon

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"rex-tg/internal/format"
	"rex-tg/internal/miniapp"
	"rex-tg/internal/registry"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// The control panel (/rt) of the Mini App. The HTTP side is package
// miniapp; this file is the daemon's side (miniapp.Control). Reads run on the
// HTTP goroutine. Every action runs its first step on the core goroutine,
// where the claim of its act id is made: the claim is read and written
// there, one action at a time, so two requests with the same act id cannot
// both run. The claim is kept in the store as "cp_act:<act>" with the value
// "<unix time>|<json result>". It holds a "still running" result from the
// start, so a request that repeats an act id gets that, or the final result.

const (
	controlActPrefix = "cp_act:"
	controlActMaxAge = 24 * time.Hour
	controlSessTTL   = 20 * time.Second
)

// controlPanel is the daemon's miniapp.Control.
type controlPanel struct {
	d *daemon

	// exec runs an action once it is claimed (core). Tests replace it.
	exec func(miniapp.ControlAction) (miniapp.ControlResult, <-chan miniapp.ControlResult)

	liveMu sync.Mutex
	live   map[string]bool // acts that this process runs now

	sessMu  sync.Mutex
	sessAt  time.Time
	sessLbl map[string]string // Rex session id -> label
}

var _ miniapp.Control = (*controlPanel)(nil)

func newControlPanel(d *daemon) *controlPanel {
	c := &controlPanel{d: d, live: map[string]bool{}}
	c.exec = c.dispatch
	return c
}

func (c *controlPanel) setLive(act string, on bool) {
	c.liveMu.Lock()
	defer c.liveMu.Unlock()
	if on {
		c.live[act] = true
	} else {
		delete(c.live, act)
	}
}

func (c *controlPanel) isLive(act string) bool {
	c.liveMu.Lock()
	defer c.liveMu.Unlock()
	return c.live[act]
}

// onCore runs f on the core goroutine and waits for it (any goroutine except
// the core). It returns false when f did not run in time.
func (c *controlPanel) onCore(ctx context.Context, f func()) bool {
	done := make(chan struct{})
	ev := evLater{f: func() { f(); close(done) }}
	t := time.NewTimer(c.d.timing.ControlCoreWait)
	defer t.Stop()
	select {
	case c.d.events <- ev:
	case <-ctx.Done():
		return false
	case <-c.d.ctx.Done():
		return false
	case <-t.C:
		return false
	}
	select {
	case <-done:
		return true
	case <-ctx.Done():
	case <-c.d.ctx.Done():
	case <-t.C:
	}
	return false
}

// sessionLabels returns the Rex sessions' names, at most controlSessTTL old.
func (c *controlPanel) sessionLabels(ctx context.Context) map[string]string {
	c.sessMu.Lock()
	defer c.sessMu.Unlock()
	if c.sessLbl != nil && c.d.now().Sub(c.sessAt) < controlSessTTL {
		return c.sessLbl
	}
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ss, err := c.d.rx.Sessions(sctx)
	if err != nil {
		return c.sessLbl // the old list, or none
	}
	m := make(map[string]string, len(ss))
	for _, s := range ss {
		m[s.ID] = s.Label
	}
	c.sessLbl, c.sessAt = m, c.d.now()
	return m
}

// State reads what the panel shows.
func (c *controlPanel) State(ctx context.Context) (miniapp.ControlState, error) {
	d := c.d
	views, err := d.reg.Agents(ctx)
	if err != nil {
		return miniapp.ControlState{}, err
	}
	// The core sends a copy; it is read only when it arrived (left out when
	// the core is busy).
	macCh := make(chan map[int64]bool, 1)
	var mac map[int64]bool
	if c.onCore(ctx, func() { macCh <- d.macThreads() }) {
		mac = <-macCh
	}
	labels := c.sessionLabels(ctx)
	now := d.now()
	views = slices.Clone(views)
	slices.SortStableFunc(views, func(a, b registry.AgentView) int {
		return cmp.Compare(stateRank(a.Pane.State), stateRank(b.Pane.State))
	})
	st := miniapp.ControlState{
		Paused:   d.paused.Load(),
		Agents:   []miniapp.ControlAgent{},
		Starters: []miniapp.Starter{},
		Folders:  []miniapp.Folder{},
		Home:     miniapp.Folder{Path: d.home, Shown: "~"},
	}
	for _, v := range views {
		emoji, state := stateEmoji(v.Pane.State)
		var cwd, title string
		if v.Conversation != nil {
			cwd, title = v.Conversation.CWD, v.Conversation.Title
		}
		a := miniapp.ControlAgent{
			Pane:    v.Pane.ID,
			Label:   paneName(v.Pane.Label),
			Agent:   runtimeName(v.Runtime),
			Emoji:   emoji,
			State:   state,
			Folder:  d.tildePath(cwd),
			Title:   format.Truncate(format.Redact(title), 100),
			Session: labels[v.Pane.Session],
			Ready:   v.Conversation != nil,
		}
		if v.Conversation != nil {
			a.Since = agoText(now, v.Pane.StateAt)
		} else {
			a.State = "waiting for session"
		}
		a.Name = format.Redact(topicName(v.Pane.Label, v.Runtime, cwd, title))
		if th := v.Thread; th != nil && th.Status != store.ThreadClosed {
			_, custom, _ := d.st.Get(ctx, tabUserKey(th.ID))
			status := "live"
			if th.Status == store.ThreadOffline {
				status = "offline"
			}
			a.Name = format.Redact(strings.TrimPrefix(th.Name, offlineMark))
			a.Mac = mac[th.ID]
			a.Topic = &miniapp.ControlTopic{
				Thread: th.ID, Name: a.Name, Status: status,
				Link: d.topicLink(th.MessageThreadID), Custom: custom,
			}
		}
		st.Agents = append(st.Agents, a)
	}
	for _, s := range d.newAgents() {
		st.Starters = append(st.Starters, miniapp.Starter{Key: s.key, Name: s.name()})
	}
	for _, dir := range d.newFolders(d.ownerChat, 0) {
		st.Folders = append(st.Folders, miniapp.Folder{Path: dir, Shown: d.tildePath(dir)})
	}
	return st, nil
}

// --- the claim ---

func controlFail(msg string) miniapp.ControlResult { return miniapp.ControlResult{Message: msg} }

// controlRunning is the result of an action that runs now: the page asks
// again with the same act id.
var controlRunning = miniapp.ControlResult{Message: "This action is still running. Look at the list in a moment.", Pending: true}

// controlBusy is the result of a request that the core did not take in time.
// Its event may still run later, so the page must ask again with the same act.
var controlBusy = miniapp.ControlResult{Message: "rex-tg is busy. Wait a moment.", Pending: true}

// controlUnknown is the result of a claim that no action of this process
// holds: the daemon stopped while the action ran, or it may still run.
var controlUnknown = controlFail("This action may still run, or rex-tg stopped while it ran. Check the list before you try again.")

func (c *controlPanel) storeResult(act string, r miniapp.ControlResult) error {
	b, _ := json.Marshal(r)
	return c.d.st.Set(c.d.ctx, controlActPrefix+act, fmt.Sprintf("%d|%s", c.d.now().Unix(), b))
}

// parseControlResult reads a stored claim.
func parseControlResult(v string) miniapp.ControlResult {
	_, js, _ := strings.Cut(v, "|")
	var r miniapp.ControlResult
	if json.Unmarshal([]byte(js), &r) != nil {
		return controlRunning // not final: start decides whether it runs
	}
	return r
}

// Do runs an action at most once per act id.
func (c *controlPanel) Do(ctx context.Context, a miniapp.ControlAction) miniapp.ControlResult {
	var res miniapp.ControlResult
	var wait <-chan miniapp.ControlResult
	if !c.onCore(ctx, func() { res, wait = c.start(a) }) {
		return controlBusy
	}
	if wait == nil {
		return res
	}
	t := time.NewTimer(c.d.timing.ControlRunWait)
	defer t.Stop()
	select {
	case r := <-wait:
		return r
	case <-ctx.Done():
	case <-c.d.ctx.Done():
	case <-t.C:
	}
	return controlRunning
}

// start claims the act and begins the action (core). It returns the result,
// or a channel that gets it when the action works in the background.
func (c *controlPanel) start(a miniapp.ControlAction) (miniapp.ControlResult, <-chan miniapp.ControlResult) {
	d := c.d
	v, ok, err := d.st.Get(d.ctx, controlActPrefix+a.Act)
	if err != nil {
		return controlFail("Could not check this action. Try again."), nil
	}
	if ok {
		r := parseControlResult(v)
		switch {
		case !r.Pending:
		case c.isLive(a.Act):
			r = controlRunning
		default:
			r = controlUnknown // a placeholder that no run of this process holds
		}
		return r, nil
	}
	if err := c.storeResult(a.Act, controlRunning); err != nil {
		return controlFail("Could not start this action. Try again."), nil
	}
	c.setLive(a.Act, true)
	res, wait := c.exec(a)
	if wait == nil {
		_ = c.storeResult(a.Act, res)
		c.setLive(a.Act, false)
	}
	return res, wait
}

// dispatch runs a claimed action (core).
func (c *controlPanel) dispatch(a miniapp.ControlAction) (miniapp.ControlResult, <-chan miniapp.ControlResult) {
	var res miniapp.ControlResult
	var wait <-chan miniapp.ControlResult
	switch a.Op {
	case miniapp.OpOpen:
		wait = c.open(a)
	case miniapp.OpRenameTopic:
		res = c.renameTopic(a)
	case miniapp.OpRenamePane:
		res, wait = c.renamePane(a)
	case miniapp.OpNew:
		res = c.newAgent(a)
	case miniapp.OpUnlink:
		res = c.unlink(a)
	case miniapp.OpPause:
		res = c.pause(a)
	default:
		res = controlFail("Unknown action.")
	}
	return res, wait
}

// background runs f in a worker; its result is stored for the act, and sent
// on the returned channel.
func (c *controlPanel) background(act string, f func() miniapp.ControlResult) <-chan miniapp.ControlResult {
	ch := make(chan miniapp.ControlResult, 1)
	c.d.spawn(func() {
		r := f()
		_ = c.storeResult(act, r)
		c.setLive(act, false)
		ch <- r
	})
	return ch
}

// --- the actions ---

// open opens the topic of a pane (core), like the button of /agents.
func (c *controlPanel) open(a miniapp.ControlAction) <-chan miniapp.ControlResult {
	d := c.d
	type answer struct {
		text  string
		alert bool
	}
	ans := make(chan answer, 1)
	d.openTopic(a.Pane, 0, func(text string, alert bool) {
		select {
		case ans <- answer{text, alert}:
		default:
		}
	})
	return c.background(a.Act, func() miniapp.ControlResult {
		t := time.NewTimer(d.timing.ControlOpenWait)
		defer t.Stop()
		var x answer
		select {
		case x = <-ans:
		case <-t.C:
			return controlFail("The topic is not ready yet. Look at the list in a moment.")
		case <-d.ctx.Done():
			return controlFail("rex-tg is stopping.")
		}
		switch {
		case x.alert:
			return controlFail(x.text)
		case x.text == "Opening…":
			return controlFail("This pane is opening already. Look at the list in a moment.")
		}
		// The core links a new topic before this barrier event.
		bar := make(chan struct{})
		d.emit(evLater{f: func() { close(bar) }})
		select {
		case <-bar:
		case <-d.ctx.Done():
			return controlFail("rex-tg is stopping.")
		}
		link := ""
		if views, err := d.reg.Agents(d.ctx); err == nil {
			for _, v := range views {
				if v.Pane.ID == a.Pane && v.Thread != nil && v.Thread.Status != store.ThreadClosed {
					link = d.topicLink(v.Thread.MessageThreadID)
				}
			}
		}
		msg := "The topic is open."
		if x.text == "Topic created" {
			msg = "The topic is made."
		}
		if link == "" {
			msg = "Asked Telegram for the topic. If it does not show, see the note in the main chat."
		}
		return miniapp.ControlResult{OK: true, Message: msg, Link: link}
	})
}

var htmlTag = regexp.MustCompile(`<[^>]*>`)

// plainText turns the HTML of a reply into plain text.
func plainText(s string) string {
	return html.UnescapeString(htmlTag.ReplaceAllString(s, ""))
}

// openThread returns a topic that exists and is not closed.
func (c *controlPanel) openThread(id int64) (store.Thread, bool) {
	th, err := c.d.st.Thread(c.d.ctx, id)
	return th, err == nil && th.Status != store.ThreadClosed
}

// renameTopic is /rename in the topic (core).
func (c *controlPanel) renameTopic(a miniapp.ControlAction) miniapp.ControlResult {
	th, ok := c.openThread(a.Thread)
	if !ok {
		return controlFail("This topic is gone or unlinked.")
	}
	var out string
	c.d.renameCommand(th, a.Name, func(h string) { out = h })
	res := miniapp.ControlResult{OK: strings.HasPrefix(out, "✏️"), Message: plainText(out)}
	if res.OK {
		res.Link = c.d.topicLink(th.MessageThreadID)
	}
	return res
}

// renamePane renames a pane in Rex (core, then a worker).
func (c *controlPanel) renamePane(a miniapp.ControlAction) (miniapp.ControlResult, <-chan miniapp.ControlResult) {
	d := c.d
	p, err := d.st.Pane(d.ctx, a.Pane)
	if err != nil || !p.GoneAt.IsZero() {
		return controlFail("That pane is gone."), nil
	}
	name := cleanLabel(strings.Join(strings.Fields(format.Redact(a.Name)), " "))
	if tidyName(name, "") == "" {
		return controlFail("A name needs a letter or a digit."), nil
	}
	return miniapp.ControlResult{}, c.background(a.Act, func() miniapp.ControlResult {
		ctx, cancel := context.WithTimeout(d.ctx, 20*time.Second)
		defer cancel()
		if err := d.rx.RenameBlock(ctx, p.Session, p.Block, name); err != nil {
			d.log.Warn("control panel: rename pane", "pane", p.ID, "err", err)
			return controlFail("Rex could not rename the pane.")
		}
		if err := d.scanAndWait(ctx); err != nil && d.ctx.Err() == nil {
			d.log.Warn("control panel: scan", "err", err)
		}
		msg := "The pane is named " + format.Truncate(name, 60) + "."
		if views, err := d.reg.Agents(d.ctx); err == nil {
			for _, v := range views {
				if v.Pane.ID != p.ID || v.Thread == nil || v.Thread.Status == store.ThreadClosed {
					continue
				}
				if _, custom, _ := d.st.Get(d.ctx, tabUserKey(v.Thread.ID)); custom {
					msg += " Its topic keeps the name you gave it. Use auto on the topic to follow the pane."
				} else {
					msg += " Its topic follows in a moment."
					d.emit(evRename{thread: v.Thread.ID})
				}
			}
		}
		return miniapp.ControlResult{OK: true, Message: msg}
	})
}

// newAgent starts an agent, like /new <agent> <folder> (core).
func (c *controlPanel) newAgent(a miniapp.ControlAction) miniapp.ControlResult {
	d := c.d
	if d.paused.Load() {
		return controlFail(plainText(newPausedText))
	}
	ag, ok := findAgent(d.newAgents(), a.Agent)
	if !ok {
		return controlFail("There is no such agent.")
	}
	if strings.ContainsAny(a.Path, "\r\n") || !filepath.IsAbs(a.Path) {
		return controlFail("That is not a folder.")
	}
	dir := filepath.Clean(a.Path)
	if !isDir(dir) {
		return controlFail("There is no such folder.")
	}
	if !c.folderAllowed(dir) {
		return controlFail("That folder is outside your home folder and not one of the folders rex-tg offers.")
	}
	d.newSeq++
	f := &newFlow{seq: d.newSeq, chat: d.ownerChat, step: newStarting, agent: ag, offered: d.newAgents(), expires: d.now().Add(newExpiry)}
	token := newToken()
	d.newFlows[token] = f
	d.spawn(func() { d.launch(token, 0, d.ownerChat, 0, ag, dir) })
	return miniapp.ControlResult{OK: true, Message: "Starting " + ag.name() + " in " + d.tildePath(dir) + ". The progress is in the main chat."}
}

// unlink is /unlink in the topic (core).
func (c *controlPanel) unlink(a miniapp.ControlAction) miniapp.ControlResult {
	th, ok := c.openThread(a.Thread)
	if !ok {
		return controlFail("This topic is already unlinked.")
	}
	c.d.unlink(th)
	return miniapp.ControlResult{OK: true, Message: "Unlinked. The topic stays in Telegram as history."}
}

// pause is /pause and /unpause (core).
func (c *controlPanel) pause(a miniapp.ControlAction) miniapp.ControlResult {
	d := c.d
	d.setPaused(a.On)
	if a.On {
		d.say(d.ownerChat, 0, "⏸ Typing is paused. Nothing is typed into panes until /unpause.", nil)
		return miniapp.ControlResult{OK: true, Message: "Typing is paused."}
	}
	d.say(d.ownerChat, 0, "▶️ Typing is on again.", nil)
	return miniapp.ControlResult{OK: true, Message: "Typing is on again."}
}

// pruneActRecords drops the claims of acts older than a day (housekeeping, a
// worker).
func (d *daemon) pruneActRecords(now time.Time) {
	keys, err := d.st.KeysWithPrefix(d.ctx, controlActPrefix)
	if err != nil {
		return
	}
	for _, k := range keys {
		v, ok, err := d.st.Get(d.ctx, k)
		if err != nil || !ok {
			continue
		}
		at, _, _ := strings.Cut(v, "|")
		n, perr := strconv.ParseInt(at, 10, 64)
		if perr != nil || now.Sub(time.Unix(n, 0)) > controlActMaxAge {
			_ = d.st.Delete(d.ctx, k)
		}
	}
}

// panelCommand answers /rt: a button that opens the control panel.
func (d *daemon) panelCommand(chat, thread int64) {
	if !d.miniAppReady() {
		d.say(chat, thread, "🎛 The control panel is part of the Mini App, which is off. Set <code>[miniapp] url</code> to turn it on.", nil)
		return
	}
	d.spawn(func() {
		id, err := d.livePage(miniapp.KindControl, 0, "main", "rex-tg", "")
		if err != nil {
			d.log.Warn("mini app: control page", "err", err)
			d.sendNow(chat, thread, "Could not open the control panel.", nil)
			return
		}
		markup := &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{
			{Text: "🎛 Open control panel", WebApp: &telegram.WebAppInfo{URL: miniAppURL(d.cfg.MiniApp.URL, id)}},
		}}}
		_, _ = d.sendNow(chat, thread, "🎛 <b>rex-tg</b> control panel", markup)
	})
}

// folderAllowed tells whether /new may start an agent in dir: it is inside
// the real home folder once links are followed (the rule of the folder
// explorer), or it is one of the folders that /new offers (core).
func (c *controlPanel) folderAllowed(dir string) bool {
	d := c.d
	if home, err := filepath.EvalSymlinks(d.home); err == nil {
		if real, err := filepath.EvalSymlinks(dir); err == nil && insideDir(home, real) {
			return true
		}
	}
	return slices.Contains(d.newFolders(d.ownerChat, 0), dir)
}

// insideDir tells whether p is root or below it.
func insideDir(root, p string) bool {
	root = filepath.Clean(root)
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}
