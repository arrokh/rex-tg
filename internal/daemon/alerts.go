package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"rex-tg/internal/agents"
	"rex-tg/internal/format"
	"rex-tg/internal/inbound"
	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

const (
	alertLines    = 40 // screen lines read for a prompt; a question with descriptions is tall
	alertRawLines = 25 // lines shown when the prompt is not understood
	alertMaxText  = 3400
	alertMaxBtn   = 28
)

var optionRe = regexp.MustCompile(`^\s*(?:[❯>›]\s*)?(\d)[.)]\s+(.+)$`)

// lastLines returns the last n lines of a screen capture.
func lastLines(screen string, n int) []string {
	screen = strings.TrimRight(screen, " \t\r\n")
	if screen == "" {
		return nil
	}
	lines := strings.Split(screen, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// screenHash is the SHA-256 of the prompt lines. Blank lines and trailing
// spaces do not count, and neither does the time on Claude Code's countdown
// ("automatically block this request in 1:59"), which changes every second:
// else every tap on such a prompt would find it changed.
func screenHash(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		l = strings.TrimRight(l, " \t\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		l = deadlineRe.ReplaceAllString(l, "automatically block this request in <time>")
		b.WriteString(l)
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// parseOptions finds numbered options on the screen: the last list that is
// numbered from 1, since the prompt's answers are at the bottom. The digit is
// the key,
// unless shortcuts is set and the answer prints its own key, such as
// Codex's "1. Yes, proceed (y)" or "3. No, … (esc)" (prompts.go).
func parseOptions(lines []string, shortcuts bool) []store.AlertOption {
	var out []store.AlertOption
	seen := map[string]bool{}
	for _, l := range lines {
		m := optionRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if m[1] == "1" && seen["1"] {
			// A list numbered from 1 again: the earlier one was text above
			// the prompt, such as a numbered line of the command or a diff.
			out, seen = nil, map[string]bool{}
		}
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		label := m[1] + " " + strings.TrimSpace(m[2])
		key := m[1]
		if k := shortcutKey(m[2]); shortcuts && k != "" {
			key = k
		}
		out = append(out, store.AlertOption{Label: format.Truncate(label, alertMaxBtn), Keys: []string{key}})
	}
	return out
}

// withEsc adds the Esc button, unless an answer already presses Escape.
func withEsc(opts []store.AlertOption) []store.AlertOption {
	for _, o := range opts {
		if len(o.Keys) == 1 && o.Keys[0] == "escape" {
			return opts
		}
	}
	return append(opts, store.AlertOption{Label: "Esc", Keys: []string{"escape"}})
}

// alertMarkup builds the buttons: `a:<alert id>:<index>`, three to a row.
func alertMarkup(id int64, opts []store.AlertOption) *telegram.InlineKeyboardMarkup {
	var rows [][]telegram.InlineKeyboardButton
	var row []telegram.InlineKeyboardButton
	for i, o := range opts {
		row = append(row, telegram.InlineKeyboardButton{Text: o.Label, CallbackData: fmt.Sprintf("a:%d:%d", id, i)})
		if len(row) == 3 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// preBlock shows lines as redacted monospace text that fits in one message.
func preBlock(lines []string, limit int) string {
	clean := make([]string, len(lines))
	for i, l := range lines {
		clean[i] = strings.TrimRight(l, " \t\r")
	}
	text := format.Redact(strings.Join(clean, "\n")) // once, so multi-line secrets match
	if r := []rune(text); len(r) > limit {
		text = "…" + string(r[len(r)-limit:])
	}
	return "<pre>" + format.Escape(text) + "</pre>"
}

// reopenAlert posts the prompt that a blocked pane shows now, after an older
// alert turned out stale. The pane stayed blocked, so no state change posts
// it.
func (d *daemon) reopenAlert(threadID int64) {
	th, err := d.st.Thread(d.ctx, threadID)
	if err != nil || th.Status != store.ThreadLive {
		return
	}
	p, ok, err := d.st.PaneByConversation(d.ctx, th.CurrentConversationID)
	if err != nil || !ok || p.State != "blocked" {
		return
	}
	if p.StateKind == store.AlertPermission || p.StateKind == store.AlertQuestion {
		d.openAlert(th, p.StateKind)
	}
}

// openAlert starts a worker that posts the alert of a blocked pane (flow 5).
func (d *daemon) openAlert(th store.Thread, kind string) {
	if d.alertBusy[th.ID] {
		d.alertWanted[th.ID] = kind // asked again when the worker ends (alertDone)
		return
	}
	if als, err := d.st.OpenAlerts(d.ctx, th.ID); err == nil && len(als) > 0 {
		return
	}
	d.alertBusy[th.ID] = true
	d.spawn(func() {
		defer d.emit(evAlertDone{th.ID})
		d.createAlert(th, kind, "")
	})
}

// alertDone runs when the alert worker ends. If the pane left the blocked
// state meanwhile, the new alert is closed at once.
func (d *daemon) alertDone(threadID int64) {
	delete(d.alertBusy, threadID)
	wanted, asked := d.alertWanted[threadID]
	delete(d.alertWanted, threadID)
	th, err := d.st.Thread(d.ctx, threadID)
	if err != nil {
		return
	}
	pane, ok, err := d.st.PaneByConversation(d.ctx, th.CurrentConversationID)
	if err != nil || !ok || pane.State != "blocked" {
		d.promptEnded(th)
		return
	}
	if asked && th.Status == store.ThreadLive {
		d.openAlert(th, wanted) // a prompt came while the worker ran
	}
}

// createAlert posts the alert of a blocked pane (a worker). follow is the
// hash of the prompt that was just answered: a follow-up (the next question
// of several, or their review page) is posted only when the screen shows
// another prompt with answers.
func (d *daemon) createAlert(th store.Thread, kind, follow string) {
	ctx := d.ctx
	conv, err := d.st.Conversation(ctx, th.CurrentConversationID)
	if err != nil {
		return
	}
	pane, ok, err := d.st.PaneByConversation(ctx, conv.ID)
	if err != nil || !ok {
		return
	}
	screen, err := d.rx.Capture(ctx, pane.Session, pane.Block)
	if err != nil {
		d.log.Warn("alert: capture", "err", err)
	}
	lines := lastLines(screen, alertLines)
	if follow != "" {
		if v := parsePromptView(lines, true); !v.ok || alertHash(lines) == follow {
			return
		}
	}
	c := d.alertContent(lines, kind, conv.Runtime)
	id, err := d.st.CreateAlert(ctx, store.Alert{
		ThreadID: th.ID, Kind: c.kind, ScreenHash: alertHash(lines), Options: c.opts,
		State: store.AlertOpen, CreatedAt: d.now(),
	})
	if err != nil {
		d.log.Warn("alert: create", "err", err)
		return
	}
	if c.dl != nil {
		d.emit(evAlertDeadline{alert: id, d: *c.dl})
	}
	if err := d.st.Set(ctx, alertHeadKey(id), c.head); err != nil {
		d.log.Warn("alert: keep its head", "err", err)
	}
	var msg telegram.Message
	send := func(text string) error {
		return d.snd.Call(sender.PriorityAlert, func(ctx context.Context, tg *telegram.Client) error {
			m, err := tg.SendMessage(ctx, telegram.SendMessageParams{
				ChatID: th.ChatID, MessageThreadID: th.MessageThreadID, Text: text, ParseMode: "HTML",
				ReplyMarkup: alertMarkup(id, c.opts),
			})
			msg = m
			return err
		})
	}
	err = send(c.text)
	if cl := telegram.Classify(err); cl == telegram.ClassBadRequest || cl == telegram.ClassParseEntities {
		// Refused, so nothing was posted: the screen's bottom lines instead.
		d.log.Warn("alert: refused; sending the screen instead", "err", err)
		err = send("🔐 <b>Permission needed</b>\n" + preBlock(lastLines(strings.Join(lines, "\n"), alertRawLines), alertMaxText))
	}
	if err != nil {
		if d.ctx.Err() == nil {
			d.log.Warn("alert: send", "err", err)
		}
		_ = d.st.SetAlertState(ctx, id, store.AlertGone)
		_ = d.st.Delete(ctx, alertHeadKey(id))
		return
	}
	d.markBelow(th.ID)
	d.log.Info("alert sent", "thread", th.ID, "alert", id,
		"latency_ms", d.now().Sub(pane.StateAt).Milliseconds()) // blocked signal to Telegram's reply
	if err := d.st.SetAlertMessage(ctx, id, msg.MessageID); err != nil {
		d.log.Warn("alert: save message", "err", err)
	}
}

// alertContent is what an alert shows and offers.
type alertContent struct {
	text, head, kind string
	opts             []store.AlertOption
	dl               *alertDeadline
}

// alertContent reads a prompt from the screen's lines: the readable view
// when the prompt is understood (alertview.go), else the screen's bottom
// lines with the numbered answers or the agent's configured ones.
func (d *daemon) alertContent(lines []string, kind string, rt agents.Runtime) alertContent {
	agent := runtimeName(rt)
	c := alertContent{kind: kind}
	if left, rough, ok := parseDeadline(lines); ok {
		// The prompt counts down on the screen: the time left is from now.
		dl := newDeadline(d.now(), left, rough, agent)
		c.dl = &dl
	}
	if v := parsePromptView(lines, kind == store.AlertPermission); v.ok {
		if v.isQuestion() {
			c.kind = store.AlertQuestion // Claude Code reports a question as a permission
		}
		c.text, c.head, c.opts = alertHTML(v, agent, c.kind, c.dl), alertHead(v, agent, c.kind), v.alertOptions()
		if format.VisibleLen(c.text) <= alertMaxText {
			return c
		}
		// Too long for one message: the screen's bottom lines instead.
		c.kind = kind
	}
	c.opts = parseOptions(lines, kind == store.AlertPermission)
	if len(c.opts) == 0 {
		for _, a := range d.cfg.Input(rt).Answers {
			c.opts = append(c.opts, store.AlertOption{Label: a.Label, Keys: a.Keys})
		}
	}
	c.opts = withEsc(c.opts)
	c.head = "🔐 <b>Permission needed</b> · " + format.Escape(agent)
	if kind == store.AlertQuestion {
		c.head = "❓ <b>Question</b> · " + format.Escape(agent)
	}
	c.text = c.head
	if c.dl != nil {
		c.text += "\n" + deadlineLine(*c.dl)
	}
	if kind == store.AlertPermission {
		c.text += "\n<i>Reply with text to answer No and send it as a note.</i>"
	}
	if raw := lastLines(strings.Join(lines, "\n"), alertRawLines); len(raw) > 0 {
		c.text += "\n" + preBlock(raw, alertMaxText)
	}
	return c
}

func alertHeadKey(id int64) string { return fmt.Sprintf("alert_head:%d", id) }

// finishAlert shrinks an alert that is done to its head and the outcome,
// without buttons, so old prompts do not fill the topic. An alert whose head
// is not known (posted before this version) only loses its buttons.
func (d *daemon) finishAlert(chatID, messageID, alertID int64, outcome string) {
	head, ok, err := d.st.Get(d.ctx, alertHeadKey(alertID))
	if err == nil && ok {
		_ = d.st.Delete(d.ctx, alertHeadKey(alertID))
	}
	if messageID == 0 {
		return
	}
	if !ok || head == "" || outcome == "" {
		d.alertEdits.Delete(alertID)
		d.removeButtons(chatID, messageID)
		return
	}
	text := head + "\n" + outcome
	d.spawn(func() {
		mu := d.alertLock(alertID)
		mu.Lock()
		defer mu.Unlock()
		defer d.alertEdits.Delete(alertID) // done: a late refresh finds the alert closed
		err := d.snd.Call(sender.PriorityAlert, func(ctx context.Context, tg *telegram.Client) error {
			_, err := tg.EditMessageText(ctx, telegram.EditMessageTextParams{ChatID: chatID, MessageID: messageID, Text: text, ParseMode: "HTML"})
			if telegram.Classify(err) == telegram.ClassNotModified {
				return nil
			}
			return err
		})
		if err != nil && d.ctx.Err() == nil {
			d.log.Debug("finish alert", "err", err)
			d.removeButtons(chatID, messageID) // at least the buttons go
		}
	})
}

// closeAlerts closes the open alerts of a topic and removes their buttons, so
// an old button can never type into whatever the screen shows later. The
// alert shrinks to its head and outcome(alert); "" keeps its text.
func (d *daemon) closeAlerts(th store.Thread, state string) {
	d.closeAlertsWith(th, state, func(store.Alert) string { return "✖ Closed" })
}

func (d *daemon) closeAlertsWith(th store.Thread, state string, outcome func(store.Alert) string) {
	als, err := d.st.OpenAlerts(d.ctx, th.ID)
	if err != nil {
		return
	}
	for _, a := range als {
		if _, busy := d.answering.Load(a.ID); busy {
			continue // an answer from Telegram is being sent: it finishes the alert
		}
		if err := d.st.SetAlertState(d.ctx, a.ID, state); err != nil {
			d.log.Warn("close alert", "err", err)
			continue
		}
		d.finishAlert(th.ChatID, a.TGMessageID, a.ID, outcome(a))
	}
}

func (d *daemon) removeButtons(chatID, messageID int64) {
	if messageID == 0 {
		return
	}
	d.spawn(func() {
		err := d.snd.Call(sender.PriorityAlert, func(ctx context.Context, tg *telegram.Client) error {
			return tg.EditMessageReplyMarkup(ctx, chatID, messageID, nil)
		})
		if err != nil && d.ctx.Err() == nil {
			d.log.Debug("remove buttons", "err", err)
		}
	})
}

// answerAlert presses option idx of an alert (flow 5), and reports whether
// it pressed the keys. It runs in a worker.
// tapped is the message whose button was pressed (0 for /approve and /deny):
// a tap right after the alert was posted can come before the alert's message
// id is saved, and the buttons must go all the same. respond tells the user
// the result; isErr marks a refusal.
func (d *daemon) answerAlert(alertID int64, idx int, tapped int64, respond func(text string, isErr bool)) bool {
	return d.answerAlertAs(alertID, idx, tapped, "", respond)
}

// answerAlertAs is answerAlert with the outcome that the shrunk alert shows
// ("" says the answer's label).
func (d *daemon) answerAlertAs(alertID int64, idx int, tapped int64, outcome string, respond func(text string, isErr bool)) bool {
	ctx := d.ctx
	// Claim the alert first: a second tap (or /approve) while the keys are
	// being sent must not press them again.
	if _, busy := d.answering.LoadOrStore(alertID, struct{}{}); busy {
		respond("Already sending an answer.", false)
		return false
	}
	defer d.answering.Delete(alertID)
	al, err := d.st.Alert(ctx, alertID)
	if err != nil || al.State != store.AlertOpen {
		respond("This prompt is gone.", true)
		return false
	}
	th, err := d.st.Thread(ctx, al.ThreadID)
	if err != nil {
		respond("This prompt is gone.", true)
		return false
	}
	if idx < 0 || idx >= len(al.Options) {
		respond("Unknown answer.", true)
		return false
	}
	if al.TGMessageID == 0 {
		al.TGMessageID = tapped
	}
	pane, lines, ok := d.alertReady(ctx, th, al, respond)
	if !ok {
		return false
	}
	opt := al.Options[idx]
	switch {
	case len(opt.Keys) == 1 && opt.Keys[0] == nextKey:
		if err := d.pressNext(ctx, th, pane, al); err != nil {
			respond("Not sent: "+err.Error()+".", true)
			return false
		}
		if outcome == "" {
			outcome = "➡ " + tickedText(lines)
		}
	case toggleOption(opt):
		// A box: the question stays, and so does the alert, with the box
		// as the screen shows it now.
		if err := d.rx.SendKeys(ctx, pane.Session, pane.Block, opt.Keys...); err != nil {
			d.log.Warn("alert: send keys", "err", err)
			respond("Could not press the keys.", true)
			return false
		}
		switch {
		case !d.refreshAlert(ctx, th, pane, al):
			respond("Pressed; the screen has not changed yet.", false)
		case strings.HasPrefix(opt.Label, "☐"):
			respond("☑ Ticked", false)
		default:
			respond("☐ Unticked", false)
		}
		return true
	default:
		if err := d.rx.SendKeys(ctx, pane.Session, pane.Block, opt.Keys...); err != nil {
			d.log.Warn("alert: send keys", "err", err)
			respond("Could not press the keys.", true)
			return false
		}
	}
	if err := d.st.SetAlertState(ctx, al.ID, store.AlertAnswered); err != nil {
		d.log.Warn("alert: save state", "err", err)
	}
	if outcome == "" {
		outcome = "✅ " + format.Escape(opt.Label)
		if len(opt.Keys) == 1 && opt.Keys[0] == "escape" {
			outcome = "✖ " + format.Escape(opt.Label)
		}
	}
	d.finishAlert(th.ChatID, al.TGMessageID, al.ID, outcome)
	d.emit(evAlertFollow{thread: th.ID, hash: al.ScreenHash}) // a next question may show
	respond("Sent: "+opt.Label, false)
	return true
}

// alertReady checks, before a key, that the alert's prompt is still on the
// screen and that every safety check passes (a worker). A stale alert is
// closed, and a newer prompt gets its own.
func (d *daemon) alertReady(ctx context.Context, th store.Thread, al store.Alert, respond func(string, bool)) (store.Pane, []string, bool) {
	// Under the claim, read the alert again: an answer that just finished
	// (a second reply, a second tap) must find it answered.
	if now, err := d.st.Alert(ctx, al.ID); err != nil || now.State != store.AlertOpen {
		respond("This prompt is gone.", true)
		return store.Pane{}, nil, false
	}
	f, pane, err := d.facts(ctx, th)
	if err != nil {
		respond("Could not read the pane: Rex is not reachable.", true)
		return pane, nil, false
	}
	// The alert is open, and the pane must still report its prompt: a
	// screen that only looks like one (a numbered list in the agent's
	// output while it works) takes no key.
	f.PromptOpen = pane.State == "blocked"
	var lines []string
	if f.PaneExists {
		screen, err := d.rx.Capture(ctx, pane.Session, pane.Block)
		if err != nil {
			respond("Could not read the screen.", true)
			return pane, nil, false
		}
		lines = lastLines(screen, alertLines)
		f.AlertMatches = alertHash(lines) == al.ScreenHash
	}
	if ok, reason := inbound.Check(inbound.ActPromptAnswer, f); !ok {
		if reason == inbound.ReasonAlertStale {
			_ = d.st.SetAlertState(ctx, al.ID, store.AlertGone)
			d.finishAlert(th.ChatID, al.TGMessageID, al.ID, "↻ The prompt changed")
			respond("This prompt is gone.", true)
			d.emit(evAlertStale{th.ID}) // a newer prompt may show now
			return pane, nil, false
		}
		respond("⛔ Not sent: "+reason+".", true)
		return pane, nil, false
	}
	return pane, lines, true
}

// refreshAlert shows a question again after a box was ticked (a worker): it
// waits until the screen shows the change, then keeps the new prompt in the
// store and edits the alert with the boxes as they are now.
func (d *daemon) refreshAlert(ctx context.Context, th store.Thread, pane store.Pane, al store.Alert) bool {
	agent := "The agent"
	if conv, err := d.st.Conversation(ctx, th.CurrentConversationID); err == nil {
		agent = runtimeName(conv.Runtime)
	}
	for range vimPolls {
		if !sleepCtx(ctx, d.timing.VimPoll) {
			return false
		}
		screen, err := d.rx.Capture(ctx, pane.Session, pane.Block)
		if err != nil {
			continue
		}
		lines := lastLines(screen, alertLines)
		h := alertHash(lines)
		v := parsePromptView(lines, false)
		if h == al.ScreenHash || !v.ok {
			continue
		}
		opts := v.alertOptions()
		if err := d.st.UpdateAlertScreen(ctx, al.ID, h, opts); err != nil {
			return false // closed meanwhile
		}
		text, markup, chat, msg := alertHTML(v, agent, store.AlertQuestion, nil), alertMarkup(al.ID, opts), th.ChatID, al.TGMessageID
		if msg == 0 {
			return true
		}
		// The same lock as finishAlert: a refresh never puts the buttons
		// back on an alert that closed meanwhile.
		mu := d.alertLock(al.ID)
		mu.Lock()
		defer mu.Unlock()
		if now, err := d.st.Alert(ctx, al.ID); err != nil || now.State != store.AlertOpen {
			return false
		}
		err = d.snd.Call(sender.PriorityAlert, func(ctx context.Context, tg *telegram.Client) error {
			_, err := tg.EditMessageText(ctx, telegram.EditMessageTextParams{ChatID: chat, MessageID: msg, Text: text, ParseMode: "HTML", ReplyMarkup: markup})
			if telegram.Classify(err) == telegram.ClassNotModified {
				return nil
			}
			return err
		})
		if err != nil && d.ctx.Err() == nil {
			d.log.Debug("refresh alert", "err", err)
		}
		return true
	}
	return false
}

// pressNext moves the cursor of a question to its "Next" row, one key at a
// time with the screen read after each, and presses Enter there (a worker).
// Every key passes the safety checks first.
func (d *daemon) pressNext(ctx context.Context, th store.Thread, pane store.Pane, al store.Alert) error {
	for range 2 * maxQuestionLines {
		screen, err := d.rx.Capture(ctx, pane.Session, pane.Block)
		if err != nil {
			return errors.New("could not read the screen")
		}
		lines := lastLines(screen, alertLines)
		if alertHash(lines) != al.ScreenHash {
			return errors.New("the question changed")
		}
		rows, cur := promptRows(lines)
		target := -1
		for i, r := range rows {
			if r == "next" {
				target = i
			}
		}
		if target < 0 || cur < 0 {
			return errors.New("no Next on the screen")
		}
		f, p, err := d.facts(ctx, th)
		if err != nil {
			return errors.New("rex is not reachable")
		}
		f.PromptOpen, f.AlertMatches = p.State == "blocked", true
		if ok, reason := inbound.Check(inbound.ActPromptAnswer, f); !ok {
			return errors.New(reason)
		}
		if cur == target {
			return d.rx.SendKeys(ctx, pane.Session, pane.Block, "enter")
		}
		key := "down"
		if target < cur {
			key = "up"
		}
		if err := d.rx.SendKeys(ctx, pane.Session, pane.Block, key); err != nil {
			return errors.New("could not press a key")
		}
		moved := false
		for range vimPolls {
			if !sleepCtx(ctx, d.timing.VimPoll) {
				return ctx.Err()
			}
			s, err := d.rx.Capture(ctx, pane.Session, pane.Block)
			if err != nil {
				continue
			}
			if _, c := promptRows(lastLines(s, alertLines)); c != cur {
				moved = true
				break
			}
		}
		if !moved {
			return errors.New("the cursor did not move")
		}
	}
	return errors.New("the Next row was not reached")
}

// tickedText lists the ticked answers of a question, for the shrunk alert.
func tickedText(lines []string) string {
	v := parsePromptView(lines, false)
	var on []string
	for _, o := range v.options {
		if o.box == 2 {
			on = append(on, o.label)
		}
	}
	if len(on) == 0 {
		return "Next"
	}
	return format.Escape(format.Truncate(strings.Join(on, ", "), 200))
}

// alertLock is the lock of one alert's message: a refresh and the final edit
// do not cross.
func (d *daemon) alertLock(id int64) *sync.Mutex {
	mu, _ := d.alertEdits.LoadOrStore(id, &sync.Mutex{})
	return mu.(*sync.Mutex)
}
