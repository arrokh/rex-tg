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
	"sort"
	"strconv"
	"strings"
	"time"

	"rex-tg/internal/config"
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

// Pages are kept this long; changes pages less long. At most maxPages are
// kept; the oldest go first.
const (
	pageMaxAge = 30 * 24 * time.Hour
	diffMaxAge = 14 * 24 * time.Hour
	maxPages   = 500
)

func pageKey(id string) string { return "page:" + id }

// pageAtKey indexes a page by its time and kind ("<unix>|<kind>"), so the
// cleanup reads no page.
func pageAtKey(id string) string { return "page_at:" + id }

func pageForKey(thread int64, kind, key string) string {
	return fmt.Sprintf("page_for:%d:%s:%s", thread, kind, key)
}

// miniAppOn tells whether the Mini App is set up.
func (d *daemon) miniAppOn() bool {
	return strings.TrimSpace(d.cfg.MiniApp.URL) != "" && d.cfg.MiniAppProblem() == ""
}

// miniBase is the Mini App's address for page buttons, or "" when the Mini
// App is off (no url, or settings that are not safe): then no button leads
// to a server that does not run.
func miniBase(cfg config.Config) string {
	if cfg.MiniAppProblem() != "" {
		return ""
	}
	return strings.TrimSpace(cfg.MiniApp.URL)
}

// miniAppReady tells whether the Mini App's server listens: only then do
// answers send their readers to it.
func (d *daemon) miniAppReady() bool { return d.miniAppOn() && d.miniUp.Load() }

// miniAppURL is the address of a page in the Mini App.
func miniAppURL(base, id string) string { return strings.TrimRight(base, "/") + "/app?p=" + id }

// savePage keeps a page and returns its id (any goroutine).
func (d *daemon) savePage(kind, title, text string) (string, error) {
	return d.savePageOf(miniapp.Page{Kind: kind, Title: title, Text: text})
}

