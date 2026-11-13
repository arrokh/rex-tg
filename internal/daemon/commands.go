package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/format"
	"rex-tg/internal/inbound"
	"rex-tg/internal/registry"
	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
	"rex-tg/internal/termimg"
)

const (
	mainHelp = "<b>Main chat</b>\n" +
		"/agents - agents in Rex panes, with buttons to open a topic\n" +
		"/new - start a coding agent in a new Rex tab\n" +
		"/topics - topics and their states\n" +
		"/status - daemon health\n" +
		"/pause - stop typing into panes\n" +
		"/unpause - allow typing again\n" +
		"/help - this list"
	topicHelp = "<b>In a topic</b>\n" +
		"Text goes to the agent. Text that starts with / goes to the agent too, except the commands below. Use //name to send /name.\n" +
		"/info - session, pane, and state\n" +
		"/screen [lines] - the pane screen as an image; /screen text sends text\n" +
		"/files - browse the agent's folder in the Mini App\n" +
		"/approve, /deny - answer the open prompt\n" +
		"/esc, /interrupt - press Escape, or stop the turn\n" +
		"/key &lt;keys&gt; - press keys\n" +
		"/force &lt;text&gt; - type text now, even at a prompt\n" +
		"/model [name], /effort [level] - show or change the agent's model and thinking effort\n" +
		"/mode progress|block|final\n" +
		"/verbose on|full|off\n" +
		"/reasoning on|off\n" +
		"/resend [id] - send again a reply that may not have arrived (the newest one without an id)\n" +
		"/mute, /unmute - answers without or with sound\n" +
		"/at &lt;time|delay&gt; &lt;text&gt; - send text to the agent later, at 09:00 or in 30m; /at lists, /at cancel &lt;n&gt; removes one\n" +
		"/rename &lt;name&gt; - rename this topic (and its Rex tab); /rename auto names it after the session again\n" +
		"/unlink - stop mirroring this session\n" +
		"/help - this list"
)

// mainCommand runs a command of the main chat. In a topic, the same commands
// answer in that topic (thread is its message_thread_id).
func (d *daemon) mainCommand(r inbound.Route, chat, thread int64, inTopic bool) {
	switch r.Command {
	case "start":
		d.say(chat, thread, "Already paired. /help lists the commands.", nil)
	case "agents":
		mac := d.macThreads()
		d.spawn(func() { d.sendAgents(chat, thread, mac) })
	case "new":
		d.newCommand(chat, thread, inTopic, r.Args)
	case "topics":
		d.say(chat, thread, d.topicsText(), nil)
	case "status":
		d.spawn(func() { d.sendNow(chat, thread, d.statusText(), nil) })
	case "pause":
		d.setPaused(true)
		d.say(chat, thread, "⏸ Typing is paused. Nothing is typed into panes until /unpause.", nil)
	case "unpause":
		d.setPaused(false)
		d.say(chat, thread, "▶️ Typing is on again.", nil)
	case "help":
		text := mainHelp
		if inTopic {
			text = topicHelp + "\n\n" + mainHelp
		}
		d.say(chat, thread, text, nil)
	case inbound.CmdTopicOnly:
		d.say(chat, thread, "Use this inside a topic.", nil)
	default:
		d.say(chat, thread, "Unknown command. /help lists the commands.", nil)
	}
}

func (d *daemon) setPaused(v bool) {
	d.paused.Store(v)
	val := "0"
	if v {
		val = "1"
	}
	if err := d.st.Set(d.ctx, "paused", val); err != nil {
		d.log.Warn("save paused", "err", err)
	}
}

// --- /agents ---

// stateRank orders the agents of /agents: the ones that wait for you first,
// then working, idle, and the rest.
func stateRank(state string) int {
	switch state {
	case "blocked":
		return 0
	case "working":
		return 1
	case "idle", "done":
		return 2
	}
	return 3
}

