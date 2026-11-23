package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rex-tg/internal/format"
	"rex-tg/internal/miniapp"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// Live pages of the Mini App: the screen of a topic's pane, which the page
// reads again every few seconds while it is open, the files of the agent's
// folder, and the changes of its repository. A topic has one page of each
// (kv "page_for:<thread>:screen:live", "page_for:<thread>:files:<folder>"
// and "page_for:<thread>:changes:<folder>"); each /screen, /files or
// /changes that sends its button makes it young again.

// livePageMaxAge is how long a live page is kept after the last /screen or
// /files that sent its button.
const livePageMaxAge = 7 * 24 * time.Hour

// screenEvery is the shortest gap between two reads of a topic's screen for
// the live screen, however many pages are open.
const screenEvery = time.Second

// maxLiveLines is how many lines of the screen the live screen shows.
const maxLiveLines = 200

// screenRead is the last live-screen read of a topic. Its lock is held
// during a read, so the pages open on one topic share one read.
type screenRead struct {
	mu sync.Mutex
	at time.Time
	sc miniapp.Screen
}

// livePage returns the id of the topic's live page of a kind and key, and
// makes a new one when there is none (any goroutine).
func (d *daemon) livePage(kind string, thread int64, key, title, dir string) (string, error) {
	return d.livePageOf(miniapp.Page{Kind: kind, Title: title, Thread: thread, Dir: dir}, key)
}

// livePageOf is livePage for a page with more fields (Conv).
func (d *daemon) livePageOf(np miniapp.Page, key string) (string, error) {
	kind, thread, title := np.Kind, np.Thread, np.Title
	d.liveMu.Lock() // two buttons made at once get the same page
	defer d.liveMu.Unlock()
	name := pageForKey(thread, kind, key)
	if id, ok, _ := d.st.Get(d.ctx, name); ok && id != "" {
		if p, ok := loadPage(d.st, id); ok && p.Kind == kind {
			p.Title = title // the topic may have a new name
			if kind == miniapp.KindProject {
				p.Conv = np.Conv // Project's Config tab must follow the session that opened it.
			}
			if v, err := json.Marshal(p); err == nil {
				_ = d.st.Set(d.ctx, pageKey(id), string(v))
			}
			_ = d.st.Set(d.ctx, pageAtKey(id), fmt.Sprintf("%d|%s", d.now().Unix(), kind))
			return id, nil
		}
	}
	id, err := d.savePageOf(np)
	if err != nil {
		return "", err
	}
	if err := d.st.Set(d.ctx, name, id); err != nil {
		return "", err
	}
	return id, nil
}

// liveScreen reads the screen of a screen page's topic for the Mini App
// (any goroutine). Reads of one topic are at least screenEvery apart.
func (d *daemon) liveScreen(p miniapp.Page) miniapp.Screen {
	d.screenMu.Lock()
	if d.screenShots == nil {
		d.screenShots = make(map[int64]*screenRead)
	}
	r := d.screenShots[p.Thread]
	if r == nil {
		r = &screenRead{}
		d.screenShots[p.Thread] = r
	}
	d.screenMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.at.IsZero() && d.now().Sub(r.at) < screenEvery {
		return r.sc
	}
	r.sc, r.at = d.readScreen(p.Thread), d.now()
	return r.sc
}

func (d *daemon) readScreen(thread int64) miniapp.Screen {
	ctx, cancel := context.WithTimeout(d.ctx, 10*time.Second)
	defer cancel()
	th, err := d.st.Thread(ctx, thread)
	if err != nil {
		return miniapp.Screen{Error: "This topic is gone."}
	}
	if th.Status == store.ThreadClosed {
		return miniapp.Screen{Error: "This topic is closed."}
	}
	f, pane, err := d.baseFacts(ctx, th)
	if err != nil || !f.PaneExists {
		return miniapp.Screen{Error: "There is no pane for this topic now."}
	}
	raw, err := d.rx.CaptureVT(ctx, pane.Session, pane.Block)
	if err != nil {
		return miniapp.Screen{Error: "Could not read the screen: Rex is not reachable."}
	}
	text, vt := screenOf(raw, maxLiveLines)
	return miniapp.Screen{Text: text, VT: vt, At: d.now().Format("15:04:05")}
}

