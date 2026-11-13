package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"rex-tg/internal/agents"
	"rex-tg/internal/format"
	"rex-tg/internal/inbound"
	"rex-tg/internal/rex"
	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// /new starts a coding agent in a new Rex tab (design section 8.7). It asks
// for the agent and the folder. Every tab goes into one Rex session, named
// by rex.new_session ("rex-tg"); when there is none, rex-tg makes it. A tab
// is named "<folder> · <agent>" at first, and later takes the name of its
// topic (see syncTab). Once the agent reports its session, its topic opens,
// as with the Open button of /agents.
//
// The core keeps each request in memory under a random token, which its
// buttons carry. The request exists before its question is sent, so its
// buttons work even when Telegram's answer to the send was lost. A restart
// forgets the requests: an old button then only says that the request
// expired. A request starts at most one agent, and a start that failed is
// never tried again, because the tab may exist anyway.

// newExpiry is how long a /new question waits for an answer.
const newExpiry = 10 * time.Minute

// maxNewFolders is the number of folder buttons.
const maxNewFolders = 6

// maxShownPath is the longest path that a message shows; a message holds at
// most 4096 characters.
const maxShownPath = 200

const (
	newPausedText = "⏸ Typing is paused, so /new starts no agent. Use /unpause first."
	newExample    = "<code>~/work/app</code>"
)

type newStep int

const (
	newPickAgent  newStep = iota // the agent buttons are shown
	newPickFolder                // the folder buttons are shown, and a path may be typed
	newStarting                  // a worker starts the agent; the request takes no more answers
	newWaiting                   // the agent runs and has not reported its session; it may ask a startup question
)

// newFlow is one /new request (core).
type newFlow struct {
	seq          int64 // order of creation: a typed folder answers the newest question
	msg          int64 // the question's message; 0 until Telegram or a button names it
	chat, thread int64 // where the question is; thread 0 is the main chat
	linked       bool  // the question is in a topic that is linked to an agent
	step         newStep
	offered      []newAgent // the agent buttons
	agent        newAgent   // the chosen agent, from newPickFolder on
	folders      []string   // the folder buttons, by index
	expires      time.Time
	answers      chan startupAnswer // newWaiting: button presses for the agent's startup question
}

// startupAnswer is a press of a startup question's button, for the worker
// that waits for the agent.
type startupAnswer struct {
	cbID       string
	msg        int64 // the question's message
	seq, index int   // which question, and which answer
}

// newToken returns a random token for a request's buttons.
func newToken() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b) // never fails (crypto/rand)
	return hex.EncodeToString(b)
}

// runtimeName is the name of an agent in messages and buttons.
func runtimeName(rt agents.Runtime) string {
	switch rt {
	case agents.Claude:
		return "Claude Code"
	case agents.Codex:
		return "Codex"
	}
	return string(rt)
}

// newAgent is an agent that /new can start.
type newAgent struct {
	key     string         // its name in /new and in button data: pi, claude, cux, codex
	rt      agents.Runtime // the runtime that the pane runs
	command []string       // what the new tab runs
}

// name is the agent's name in messages and buttons.
func (a newAgent) name() string {
	if a.key == string(a.rt) {
		return runtimeName(a.rt)
	}
	return a.key
}

// newAgents lists the agents that /new offers: pi, Claude Code, and Codex,
// and cux when it is installed. cux starts Claude Code with one of several
// Claude accounts and runs it as its child, so its pane counts as Claude
// Code (agents.claude_argv0).
func (d *daemon) newAgents() []newAgent {
	list := []newAgent{
		{key: "pi", rt: agents.Pi, command: d.cfg.Input(agents.Pi).Launch},
		{key: "claude", rt: agents.Claude, command: d.cfg.Input(agents.Claude).Launch},
	}
	if d.hasProgram("cux") {
		list = append(list, newAgent{key: "cux", rt: agents.Claude, command: []string{"cux"}})
	}
	return append(list, newAgent{key: "codex", rt: agents.Codex, command: d.cfg.Input(agents.Codex).Launch})
}

