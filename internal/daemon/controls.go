package daemon

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"

	"rex-tg/internal/agents"
	"rex-tg/internal/format"
	"rex-tg/internal/inbound"
	"rex-tg/internal/store"
	"rex-tg/internal/tail"
	"rex-tg/internal/telegram"
)

// Agent controls: /model, /effort, and later more settings (design section
// 8.10). Everything that depends on an agent lives in its adapter
// (agents.Controller). This file only keeps the latest values, draws the card,
// and queues the command text through the normal input path.

// controlStore keeps the latest value of each control per session. The values
// also live in the key-value table, so a restart keeps them. It is safe for
// concurrent use: the core reads transcript lines, workers answer commands.
type controlStore struct {
	mu   sync.Mutex
	vals map[int64]map[string]string // by conversation id
}

func controlKey(conv int64) string { return fmt.Sprintf("ctl:%d", conv) }

// load returns the map of a session, reading the key-value table the first
// time. The caller holds the lock.
func (d *daemon) ctlLoad(conv int64) map[string]string {
	if m, ok := d.ctl.vals[conv]; ok {
		return m
	}
	if d.ctl.vals == nil {
		d.ctl.vals = map[int64]map[string]string{}
	}
	m := map[string]string{}
	if v, ok, err := d.st.Get(d.ctx, controlKey(conv)); err == nil && ok {
		_ = json.Unmarshal([]byte(v), &m)
		if m == nil {
			m = map[string]string{}
		}
	}
	d.ctl.vals[conv] = m
	return m
}

// ctlMerge adds values to a session. With onlyMissing, a key that has a value
// keeps it: a scan of old lines must not beat a value from a newer line.
func (d *daemon) ctlMerge(conv int64, add map[string]string, onlyMissing bool) {
	if len(add) == 0 {
		return
	}
	d.ctl.mu.Lock()
	defer d.ctl.mu.Unlock()
	cur := d.ctlLoad(conv)
	if onlyMissing {
		for k := range add {
			if cur[k] != "" {
				delete(add, k)
			}
		}
	}
	if !agents.MergeSeen(cur, add) {
		return
	}
	b, err := json.Marshal(cur)
	if err != nil {
		return
	}
	if err := d.st.Set(d.ctx, controlKey(conv), string(b)); err != nil {
		d.log.Warn("save control values", "conversation", conv, "err", err)
	}
}

// controlValues returns a copy of the values seen so far for a session.
func (d *daemon) controlValues(conv int64) map[string]string {
	d.ctl.mu.Lock()
	defer d.ctl.mu.Unlock()
	out := map[string]string{}
	for k, v := range d.ctlLoad(conv) {
		out[k] = v
	}
	return out
}

// noteSeen is called for every transcript line of a session (from onBatch).
func (d *daemon) noteSeen(conv int64, a agents.Adapter, line []byte) {
	if c, ok := a.(agents.Controller); ok {
		d.ctlMerge(conv, c.Seen(line), false)
	}
}

// scanValues reads a whole transcript for the latest values. It is slow for a
// long session, so it runs in a worker and only when no value is known.
func (d *daemon) scanValues(conv store.Conversation, c agents.Controller) map[string]string {
	found := map[string]string{}
	if conv.Transcript == "" {
		return found
	}
	_, err := tail.Scan(conv.Transcript, func(l tail.Line) error {
		agents.MergeSeen(found, c.Seen(l.Data))
		return nil
	})
	if err != nil {
		d.log.Debug("scan transcript for settings", "conversation", conv.ID, "err", err)
	}
	return found
}

// currentValues returns the known values of a session. When none is known, it
// scans the transcript once (call it from a worker).
func (d *daemon) currentValues(conv store.Conversation, c agents.Controller) map[string]string {
	cur := d.controlValues(conv.ID)
	if len(cur) > 0 {
		return cur
	}
	d.ctlMerge(conv.ID, d.scanValues(conv, c), true)
	return d.controlValues(conv.ID)
}

// controlOf finds a control of a runtime, with the choices that the config or
// the agent's own files give (call it from a worker: it may read a file).
func (d *daemon) controlOf(rt agents.Runtime, name string) (agents.Control, agents.Controller, bool) {
	a, ok := d.adapters[rt]
	if !ok {
		return agents.Control{}, nil, false
	}
	ctrl, ok := a.(agents.Controller)
	if !ok {
		return agents.Control{}, nil, false
	}
	c, ok := agents.FindControl(ctrl.Controls(), name)
	if !ok {
		return agents.Control{}, nil, false
	}
	if ch := d.cfg.Input(rt).Choices[name]; len(ch) > 0 {
		c.Choices = configChoices(c, ch)
	} else if src, ok := a.(agents.ChoiceSource); ok {
		if ch := src.Choices(d.home, name); len(ch) > 0 {
			c.Choices = ch
		}
	}
	return c, ctrl, true
}