func stateEmoji(state string) (string, string) {
	switch state {
	case "working":
		return "🟢", "working"
	case "blocked":
		return "🟡", "waiting for you"
	case "error":
		return "🔴", "error"
	case "idle", "done":
		return "⚪", "idle"
	}
	return "⚪", "state unknown"
}

// renderAgents builds the /agents text and buttons (design section 8.7): the
// agents that wait for you first, each with its state and how long it has
// been in it. mac holds the topics whose session is in use on the Mac. Each
// button reports the tap to the daemon, also for an agent that has a topic:
// Telegram does not report a topic that the user deleted, so openTopic checks
// it first and makes a new one when it is gone.
func (d *daemon) renderAgents(views []registry.AgentView, mac map[int64]bool, now time.Time) (string, *telegram.InlineKeyboardMarkup) {
	views = slices.Clone(views)
	slices.SortStableFunc(views, func(a, b registry.AgentView) int {
		return cmp.Compare(stateRank(a.Pane.State), stateRank(b.Pane.State))
	})
	var b strings.Builder
	if len(views) == 0 {
		b.WriteString("No agents are running in Rex panes.")
	} else {
		fmt.Fprintf(&b, "<b>Active agents (%d)</b>", len(views))
	}
	var rows [][]telegram.InlineKeyboardButton
	more := 0
	for i, v := range views {
		if len(rows) >= maxBoardAgents || b.Len() >= maxBoardText {
			more = len(views) - i // Telegram takes 4096 characters and 100 buttons
			break
		}
		emoji, state := stateEmoji(v.Pane.State)
		if since := agoText(now, v.Pane.StateAt); since != "" && v.Conversation != nil {
			state += " · " + since
		}
		if v.Thread != nil && mac[v.Thread.ID] {
			state += " · 💻 on the Mac"
		}
		var cwd, title string
		if v.Conversation != nil {
			cwd, title = v.Conversation.CWD, v.Conversation.Title
		}
		switch {
		case v.Conversation == nil:
			state = "waiting for session"
		case v.Resolution == store.SourceGuess:
			state += " (guess)"
		}
		name := topicName(v.Pane.Label, v.Runtime, cwd, title)
		fmt.Fprintf(&b, "\n%s <b>%s</b> · %s\n   %s", emoji, esc(noAutoLink(name)), state, agentHTML(v.Runtime, cwd))
		if paneName(v.Pane.Label) != "" && title != "" {
			fmt.Fprintf(&b, " · \"%s\"", format.Escape(noAutoLink(format.Truncate(format.Redact(title), 50))))
		}
		text := "Open " + name
		if v.Thread != nil && v.Thread.Status != store.ThreadClosed {
			text = "↗ " + name
		}
		rows = append(rows, []telegram.InlineKeyboardButton{{Text: text, CallbackData: fmt.Sprintf("o:%d", v.Pane.ID)}})
	}
	if more > 0 {
		fmt.Fprintf(&b, "\n… and %d more (the ones that wait for you come first).", more)
	}
	if len(views) > 0 {
		b.WriteString("\n\n<i>This list updates by itself.</i>")
	}
	rows = append(rows, []telegram.InlineKeyboardButton{{Text: "↻ Refresh", CallbackData: "ag:r"}})
	return b.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// The list stays within Telegram's limits (4096 characters, 100 buttons): at
// most maxBoardAgents agents, and no new one after maxBoardText characters.
const (
	maxBoardAgents = 20
	maxBoardText   = 3500
)

// agoText is a short, coarse age, such as "3m" or "2h". It changes seldom,
// so the live board needs few edits.
func agoText(now, at time.Time) string {
	if at.IsZero() || at.After(now) {
		return ""
	}
	switch d := now.Sub(at); {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}

func (d *daemon) agentsView(mac map[int64]bool) (string, *telegram.InlineKeyboardMarkup, error) {
	ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
	defer cancel()
	if err := d.scanAndWait(ctx); err != nil && d.ctx.Err() == nil {
		d.log.Warn("agents: scan", "err", err)
	}
	views, err := d.reg.Agents(d.ctx)
	if err != nil {
		return "", nil, err
	}
	text, markup := d.renderAgents(views, mac, d.now())
	return text, markup, nil
}

// sendAgents posts the /agents list (worker). It becomes the live board.
func (d *daemon) sendAgents(chat, thread int64, mac map[int64]bool) {
	text, markup, err := d.agentsView(mac)
	if err != nil {
		d.log.Warn("agents", "err", err)
		d.sendNow(chat, thread, "Could not list the agents.", nil)
		return
	}
	if m, err := d.sendNow(chat, thread, text, markup); err == nil && m.MessageID != 0 {
		d.emit(evBoard{chat: chat, msg: m.MessageID, html: text})
	}
}

// refreshAgents is "↻ Refresh" (worker). The refreshed list becomes the live
// board.
func (d *daemon) refreshAgents(chat, msg int64, mac map[int64]bool) {
	text, markup, err := d.agentsView(mac)
	if err != nil {
		return
	}
	err = d.snd.Call(sender.PriorityEdit, func(ctx context.Context, tg *telegram.Client) error {
		_, err := tg.EditMessageText(ctx, telegram.EditMessageTextParams{
			ChatID: chat, MessageID: msg, Text: text, ParseMode: "HTML", ReplyMarkup: markup,
		})
		if telegram.Classify(err) == telegram.ClassNotModified {
			return nil
		}
		return err
	})
	if err != nil {
		if d.ctx.Err() == nil {
			d.log.Debug("refresh agents", "err", err)
		}
		return
	}
	d.emit(evBoard{chat: chat, msg: msg, html: text})
}

// --- /topics and /status ---

func (d *daemon) topicsText() string {
	ths, err := d.st.Threads(d.ctx)
	if err != nil {
		return "Could not list the topics."
	}
	var b strings.Builder
	n := 0
	for _, th := range ths {
		if th.Status == store.ThreadClosed {
			continue
		}
		n++
		emoji := "🟢"
		name := th.Name
		if th.Status == store.ThreadOffline {
			emoji = "🔴"
			name = strings.TrimPrefix(name, offlineMark) // show the dot once
		}
		mute := ""
		if th.Muted {
			mute = " 🔕"
		}
		fmt.Fprintf(&b, "\n%s %s · %s%s", emoji, esc(noAutoLink(name)), th.Status, mute)
	}
	if n == 0 {
		return "No topics yet. Use /agents to open one."
	}
	return fmt.Sprintf("<b>Topics (%d)</b>%s", n, b.String())
}

func (d *daemon) statusText() string {
	ctx := d.ctx
	live, offline, queued := 0, 0, 0
	if ths, err := d.st.Threads(d.ctx); err == nil {
		for _, th := range ths {
			switch th.Status {
			case store.ThreadLive:
				live++
			case store.ThreadOffline:
				offline++
			}
			if th.Status != store.ThreadClosed {
				if qs, err := d.st.QueuedInbound(ctx, th.ID); err == nil {
					queued += len(qs)
				}
			}
		}
	}
	rexLine := "unreachable"
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if w, err := d.rx.Whoami(rctx); err == nil {
		rexLine = "ok (" + w.RexVersion + ")"
	}
	typing := "on"
	if d.paused.Load() {
		typing = "paused"
	}
	return fmt.Sprintf("<b>rex-tg is running</b>\nTopics: %d live, %d offline\nQueued messages: %d\nTyping: %s\nRex: %s",
		live, offline, queued, typing, format.Escape(rexLine))
}

// --- topic commands ---

func (d *daemon) topicCommand(u telegram.Update, m *telegram.Message, th store.Thread, r inbound.Route) {
	upd := u.UpdateID
	reply := func(tag, html string) { d.replyTopic(th, fmt.Sprintf("%d:%s", upd, tag), html) }
	switch r.Command {
	case "info":
		reply("info", d.infoText(th))
	case "screen":
		d.screenCommand(th, upd, r.Args)
	case "files":
		d.filesCommand(th, upd)
	case "approve", "deny":
		d.spawn(func() { d.approveDeny(th, upd, r.Command == "approve") })
	case "esc":
		d.keysCommand(th, upd, []string{"escape"}, false)
	case "interrupt":
		keys, piVim := []string{"escape"}, false
		if conv, err := d.st.Conversation(d.ctx, th.CurrentConversationID); err == nil {
			if k := d.cfg.Input(conv.Runtime).InterruptKeys; len(k) > 0 {
				keys = k
			}
			piVim = conv.Runtime == agents.Pi
		}
		d.keysCommand(th, upd, keys, piVim)
	case "key":
		keys := strings.Fields(r.Args)
		for _, k := range keys {
			if !keyNameRe.MatchString(k) {
				reply("key", "Key names use letters, digits, + - _ only, for example ctrl+c or down.")
				return
			}
		}
		if len(keys) == 0 || len(keys) > 10 {
			reply("key", "Usage: /key &lt;keys&gt;, for example /key ctrl+c or /key down enter. At most 10 keys.")
			return
		}
		d.keysCommand(th, upd, keys, false)
	case "force":
		if r.Args == "" {
			reply("force", "Usage: /force &lt;text&gt;")
			return
		}
		d.queueText(th.ID, upd, m.MessageID, r.Args, "text", inbound.ActForce)
	case "model", "effort":
		d.controlCommand(th, upd, m.MessageID, r.Command, r.Args)
	case "mode":
		d.setting(th, upd, "mode", r.Args, []string{store.ModeProgress, store.ModeBlock, store.ModeFinal})
	case "verbose":
		d.setting(th, upd, "verbose", r.Args, []string{store.VerboseOff, store.VerboseOn, store.VerboseFull})
	case "reasoning":
		d.setting(th, upd, "reasoning", r.Args, []string{"on", "off"})
	case "resend":
		d.resend(th, upd, r.Args)
	case "unlink":
		d.unlink(th)
	case "at":
		d.atCommand(th, r.Args, func(html string) { reply("at", html) })
	case "rename":
		d.renameCommand(th, r.Args, func(html string) { reply("rename", html) })
	case "mute", "unmute":
		d.setMuted(th, r.Command == "mute")
		if r.Command == "mute" {
			reply("mute", "🔕 Answers in this topic are sent without sound.")
		} else {
			reply("mute", "🔔 Answers in this topic make sound again.")
		}
	case "help":
		reply("help", topicHelp)
	default:
		d.mainCommand(r, th.ChatID, th.MessageThreadID, true)
	}
}

func (d *daemon) infoText(th store.Thread) string {
	ctx := d.ctx
	var b strings.Builder
	fmt.Fprintf(&b, "ℹ️ <b>%s</b>\nTopic: %s", esc(noAutoLink(th.Name)), th.Status)
	if conv, err := d.st.Conversation(ctx, th.CurrentConversationID); err == nil {
		title := conv.Title
		if title == "" {
			title = conv.NativeID
		}
		fmt.Fprintf(&b, "\nSession: %s (%s)", format.Escape(noAutoLink(format.Truncate(format.Redact(title), 100))), conv.Runtime)
		if conv.CWD != "" {
			fmt.Fprintf(&b, "\nFolder: <code>%s</code>", esc(d.tildePath(conv.CWD)))
		}
		if line := d.controlsLine(conv); line != "" {
			b.WriteString("\n" + line)
		}
	}
	if p, ok, err := d.st.PaneByConversation(ctx, th.CurrentConversationID); err == nil && ok {
		state := p.State
		if state == "" {
			state = "unknown"
		}
		fmt.Fprintf(&b, "\nPane: %s · state %s", esc(firstNonEmpty(p.Label, p.Block)), format.Escape(state))
		if !p.StateAt.IsZero() {
			fmt.Fprintf(&b, " since %s", p.StateAt.Local().Format("15:04:05"))
		}
	} else {
		b.WriteString("\nPane: none")
	}
	fmt.Fprintf(&b, "\nMode: %s · verbose %s · reasoning %s · muted %s", th.Mode, th.Verbose, onOff(th.Reasoning), onOff(th.Muted))
	if d.paused.Load() {
		b.WriteString("\nTyping: paused")
	}
	if qs, err := d.st.QueuedInbound(ctx, th.ID); err == nil && len(qs) > 0 {
		fmt.Fprintf(&b, "\nWaiting messages: %d", len(qs))
	}
	return b.String()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// screenCommand shows the pane screen, redacted, as an image. With the word
// "text", or when the image cannot be made or sent, it shows monospace text.
func (d *daemon) screenCommand(th store.Thread, upd int64, args string) {
	n, asText := 30, false
	for _, f := range strings.Fields(args) {
		if strings.EqualFold(f, "text") {
			asText = true
		} else if v, err := strconv.Atoi(f); err == nil && v > 0 {
			n = min(v, 80)
		}
	}
	d.spawn(func() {
		f, pane, err := d.baseFacts(d.ctx, th)
		tag := fmt.Sprintf("%d:screen", upd)
		if err != nil || !f.PaneExists {
			d.replyTopic(th, tag, "There is no pane for this topic now.")
			return
		}
		screen, err := d.rx.Capture(d.ctx, pane.Session, pane.Block)
		if err != nil {
			d.replyTopic(th, tag, "Could not read the screen: Rex is not reachable.")
			return
		}
		lines := lastLines(format.Redact(screen), n) // before cutting, so a multi-line secret is whole
		if len(lines) == 0 {
			d.replyTopic(th, tag, "The screen is empty.")
			return
		}
		if !asText && d.sendScreenImage(th, lines) {
			return
		}
		d.replyTopic(th, tag, preBlock(lines, 3800))
	})
}

// sendScreenImage sends the (already redacted) lines as a silent photo, once.
// It returns false when the text should be sent instead: the image could not
// be made, or Telegram did not take the photo (nothing was posted). Any other
// failure has an unknown outcome, so nothing is sent again; /screen can be
// asked again.
func (d *daemon) sendScreenImage(th store.Thread, lines []string) bool {
	data, err := termimg.Render(lines)
	if err != nil {
		d.log.Warn("screen image", "err", err)
		return false
	}
	caption := th.Name
	if caption == "" {
		caption = "Screen"
	}
	caption += " · " + d.now().Format("15:04:05")
	markup := d.liveScreenButton(th) // nil without the Mini App
	err = d.snd.Call(sender.PriorityAnswer, func(ctx context.Context, tg *telegram.Client) error {
		_, err := tg.SendPhotoMarkup(ctx, th.ChatID, th.MessageThreadID, "screen.png", data, caption, true, markup)
		return err
	})
	if err == nil {
		return true
	}
	if d.ctx.Err() != nil {
		return true
	}
	switch telegram.Classify(err) {
	case telegram.ClassBadRequest, telegram.ClassNotSent:
		d.log.Warn("screen photo not sent; sending text", "err", err)
		return false
	}
	d.log.Warn("screen photo: outcome unknown", "err", err)
	return true
}

// keysCommand presses keys in the pane after the checks of design section 9.5.
// With piVim (an /interrupt in a pi pane), pi-vim is first brought to plain
// NORMAL mode, where an Escape reaches pi: in the other modes pi-vim keeps
// the Escape for itself.
func (d *daemon) keysCommand(th store.Thread, upd int64, keys []string, piVim bool) {
	d.spawn(func() {
		tag := fmt.Sprintf("%d:keys", upd)
		f, pane, err := d.facts(d.ctx, th)
		if err != nil {
			d.replyTopic(th, tag, "Could not reach Rex.")
			return
		}
		if ok, reason := inbound.Check(inbound.ActKeys, f); !ok {
			d.replyTopic(th, tag, "⛔ Not sent: "+format.Escape(reason)+".")
			return
		}
		if piVim {
			pressed, err := d.settlePiVim(d.ctx, pane, vimNormal)
			if err == nil && pressed { // that took time: check again
				switch paused, front, cerr := d.safeNow(pane); {
				case cerr != nil:
					err = errRetryBefore
				case paused:
					err = errPausedBefore
				case !front:
					err = errGoneBefore
				}
			}
			switch {
			case errors.Is(err, errVimStuck):
				d.replyTopic(th, tag, "⚠️ The pi editor did not react to Escape, so nothing more was pressed. Check with /screen.")
				return
			case errors.Is(err, errPausedBefore):
				d.replyTopic(th, tag, "⛔ Not sent: "+format.Escape(inbound.ReasonPaused)+".")
				return
			case errors.Is(err, errGoneBefore):
				d.replyTopic(th, tag, "⛔ Not sent: "+format.Escape(inbound.ReasonAgentNotFront)+".")
				return
			case err != nil:
				d.replyTopic(th, tag, "Could not press the keys.")
				return
			}
		}
		if err := d.rx.SendKeys(d.ctx, pane.Session, pane.Block, keys...); err != nil {
			d.replyTopic(th, tag, "Could not press the keys.")
			return
		}
		d.replyTopic(th, tag, "⌨️ Sent: "+format.Escape(strings.Join(keys, " ")))
	})
}

// approveDeny presses the first (approve) or last (deny) answer of the open
// prompt, not counting Esc.
func (d *daemon) approveDeny(th store.Thread, upd int64, approve bool) {
	tag := fmt.Sprintf("%d:answer", upd)
	als, err := d.st.OpenAlerts(d.ctx, th.ID)
	if err != nil || len(als) == 0 {
		d.replyTopic(th, tag, "⛔ Not sent: "+format.Escape(inbound.ReasonNoPrompt)+".")
		return
	}
	al := als[len(als)-1]
	var idx []int
	for i, o := range al.Options {
		if len(o.Keys) == 1 && o.Keys[0] == "escape" {
			continue
		}
		idx = append(idx, i)
	}
	if len(idx) == 0 && approve {
		d.replyTopic(th, tag, "This prompt has no answer to press. Use /esc.")
		return
	}
	var pick int
	if approve {
		// The first answer that says yes, else the first one.
		pick = idx[0]
		for i, o := range al.Options {
			if answerWord(o.Label) == "yes" {
				pick = i
				break
			}
		}
	} else {
		// Never the last answer on a guess: on a menu without a No it may
		// be "Allow and don't ask me again" (Codex). denyAnswer refuses.
		var ok bool
		if pick, ok = denyAnswer(al); !ok {
			d.replyTopic(th, tag, "This prompt has no answer that refuses it. Use /esc.")
			return
		}
	}
	d.answerAlert(al.ID, pick, 0, func(text string, _ bool) { d.replyTopic(th, tag, format.Escape(text)) })
}

func (d *daemon) setting(th store.Thread, upd int64, name, arg string, allowed []string) {
	reply := func(html string) { d.replyTopic(th, fmt.Sprintf("%d:%s", upd, name), html) }
	arg = strings.ToLower(strings.TrimSpace(arg))
	cur := map[string]string{"mode": th.Mode, "verbose": th.Verbose, "reasoning": onOff(th.Reasoning)}[name]
	ok := false
	for _, a := range allowed {
		ok = ok || a == arg
	}
	if !ok {
		reply(fmt.Sprintf("/%s is %s. Use: %s", name, format.Escape(cur), format.Escape(strings.Join(allowed, ", "))))
		return
	}
	mode, verbose, reasoning := th.Mode, th.Verbose, th.Reasoning
	switch name {
	case "mode":
		mode = arg
	case "verbose":
		verbose = arg
	case "reasoning":
		reasoning = arg == "on"
	}
	if err := d.st.SetThreadSettings(d.ctx, th.ID, mode, verbose, reasoning, th.Muted); err != nil {
		d.log.Warn("save settings", "err", err)
		reply("Could not save the setting.")
		return
	}
	th.Mode, th.Verbose, th.Reasoning = mode, verbose, reasoning
	d.tracker(th).SetSettings(d.settings(th))
	reply(fmt.Sprintf("/%s is now %s.", name, format.Escape(arg)))
}

func (d *daemon) setMuted(th store.Thread, muted bool) {
	if err := d.st.SetThreadSettings(d.ctx, th.ID, th.Mode, th.Verbose, th.Reasoning, muted); err != nil {
		d.log.Warn("save muted", "err", err)
	}
}

// resend sends the parts of an outbox message that may not have arrived.
//
// Without an id it takes the newest reply of the topic that may not have
// arrived: tapping "/resend" in a message sends the command without its
// argument (seen live). The id of a "may not have arrived" notice stands for
// the reply that the notice is about.
func (d *daemon) resend(th store.Thread, upd int64, arg string) {
	reply := func(html string) { d.replyTopic(th, fmt.Sprintf("%d:resend", upd), html) }
	var id int64
	if arg = strings.TrimPrefix(strings.TrimSpace(arg), "#"); arg == "" {
		newest, ok, err := d.st.NewestUnknown(d.ctx, th.ID)
		switch {
		case err != nil:
			reply("Could not resend it.")
			return
		case !ok:
			reply("Nothing to resend: no reply in this topic may have been lost. Usage: /resend &lt;id&gt;")
			return
		}
		id = newest
	} else {
		n, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			reply("Usage: /resend [id]")
			return
		}
		id = n
	}
	row, err := d.st.OutboxRow(d.ctx, id)
	for hops := 0; err == nil && hops < 10; hops++ {
		target, ok := sender.NoticeTarget(row.Key)
		if !ok {
			break
		}
		id = target // a notice about a reply: send that reply
		row, err = d.st.OutboxRow(d.ctx, id)
	}
	if err != nil || row.ThreadID != th.ID {
		reply("There is no message " + escapef("#%d", id) + " in this topic.")
		return
	}
	n, err := d.st.ResendUnknown(d.ctx, id)
	if err != nil {
		reply("Could not resend it.")
		return
	}
	d.snd.Wake()
	if n == 0 {
		reply(escapef("Nothing to resend: no part of #%d is marked unknown.", id))
		return
	}
	reply(escapef("Sending %d part(s) of #%d again.", n, id))
}

// unlink closes the topic in rex-tg. The topic stays in Telegram as history.
func (d *daemon) unlink(th store.Thread) {
	d.closeAlerts(th, store.AlertGone)
	d.closeThread(th.ID)
	d.say(th.ChatID, th.MessageThreadID, "🔌 Unlinked. This topic stays as history; rex-tg no longer mirrors the session.", nil)
}

var _ = agents.Pi

// answerWord returns "yes" or "no" when an answer label is a plain yes or no
// ("Yes", "1 Yes", "4 No", "No, and tell the agent"), else "".
func answerWord(label string) string {
	f := strings.Fields(strings.ToLower(label))
	if len(f) > 1 && strings.Trim(f[0], ".)") != "" && strings.Trim(f[0], "0123456789.)") == "" {
		f = f[1:] // a leading option number
	}
	if len(f) == 0 {
		return ""
	}
	switch strings.Trim(f[0], ",.:;!") {
	case "yes":
		return "yes"
	case "no":
		return "no"
	}
	return ""
}

// esc redacts secrets, then escapes the text for Telegram HTML.
func esc(s string) string { return format.Escape(format.Redact(s)) }

// keyNameRe is what /key accepts: plain key names such as enter, ctrl+c, or
// f5. Anything that starts with a dash could be read as an option of rex.
var keyNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+_-]*$`)