// screenOf turns a capture with control sequences into the bottom n lines
// of the screen: as plain text, and with its colors (only SGR sequences,
// each line ending in a reset). Secrets are redacted on the whole plain
// text first, like /screen. A line that lost a secret keeps no colors (its
// redacted plain text stands in). When redaction joined lines, there are
// no colors at all.
func screenOf(raw string, n int) (text, vt string) {
	rows := strings.Split(raw, "\n")
	vts := make([]string, 0, len(rows))
	plains := make([]string, 0, len(rows))
	for _, r := range rows {
		v, p := sgrOnly(r)
		vts, plains = append(vts, v), append(plains, p)
	}
	for len(plains) > 0 && strings.TrimSpace(plains[len(plains)-1]) == "" {
		vts, plains = vts[:len(vts)-1], plains[:len(plains)-1]
	}
	if len(plains) == 0 {
		return "", ""
	}
	red := strings.Split(format.Redact(strings.Join(plains, "\n")), "\n")
	if len(red) != len(plains) {
		return strings.Join(lastLines(strings.Join(red, "\n"), n), "\n"), ""
	}
	for i := range plains {
		if red[i] != plains[i] {
			vts[i] = red[i]
		}
	}
	start := max(0, len(red)-n)
	for i := start; i < len(vts); i++ {
		vts[i] += "\x1b[0m"
	}
	return strings.Join(red[start:], "\n"), strings.Join(vts[start:], "\n")
}

// sgrOnly reads one captured line: it keeps the text and the SGR sequences
// (colors and styles, ESC [ … m) in vt, the text alone in plain, and drops
// every other control sequence (links, titles, cursor moves) and control
// character.
func sgrOnly(line string) (vt, plain string) {
	var v, p strings.Builder
	for i := 0; i < len(line); {
		c := line[i]
		switch {
		case c == 0x1b && i+1 < len(line) && line[i+1] == '[':
			j := i + 2
			for j < len(line) && line[j] >= 0x30 && line[j] <= 0x3f { // parameters
				j++
			}
			params := line[i+2 : j]
			for j < len(line) && line[j] >= 0x20 && line[j] <= 0x2f { // intermediates
				j++
			}
			if j >= len(line) {
				return v.String(), p.String() // cut short: drop it
			}
			if line[j] == 'm' && strings.Trim(params, "0123456789;:") == "" {
				v.WriteString(line[i : j+1])
			}
			i = j + 1
		case c == 0x1b && i+1 < len(line) && strings.IndexByte("]P_^X", line[i+1]) >= 0:
			// A string: up to BEL or ESC \.
			j := i + 2
			for j < len(line) && line[j] != 0x07 && !(line[j] == 0x1b && j+1 < len(line) && line[j+1] == '\\') {
				j++
			}
			switch {
			case j >= len(line):
				i = j
			case line[j] == 0x07:
				i = j + 1
			default:
				i = j + 2
			}
		case c == 0x1b:
			// ESC, any intermediates (such as "(" of a charset switch), and
			// a final character.
			j := i + 1
			for j < len(line) && line[j] >= 0x20 && line[j] <= 0x2f {
				j++
			}
			i = j + 1
		case c == '\t':
			v.WriteByte(c)
			p.WriteByte(c)
			i++
		case c < 0x20 || c == 0x7f:
			i++
		default:
			v.WriteByte(c)
			p.WriteByte(c)
			i++
		}
	}
	return v.String(), p.String()
}

// liveScreenButton is the button that opens a topic's live screen, or nil
// when the Mini App is not ready (any goroutine).
func (d *daemon) liveScreenButton(th store.Thread) *telegram.InlineKeyboardMarkup {
	if !d.miniAppReady() {
		return nil
	}
	id, err := d.screenPage(th)
	if err != nil {
		d.log.Warn("mini app: live screen page", "err", err)
		return nil
	}
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{
		{Text: "🖥 Live screen", WebApp: &telegram.WebAppInfo{URL: miniAppURL(d.cfg.MiniApp.URL, id)}},
	}}}
}

// screenPage returns the id of a topic's live screen page (any goroutine).
func (d *daemon) screenPage(th store.Thread) (string, error) {
	return d.livePage(miniapp.KindScreen, th.ID, "live", firstNonEmpty(th.Name, "Screen"), "")
}

// repoTop is the top folder of the git repository that holds cwd. ok is
// false outside a repository (worker).
func (d *daemon) repoTop(ctx context.Context, cwd string) (string, bool) {
	out, err := git(ctx, cwd, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return cwd, false
	}
	top := strings.TrimRight(out, "\n")
	if top == "" {
		return cwd, false
	}
	return filepath.Clean(top), true
}

// filesPage returns the id of the files page of the agent's folder: the
// repository's top folder when it works in a git repository, else its
// working folder (worker).
func (d *daemon) filesPage(ctx context.Context, th store.Thread, cwd string) (string, error) {
	root, _ := d.repoTop(ctx, cwd)
	root = filepath.Clean(root)
	return d.livePage(miniapp.KindFiles, th.ID, root, "📂 "+filepath.Base(root), root)
}

// changesPage returns the id of the changes page of a repository's top folder.
func (d *daemon) changesPage(th store.Thread, top string) (string, error) {
	return d.livePage(miniapp.KindChanges, th.ID, top, "📝 "+filepath.Base(top), top)
}

// projectPage returns the combined files and changes page for a folder.
func (d *daemon) projectPage(th store.Thread, root string, conv int64) (string, error) {
	return d.livePageOf(miniapp.Page{Kind: miniapp.KindProject, Thread: th.ID, Conv: conv, Title: "📁 " + filepath.Base(root), Dir: root}, root)
}