// configChoices are the config's choices for a control. A control with a
// fixed list (not Free) keeps only the values that its adapter accepts, so no
// button can offer a value that the adapter refuses.
func configChoices(c agents.Control, ch []string) []string {
	if c.Free {
		return append([]string(nil), ch...)
	}
	var out []string
	for _, v := range ch {
		if w, err := c.CheckValue(v); err == nil {
			out = append(out, w)
		}
	}
	if len(out) == 0 {
		return c.Choices
	}
	return out
}

// controlCommand runs /model, /effort, and any other control of the agent.
// With no argument it shows the card; with one it queues the command.
func (d *daemon) controlCommand(th store.Thread, orig, tgMsg int64, name, args string) {
	d.spawn(func() {
		conv, c, ctrl, ok := d.topicControl(th, orig, name)
		if !ok {
			return
		}
		if args = strings.TrimSpace(args); args == "" {
			text, markup := d.controlCard(conv, c, ctrl)
			d.replyTopicMarkup(th, text, markup)
			return
		}
		d.applyControl(th, conv, c, ctrl, orig, tgMsg, args)
	})
}

// topicControl finds the session and the control of a topic, and answers when
// there is none.
func (d *daemon) topicControl(th store.Thread, orig int64, name string) (store.Conversation, agents.Control, agents.Controller, bool) {
	tag := fmt.Sprintf("%d:%s", orig, name)
	conv, err := d.st.Conversation(d.ctx, th.CurrentConversationID)
	if err != nil {
		d.replyTopic(th, tag, "This topic has no session now.")
		return conv, agents.Control{}, nil, false
	}
	c, ctrl, ok := d.controlOf(conv.Runtime, name)
	if !ok {
		d.replyTopic(th, tag, escapef("This agent has no /%s setting.", name))
		return conv, c, nil, false
	}
	return conv, c, ctrl, true
}

// applyControl checks a value, asks the adapter for the command text, and
// queues the text like a message typed in the topic. Queuing goes through
// evQueueText, so the text waits until the agent is idle and meets every
// check before typing. It is typed at most once: the row's id comes from the
// update (orig), and a repeat of the update is ignored. tgMsg is the user's
// own message, which gets the reactions; it is 0 for a button.
func (d *daemon) applyControl(th store.Thread, conv store.Conversation, c agents.Control, ctrl agents.Controller, orig, tgMsg int64, value string) {
	tag := fmt.Sprintf("%d:%s", orig, c.Name)
	v, err := c.CheckValue(value)
	if err != nil {
		d.replyTopic(th, tag, esc(err.Error()))
		return
	}
	text, err := ctrl.Command(c.Name, v, d.currentValues(conv, ctrl))
	if err != nil {
		d.replyTopic(th, tag, "⛔ "+esc(err.Error()))
		return
	}
	if qs, err := d.st.QueuedInbound(d.ctx, th.ID); err == nil {
		for _, q := range qs {
			if q.Body == text {
				d.replyTopic(th, tag, "That command is already waiting for the agent.")
				return
			}
		}
	}
	d.replyTopic(th, tag, "⚙️ Sending <code>"+format.Escape(text)+"</code> to the agent.")
	d.emit(evQueueText{threadID: th.ID, orig: orig, tgMsg: tgMsg, text: text, kind: "text", action: inbound.ActText})
	// Some agents record a setting only at their next turn (Codex). Keep the
	// new value now, so a second change right after does not undo the first.
	// The transcript corrects it when it shows another value.
	d.ctlMerge(conv.ID, map[string]string{c.Name: v}, false)
}

// maxControlButtons caps the buttons of a card.
const maxControlButtons = 30

