package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rex-tg/internal/format"
	"rex-tg/internal/inbound"
	"rex-tg/internal/miniapp"
	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// The Mini App ([miniapp] url) shows what is too big for a message: a long
// answer, and what a turn changed in its git repository. Its pages are kept
// in the store (kv "page:<id>", with a random id) for pageMaxAge; an answer
// names its pages in kv "page_for:<thread>:<kind>:<event key>". The buttons
// that open them sit under the answer, with the quick replies
// ([display] quick_replies), which send their text to the agent.

const pageMaxAge = 30 * 24 * time.Hour

func pageKey(id string) string { return "page:" + id }

func pageForKey(thread int64, kind, key string) string {
	return fmt.Sprintf("page_for:%d:%s:%s", thread, kind, key)
}

// miniAppOn tells whether the Mini App is set up.
func (d *daemon) miniAppOn() bool { return strings.TrimSpace(d.cfg.MiniApp.URL) != "" }

// miniAppURL is the address of a page in the Mini App.
func miniAppURL(base, id string) string { return strings.TrimRight(base, "/") + "/app?p=" + id }

// savePage keeps a page and returns its id (any goroutine).
func (d *daemon) savePage(kind, title, text string) (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b[:])
	v, err := json.Marshal(miniapp.Page{Kind: kind, Title: title, Text: text, At: d.now()})
	if err != nil {
		return "", err
	}
	if err := d.st.Set(d.ctx, pageKey(id), string(v)); err != nil {
		return "", err
	}
	return id, nil
}

// loadPage reads a page (any goroutine).
func loadPage(st *store.Store, id string) (miniapp.Page, bool) {
	if id == "" || len(id) > 64 {
		return miniapp.Page{}, false
	}
	v, ok, err := st.Get(context.Background(), pageKey(id))
	if err != nil || !ok {
		return miniapp.Page{}, false
	}
	var p miniapp.Page
	if json.Unmarshal([]byte(v), &p) != nil {
		return miniapp.Page{}, false
	}
	return p, true
}

// serveMiniApp runs the Mini App's server until ctx ends (a worker). The
// server checks Telegram's signature on every page request, with the key
// derived from the token at the start.
func (d *daemon) serveMiniApp(ctx context.Context) {
	owner := func() int64 { return d.kvInt("owner_user_id") }
	srv := &http.Server{
		Handler:           miniapp.NewWithSecret(d.miniSecret, owner, func(id string) (miniapp.Page, bool) { return loadPage(d.st, id) }, d.log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}
	ln, err := net.Listen("tcp", d.cfg.MiniApp.Listen)
	if err != nil {
		d.log.Warn("mini app: listen", "addr", d.cfg.MiniApp.Listen, "err", err)
		return
	}
	d.log.Info("mini app: serving", "addr", ln.Addr().String())
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		d.log.Warn("mini app: serve", "err", err)
	}
}

// longAnswer makes a long answer short in the chat (core): its start, and a
// page with all of it. ok is false when the answer stays as it is.
func (d *daemon) longAnswer(th store.Thread, key, md string) ([]store.PartContent, bool) {
	if !d.miniAppOn() || len([]rune(md)) <= d.cfg.MiniApp.AnswerPreview {
		return nil, false
	}
	title := format.Truncate(firstLine(format.Plain(md)), 80)
	if title == "" {
		title = "Answer"
	}
	id, err := d.savePage(miniapp.KindAnswer, title, md)
	if err != nil {
		d.log.Warn("mini app: keep the answer", "err", err)
		return nil, false
	}
	if err := d.st.Set(d.ctx, pageForKey(th.ID, miniapp.KindAnswer, key), id); err != nil {
		return nil, false
	}
	ms := format.Messages(md, answerPreviewChars, partLimit)
	if len(ms) == 0 {
		return nil, false
	}
	html := ms[0].HTML + "\n\n<i>… the whole answer is behind 📄 below.</i>"
	return []store.PartContent{{Content: html, Format: store.FormatHTML}}, true
}