// installed reports whether a program is on the PATH, or in a folder where
// user programs usually are. launchd starts the daemon with a short PATH,
// while /new starts agents through the login shell, which knows more.
func installed(home, name string) bool {
	if _, err := exec.LookPath(name); err == nil {
		return true
	}
	for _, dir := range []string{filepath.Join(home, ".local", "bin"), "/opt/homebrew/bin", "/usr/local/bin"} {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return true
		}
	}
	return false
}

// agentKeys is "pi|claude|codex", for help texts.
func agentKeys(list []newAgent) string {
	keys := make([]string, len(list))
	for i, a := range list {
		keys[i] = a.key
	}
	return strings.Join(keys, "|")
}

// parseNewArgs reads "/new [agent [folder]]". The folder is the rest of the
// text, so it may contain spaces. ok is false when there is no agent; bad is
// the first word when it names none of list.
func parseNewArgs(args string, list []newAgent) (a newAgent, ok bool, folder, bad string) {
	args = strings.TrimSpace(args)
	if args == "" {
		return newAgent{}, false, "", ""
	}
	word, rest := args, ""
	if i := strings.IndexFunc(args, unicode.IsSpace); i >= 0 {
		word, rest = args[:i], args[i:]
	}
	a, ok = findAgent(list, word)
	if !ok {
		return newAgent{}, false, "", word
	}
	return a, true, strings.TrimSpace(rest), ""
}

// findAgent returns the agent of list with this key, ignoring case.
func findAgent(list []newAgent, key string) (newAgent, bool) {
	for _, a := range list {
		if strings.EqualFold(a.key, key) {
			return a, true
		}
	}
	return newAgent{}, false
}

// folderPath turns a typed folder into a clean absolute path. "~" is the home
// folder, and a relative path starts there. Quotes around the path, which a
// phone may add, are removed.
func folderPath(text, home string) (string, error) {
	s := strings.TrimSpace(text)
	for _, q := range [][2]string{{`"`, `"`}, {"'", "'"}, {"`", "`"}, {"“", "”"}, {"‘", "’"}} {
		if len(s) >= len(q[0])+len(q[1]) && strings.HasPrefix(s, q[0]) && strings.HasSuffix(s, q[1]) {
			s = strings.TrimSpace(s[len(q[0]) : len(s)-len(q[1])])
			break
		}
	}
	switch {
	case s == "":
		return "", errors.New("no folder")
	case strings.ContainsAny(s, "\r\n\x00"):
		return "", errors.New("a folder is one line")
	case s == "~":
		s = home
	case strings.HasPrefix(s, "~/"):
		s = filepath.Join(home, s[2:])
	case strings.HasPrefix(s, "~"):
		return "", errors.New("use ~/ for your home folder")
	case !filepath.IsAbs(s):
		s = filepath.Join(home, s)
	}
	return filepath.Clean(s), nil
}

// linkedFolderText reports whether a text in a linked topic answers the
// folder question: a path that starts with "~", or the absolute path of an
// existing folder. Any other text goes to the agent as usual, also an agent
// command such as /compact, or //new (which sends /new).
func linkedFolderText(text, home string) bool {
	switch {
	case strings.HasPrefix(text, "~"):
		return true
	case strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "//"):
		p, err := folderPath(text, home)
		return err == nil && isDir(p)
	}
	return false
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// maxLabel is the longest session or tab name that Rex takes, in characters.
const maxLabel = 128

// cleanLabel puts a name in the form that Rex takes for a tab: Rex refuses
// control characters and names longer than 128 characters, and a name that
// starts with "-" would be read as an option of rex. It may return "".
func cleanLabel(s string) string {
	l := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "-"))
	if r := []rune(l); len(r) > maxLabel {
		l = strings.TrimSpace(string(r[:maxLabel]))
	}
	return l
}

// tabLabel is the first name of the tab of a new agent: "<folder> · <agent>",
// such as "app · pi", or the agent alone when the folder has no name.
func tabLabel(dir, agent string) string {
	suffix := " · " + agent
	f := cleanLabel(folderBase(dir))
	if f == "" {
		return agent
	}
	if r := []rune(f); len(r)+len([]rune(suffix)) > maxLabel {
		f = strings.TrimSpace(string(r[:maxLabel-len([]rune(suffix))]))
	}
	return f + suffix
}

