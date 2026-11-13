package daemon

import (
	"context"
	"encoding/json"
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
// reads again every few seconds while it is open, and the files of the
// agent's folder. A topic has one page of each (kv "page_for:<thread>:
// screen:live" and "page_for:<thread>:files:<folder>"); each /screen or
// /files that sends its button makes it young again.

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
	d.liveMu.Lock() // two buttons made at once get the same page
	defer d.liveMu.Unlock()
	name := pageForKey(thread, kind, key)
	if id, ok, _ := d.st.Get(d.ctx, name); ok && id != "" {
		if p, ok := loadPage(d.st, id); ok && p.Kind == kind {
			p.Title = title // the topic may have a new name
			if v, err := json.Marshal(p); err == nil {
				_ = d.st.Set(d.ctx, pageKey(id), string(v))
			}
			_ = d.st.Set(d.ctx, pageAtKey(id), fmt.Sprintf("%d|%s", d.now().Unix(), kind))
			return id, nil
		}
	}
	id, err := d.savePageOf(miniapp.Page{Kind: kind, Title: title, Thread: thread, Dir: dir})
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
	screen, err := d.rx.Capture(ctx, pane.Session, pane.Block)
	if err != nil {
		return miniapp.Screen{Error: "Could not read the screen: Rex is not reachable."}
	}
	lines := lastLines(format.Redact(screen), maxLiveLines) // redacted whole, like /screen
	return miniapp.Screen{Text: strings.Join(lines, "\n"), At: d.now().Format("15:04:05")}
}

// liveScreenButton is the button that opens a topic's live screen, or nil
// when the Mini App is not ready (any goroutine).
func (d *daemon) liveScreenButton(th store.Thread) *telegram.InlineKeyboardMarkup {
	if !d.miniAppReady() {
		return nil
	}
	id, err := d.livePage(miniapp.KindScreen, th.ID, "live", firstNonEmpty(th.Name, "Screen"), "")
	if err != nil {
		d.log.Warn("mini app: live screen page", "err", err)
		return nil
	}
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{
		{Text: "🖥 Live screen", WebApp: &telegram.WebAppInfo{URL: miniAppURL(d.cfg.MiniApp.URL, id)}},
	}}}
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
		root := cwd
		if out, err := git(d.ctx, cwd, nil, "rev-parse", "--show-toplevel"); err == nil {
			if top := strings.TrimRight(out, "\n"); top != "" {
				root = top
			}
		}
		root = filepath.Clean(root)
		id, err := d.livePage(miniapp.KindFiles, th.ID, root, "📂 "+filepath.Base(root), root)
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
