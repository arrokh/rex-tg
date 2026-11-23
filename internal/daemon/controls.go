package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"sync"
	"time"

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

// currentValues returns the known values of a session. If any control is
// missing, it scans the transcript to fill the gaps (call it from a worker).
// Values already stored win, because the scan may be older.
func (d *daemon) currentValues(conv store.Conversation, c agents.Controller) map[string]string {
	cur := d.controlValues(conv.ID)
	missing := false
	for _, control := range c.Controls() {
		if cur[control.Name] == "" {
			missing = true
			break
		}
	}
	if missing {
		d.ctlMerge(conv.ID, d.scanValues(conv, c), true)
		cur = d.controlValues(conv.ID)
	}
	return cur
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
// evQueueText, so the text waits until the agent is idle (kind "control"
// never steers a running turn) and meets every check before typing. It is typed at most once: the row's id comes from the
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
	// Some agents record a setting only at their next turn (Codex). The new
	// value is kept once the command is typed (onTyped), so a second change
	// right after does not undo the first. A command that is refused,
	// cancelled, or expires changes nothing. The transcript corrects the
	// value when it shows another one.
	d.emit(evQueueText{threadID: th.ID, orig: orig, tgMsg: tgMsg, text: text, kind: kindControl, action: inbound.ActText,
		ctl: &ctlSet{conv: conv.ID, name: c.Name, value: v}})
}

// ctlSet is the value that a queued control command sets, kept until the
// command is typed (core: daemon.ctlPending, by row id).
type ctlSet struct {
	conv        int64
	name, value string // legacy single value
	values      map[string]string
}

// controlTyped keeps the value of a control command that was typed (core),
// and forgets the pending command either way (typed false: refused,
// expired, cancelled, or failed). It reports whether the row was one.
func (d *daemon) controlTyped(row int64, typed bool) bool {
	p, ok := d.ctlPending[row]
	if !ok {
		return false
	}
	delete(d.ctlPending, row)
	if err := d.st.Delete(d.ctx, ctlPendingKey(row)); err != nil {
		d.log.Warn("control: forget the pending command", "err", err)
	}
	if typed {
		values := p.values
		if len(values) == 0 && p.name != "" {
			values = map[string]string{p.name: p.value}
		}
		d.ctlMerge(p.conv, values, false)
	}
	return true
}

// ctlPendingKey keeps a queued control command in the store, so a stop
// between queuing and typing does not lose the value it sets.
func ctlPendingKey(row int64) string { return fmt.Sprintf("ctl_pending:%d", row) }

// keepPending records a queued control command (core).
func (d *daemon) keepPending(row int64, p ctlSet) bool {
	b, err := json.Marshal(struct {
		Conv   int64             `json:"conv"`
		Name   string            `json:"name,omitempty"`
		Value  string            `json:"value,omitempty"`
		Values map[string]string `json:"values,omitempty"`
	}{p.conv, p.name, p.value, p.values})
	if err == nil {
		err = d.st.Set(d.ctx, ctlPendingKey(row), string(b))
	}
	if err != nil {
		d.log.Warn("control: keep the pending command", "err", err)
		return false
	}
	d.ctlPending[row] = p
	return true
}

// loadPending restores the control commands that were queued before a stop
// and are still waiting (startup); the others are forgotten.
func (d *daemon) loadPending(ctx context.Context) {
	keys, err := d.st.KeysWithPrefix(ctx, "ctl_pending:")
	if err != nil {
		return
	}
	for _, k := range keys {
		var row int64
		if _, err := fmt.Sscanf(k, "ctl_pending:%d", &row); err != nil {
			_ = d.st.Delete(ctx, k)
			continue
		}
		in, found, err := d.st.InboundByID(ctx, row)
		v, ok, gerr := d.st.Get(ctx, k)
		var p struct {
			Conv   int64             `json:"conv"`
			Name   string            `json:"name"`
			Value  string            `json:"value"`
			Values map[string]string `json:"values"`
		}
		// Only rows that still wait: a row that was being typed at the stop
		// becomes unknown (RecoverTyping), and its value is not counted.
		if err != nil || !found || gerr != nil || !ok || json.Unmarshal([]byte(v), &p) != nil || in.State != store.InboundQueued {
			_ = d.st.Delete(ctx, k)
			continue
		}
		d.ctlPending[row] = ctlSet{conv: p.Conv, name: p.Name, value: p.Value, values: p.Values}
	}
}

// controlStale reports a queued control command whose session is gone from
// the topic: another session took the pane since it was queued (another
// agent, or the same one after /clear or a resume), so its value would go to
// a session it was not meant for (core).
func (d *daemon) controlStale(th store.Thread, row int64) bool {
	p, ok := d.ctlPending[row]
	return ok && p.conv != 0 && th.CurrentConversationID != p.conv
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
	if err := d.st.Set(d.ctx, claim, strconv.FormatInt(d.now().Unix(), 10)); err != nil { // the time, for housekeeping
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

// cardClaimAge is how long a used card's claim is kept: a tap on a card that
// old says the card was used. After it, a tap counts like a tap on a new
// card, still checked against the card's choices (their hash).
const cardClaimAge = 7 * 24 * time.Hour

// pruneControlRecords drops the store's control records that are no longer
// needed (housekeeping, a worker): the claims of used cards older than
// cardClaimAge, and the known values of conversations that are gone.
func (d *daemon) pruneControlRecords(now time.Time) {
	if keys, err := d.st.KeysWithPrefix(d.ctx, "cf_card:"); err == nil {
		for _, k := range keys {
			v, ok, err := d.st.Get(d.ctx, k)
			if err != nil || !ok {
				continue
			}
			at, perr := strconv.ParseInt(v, 10, 64)
			if perr != nil || at < 1e9 { // "1": a claim from before the time was kept
				at = 0
			}
			if now.Sub(time.Unix(at, 0)) > cardClaimAge {
				_ = d.st.Delete(d.ctx, k)
			}
		}
	}
	// The tab names and last turns of topics that are closed or gone.
	for _, prefix := range []string{"tab_set:", "tab_user:", "last_turn:"} {
		keys, err := d.st.KeysWithPrefix(d.ctx, prefix)
		if err != nil {
			continue
		}
		for _, k := range keys {
			var tid int64
			if _, err := fmt.Sscanf(strings.TrimPrefix(k, prefix), "%d", &tid); err != nil {
				continue
			}
			if th, err := d.st.Thread(d.ctx, tid); errors.Is(err, store.ErrNotFound) || (err == nil && th.Status == store.ThreadClosed) {
				_ = d.st.Delete(d.ctx, k)
			}
		}
	}
	if keys, err := d.st.KeysWithPrefix(d.ctx, "ctl:"); err == nil {
		for _, k := range keys {
			var conv int64
			if _, err := fmt.Sscanf(k, "ctl:%d", &conv); err != nil {
				continue
			}
			if _, err := d.st.Conversation(d.ctx, conv); errors.Is(err, store.ErrNotFound) {
				_ = d.st.Delete(d.ctx, k)
			}
		}
	}
}
