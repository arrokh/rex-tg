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
	"rex-tg/internal/inbound"
	"rex-tg/internal/miniapp"
	"rex-tg/internal/registry"
	"rex-tg/internal/rex"
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
	tabOf   map[string]string // "<session>/<block>" -> the name of its tab
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

// rexNames returns the Rex sessions' names, and the tab (window) name of each
// pane by "<session>/<block>", at most controlSessTTL old: one list of
// sessions, panes, and tabs per refresh, however many agents show.
func (c *controlPanel) rexNames(ctx context.Context) (labels, tabs map[string]string) {
	c.sessMu.Lock()
	defer c.sessMu.Unlock()
	if c.sessLbl != nil && c.d.now().Sub(c.sessAt) < controlSessTTL {
		return c.sessLbl, c.tabOf
	}
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ss, err := c.d.rx.Sessions(sctx)
	if err != nil {
		return c.sessLbl, c.tabOf // the old lists, or none
	}
	labels = make(map[string]string, len(ss))
	tabs = map[string]string{}
	for _, s := range ss {
		labels[s.ID] = s.Label
		blocks, err1 := c.d.rx.Blocks(sctx, s.ID)
		windows, err2 := c.d.rx.Windows(sctx, s.ID)
		if err1 != nil || err2 != nil {
			continue // its panes show without a tab
		}
		names := map[string]string{}
		for _, w := range windows {
			names[w.ID] = w.Label
		}
		for _, b := range blocks {
			if l, ok := names[b.WindowID]; ok {
				tabs[s.ID+"/"+b.ID] = l
			}
		}
	}
	c.sessLbl, c.tabOf, c.sessAt = labels, tabs, c.d.now()
	return labels, tabs
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
	labels, tabs := c.rexNames(ctx)
	now := d.now()
	views = slices.Clone(views)
	slices.SortStableFunc(views, func(a, b registry.AgentView) int {
		return cmp.Compare(stateRank(a.Pane.State), stateRank(b.Pane.State))
	})
	st := miniapp.ControlState{
		Paused:   d.paused.Load(),
		Files:    d.miniAppReady() && d.cfg.MiniApp.Files,
		Changes:  d.miniAppReady() && d.cfg.MiniApp.Changes,
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
			Tab:     tabs[v.Pane.Session+"/"+v.Pane.Block],
			Block:   v.Pane.Block,
			Ready:   v.Conversation != nil,
		}
		if v.Conversation != nil {
			a.Since = agoText(now, v.Pane.StateAt)
		} else {
			a.State = "waiting for session"
		}
		a.Name = format.Redact(topicName(v.Pane.Label, v.Runtime, cwd, title))
		if th := v.Thread; th != nil && th.Status != store.ThreadClosed && !d.topicTerminating(th.ID) {
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
	for _, f := range d.folderChoices(d.ownerChat, 0, maxPanelFolders) {
		st.Folders = append(st.Folders, miniapp.Folder{Path: f.path, Shown: d.tildePath(f.path), Pinned: f.pinned})
	}
	st.Topics = c.topics(ctx, mac, labels, tabs)
	return st, nil
}

// topics lists every topic that is not closed, the newest activity first,
// each with the Rex session, tab, and pane of its agent when it has a pane.
func (c *controlPanel) topics(ctx context.Context, mac map[int64]bool, labels, tabs map[string]string) []miniapp.TopicRow {
	d := c.d
	rows := []miniapp.TopicRow{}
	ths, err := d.st.Threads(ctx, store.ThreadLive, store.ThreadOffline)
	if err != nil {
		return rows
	}
	now := d.now()
	type row struct {
		r  miniapp.TopicRow
		at time.Time
	}
	var list []row
	for _, th := range ths {
		if d.topicTerminating(th.ID) {
			continue
		}
		_, custom, _ := d.st.Get(ctx, tabUserKey(th.ID))
		status := "live"
		if th.Status == store.ThreadOffline {
			status = "offline"
		}
		r := miniapp.TopicRow{
			Thread: th.ID, Name: format.Redact(strings.TrimPrefix(th.Name, offlineMark)), Status: status,
			Link: d.topicLink(th.MessageThreadID), Custom: custom,
		}
		at := th.CreatedAt
		if conv, err := d.st.Conversation(ctx, th.CurrentConversationID); err == nil {
			r.Agent, r.Folder = runtimeName(conv.Runtime), d.tildePath(conv.CWD)
			if cur, ok, err := d.st.Cursor(ctx, conv.Transcript); err == nil && ok && cur.LastEventAt.After(at) {
				at = cur.LastEventAt
			}
			if p, ok, err := d.st.PaneByConversation(ctx, conv.ID); err == nil && ok && p.GoneAt.IsZero() {
				r.Pane, r.Session, r.Tab = p.ID, labels[p.Session], tabs[p.Session+"/"+p.Block]
				r.PaneLabel = paneName(p.Label)
			}
		}
		r.Since = agoText(now, at)
		list = append(list, row{r, at})
	}
	slices.SortStableFunc(list, func(a, b row) int { return b.at.Compare(a.at) })
	for _, x := range list {
		rows = append(rows, x.r)
	}
	return rows
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

// Page-bound sends keep an unknown result pending, so Annotate and Templates
// retry the same act instead of creating a second queued message.
var controlUnknownPageSend = miniapp.ControlResult{Message: controlUnknown.Message, Pending: true}

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
			if a.Op == miniapp.OpNotes || a.Op == miniapp.OpTemplateMessage {
				r = controlUnknownPageSend // never queue a second page-bound message on uncertainty
			} else {
				r = controlUnknown // a placeholder that no run of this process holds
			}
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
	case miniapp.OpRename:
		res, wait = c.rename(a)
	case miniapp.OpPage:
		res, wait = c.page(a)
	case miniapp.OpNew:
		res, wait = c.newAgent(a)
	case miniapp.OpForgetFolder, miniapp.OpForgetFolders:
		res, wait = c.forgetFolders(a)
	case miniapp.OpCleanTopics:
		res = c.cleanTopics()
	case miniapp.OpUnlink:
		res = c.unlink(a)
	case miniapp.OpNotes:
		res = c.notes(a)
	case miniapp.OpTemplateMessage:
		res = c.templateMessage(a)
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

// rename is /rename for a pane: its topic, the pane, and its tab (core, then
// a worker).
func (c *controlPanel) rename(a miniapp.ControlAction) (miniapp.ControlResult, <-chan miniapp.ControlResult) {
	d := c.d
	pl, refusal := d.planRename(nil, a.Pane, a.Name)
	if refusal != "" {
		return controlFail(plainText(refusal)), nil
	}
	return miniapp.ControlResult{}, c.background(a.Act, func() miniapp.ControlResult {
		rep := d.runRename(pl)
		res := miniapp.ControlResult{OK: rep.topic || rep.topicSame || rep.pane || rep.tab, Message: plainText(rep.text(pl.name, pl.auto))}
		if pl.th != nil && (rep.topic || rep.topicSame) {
			res.Link = d.topicLink(pl.th.MessageThreadID)
		}
		return res
	})
}

// page returns a Mini App page of the agent's topic (core, then a worker for
// the pages that read git).
func (c *controlPanel) page(a miniapp.ControlAction) (miniapp.ControlResult, <-chan miniapp.ControlResult) {
	d := c.d
	if !d.miniAppReady() {
		return controlFail("The Mini App is not ready."), nil
	}
	p, err := d.st.Pane(d.ctx, a.Pane)
	if err != nil || !p.GoneAt.IsZero() {
		return controlFail("That pane is gone."), nil
	}
	th, ok := d.paneThread(p)
	if !ok || th.Status == store.ThreadClosed {
		return controlFail("This agent has no topic. Open one first."), nil
	}
	if a.Kind == miniapp.PageScreen {
		id, err := d.screenPage(th)
		if err != nil {
			return controlFail("Could not open the live screen."), nil
		}
		return miniapp.ControlResult{OK: true, Message: "Opening the live screen.", Page: id}, nil
	}
	if a.Kind == miniapp.PageFiles && !d.cfg.MiniApp.Files {
		return controlFail("The file browser is off ([miniapp] files)."), nil
	}
	if a.Kind == miniapp.PageChanges && !d.cfg.MiniApp.Changes {
		return controlFail("The changes view is off ([miniapp] changes)."), nil
	}
	conv, err := d.st.Conversation(d.ctx, th.CurrentConversationID)
	if err != nil || conv.CWD == "" {
		return controlFail("This agent has no folder yet."), nil
	}
	cwd := conv.CWD
	return miniapp.ControlResult{}, c.background(a.Act, func() miniapp.ControlResult {
		ctx, cancel := context.WithTimeout(d.ctx, time.Minute)
		defer cancel()
		var id string
		var err error
		if a.Kind == miniapp.PageFiles {
			id, err = d.filesPage(ctx, th, cwd)
		} else {
			var top string
			var ok bool
			if top, ok = d.repoTop(ctx, cwd); !ok {
				return controlFail(d.tildePath(cwd) + " is not in a git repository, so there are no changes to show.")
			}
			id, err = d.changesPage(th, top)
		}
		if err != nil {
			return controlFail("Could not open the page.")
		}
		return miniapp.ControlResult{OK: true, Message: "Opening the page.", Page: id}
	})
}

// newAgent starts an agent, like /new <agent> <folder> (core). The result
// comes once the tab exists, with the pane's Rex block; the wait for the
// agent, the progress in the main chat, and the topic go on as for /new.
func (c *controlPanel) newAgent(a miniapp.ControlAction) (miniapp.ControlResult, <-chan miniapp.ControlResult) {
	d := c.d
	if d.paused.Load() {
		return controlFail(plainText(newPausedText)), nil
	}
	ag, ok := findAgent(d.newAgents(), a.Agent)
	if !ok {
		return controlFail("There is no such agent."), nil
	}
	if strings.ContainsAny(a.Path, "\r\n") || !filepath.IsAbs(a.Path) {
		return controlFail("That is not a folder."), nil
	}
	dir := filepath.Clean(a.Path)
	if !isDir(dir) {
		return controlFail("There is no such folder."), nil
	}
	if !c.folderAllowed(dir) {
		return controlFail("That folder is outside your home folder and not one of the folders rex-tg offers."), nil
	}
	d.newSeq++
	f := &newFlow{seq: d.newSeq, chat: d.ownerChat, step: newStarting, agent: ag, offered: d.newAgents(), expires: d.now().Add(newExpiry)}
	token := newToken()
	d.newFlows[token] = f
	type start struct {
		st   rex.Started
		fail string
	}
	got := make(chan start, 1) // launchWith calls back once
	d.spawn(func() {
		d.launchWith(token, 0, d.ownerChat, 0, ag, dir, func(st rex.Started, fail string) { got <- start{st, fail} })
	})
	return miniapp.ControlResult{}, c.background(a.Act, func() miniapp.ControlResult {
		// launchWith calls back once on every path, after Rex answers or
		// times out. Until then the request is pending: a retry with the same
		// act gets this result, with the new pane, when it comes.
		select {
		case s := <-got:
			if s.fail != "" {
				return controlFail(s.fail)
			}
			return miniapp.ControlResult{OK: true, Block: s.st.BlockID,
				Message: "Starting " + ag.name() + " in " + d.tildePath(dir) + ". The progress is in the main chat."}
		case <-d.ctx.Done():
			return controlFail("rex-tg stopped while the agent started. Check the list before you try again.")
		case <-d.ctx.Done():
			return controlFail("rex-tg is stopping.")
		}
	})
}

// forgetFolders hides one recent folder, or all of them, from the lists of
// the panel and /new (worker). The folder returns when a session in it is
// used again. A pinned folder (rex.new_folders) is never hidden.
func (c *controlPanel) forgetFolders(a miniapp.ControlAction) (miniapp.ControlResult, <-chan miniapp.ControlResult) {
	d := c.d
	return miniapp.ControlResult{}, c.background(a.Act, func() miniapp.ControlResult {
		choices := d.folderChoices(d.ownerChat, 0, maxPanelFolders)
		hide := func(dir string) bool {
			return d.st.Set(d.ctx, folderHiddenKey(dir), strconv.FormatInt(d.now().UnixMilli(), 10)) == nil
		}
		if a.Op == miniapp.OpForgetFolder {
			dir := filepath.Clean(a.Path)
			i := slices.IndexFunc(choices, func(f folderChoice) bool { return f.path == dir })
			switch {
			case i < 0:
				return controlFail("That folder is not in the list.")
			case choices[i].pinned:
				return controlFail("That folder is pinned in [rex] new_folders. Remove it from the settings to drop it.")
			case !hide(dir):
				return controlFail("Could not forget the folder now. Try again.")
			}
			return miniapp.ControlResult{OK: true, Message: "Forgot " + d.tildePath(dir) + ". It comes back when an agent runs there again."}
		}
		// Every recent folder, not only the ones the list shows: the next
		// ones would take their places.
		pinned := map[string]bool{}
		for _, f := range choices {
			if f.pinned {
				pinned[f.path] = true
			}
		}
		hidden := d.hiddenFolders()
		recent, err := d.st.RecentFolderUses(d.ctx, maxRecentFolders)
		if err != nil {
			return controlFail("Could not read the recent folders now. Try again.")
		}
		n := 0
		for _, u := range recent {
			if at, ok := hidden[u.Path]; ok && folderHidden(at, u.Started) {
				continue // forgotten already
			}
			if !pinned[u.Path] && hide(u.Path) {
				n++
			}
		}
		if n == 0 {
			return controlFail("No recent folders to forget.")
		}
		return miniapp.ControlResult{OK: true, Message: fmt.Sprintf("Forgot %d recent folder(s). Pinned folders stay.", n)}
	})
}

// cleanTopics unlinks every topic whose agent stopped, as /unlink does (core).
func (c *controlPanel) cleanTopics() miniapp.ControlResult {
	d := c.d
	ths, err := d.st.Threads(d.ctx, store.ThreadOffline)
	if err != nil {
		return controlFail("Could not list the topics now. Try again.")
	}
	if len(ths) == 0 {
		return controlFail("No stopped topics.")
	}
	for _, th := range ths {
		d.unlink(th)
	}
	if len(ths) == 1 {
		return miniapp.ControlResult{OK: true, Message: "Unlinked 1 stopped topic."}
	}
	return miniapp.ControlResult{OK: true, Message: fmt.Sprintf("Unlinked %d stopped topics.", len(ths))}
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
	return slices.ContainsFunc(d.folderChoices(d.ownerChat, 0, maxPanelFolders), func(f folderChoice) bool { return f.path == dir })
}

// insideDir tells whether p is root or below it.
func insideDir(root, p string) bool {
	root = filepath.Clean(root)
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}

// maxRecentFolders is how many recent folders "Clean up" forgets and
// housekeeping keeps the records of.
const maxRecentFolders = 100

// pruneFolderHides drops the forgotten-folder records of folders that are gone
// from the disk or no longer among the 100 most recent folders (housekeeping,
// a worker). A record is otherwise kept, so a folder that stays in the lists
// does not come back by itself: only a newer session brings it back.
func (d *daemon) pruneFolderHides() {
	keys, err := d.st.KeysWithPrefix(d.ctx, "folder_hidden:")
	if err != nil || len(keys) == 0 {
		return
	}
	uses, err := d.st.RecentFolderUses(d.ctx, maxRecentFolders)
	if err != nil {
		return
	}
	recent := map[string]bool{}
	for _, u := range uses {
		recent[u.Path] = true
	}
	for _, k := range keys {
		dir := strings.TrimPrefix(k, "folder_hidden:")
		if !recent[dir] || !isDir(dir) {
			_ = d.st.Delete(d.ctx, k)
		}
	}
}

// notes sends the notes of an Annotate or answer page to the topic's agent,
// like a message typed in the topic (core): every check of the typing
// applies, and the text waits until the agent is idle. The act's claim runs
// it once; its queued row comes from the act too, so it is queued once.
func (c *controlPanel) notes(a miniapp.ControlAction) miniapp.ControlResult {
	d := c.d
	th, err := d.st.Thread(d.ctx, a.Thread)
	if err != nil || th.Status == store.ThreadClosed {
		return controlFail("This topic is closed. Use Copy, and paste the notes where the agent is.")
	}
	if a.Conv <= 0 || th.CurrentConversationID != a.Conv {
		return controlFail("The agent in this topic is in another session now, so the notes were not sent. Use Copy.")
	}
	if !d.queueText(th.ID, annotRowID(a.Act), 0, a.Text, "text", inbound.ActText) {
		return controlFail("Could not send the notes. Try again.")
	}
	d.annotSent(th.ID, a.Act, a.Text)
	return miniapp.ControlResult{OK: true, Message: "Sent to the agent. It gets them when it is idle."}
}

// templateMessage queues a saved template as normal user text (core): the
// usual pane, prompt, and pause checks apply before anything is typed.
func (c *controlPanel) templateMessage(a miniapp.ControlAction) miniapp.ControlResult {
	d := c.d
	th, err := d.st.Thread(d.ctx, a.Thread)
	if err != nil || th.Status == store.ThreadClosed {
		return controlFail("This topic is closed. Open Templates from the topic where you want to send this message.")
	}
	if a.Conv <= 0 || th.CurrentConversationID != a.Conv {
		return controlFail("The agent in this topic is in another session now. The template was not sent.")
	}
	if !d.queueText(th.ID, templateRowID(a.Act), 0, a.Text, "text", inbound.ActText) {
		return controlFail("Could not queue the template message. Try again.")
	}
	d.templateSent(th.ID, a.Act, a.Text)
	return miniapp.ControlResult{OK: true, Message: "Template sent to the agent. It gets the message when it is ready."}
}