// controlCard is the text and buttons of a control with no argument.
func (d *daemon) controlCard(conv store.Conversation, c agents.Control, ctrl agents.Controller) (string, *telegram.InlineKeyboardMarkup) {
	cur := d.currentValues(conv, ctrl)[c.Name]
	shown := "not seen yet"
	if cur != "" {
		shown = "<code>" + format.Escape(cur) + "</code>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>: %s", format.Escape(c.Title), shown)
	if len(c.Choices) > 0 {
		b.WriteString("\nTap a value")
	} else {
		b.WriteString("\nSend")
	}
	if c.Free {
		fmt.Fprintf(&b, " or send /%s &lt;value&gt;.", c.Name)
	} else {
		fmt.Fprintf(&b, ". You can also send /%s &lt;value&gt;.", c.Name)
	}
	if len(c.Choices) == 0 {
		return b.String(), nil
	}
	per, longest := 3, 0
	for _, ch := range c.Choices {
		longest = max(longest, len([]rune(ch)))
	}
	if longest > 14 {
		per = 2
	}
	var rows [][]telegram.InlineKeyboardButton
	for i, ch := range c.Choices {
		if i >= maxControlButtons {
			break
		}
		label := ch
		if ch == cur {
			label = "✓ " + ch
		}
		btn := telegram.InlineKeyboardButton{Text: label, CallbackData: controlData(c.Name, i, ch)}
		if i%per == 0 {
			rows = append(rows, nil)
		}
		rows[len(rows)-1] = append(rows[len(rows)-1], btn)
	}
	return b.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// controlData is the callback data of a choice: cf:<control>:<index>:<hash>.
// The hash of the value lets the tap check that the choice at that index is
// still the one that the button showed. The data stays under 64 bytes because
// control names are short.
func controlData(control string, idx int, value string) string {
	return fmt.Sprintf("cf:%s:%d:%s", control, idx, choiceHash(value))
}

func choiceHash(v string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(v))
	return fmt.Sprintf("%04x", h.Sum32()&0xffff)
}

// controlButton handles a tap on a choice of a card. parts is the callback
// data without "cf".
func (d *daemon) controlButton(upd int64, cb *telegram.CallbackQuery, parts []string) {
	th, ok := d.threadOfMessage(cb.Message)
	if !ok {
		d.answerCallback(cb.ID, "This topic is closed.", false)
		return
	}
	var idx int
	if len(parts) != 3 {
		d.answerCallback(cb.ID, "Unknown button.", false)
		return
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &idx); err != nil || idx < 0 {
		d.answerCallback(cb.ID, "Unknown button.", false)
		return
	}
	name, hash := parts[0], parts[2]
	chat, msg := cb.Message.Chat.ID, cb.Message.MessageID
	// One tap per card, also for two quick taps and after a restart: the
	// core claims the card in the store before anything is queued.
	claim := fmt.Sprintf("cf_card:%d:%d", chat, msg)
	if _, done, err := d.st.Get(d.ctx, claim); err != nil || done {
		d.answerCallback(cb.ID, "This card was used already. Send /"+name+" again.", false)
		return
	}
	if err := d.st.Set(d.ctx, claim, "1"); err != nil {
		d.answerCallback(cb.ID, "", false)
		return
	}
	d.spawn(func() {
		stale := func() { d.answerCallback(cb.ID, "That button is out of date. Send /"+name+" again.", true) }
		conv, c, ctrl, ok := d.topicControl(th, upd, name)
		if !ok {
			d.answerCallback(cb.ID, "", false)
			return
		}
		if idx >= len(c.Choices) || choiceHash(c.Choices[idx]) != hash {
			stale()
			return
		}
		d.answerCallback(cb.ID, "Sending…", false)
		d.removeButtons(chat, msg) // one tap per card
		d.applyControl(th, conv, c, ctrl, upd, 0, c.Choices[idx])
	})
}

// replyTopicMarkup answers in a topic with buttons. Buttons are not part of
// the outbox, so the card goes out at once and is not repeated after a restart.
func (d *daemon) replyTopicMarkup(th store.Thread, html string, markup *telegram.InlineKeyboardMarkup) {
	_, _ = d.sendNow(th.ChatID, th.MessageThreadID, html, markup)
}

// controlsLine is the /info line with the values seen so far, or "".
func (d *daemon) controlsLine(conv store.Conversation) string {
	a, ok := d.adapters[conv.Runtime]
	if !ok {
		return ""
	}
	ctrl, ok := a.(agents.Controller)
	if !ok {
		return ""
	}
	vals := d.controlValues(conv.ID)
	var parts []string
	for _, c := range ctrl.Controls() {
		if v := vals[c.Name]; v != "" {
			parts = append(parts, fmt.Sprintf("%s: <code>%s</code>", format.Escape(c.Title), format.Escape(v)))
		}
	}
	return strings.Join(parts, " · ")
}