// findSession returns the first Rex session with this name, ignoring case
// and the spaces around it.
func findSession(sessions []rex.Session, label string) (rex.Session, bool) {
	for _, s := range sessions {
		if strings.EqualFold(strings.TrimSpace(s.Label), label) {
			return s, true
		}
	}
	return rex.Session{}, false
}

// newQuestion is the text and the buttons of a /new question. The buttons
// carry the request's token and, for a folder, its index: callback data
// holds at most 64 bytes, so never a path.
func newQuestion(token string, f newFlow, tilde func(string) string) (string, *telegram.InlineKeyboardMarkup) {
	cancel := []telegram.InlineKeyboardButton{{Text: "✖ Cancel", CallbackData: "nw:" + token + ":x"}}
	if f.step == newPickAgent {
		text := "🆕 <b>New agent</b>\nWhich coding agent?"
		if f.linked {
			text += "\nTo send /new to the agent of this topic, send //new."
		}
		// Up to three buttons fit in a row on a phone; more go two by two.
		per := len(f.offered)
		switch {
		case per > 3:
			per = 2
		case per == 0:
			per = 1
		}
		var rows [][]telegram.InlineKeyboardButton
		for i, a := range f.offered {
			if i%per == 0 {
				rows = append(rows, nil)
			}
			rows[len(rows)-1] = append(rows[len(rows)-1], telegram.InlineKeyboardButton{Text: a.name(), CallbackData: "nw:" + token + ":a:" + a.key})
		}
		return text, &telegram.InlineKeyboardMarkup{InlineKeyboard: append(rows, cancel)}
	}
	text := "🆕 <b>New " + format.Escape(f.agent.name()) + "</b>\nWhich folder? "
	if len(f.folders) > 0 {
		text += "Tap one, or send a path, such as " + newExample + "."
	} else {
		text += "Send a path, such as " + newExample + "."
	}
	var rows [][]telegram.InlineKeyboardButton
	for i, dir := range f.folders {
		rows = append(rows, []telegram.InlineKeyboardButton{{
			Text: format.Redact(format.Truncate(tilde(dir), maxShownPath)), CallbackData: fmt.Sprintf("nw:%s:d:%d", token, i),
		}})
	}
	rows = append(rows, cancel)
	return text, &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// shownPath is a folder as messages show it: from ~, redacted, escaped, and
// cut to maxShownPath characters.
func (d *daemon) shownPath(dir string) string {
	return "<code>" + esc(format.Truncate(d.tildePath(dir), maxShownPath)) + "</code>"
}

// newFolders returns the folders to offer: the pinned ones (rex.new_folders)
// first, then the folder of this topic's agent, then the folders of recent
// sessions. Only folders that still exist count. It reads the disk, so it
// runs in a worker.
func (d *daemon) newFolders(chat, thread int64) []string {
	var dirs []string
	seen := map[string]bool{}
	add := func(dir string) {
		if dir != "" && !seen[dir] && len(dirs) < maxNewFolders && isDir(dir) {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	for _, dir := range d.cfg.Rex.NewFolders {
		add(dir)
	}
	if thread != 0 {
		if th, ok, err := d.st.ThreadByTopic(d.ctx, chat, thread); err == nil && ok && th.Status != store.ThreadClosed {
			if conv, err := d.st.Conversation(d.ctx, th.CurrentConversationID); err == nil {
				add(conv.CWD)
			}
		}
	}
	recent, err := d.st.RecentFolders(d.ctx, 4*maxNewFolders)
	if err != nil {
		d.log.Warn("new: recent folders", "err", err)
	}
	for _, dir := range recent {
		add(dir)
	}
	return dirs
}

// newCommand runs /new [agent [folder]] (core). linked tells that it came
// from a topic that is linked to an agent.
func (d *daemon) newCommand(chat, thread int64, linked bool, args string) {
	if d.paused.Load() {
		d.say(chat, thread, newPausedText, nil)
		return
	}
	list := d.newAgents()
	a, picked, folder, bad := parseNewArgs(args, list)
	if bad != "" {
		d.say(chat, thread, "There is no agent named <code>"+esc(format.Truncate(bad, 40))+"</code>. Use /new, or /new "+agentKeys(list)+" [folder].", nil)
		return
	}
	d.newSeq++
	f := &newFlow{seq: d.newSeq, chat: chat, thread: thread, linked: linked, step: newPickAgent, offered: list, expires: d.now().Add(newExpiry)}
	token := newToken()
	if picked && folder != "" {
		if dir, ok := d.checkFolder(chat, thread, folder); ok {
			f.step, f.agent = newStarting, a
			d.newFlows[token] = f
			d.spawn(func() { d.launch(token, 0, chat, thread, a, dir) })
		}
		return
	}
	if picked {
		f.step, f.agent = newPickFolder, a
	}
	d.newFlows[token] = f
	q := *f
	d.spawn(func() { d.ask(token, q) })
}

// ask posts a request's question, or puts it in the question's message when
// that is known (worker). Before a folder question it lists the folders and
// hands them to the core, which then has them before any button shows.
func (d *daemon) ask(token string, f newFlow) {
	if f.step == newPickFolder {
		f.folders = d.newFolders(f.chat, f.thread)
		d.emit(evNewFolders{token: token, folders: f.folders})
	}
	text, markup := newQuestion(token, f, d.tildePath)
	if f.msg != 0 {
		d.editMessage(f.chat, f.msg, text, markup)
		return
	}
	if m, err := d.sendNow(f.chat, f.thread, text, markup); err == nil {
		d.emit(evNewSent{token: token, msg: m.MessageID})
	}
}

// checkFolder reads a typed folder (core). When it is not an existing
// folder, it says so in the chat and returns false.
func (d *daemon) checkFolder(chat, thread int64, text string) (string, bool) {
	dir, err := folderPath(text, d.home)
	if err != nil {
		d.say(chat, thread, "That is not a folder ("+format.Escape(err.Error())+"). Send a path, such as "+newExample+".", nil)
		return "", false
	}
	if !isDir(dir) {
		d.say(chat, thread, "There is no folder "+d.shownPath(dir)+". Send another path.", nil)
		return "", false
	}
	return dir, true
}

// newButton handles a button of a /new question (core): "nw:<token>:a:<agent>"
// picks the agent, "nw:<token>:d:<index>" the folder, "nw:<token>:x" cancels.
func (d *daemon) newButton(cb *telegram.CallbackQuery, parts []string) {
	chat, msg := cb.Message.Chat.ID, cb.Message.MessageID
	var f *newFlow
	token := ""
	if len(parts) >= 2 {
		token = parts[0]
		f = d.newFlows[token]
	}
	if f == nil || f.chat != chat || (f.step < newStarting && d.now().After(f.expires)) {
		// Expired, cancelled, started, or forgotten by a restart. Only the
		// buttons go: the text may say that the agent started.
		d.answerCallback(cb.ID, "This request expired. Send /new again.", false)
		d.removeButtons(chat, msg)
		return
	}
	kind := parts[1]
	if kind == "o" { // an answer to the agent's startup question
		if f.step == newWaiting && len(parts) == 4 {
			seq, err1 := strconv.Atoi(parts[2])
			idx, err2 := strconv.Atoi(parts[3])
			if err1 == nil && err2 == nil {
				select {
				case f.answers <- startupAnswer{cbID: cb.ID, msg: msg, seq: seq, index: idx}:
				default:
					d.answerCallback(cb.ID, "One moment: the last answer is still being pressed.", false)
				}
				return
			}
		}
		d.answerCallback(cb.ID, "This question is no longer open.", false)
		d.removeButtons(chat, msg)
		return
	}
	if f.msg == 0 {
		f.msg = msg // Telegram's answer to the send was lost; the button names the message
	}
	if f.step >= newStarting {
		d.answerCallback(cb.ID, "The agent is starting already.", false)
		return
	}
	switch {
	case kind == "x" && len(parts) == 2:
		delete(d.newFlows, token)
		d.answerCallback(cb.ID, "Cancelled", false)
		d.editAsync(chat, msg, "✖ Cancelled. No agent was started.", nil)
		return
	case kind == "a" && len(parts) == 3 && f.step == newPickAgent:
		if a, ok := findAgent(f.offered, parts[2]); ok {
			f.agent, f.step, f.folders = a, newPickFolder, nil
			f.expires = d.now().Add(newExpiry)
			d.answerCallback(cb.ID, "", false)
			q := *f
			d.spawn(func() { d.ask(token, q) })
			return
		}
	case kind == "d" && len(parts) == 3 && f.step == newPickFolder:
		if i, err := strconv.Atoi(parts[2]); err == nil && i >= 0 && i < len(f.folders) {
			d.answerCallback(cb.ID, "", false)
			d.startFlow(token, f, f.folders[i])
			return
		}
	}
	d.answerCallback(cb.ID, "That button is old.", false)
}

// newFolderText takes a typed folder for a /new question that waits for one
// in this chat and topic (core). A reply to the question always counts. In a
// topic that is linked to an agent, other text counts only when it is a path
// (see linkedFolderText), so that messages for the agent still reach it. A
// bot command stays a command. It reports whether it took the message.
func (d *daemon) newFolderText(u telegram.Update, m *telegram.Message, linked bool) bool {
	if len(d.newFlows) == 0 {
		return false
	}
	text := strings.TrimSpace(m.Text)
	if r := inbound.RouteText(text, linked, d.botUser); r.Kind == inbound.RouteCommand && r.Command != inbound.CmdUnknown {
		return false
	}
	now := d.now()
	waiting := func(g *newFlow) bool {
		return g.chat == m.Chat.ID && g.step == newPickFolder && !now.After(g.expires)
	}
	var token string
	var f *newFlow
	if rm := m.ReplyToMessage; rm != nil {
		for t, g := range d.newFlows {
			if g.msg != 0 && g.msg == rm.MessageID && waiting(g) {
				token, f = t, g
			}
		}
	}
	if f == nil {
		if linked && !linkedFolderText(text, d.home) {
			return false
		}
		thread := int64(0)
		if m.IsTopicMessage {
			thread = m.MessageThreadID
		}
		for t, g := range d.newFlows { // the newest question in this topic
			if waiting(g) && g.thread == thread && (f == nil || g.seq > f.seq) {
				token, f = t, g
			}
		}
	}
	if f == nil {
		return false
	}
	if !d.recorded(u, m, "command", m.Text, 0, store.InboundDone) {
		return true // a repeat, or not stored: onUpdates deals with it
	}
	if dir, ok := d.checkFolder(f.chat, f.thread, text); ok {
		d.startFlow(token, f, dir)
	}
	return true
}

// startFlow starts the agent of a request in a worker (core).
func (d *daemon) startFlow(token string, f *newFlow, dir string) {
	if d.paused.Load() {
		delete(d.newFlows, token)
		if f.msg != 0 {
			d.editAsync(f.chat, f.msg, newPausedText, nil)
		} else {
			d.say(f.chat, f.thread, newPausedText, nil)
		}
		return
	}
	f.step = newStarting
	msg, chat, thread, a := f.msg, f.chat, f.thread, f.agent
	d.spawn(func() { d.launch(token, msg, chat, thread, a, dir) })
}

// expireNewFlows ends the /new questions that waited too long (core).
func (d *daemon) expireNewFlows(now time.Time) {
	for token, f := range d.newFlows {
		if f.step < newStarting && now.After(f.expires) {
			delete(d.newFlows, token)
			if f.msg != 0 {
				d.editAsync(f.chat, f.msg, "⌛ This /new request expired. Send /new again.", nil)
			}
		}
	}
}

// launch starts the agent in a new Rex tab, then waits for it (worker). msg
// is the message to show the progress in; 0 sends a new one. token names the
// request ("" for /new with all its arguments).
func (d *daemon) launch(token string, msg, chat, thread int64, a newAgent, dir string) {
	defer d.emit(evNewDone{token})
	show := func(html string) {
		if msg == 0 {
			if m, err := d.sendNow(chat, thread, html, nil); err == nil {
				msg = m.MessageID
			}
			return
		}
		d.editMessage(chat, msg, html, nil)
	}
	name := format.Escape(a.name())
	where := d.shownPath(dir)
	if !isDir(dir) {
		show("⚠️ The folder " + where + " is gone, so " + name + " was not started. Send /new again.")
		return
	}
	show("⏳ Starting " + name + " in " + where + "…")

	st, in, err := d.startTab(a, dir)
	if errors.Is(err, errNoRex) {
		show("⚠️ Could not reach Rex, so " + name + " was not started.")
		return
	}
	if err != nil {
		d.log.Warn("new: start agent", "agent", a.key, "err", err)
		if errors.Is(err, context.DeadlineExceeded) {
			show("⚠️ Rex did not answer in time. " + name + " may have started anyway: check /agents before you try again.")
		} else {
			show("⚠️ Could not start " + name + ": " + esc(format.Truncate(err.Error(), 300)))
		}
		return
	}
	d.log.Info("new: agent started", "agent", a.key, "session", st.SessionID, "block", st.BlockID)
	show("✅ Started " + name + " in " + where + ", in " + in + ".\nIts topic opens when " + name + " reports its session.")
	answers := make(chan startupAnswer, 1)
	d.emit(evNewWaiting{token: token, answers: answers})
	d.awaitAgent(chat, thread, a, st, token, answers)
}

// errNoRex: Rex did not list its sessions, so nothing was started.
var errNoRex = errors.New("rex sessions not listed")

// startTab opens a tab that runs the agent, in the Rex session for /new
// (rex.new_session), or in a new session of that name (worker). in says
// where, as HTML. Starts run one at a time, so two starts make one session.
func (d *daemon) startTab(a newAgent, dir string) (st rex.Started, in string, err error) {
	d.newMu.Lock()
	defer d.newMu.Unlock()
	sessions, err := d.rx.Sessions(d.ctx)
	if err != nil {
		d.log.Warn("new: list sessions", "err", err)
		return rex.Started{}, "", errNoRex
	}
	label := strings.TrimSpace(d.cfg.Rex.NewSession)
	tab := rex.Tab{Label: tabLabel(dir, a.key), CWD: dir, Command: a.command}
	if s, ok := d.ownSession(sessions); ok {
		// Found by its ID when an agent renamed it; the name check names it
		// back (keepSessionName).
		st, err = d.rx.NewWindow(d.ctx, s.ID, tab)
		return st, "a new tab <b>" + esc(tab.Label) + "</b> of the Rex session <b>" + esc(label) + "</b>", err
	}
	st, err = d.rx.NewSession(d.ctx, label, tab)
	if err == nil {
		d.keepSessionID(st.SessionID)
	}
	return st, "the tab <b>" + esc(tab.Label) + "</b> of the new Rex session <b>" + esc(label) + "</b>", err
}

// awaitAgent waits until the new agent reports its session, then has the
// core open its topic (worker). It stops when the agent exits, or after
// Timing.NewAgentWait, and then shows the end of the pane's screen. A
// startup question on the screen goes to the chat with its answers as
// buttons (see startup.go), and keeps the wait going, up to newExpiry. The
// presses come through answers.
func (d *daemon) awaitAgent(chat, thread int64, a newAgent, st rex.Started, token string, answers <-chan startupAnswer) {
	name, rt := format.Escape(a.name()), a.rt
	start := time.Now()
	deadline := start.Add(d.timing.NewAgentWait)
	poll := time.NewTicker(d.timing.NewAgentPoll)
	defer poll.Stop()
	var q *shownQuestion
	seq, seen := 0, ""
	defer func() { d.closeQuestion(chat, q, "") }()
	for {
		select {
		case <-d.ctx.Done():
			return
		case a := <-answers:
			if d.onStartupAnswer(chat, st, rt, name, q, a) {
				// The agent goes on starting: give it the whole wait again.
				deadline = minTime(time.Now().Add(d.timing.NewAgentWait), start.Add(newExpiry))
			}
			continue
		case <-poll.C:
		}
		if id, ok := d.agentPane(st.BlockID); ok {
			d.emit(evNewReady{paneID: id, thread: thread})
			return
		}
		p, err := d.rx.Process(d.ctx, st.SessionID, st.BlockID)
		switch {
		case rex.IsGone(err):
			_, _ = d.sendNow(chat, thread, "⚠️ "+name+" stopped, and its tab closed.", nil)
			return
		case err == nil && p.Foreground.PID == 0 && p.LastExit != nil:
			d.sayScreen(chat, thread, st, fmt.Sprintf("⚠️ %s exited with code %d.", name, p.LastExit.Code))
			return
		}
		m, ok, err := d.readStartupMenu(d.ctx, st)
		now := time.Now()
		switch {
		case err != nil: // keep what is shown; the next read decides
		case ok:
			// While a question waits for you, so does rex-tg, up to newExpiry.
			if until := now.Add(d.timing.NewAgentWait); until.After(deadline) {
				deadline = minTime(until, start.Add(newExpiry))
			}
			// A list counts once it shows on two reads in a row, so a frame
			// drawn halfway is never posted.
			if id := m.id(); (q == nil || q.id != id) && seen == id {
				d.closeQuestion(chat, q, "")
				seq++
				q = d.postQuestion(chat, thread, token, name, m, seq)
			}
			seen = m.id()
		default:
			seen = ""
			if q != nil {
				d.closeQuestion(chat, q, "")
				q = nil
			}
		}
		if now.After(deadline) {
			d.sayScreen(chat, thread, st, "⏳ "+name+" has not reported its session yet, so it has no topic. When it is ready, open it with /agents.")
			return
		}
	}
}

// dropAnswers answers the button presses that reached a request after its
// worker stopped waiting (core). Nothing reads them any more, and Telegram
// shows a press as pending until it is answered.
func (d *daemon) dropAnswers(f *newFlow) {
	for f.answers != nil {
		select {
		case a := <-f.answers:
			d.answerCallback(a.cbID, "This question is no longer open.", false)
			d.removeButtons(f.chat, a.msg)
		default:
			return
		}
	}
}

// minTime returns the earlier of two times.
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// shownQuestion is a startup question that awaitAgent posted.
type shownQuestion struct {
	id       string
	seq      int
	labels   []string
	html     string // the question's text, without the outcome
	markup   *telegram.InlineKeyboardMarkup
	msg      int64 // its message; 0 when Telegram's answer to the send was lost
	answered bool
	closed   bool // its buttons are gone
}

// postQuestion posts a startup question with a button for each answer
// (worker). It is posted once, also when the send fails: a send whose
// outcome is unknown is never repeated.
func (d *daemon) postQuestion(chat, thread int64, token, name string, m startupMenu, seq int) *shownQuestion {
	html := "❓ <b>" + name + " asks this before it starts:</b>"
	if len(m.question) > 0 {
		html += "\n" + preBlock(m.question, 1500)
	}
	var rows [][]telegram.InlineKeyboardButton
	for i, l := range m.labels {
		rows = append(rows, []telegram.InlineKeyboardButton{{
			Text: format.Redact(format.Truncate(l, 60)), CallbackData: fmt.Sprintf("nw:%s:o:%d:%d", token, seq, i),
		}})
	}
	q := &shownQuestion{id: m.id(), seq: seq, labels: m.labels, html: html, markup: &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}}
	if sent, err := d.sendNow(chat, thread, html+"\nTap an answer. rex-tg moves the cursor there and presses Enter.", q.markup); err == nil {
		q.msg = sent.MessageID
	}
	return q
}

// closeQuestion removes the buttons of a question that is still open, with
// a note (worker). A question that was answered here keeps its text.
func (d *daemon) closeQuestion(chat int64, q *shownQuestion, note string) {
	if q == nil || q.closed || q.msg == 0 {
		return
	}
	q.closed = true
	if q.answered {
		return
	}
	if note == "" {
		note = "This question is no longer on the screen."
	}
	d.editMessage(chat, q.msg, q.html+"\n"+note, nil)
}

// onStartupAnswer presses the answer of a button, if it belongs to the
// question on the screen (worker). It reports whether the answer was pressed.
func (d *daemon) onStartupAnswer(chat int64, st rex.Started, rt agents.Runtime, name string, q *shownQuestion, a startupAnswer) bool {
	if q == nil || q.closed || a.seq != q.seq || a.index < 0 || a.index >= len(q.labels) {
		d.answerCallback(a.cbID, "This question is no longer open.", false)
		d.removeButtons(chat, a.msg)
		return false
	}
	if q.msg == 0 {
		q.msg = a.msg // Telegram's answer to the send was lost; the button names the message
	}
	d.answerCallback(a.cbID, "", false)
	label := esc(q.labels[a.index])
	keep := false // keep the buttons, so you can tap again
	var note string
	switch err := d.answerStartup(d.ctx, st, rt, q.id, a.index); {
	case err == nil:
		q.answered = true
		note = "✅ Answered: " + label
	case errors.Is(err, errPausedBefore):
		note, keep = "⏸ Typing is paused, so nothing was pressed. Use /unpause, then tap again.", true
	case errors.Is(err, errRetryBefore):
		note, keep = "⚠️ Could not reach Rex, so the answer may be incomplete. Check the screen, then tap again.", true
	case errors.Is(err, errGoneBefore):
		note = "⛔ Not sent: " + format.Escape(inbound.ReasonAgentNotFront) + "."
	case errors.Is(err, errStartupStuck):
		note = "⚠️ " + name + " did not react to a key, so nothing more was pressed. Answer it on the Mac."
	case errors.Is(err, errStartupGone):
		note = "This question is no longer on the screen."
	default: // the daemon stops
		return false
	}
	if keep {
		d.editMessage(chat, q.msg, q.html+"\n"+note, q.markup)
		return false
	}
	q.closed = true
	d.editMessage(chat, q.msg, q.html+"\n"+note, nil)
	return q.answered
}

// agentPane returns the pane row of a Rex block once its agent has a session.
func (d *daemon) agentPane(block string) (int64, bool) {
	views, err := d.reg.Agents(d.ctx)
	if err != nil {
		return 0, false
	}
	for _, v := range views {
		if v.Pane.Block == block && v.Conversation != nil {
			return v.Pane.ID, true
		}
	}
	return 0, false
}

// sayScreen sends html with the last lines of the pane's screen (worker).
func (d *daemon) sayScreen(chat, thread int64, st rex.Started, html string) {
	if screen, err := d.rx.Capture(d.ctx, st.SessionID, st.BlockID); err == nil {
		if lines := lastLines(screen, 12); len(lines) > 0 {
			html += "\nThe pane shows:\n" + preBlock(lines, 1500)
		}
	}
	_, _ = d.sendNow(chat, thread, html, nil)
}

// openNewTopic opens the topic of the agent that /new started (core). Only a
// failure is reported: the topic link itself is the answer.
func (d *daemon) openNewTopic(paneID, thread int64) {
	chat := d.ownerChat
	d.openTopic(paneID, thread, func(text string, alert bool) {
		if alert && text != "" {
			d.say(chat, thread, format.Escape(text), nil)
		}
	})
}

// editMessage replaces the text and buttons of a bot message (worker). A nil
// markup removes the buttons.
func (d *daemon) editMessage(chat, msg int64, html string, markup *telegram.InlineKeyboardMarkup) {
	err := d.snd.Call(sender.PriorityAnswer, func(ctx context.Context, tg *telegram.Client) error {
		_, err := tg.EditMessageText(ctx, telegram.EditMessageTextParams{
			ChatID: chat, MessageID: msg, Text: html, ParseMode: "HTML", ReplyMarkup: markup,
		})
		if telegram.Classify(err) == telegram.ClassNotModified {
			return nil
		}
		return err
	})
	if err != nil && d.ctx.Err() == nil {
		d.log.Warn("edit message", "chat", chat, "message", msg, "err", err)
	}
}

// editAsync is editMessage in a new worker, for the core.
func (d *daemon) editAsync(chat, msg int64, html string, markup *telegram.InlineKeyboardMarkup) {
	d.spawn(func() { d.editMessage(chat, msg, html, markup) })
}
