package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"rex-tg/internal/format"
	"rex-tg/internal/inbound"
	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

const (
	alertLines   = 25
	alertMaxText = 3400
	alertMaxBtn  = 28
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
// spaces do not count.
func screenHash(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		l = strings.TrimRight(l, " \t\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// parseOptions finds numbered options on the screen. The digit is the key.
func parseOptions(lines []string) []store.AlertOption {
	var out []store.AlertOption
	seen := map[string]bool{}
	for _, l := range lines {
		m := optionRe.FindStringSubmatch(l)
		if m == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		label := m[1] + " " + strings.TrimSpace(m[2])
		out = append(out, store.AlertOption{Label: format.Truncate(label, alertMaxBtn), Keys: []string{m[1]}})
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
		clean[i] = format.Redact(strings.TrimRight(l, " \t\r"))
	}
	text := strings.Join(clean, "\n")
	if r := []rune(text); len(r) > limit {
		text = "…" + string(r[len(r)-limit:])
	}
	return "<pre>" + format.Escape(text) + "</pre>"
}

// openAlert starts a worker that posts the alert of a blocked pane (flow 5).
func (d *daemon) openAlert(th store.Thread, kind string) {
	if d.alertBusy[th.ID] {
		return
	}
	if als, err := d.st.OpenAlerts(d.ctx, th.ID); err == nil && len(als) > 0 {
		return
	}
	d.alertBusy[th.ID] = true
	d.spawn(func() {
		defer d.emit(evAlertDone{th.ID})
		d.createAlert(th, kind)
	})
}

// alertDone runs when the alert worker ends. If the pane left the blocked
// state meanwhile, the new alert is closed at once.
func (d *daemon) alertDone(threadID int64) {
	delete(d.alertBusy, threadID)
	th, err := d.st.Thread(d.ctx, threadID)
	if err != nil {
		return
	}
	pane, ok, err := d.st.PaneByConversation(d.ctx, th.CurrentConversationID)
	if err != nil || !ok || pane.State != "blocked" {
		d.closeAlerts(th, store.AlertGone)
	}
}

func (d *daemon) createAlert(th store.Thread, kind string) {
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
	opts := parseOptions(lines)
	if len(opts) == 0 {
		for _, a := range d.cfg.Input(conv.Runtime).Answers {
			opts = append(opts, store.AlertOption{Label: a.Label, Keys: a.Keys})
		}
	}
	opts = withEsc(opts)
	id, err := d.st.CreateAlert(ctx, store.Alert{
		ThreadID: th.ID, Kind: kind, ScreenHash: screenHash(lines), Options: opts,
		State: store.AlertOpen, CreatedAt: d.now(),
	})
	if err != nil {
		d.log.Warn("alert: create", "err", err)
		return
	}
	head := "🔐 <b>Permission needed</b>"
	if kind == store.AlertQuestion {
		head = "❓ <b>The agent asks a question</b>"
	}
	text := head
	if len(lines) > 0 {
		text += "\n" + preBlock(lines, alertMaxText)
	}
	var msg telegram.Message
	err = d.snd.Call(sender.PriorityAlert, func(ctx context.Context, tg *telegram.Client) error {
		m, err := tg.SendMessage(ctx, telegram.SendMessageParams{
			ChatID: th.ChatID, MessageThreadID: th.MessageThreadID, Text: text, ParseMode: "HTML",
			ReplyMarkup: alertMarkup(id, opts),
		})
		msg = m
		return err
	})
	if err != nil {
		if d.ctx.Err() == nil {
			d.log.Warn("alert: send", "err", err)
		}
		_ = d.st.SetAlertState(ctx, id, store.AlertGone)
		return
	}
	if err := d.st.SetAlertMessage(ctx, id, msg.MessageID); err != nil {
		d.log.Warn("alert: save message", "err", err)
	}
}

// closeAlerts closes the open alerts of a topic and removes their buttons, so
// an old button can never type into whatever the screen shows later.
func (d *daemon) closeAlerts(th store.Thread, state string) {
	als, err := d.st.OpenAlerts(d.ctx, th.ID)
	if err != nil {
		return
	}
	for _, a := range als {
		if err := d.st.SetAlertState(d.ctx, a.ID, state); err != nil {
			d.log.Warn("close alert", "err", err)
			continue
		}
		d.removeButtons(th.ChatID, a.TGMessageID)
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

// answerAlert presses option idx of an alert (flow 5). It runs in a worker.
// respond tells the user the result; isErr marks a refusal.
func (d *daemon) answerAlert(alertID int64, idx int, respond func(text string, isErr bool)) {
	ctx := d.ctx
	al, err := d.st.Alert(ctx, alertID)
	if err != nil || al.State != store.AlertOpen {
		respond("This prompt is gone.", true)
		return
	}
	th, err := d.st.Thread(ctx, al.ThreadID)
	if err != nil {
		respond("This prompt is gone.", true)
		return
	}
	if idx < 0 || idx >= len(al.Options) {
		respond("Unknown answer.", true)
		return
	}
	f, pane, err := d.facts(ctx, th)
	if err != nil {
		respond("Could not read the pane: Rex is not reachable.", true)
		return
	}
	f.PromptOpen = true
	if f.PaneExists {
		screen, err := d.rx.Capture(ctx, pane.Session, pane.Block)
		if err != nil {
			respond("Could not read the screen.", true)
			return
		}
		f.AlertMatches = screenHash(lastLines(screen, alertLines)) == al.ScreenHash
	}
	if ok, reason := inbound.Check(inbound.ActPromptAnswer, f); !ok {
		if reason == inbound.ReasonAlertStale {
			_ = d.st.SetAlertState(ctx, al.ID, store.AlertGone)
			d.removeButtons(th.ChatID, al.TGMessageID)
			respond("This prompt is gone.", true)
			return
		}
		respond("⛔ Not sent: "+reason+".", true)
		return
	}
	opt := al.Options[idx]
	if err := d.rx.SendKeys(ctx, pane.Session, pane.Block, opt.Keys...); err != nil {
		d.log.Warn("alert: send keys", "err", err)
		respond("Could not press the keys.", true)
		return
	}
	if err := d.st.SetAlertState(ctx, al.ID, store.AlertAnswered); err != nil {
		d.log.Warn("alert: save state", "err", err)
	}
	d.removeButtons(th.ChatID, al.TGMessageID)
	respond("Sent: "+opt.Label, false)
}