// savePageOf keeps a page and returns its new random id (any goroutine).
func (d *daemon) savePageOf(p miniapp.Page) (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b[:])
	p.At = d.now()
	v, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	if err := d.st.Set(d.ctx, pageKey(id), string(v)); err != nil {
		return "", err
	}
	if err := d.st.Set(d.ctx, pageAtKey(id), fmt.Sprintf("%d|%s", d.now().Unix(), p.Kind)); err != nil {
		_ = d.st.Delete(d.ctx, pageKey(id))
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
// server checks Telegram's signature (or the buttons' key) on every page
// request, with the key derived from the token at the start. With
// [miniapp] tailnet_only it answers only requests from the tailnet.
func (d *daemon) serveMiniApp(ctx context.Context) {
	owner := func() int64 { return d.kvInt("owner_user_id") }
	srv := &http.Server{
		Handler: miniapp.NewWithSecret(d.miniSecret, owner, func(id string) (miniapp.Page, bool) { return loadPage(d.st, id) }, d.log).
			WithScreen(d.liveScreen).
			WithFiles(func() bool { return d.cfg.MiniApp.Files }, format.Redact).
			WithChanges(func() bool { return d.cfg.MiniApp.Changes }, format.Redact, d.lastTurn).
			WithControl(newControlPanel(d), d.home).
			WithLaunch(d, d.keyOK).
			WithAnnotate(d).
			WithTailnetOnly(d.cfg.MiniApp.TailnetOnly).
			WithLogins(d.logins).
			Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}
	var ln net.Listener
	for wait := time.Second; ; wait = min(2*wait, time.Minute) {
		var err error
		if ln, err = net.Listen("tcp", d.cfg.MiniApp.Listen); err == nil {
			break
		}
		// The old daemon may still hold the port for a moment.
		d.log.Warn("mini app: listen; trying again", "addr", d.cfg.MiniApp.Listen, "err", err, "in", wait)
		if !sleepCtx(ctx, wait) {
			return
		}
	}
	d.miniUp.Store(true)
	defer d.miniUp.Store(false)
	d.log.Info("mini app: serving", "addr", ln.Addr().String(), "tailnet_only", d.cfg.MiniApp.TailnetOnly)
	if d.cfg.MiniApp.TailnetOnly {
		d.log.Info("mini app: answers only requests from your tailnet (tailscale serve); a Funnel address is refused")
	}
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

// longAnswerIf is longAnswer for an answer (isAnswer), with its text
// redacted first.
func (d *daemon) longAnswerIf(isAnswer bool, th store.Thread, key, markdown string) ([]store.PartContent, bool) {
	if !isAnswer {
		return nil, false
	}
	return d.longAnswer(th, key, format.Redact(markdown))
}

// longAnswer makes a long answer short in the chat (core): its start, and a
// page with all of it. ok is false when the answer stays as it is.
func (d *daemon) longAnswer(th store.Thread, key, md string) ([]store.PartContent, bool) {
	if !d.miniAppReady() || len([]rune(md)) <= max(d.cfg.MiniApp.AnswerPreview, 2*answerPreviewChars) {
		return nil, false
	}
	title := format.Truncate(firstLine(format.Plain(md)), 80)
	if title == "" {
		title = "Answer"
	}
	// The page knows its topic, so its notes can go to the agent (Annotate).
	id, err := d.savePageOf(miniapp.Page{Kind: miniapp.KindAnswer, Title: title, Text: md, Thread: th.ID, Conv: th.CurrentConversationID})
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

// quickPerRow is how many quick replies share a row: on a phone, more cut
// their labels ("Conti…").
const quickPerRow = 2

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
		for _, b := range pages {
			// One to a row: their labels ("📝 2 files · +40 −12") are long
			// for a phone.
			rows = append(rows, []telegram.InlineKeyboardButton{b})
		}
	}
	var row []telegram.InlineKeyboardButton
	for i, q := range quick {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		row = append(row, telegram.InlineKeyboardButton{Text: q, CallbackData: fmt.Sprintf("%s:%d:%s", quickReplyData, i, quickHash(q))})
		if len(row) == quickPerRow {
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
type turnTree struct {
	top, tree, key string
	answered       bool // retained for replay until the transcript commit succeeds
}

type evTurnTree struct {
	conv int64
	t    turnTree
}

// evTurnRange is what a turn changed, for /changes (BaseTurn).
type evTurnRange struct {
	thread int64
	r      miniapp.TurnRange
}

type evTurnChanges struct {
	thread int64
	key    string
	page   string
	label  string
}

// changesOn tells whether turn changes are shown.
func (d *daemon) changesOn() bool { return d.miniAppReady() && d.cfg.Display.ShowChanges }

// turnStarted takes the snapshot of a turn's start (core). joins is true
// for a prompt that joins a running turn (a steered message): that turn
// keeps the snapshot of its start. A new turn always takes a fresh one, so
// a turn that ended without an answer leaves nothing behind.
func (d *daemon) turnStarted(conv int64, cwd, key string, joins bool) {
	if !d.changesOn() || cwd == "" {
		return
	}
	if old, ok := d.turnTrees[conv]; ok && (old.key == key || joins && !old.answered) {
		return // replay must keep the original start, even after a failed commit
	}
	d.turnTrees[conv] = turnTree{key: key} // taken; filled when the snapshot is done
	d.spawn(func() {
		top, tree, err := gitTree(d.ctx, cwd)
		if err != nil {
			return // not a repository, or git failed: no changes for this turn
		}
		d.emit(evTurnTree{conv: conv, t: turnTree{top: top, tree: tree, key: key}})
	})
}

func (d *daemon) onTurnTree(e evTurnTree) {
	if current, ok := d.turnTrees[e.conv]; ok && current.key == e.t.key {
		e.t.answered = current.answered
		d.turnTrees[e.conv] = e.t
	}
}

// turnAnswered compares the repository with the turn's start, and keeps the
// changes as a page of the answer (core).
func (d *daemon) turnAnswered(conv, thread int64, key string) {
	// Keep the start until the transcript transaction commits. A failed
	// final-answer batch may replay the prompt and answer together.
	start, ok := d.turnTrees[conv]
	if ok {
		retained := start
		retained.answered = true
		d.turnTrees[conv] = retained
	}
	if !ok || start.tree == "" {
		return
	}
	at := d.now()
	d.spawn(func() {
		_, end, err := gitTree(d.ctx, start.top)
		if err != nil {
			return
		}
		d.emit(evTurnRange{thread: thread, r: miniapp.TurnRange{Top: start.top, From: start.tree, To: end, At: at}})
		if end == start.tree {
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

func lastTurnKey(thread int64) string { return fmt.Sprintf("last_turn:%d", thread) }

// onTurnRange keeps what the topic's newest turn changed (core). A turn that
// ended before the kept one does not replace it.
func (d *daemon) onTurnRange(e evTurnRange) {
	if th, err := d.st.Thread(d.ctx, e.thread); err != nil || th.Status == store.ThreadClosed {
		return // closed while the snapshot ran: closeThread already dropped its key
	}
	if old, ok := readLastTurn(d.st, e.thread); ok && old.At.After(e.r.At) {
		return
	}
	b, err := json.Marshal(e.r)
	if err == nil {
		err = d.st.Set(d.ctx, lastTurnKey(e.thread), string(b))
	}
	if err != nil {
		d.log.Warn("last turn: keep", "err", err)
	}
}

// lastTurn is the newest turn of a changes page's topic, or the zero range
// (the Mini App's server).
func (d *daemon) lastTurn(p miniapp.Page) miniapp.TurnRange {
	t, _ := readLastTurn(d.st, p.Thread)
	return t
}

func readLastTurn(st *store.Store, thread int64) (miniapp.TurnRange, bool) {
	v, ok, err := st.Get(context.Background(), lastTurnKey(thread))
	if err != nil || !ok {
		return miniapp.TurnRange{}, false
	}
	var t miniapp.TurnRange
	if json.Unmarshal([]byte(v), &t) != nil {
		return miniapp.TurnRange{}, false
	}
	return t, true
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
		if try < 12 { // about 4 minutes, for an answer held back by the rate limit or a network outage
			d.later(time.Duration(min(try+1, 6))*5*time.Second, func() { d.refreshAnswerButtons(thread, key, try+1) })
		}
		return
	}
	markup := answerButtons(d.st, d.cfg.Display.QuickReplies, miniBase(d.cfg), thread, key)
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
	// No file system monitor: it can run a program of the repository's.
	cmd := exec.CommandContext(c, "git", append([]string{"-C", dir, "-c", "core.fsmonitor=false"}, args...)...)
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
	f := strings.Split(strings.TrimRight(out, "\n"), "\n") // a path may hold spaces
	if len(f) != 2 || f[0] == "" || f[1] == "" {
		return "", "", errors.New("git: no repository")
	}
	top, gitDir := f[0], f[1]
	if err := checkUntracked(ctx, top); err != nil {
		return "", "", err
	}
	tmp, err := os.CreateTemp("", "rex-tg-index-*")
	if err != nil {
		return "", "", err
	}
	name := tmp.Name()
	defer os.Remove(name)
	b, rerr := os.ReadFile(filepath.Join(gitDir, "index"))
	if rerr == nil {
		_, _ = tmp.Write(b) // a copy keeps git's file stat cache: only changed files are read
	}
	tmp.Close()
	if rerr != nil {
		_ = os.Remove(name) // no index yet: git starts a new one (an empty file is no index)
	}
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

// Limits of the new files that a snapshot takes in: a repository with more
// (build output that is not ignored, a home folder) gets no changes, rather
// than a slow turn and a .git full of copies.
const (
	maxUntrackedFiles = 300
	maxUntrackedBytes = 20 << 20
)

// checkUntracked refuses a repository whose new, not ignored files are too
// many or too big for a snapshot.
func checkUntracked(ctx context.Context, top string) error {
	out, err := git(ctx, top, nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	var n int
	var size int64
	for _, name := range strings.Split(out, "\x00") {
		if name == "" {
			continue
		}
		if n++; n > maxUntrackedFiles {
			return errors.New("git: too many new files for a snapshot")
		}
		if fi, err := os.Lstat(filepath.Join(top, name)); err == nil {
			if size += fi.Size(); size > maxUntrackedBytes {
				return errors.New("git: new files too big for a snapshot")
			}
		}
	}
	return nil
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

// prunePages drops the pages that are too old, the oldest beyond maxPages,
// and the names of pages that are gone (housekeeping, a worker). It reads
// the small index (page_at:), not the pages.
func (d *daemon) prunePages(now time.Time) {
	keys, err := d.st.KeysWithPrefix(d.ctx, "page_at:")
	if err != nil {
		return
	}
	type entry struct {
		id string
		at int64
	}
	var keep []entry
	drop := func(id string) {
		_ = d.st.Delete(d.ctx, pageKey(id))
		_ = d.st.Delete(d.ctx, pageAtKey(id))
	}
	for _, k := range keys {
		id := strings.TrimPrefix(k, "page_at:")
		v, ok, err := d.st.Get(d.ctx, k)
		if err != nil || !ok {
			continue
		}
		atText, kind, _ := strings.Cut(v, "|")
		at, perr := strconv.ParseInt(atText, 10, 64)
		age := pageMaxAge
		switch kind {
		case miniapp.KindDiff:
			age = diffMaxAge
		case miniapp.KindScreen, miniapp.KindFiles, miniapp.KindChanges, miniapp.KindControl, miniapp.KindAnnotate:
			age = livePageMaxAge // since the last use
		}
		if perr != nil || now.Sub(time.Unix(at, 0)) > age {
			drop(id)
			continue
		}
		keep = append(keep, entry{id, at})
	}
	if len(keep) > maxPages {
		sort.Slice(keep, func(i, j int) bool { return keep[i].at < keep[j].at })
		for _, e := range keep[:len(keep)-maxPages] {
			drop(e.id)
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
