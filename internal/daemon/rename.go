package daemon

import (
	"context"
	"slices"
	"strings"
	"time"

	"rex-tg/internal/format"
	"rex-tg/internal/rex"
	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// One rename for a topic, its Rex pane, and its tab. /rename in a topic and
// the control panel's rename both use it. The core part (planRename) checks
// the name, finds the pane and the topic, and keeps the name that the topic
// and its tab follow (kv tab_user:<thread>). The Rex part (runRename) runs
// in a worker: it renames the pane, and the tab when it is a tab of the
// user's own sessions. The tab in the session of /new is left to syncTab,
// which follows the kept name: a direct rename there would look to syncTab
// like a rename from outside.

// autoPaneName is the name that Rex gives a new pane. paneName reads it as
// "no name", so the topic falls back to the session's name.
const autoPaneName = "shell"

// renamePlan is what the core decided.
type renamePlan struct {
	name string // the new name; "" for auto
	auto bool
	th   *store.Thread       // the topic, when the agent has one that is open
	conv *store.Conversation // the agent's session, when known
	pane *store.Pane         // the agent's pane, when it has one now
}

// offline tells that the topic's agent stopped: the topic keeps its name
// until the agent runs again (rename).
func (pl renamePlan) offline() bool { return pl.th != nil && pl.th.Status == store.ThreadOffline }

// renameUsage is the reply of /rename without a name.
const renameUsage = "Usage: /rename &lt;name&gt; names this topic, its Rex pane, and its tab. /rename auto names them after the agent's session again."

// planRename is the core's part of a rename. th is the topic of /rename, or
// nil: then paneID is the pane that the panel renames. On a refusal it
// returns the reason as HTML.
func (d *daemon) planRename(th *store.Thread, paneID int64, args string) (renamePlan, string) {
	var pl renamePlan
	name := cleanLabel(strings.Join(strings.Fields(format.Redact(args)), " "))
	switch {
	case name == "":
		return pl, renameUsage
	case strings.EqualFold(name, "auto"):
		pl.auto = true
	case tidyName(name, "") == "":
		return pl, "A name needs a letter or a digit."
	default:
		pl.name = name
	}
	var p store.Pane
	var havePane bool
	if th != nil {
		pl.th = th
		if q, ok, err := d.st.PaneByConversation(d.ctx, th.CurrentConversationID); err == nil && ok {
			p, havePane = q, true
		}
		if conv, err := d.st.Conversation(d.ctx, th.CurrentConversationID); err == nil {
			pl.conv = &conv
		}
	} else {
		q, err := d.st.Pane(d.ctx, paneID)
		if err != nil || !q.GoneAt.IsZero() {
			return pl, "That pane is gone."
		}
		p, havePane = q, true
		if q.ConversationID != 0 {
			if conv, err := d.st.Conversation(d.ctx, q.ConversationID); err == nil {
				pl.conv = &conv
			}
			if t, ok := d.paneThread(q); ok {
				pl.th = &t
			}
		}
	}
	if havePane && p.GoneAt.IsZero() && p.Session != "" && p.Block != "" {
		pl.pane = &p
	}
	if pl.th != nil {
		var err error
		if pl.auto {
			err = d.st.Delete(d.ctx, tabUserKey(pl.th.ID))
		} else {
			err = d.st.Set(d.ctx, tabUserKey(pl.th.ID), pl.name)
		}
		if err != nil {
			return pl, "Could not change the name now. Try again."
		}
	}
	return pl, ""
}

// paneThread returns the open topic of a pane's session.
func (d *daemon) paneThread(p store.Pane) (store.Thread, bool) {
	if p.ConversationID == 0 {
		return store.Thread{}, false
	}
	th, ok, err := d.st.ThreadByConversation(d.ctx, p.ConversationID)
	return th, ok && err == nil
}

// renameReport is what a rename did. skips say what it did not, and why.
type renameReport struct {
	topic, pane, tab bool
	topicSame        bool // Telegram says the topic had the name already
	skips            []string
}

// runRename is the Rex and Telegram part of a rename (worker). The topic is
// edited here and the answer of Telegram is reported: for a name before the
// pane is renamed, for auto after it and a scan (so the name is right).
func (d *daemon) runRename(pl renamePlan) renameReport {
	ctx, cancel := context.WithTimeout(d.ctx, 30*time.Second)
	defer cancel()
	var rep renameReport
	topic := func() {
		if pl.th != nil {
			d.renameTopicNow(pl, &rep)
		}
	}
	if !pl.auto {
		topic()
	}
	if pl.pane == nil {
		if pl.auto {
			topic()
		}
		rep.skips = append(rep.skips, "its Rex pane and its tab (the agent has no pane now)")
		return rep
	}
	p := *pl.pane
	label := pl.name
	if pl.auto {
		label = autoPaneName
	}
	if err := d.rx.RenameBlock(ctx, p.Session, p.Block, label); err != nil {
		d.log.Warn("rename pane", "pane", p.ID, "err", err)
		rep.skips = append(rep.skips, "its Rex pane (Rex could not rename it)")
		if pl.auto {
			// The pane keeps its name, so the topic and the tab keep it too.
			what := "its tab"
			if pl.th != nil && !pl.offline() {
				what = "the topic and its tab"
			}
			rep.skips = append(rep.skips, what+" (the pane still has its name)")
			if pl.offline() {
				topic() // says that an offline topic waits
			}
			return rep
		}
	} else {
		rep.pane = true
		// The registry sees the new label, so the panel shows it.
		if err := d.scanAndWait(ctx); err != nil && d.ctx.Err() == nil {
			d.log.Warn("rename: scan", "err", err)
		}
	}
	if pl.auto {
		topic()
	}
	state, why := d.renameTab(ctx, pl, p)
	switch state {
	case tabRenamed, tabFollows:
		rep.tab = true
	default:
		rep.skips = append(rep.skips, "its tab ("+why+")")
	}
	return rep
}

// renameTopicNow edits the topic's name in Telegram and says in rep how it
// went (worker). It calls Telegram even when the stored name equals the new
// one: an explicit rename must reach Telegram, which may differ from what
// rex-tg stored. An offline topic keeps its name until the agent runs again.
func (d *daemon) renameTopicNow(pl renamePlan, rep *renameReport) {
	th, err := d.st.Thread(d.ctx, pl.th.ID)
	if err != nil || th.Status == store.ThreadClosed {
		rep.skips = append(rep.skips, "the topic (it is unlinked)")
		return
	}
	conv := pl.conv
	if conv == nil {
		c, err := d.st.Conversation(d.ctx, th.CurrentConversationID)
		if err != nil {
			rep.skips = append(rep.skips, "the topic (its session is not known)")
			return
		}
		conv = &c
	}
	if th.Status == store.ThreadOffline {
		rep.skips = append(rep.skips, "the topic (it takes the name when the agent runs again)")
		d.syncTab(th, *conv)
		return
	}
	name := d.desiredName(th, *conv)
	err = d.editTopic(th, name)
	c := telegram.Classify(err)
	d.log.Info("topic renamed by command", "thread", th.ID, "telegram", c.String())
	switch c {
	case telegram.ClassNone:
		rep.topic = true
	case telegram.ClassNotModified:
		rep.topicSame = true
	case telegram.ClassThreadNotFound:
		d.emit(evSender{sender.Event{Kind: sender.ThreadGone, ThreadID: th.ID}}) // the user deleted it
		rep.skips = append(rep.skips, "the topic (it is gone from Telegram)")
		return
	default:
		if d.ctx.Err() == nil {
			d.log.Warn("editForumTopic", "err", err)
		}
		why := "Telegram refused it"
		switch c {
		case telegram.ClassUnknownOutcome:
			why = "Telegram did not answer in time"
		case telegram.ClassNotSent:
			why = "Telegram could not be reached"
		}
		// The new name is kept (tab_user), so the 30-second name check
		// tries the topic again.
		rep.skips = append(rep.skips, "the topic ("+why+"; rex-tg tries again within a minute)")
		return
	}
	if err := d.st.SetThreadName(d.ctx, th.ID, name); err != nil {
		d.log.Warn("rename topic", "err", err)
	}
}

// editTopic sets a topic's name in Telegram.
func (d *daemon) editTopic(th store.Thread, name string) error {
	if d.editTopicFn != nil {
		return d.editTopicFn(th, name)
	}
	return d.snd.Call(sender.PriorityEdit, func(ctx context.Context, tg *telegram.Client) error {
		return tg.EditForumTopic(ctx, th.ChatID, th.MessageThreadID, name)
	})
}

type tabResult int

const (
	tabSkipped tabResult = iota
	tabRenamed           // renamed here
	tabFollows           // in the session of /new: syncTab names it
)

// renameTab renames the tab of the pane (worker). Only the pane that owns
// the tab names it: a tab that holds another agent's pane keeps its name. In
// the session of /new, a tab with a topic is then left to syncTab. Any other
// tab is renamed here, with the name that syncTab would give it.
func (d *daemon) renameTab(ctx context.Context, pl renamePlan, p store.Pane) (tabResult, string) {
	d.tabMu.Lock()
	isOwn, err := d.inOwnSession(p.Session)
	d.tabMu.Unlock()
	if err != nil {
		return tabSkipped, "Rex did not list its sessions"
	}
	blocks, err := d.rx.Blocks(ctx, p.Session)
	if err != nil {
		return tabSkipped, "Rex did not list the panes"
	}
	j := slices.IndexFunc(blocks, func(b rex.Block) bool { return b.ID == p.Block })
	if j < 0 || blocks[j].WindowID == "" {
		return tabSkipped, "the pane has no tab"
	}
	window := blocks[j].WindowID
	hasAgent := func(block string) bool {
		q, ok, err := d.st.PaneByBlock(d.ctx, p.Host, block)
		return err == nil && ok && q.ConversationID != 0
	}
	if tabOwner(blocks, window, p.Block, hasAgent) != p.Block {
		return tabSkipped, "it also holds another agent"
	}
	if isOwn && pl.th != nil {
		// syncTab gives the tab the name that was kept for the topic.
		if th, err := d.st.Thread(d.ctx, pl.th.ID); err == nil && th.Status != store.ThreadClosed {
			conv := pl.conv
			if conv == nil {
				c, err := d.st.Conversation(d.ctx, th.CurrentConversationID)
				if err != nil {
					return tabSkipped, "its session is not known"
				}
				conv = &c
			}
			d.syncTab(th, *conv)
		}
		return tabFollows, ""
	}
	windows, err := d.rx.Windows(ctx, p.Session)
	if err != nil {
		return tabSkipped, "Rex did not list the tabs"
	}
	k := slices.IndexFunc(windows, func(w rex.Window) bool { return w.ID == window })
	if k < 0 {
		return tabSkipped, "the pane has no tab"
	}
	var others []string
	for _, w := range windows {
		if w.ID != window {
			others = append(others, w.Label)
		}
	}
	label := pl.name
	if pl.auto {
		if pl.conv == nil {
			return tabSkipped, "the agent has no session name yet"
		}
		paneLabel := autoPaneName
		if q, err := d.st.Pane(d.ctx, p.ID); err == nil {
			paneLabel = q.Label
		}
		label = tabName(paneLabel, pl.conv.Runtime, pl.conv.CWD, d.convTitle(*pl.conv), windows[k].Label)
	}
	label = uniqueTabName(label, others)
	if label == "" {
		return tabSkipped, "no name to give it"
	}
	if label == windows[k].Label {
		return tabRenamed, ""
	}
	if err := d.rx.RenameWindow(ctx, p.Session, window, label); err != nil {
		d.log.Warn("rename tab", "window", window, "err", err)
		return tabSkipped, "Rex could not rename it"
	}
	return tabRenamed, ""
}

// text says in plain words what the rename did.
func (r renameReport) text(name string, auto bool) string {
	var parts []string
	if r.topic {
		parts = append(parts, "the topic")
	}
	if r.pane {
		if r.topic {
			parts = append(parts, "its Rex pane")
		} else {
			parts = append(parts, "the Rex pane")
		}
	}
	if r.tab {
		if r.topic || r.pane {
			parts = append(parts, "its tab")
		} else {
			parts = append(parts, "the tab")
		}
	}
	var b strings.Builder
	if len(parts) == 0 && r.topicSame && len(r.skips) == 0 {
		// only the note
	} else if len(parts) == 0 {
		b.WriteString("Nothing was renamed.")
	} else {
		if auto {
			b.WriteString("✏️ Reset " + joinWords(parts) + " to the automatic name.")
		} else {
			b.WriteString("✏️ Renamed " + joinWords(parts) + " to " + format.Truncate(name, 60) + ".")
		}
	}
	if r.topicSame {
		b.WriteString(" The topic already had that name in Telegram.")
	}
	if len(r.skips) > 0 {
		b.WriteString(" Not renamed: " + strings.Join(r.skips, "; ") + ".")
	}
	return strings.TrimSpace(b.String())
}

// joinWords is "a", "a and b", or "a, b, and c".
func joinWords(w []string) string {
	switch len(w) {
	case 0:
		return ""
	case 1:
		return w[0]
	case 2:
		return w[0] + " and " + w[1]
	}
	return strings.Join(w[:len(w)-1], ", ") + ", and " + w[len(w)-1]
}

// renameCommand is /rename <name> in a topic (core). An agent cannot rename
// its topic. /rename names the topic, its Rex pane, and its tab; the name
// sticks until /rename auto, or until the tab gets the "<folder> · <agent>"
// form again.
func (d *daemon) renameCommand(th store.Thread, args string, reply func(string)) {
	pl, refusal := d.planRename(&th, 0, args)
	if refusal != "" {
		reply(refusal)
		return
	}
	d.spawn(func() { reply(format.Escape(d.runRename(pl).text(pl.name, pl.auto))) })
}