// filesCommand answers /files with a button that opens the agent's folder
// in the Mini App: the repository's top folder when the agent works in a
// git repository, else its working folder.
func (d *daemon) filesCommand(th store.Thread, upd int64) {
	tag := fmt.Sprintf("%d:files", upd)
	switch {
	case !d.miniAppReady():
		d.replyTopic(th, tag, "📂 The file browser is part of the Mini App, which is off. Set <code>[miniapp] url</code> to turn it on.")
		return
	case !d.cfg.MiniApp.Files:
		d.replyTopic(th, tag, "📂 The file browser is off (<code>[miniapp] files</code>).")
		return
	}
	conv, err := d.st.Conversation(d.ctx, th.CurrentConversationID)
	if err != nil || conv.CWD == "" {
		d.replyTopic(th, tag, "This topic has no folder yet.")
		return
	}
	cwd := conv.CWD
	d.spawn(func() {
		root, _ := d.repoTop(d.ctx, cwd)
		root = filepath.Clean(root)
		id, err := d.filesPage(d.ctx, th, cwd)
		if err != nil {
			d.log.Warn("mini app: files page", "err", err)
			d.replyTopic(th, tag, "Could not open the file browser.")
			return
		}
		markup := &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{
			{Text: "📂 Browse files", WebApp: &telegram.WebAppInfo{URL: miniAppURL(d.cfg.MiniApp.URL, id)}},
		}}}
		d.replyTopicMarkup(th, "📂 <code>"+format.Escape(d.tildePath(root))+"</code>", markup)
	})
}

// changesCommand answers /changes with what changed in the agent's git
// repository: not committed, on this branch since it left the main branch,
// and in the agent's last turn. When any of them shows changes, a button
// opens them in the Mini App, where each can be picked.
func (d *daemon) changesCommand(th store.Thread, upd int64) {
	tag := fmt.Sprintf("%d:changes", upd)
	switch {
	case !d.miniAppReady():
		d.replyTopic(th, tag, "📝 The changes view is part of the Mini App, which is off. Set <code>[miniapp] url</code> to turn it on.")
		return
	case !d.cfg.MiniApp.Changes:
		d.replyTopic(th, tag, "📝 The changes view is off (<code>[miniapp] changes</code>).")
		return
	}
	conv, err := d.st.Conversation(d.ctx, th.CurrentConversationID)
	if err != nil || conv.CWD == "" {
		d.replyTopic(th, tag, "This topic has no folder yet.")
		return
	}
	cwd := conv.CWD
	turn, _ := readLastTurn(d.st, th.ID)
	d.spawn(func() {
		ctx, cancel := context.WithTimeout(d.ctx, time.Minute)
		defer cancel()
		all, err := miniapp.AllChanges(ctx, cwd, turn)
		switch {
		case errors.Is(err, miniapp.ErrNotRepo):
			d.replyTopic(th, tag, "📝 <code>"+format.Escape(d.tildePath(cwd))+"</code> is not in a git repository, so there are no changes to show.")
			return
		case err != nil:
			d.replyTopic(th, tag, "📝 Could not read the changes: git failed or took too long.")
			return
		}
		head := all[0]
		var lines []string
		some := false
		for _, l := range all {
			switch l.Base {
			case miniapp.BaseHead:
				lines = append(lines, "Not committed: "+changesLabel(l))
			case miniapp.BaseBranch:
				lines = append(lines, "This branch vs <code>"+format.Escape(l.Main)+"</code>: "+changesLabel(l))
			case miniapp.BaseTurn:
				lines = append(lines, "Last turn ("+l.TurnAt+"): "+changesLabel(l))
			}
			some = some || l.Total > 0
		}
		top, _ := d.repoTop(d.ctx, cwd)
		where := "📝 <code>" + format.Escape(d.tildePath(top)) + "</code>"
		if head.Branch != "" {
			where += " · " + format.Escape(head.Branch)
		}
		text := where + "\n" + strings.Join(lines, "\n")
		if !some {
			d.replyTopic(th, tag, text)
			return
		}
		id, err := d.changesPage(th, top)
		if err != nil {
			d.log.Warn("mini app: changes page", "err", err)
			d.replyTopic(th, tag, "Could not open the changes view.")
			return
		}
		markup := &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{
			{Text: "📝 View changes", WebApp: &telegram.WebAppInfo{URL: miniAppURL(d.cfg.MiniApp.URL, id)}},
		}}}
		d.replyTopicMarkup(th, text, markup)
	})
}

// changesLabel sums a list: "3 files · +10 −2", or "nothing".
func changesLabel(l miniapp.ChangeList) string {
	if l.Total == 0 {
		return "nothing"
	}
	return changeStat{files: l.Total, added: l.Added, removed: l.Removed}.label()
}