// answerPreviewChars is about how much of a long answer the chat shows.
const answerPreviewChars = 1200

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// Quick replies: callback data "qr:<index>:<hash>". The hash is of the text,
// so a button made before the list changed sends nothing wrong.
const quickReplyData = "qr"

func quickHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:4])
}

// answerMarkup is the sender's hook for the buttons under an answer: the
// Mini App's pages of that answer, and the quick replies. Only the answer's
// last text message gets them. It runs in the sender (any goroutine).
func answerMarkup(st *store.Store, quick []string, base string) func(store.Part) *telegram.InlineKeyboardMarkup {
	return func(p store.Part) *telegram.InlineKeyboardMarkup {
		if p.Kind != "answer" {
			return nil
		}
		row, err := st.OutboxRow(context.Background(), p.OutboxID)
		if err != nil {
			return nil
		}
		last := -1
		for _, q := range row.Parts {
			switch q.Format {
			case store.FormatHTML, store.FormatPlain, store.FormatRich:
				last = q.Part
			}
		}
		if p.Part != last {
			return nil
		}
		return answerButtons(st, quick, base, p.ThreadID, p.Key)
	}
}

// answerButtons builds the keyboard of an answer (any goroutine).
func answerButtons(st *store.Store, quick []string, base string, thread int64, key string) *telegram.InlineKeyboardMarkup {
	var rows [][]telegram.InlineKeyboardButton
	if strings.TrimSpace(base) != "" {
		var pages []telegram.InlineKeyboardButton
		if id, ok, _ := st.Get(context.Background(), pageForKey(thread, miniapp.KindAnswer, key)); ok && id != "" {
			pages = append(pages, telegram.InlineKeyboardButton{Text: "📄 Whole answer", WebApp: &telegram.WebAppInfo{URL: miniAppURL(base, id)}})
		}
		if v, ok, _ := st.Get(context.Background(), pageForKey(thread, miniapp.KindDiff, key)); ok {
			if id, label, found := strings.Cut(v, "|"); found && id != "" {
				pages = append(pages, telegram.InlineKeyboardButton{Text: "📝 " + label, WebApp: &telegram.WebAppInfo{URL: miniAppURL(base, id)}})
			}
		}
		if len(pages) > 0 {
			rows = append(rows, pages)
		}
	}
	var row []telegram.InlineKeyboardButton
	for i, q := range quick {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		row = append(row, telegram.InlineKeyboardButton{Text: q, CallbackData: fmt.Sprintf("%s:%d:%s", quickReplyData, i, quickHash(q))})
		if len(row) == 4 {
			rows, row = append(rows, row), nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil
	}
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// quickReplyButton sends a quick reply's text to the agent, like a message
// typed in the topic (core). Its row id comes from the tap's update, so a
// repeat of the update types nothing.
func (d *daemon) quickReplyButton(upd int64, cb *telegram.CallbackQuery, parts []string) {
	th, ok := d.threadOfMessage(cb.Message)
	if !ok || len(parts) != 3 {
		d.answerCallback(cb.ID, "This topic is closed.", false)
		return
	}
	i, err := strconv.Atoi(parts[1])
	if err != nil || i < 0 || i >= len(d.cfg.Display.QuickReplies) || quickHash(strings.TrimSpace(d.cfg.Display.QuickReplies[i])) != parts[2] {
		d.answerCallback(cb.ID, "This button is out of date.", false)
		return
	}
	text := strings.TrimSpace(d.cfg.Display.QuickReplies[i])
	if !d.queueText(th.ID, upd, 0, text, "text", inbound.ActText) {
		d.answerCallback(cb.ID, "This topic is closed.", false)
		return
	}
	d.answerCallback(cb.ID, "Sending “"+text+"”…", false)
}

// Turn changes: a snapshot of the repository's files (a git tree, written
// through a separate index, so the user's index stays as it is) when a turn
// starts, and another when it ends with its answer; the diff of the two is
// a page.

// turnTree is the snapshot of a session's repository at its turn's start.
type turnTree struct{ top, tree string }

type evTurnTree struct {
	conv int64
	t    turnTree
}

type evTurnChanges struct {
	thread int64
	key    string
	page   string
	label  string
}

// changesOn tells whether turn changes are shown.
func (d *daemon) changesOn() bool { return d.miniAppOn() && d.cfg.Display.ShowChanges }

// turnStarted takes the snapshot of a turn's start (core).
func (d *daemon) turnStarted(conv int64, cwd string) {
	if !d.changesOn() || cwd == "" {
		return
	}
	if _, ok := d.turnTrees[conv]; ok {
		return // the turn already has one (a message that joined it)
	}
	d.turnTrees[conv] = turnTree{} // taken; filled when the snapshot is done
	d.spawn(func() {
		top, tree, err := gitTree(d.ctx, cwd)
		if err != nil {
			return // not a repository, or git failed: no changes for this turn
		}
		d.emit(evTurnTree{conv: conv, t: turnTree{top: top, tree: tree}})
	})
}

func (d *daemon) onTurnTree(e evTurnTree) {
	if _, ok := d.turnTrees[e.conv]; ok {
		d.turnTrees[e.conv] = e.t
	}
}

// turnAnswered compares the repository with the turn's start, and keeps the
// changes as a page of the answer (core).
func (d *daemon) turnAnswered(conv, thread int64, key string) {
	start, ok := d.turnTrees[conv]
	delete(d.turnTrees, conv)
	if !ok || start.tree == "" {
		return
	}
	d.spawn(func() {
		_, end, err := gitTree(d.ctx, start.top)
		if err != nil || end == start.tree {
			return
		}
		st, diff, err := gitChanges(d.ctx, start.top, start.tree, end)
		if err != nil || st.files == 0 {
			return
		}
		label := st.label()
		id, err := d.savePage(miniapp.KindDiff, "Changes · "+label, format.Redact(diff))
		if err != nil {
			return
		}
		d.emit(evTurnChanges{thread: thread, key: key, page: id, label: label})
	})
}

// onTurnChanges names the changes page under the answer, and puts its button on
// the answer when the answer is out already (core).
func (d *daemon) onTurnChanges(e evTurnChanges) {
	if err := d.st.Set(d.ctx, pageForKey(e.thread, miniapp.KindDiff, e.key), e.page+"|"+e.label); err != nil {
		return
	}
	d.refreshAnswerButtons(e.thread, e.key, 0)
}

// refreshAnswerButtons sets the keyboard of an answer that is out already;
// one that is not out yet gets it from the sender's hook. It looks again a
// few times while the answer waits to go out (core).
func (d *daemon) refreshAnswerButtons(thread int64, key string, try int) {
	id, ok, err := d.st.OutboxIDByKey(d.ctx, thread, key)
	if err != nil || !ok {
		return
	}
	row, err := d.st.OutboxRow(d.ctx, id)
	if err != nil {
		return
	}
	var last store.Part
	found := false
	for _, p := range row.Parts {
		switch p.Format {
		case store.FormatHTML, store.FormatPlain, store.FormatRich:
			last, found = p, true
		}
	}
	if !found {
		return
	}
	if last.TGMessageID == 0 || last.State != store.OutboxSent {
		if try < 5 {
			d.later(3*time.Second, func() { d.refreshAnswerButtons(thread, key, try+1) })
		}
		return
	}
	markup := answerButtons(d.st, d.cfg.Display.QuickReplies, d.cfg.MiniApp.URL, thread, key)
	chat, msg := last.ChatID, last.TGMessageID
	d.spawn(func() {
		_ = d.snd.Call(sender.PriorityEdit, func(ctx context.Context, tg *telegram.Client) error {
			return tg.EditMessageReplyMarkup(ctx, chat, msg, markup)
		})
	})
}

// later runs f in the core after a wait.
func (d *daemon) later(wait time.Duration, f func()) {
	d.spawn(func() {
		if sleepCtx(d.ctx, wait) {
			d.emit(evLater{f: f})
		}
	})
}

type evLater struct{ f func() }

// changeStat sums a diff.
type changeStat struct{ files, added, removed int }

func (c changeStat) label() string {
	files := "1 file"
	if c.files != 1 {
		files = fmt.Sprintf("%d files", c.files)
	}
	return fmt.Sprintf("%s · +%d −%d", files, c.added, c.removed)
}

// gitTimeout bounds each git call: a huge repository gets no changes rather
// than a slow daemon.
const gitTimeout = 15 * time.Second

// maxDiffBytes caps a changes page.
const maxDiffBytes = 2 << 20

func git(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	c, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(c, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0"), env...)
	out, err := cmd.Output()
	return string(out), err
}

// gitTree writes the repository's working files (tracked and untracked, not
// ignored) as a git tree through a copy of its index, and returns the
// repository's top folder and the tree. The user's own index and files stay
// as they are; git keeps the new objects until its next cleanup.
func gitTree(ctx context.Context, dir string) (top, tree string, err error) {
	out, err := git(ctx, dir, nil, "rev-parse", "--show-toplevel", "--absolute-git-dir")
	if err != nil {
		return "", "", err
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return "", "", errors.New("git: no repository")
	}
	top, gitDir := f[0], f[1]
	tmp, err := os.CreateTemp("", "rex-tg-index-*")
	if err != nil {
		return "", "", err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if b, err := os.ReadFile(filepath.Join(gitDir, "index")); err == nil {
		_, _ = tmp.Write(b) // a copy keeps git's file stat cache: only changed files are read
	}
	tmp.Close()
	env := []string{"GIT_INDEX_FILE=" + name}
	if _, err := git(ctx, top, env, "add", "-A", "--", "."); err != nil {
		return "", "", err
	}
	out, err = git(ctx, top, env, "write-tree")
	if err != nil {
		return "", "", err
	}
	return top, strings.TrimSpace(out), nil
}

// gitChanges sums and shows the difference of two trees.
func gitChanges(ctx context.Context, top, from, to string) (changeStat, string, error) {
	var st changeStat
	num, err := git(ctx, top, nil, "diff", "--numstat", from, to)
	if err != nil {
		return st, "", err
	}
	for _, l := range strings.Split(strings.TrimSpace(num), "\n") {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		st.files++
		a, _ := strconv.Atoi(f[0]) // "-" for a binary file
		r, _ := strconv.Atoi(f[1])
		st.added += a
		st.removed += r
	}
	diff, err := git(ctx, top, nil, "diff", "--no-color", "--no-ext-diff", from, to)
	if err != nil {
		return st, "", err
	}
	if len(diff) > maxDiffBytes {
		diff = diff[:maxDiffBytes] + "\n… (the rest is cut)\n"
	}
	return st, diff, nil
}

// prunePages drops the pages older than pageMaxAge and the names of pages
// that are gone (housekeeping, a worker).
func (d *daemon) prunePages(now time.Time) {
	keys, err := d.st.KeysWithPrefix(d.ctx, "page:")
	if err != nil {
		return
	}
	for _, k := range keys {
		p, ok := loadPage(d.st, strings.TrimPrefix(k, "page:"))
		if !ok || now.Sub(p.At) > pageMaxAge {
			_ = d.st.Delete(d.ctx, k)
		}
	}
	names, err := d.st.KeysWithPrefix(d.ctx, "page_for:")
	if err != nil {
		return
	}
	for _, k := range names {
		v, ok, err := d.st.Get(d.ctx, k)
		if err != nil || !ok {
			continue
		}
		id, _, _ := strings.Cut(v, "|")
		if _, ok := loadPage(d.st, id); !ok {
			_ = d.st.Delete(d.ctx, k)
		}
	}
}

// bothMarkups asks a for the buttons of a part, then b.
func bothMarkups(a, b func(store.Part) *telegram.InlineKeyboardMarkup) func(store.Part) *telegram.InlineKeyboardMarkup {
	return func(p store.Part) *telegram.InlineKeyboardMarkup {
		if m := a(p); m != nil {
			return m
		}
		return b(p)
	}
}
