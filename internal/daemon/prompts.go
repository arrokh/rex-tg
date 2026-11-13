package daemon

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"rex-tg/internal/format"
	"rex-tg/internal/inbound"
	"rex-tg/internal/registry"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// What rex-tg reads from a prompt beyond its answers (design section 8.6),
// per agent:
//
//   - Claude Code's safety checks block a request by themselves after a
//     while: "Claude Code will automatically block this request in 2:00, to
//     avoid holding up an unattended session" (or "in about 2 minutes"). The
//     alert says by when to answer, a reminder comes 30 seconds before, and
//     when the time ran out the topic says so.
//   - A text reply to an alert is a note: rex-tg refuses the prompt with Esc
//     (denyAnswer; Claude Code then asks "What should Claude do instead?",
//     and Codex's Esc is "No, and tell Codex what to do differently") and
//     types the note as the next message, when the agent waits for it. This
//     is what Claude Code's "Tab to amend" does for No, through the checks
//     that every answer and every message pass.
//   - Codex prints the key of an answer in brackets, such as "(y)" or
//     "(esc)"; that key is pressed, not the number (parseOptions).

// deadlineSlack is how early before its deadline a prompt that goes away
// unanswered still counts as blocked by the time limit (at most a tenth of
// the limit). The reminder comes Timing.DeadlineWarn before the deadline, and
// only when that is at least half of DeadlineWarn after the alert.
const deadlineSlack = 5 * time.Second

var (
	// deadlineRe finds Claude Code's time limit: "in 2:00" or "in about 2
	// minutes" / "in about 45 seconds".
	deadlineRe = regexp.MustCompile(`(?i)automatically block this request in (?:(\d+):(\d{2})|about (\d+) (minute|second)s?)`)
	// shortcutRe is the key that an answer prints after it: "(y)", "(esc)".
	shortcutRe = regexp.MustCompile(`\((esc|y|n|a|p)\)\s*$`)
)

// parseDeadline finds the time limit of a prompt on the screen: the time
// left when the screen was read, and whether it is rounded ("in about 2
// minutes"). The line counts only inside the prompt, a few lines above its
// question or answers, so an agent's output that quotes it does not.
func parseDeadline(lines []string) (left time.Duration, rough, ok bool) {
	for i, l := range lines {
		m := deadlineRe.FindStringSubmatch(l)
		if m == nil || !promptFollows(lines[i+1:]) {
			continue
		}
		if m[1] != "" {
			mins, _ := strconv.Atoi(m[1])
			secs, _ := strconv.Atoi(m[2])
			return time.Duration(mins)*time.Minute + time.Duration(secs)*time.Second, false, true
		}
		n, _ := strconv.Atoi(m[3])
		if strings.EqualFold(m[4], "minute") {
			return time.Duration(n) * time.Minute, true, true
		}
		return time.Duration(n) * time.Second, true, true
	}
	return 0, false, false
}

// promptFollows tells whether the prompt's question or an answer comes
// within the next few lines.
func promptFollows(next []string) bool {
	for i, l := range next {
		if i == 4 {
			break
		}
		if optionRe.MatchString(l) || strings.Contains(strings.ToLower(l), "proceed?") {
			return true
		}
	}
	return false
}

// shortcutKey returns the key that an answer's text prints, or "".
func shortcutKey(text string) string {
	m := shortcutRe.FindStringSubmatch(strings.ToLower(text))
	switch {
	case m == nil:
		return ""
	case m[1] == "esc":
		return "escape"
	}
	return m[1]
}

// clockText is a duration as the prompts show it: "2:00", "0:45".
func clockText(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// alertDeadline is the time limit of an open alert (core).
type alertDeadline struct {
	at    time.Time
	limit time.Duration // as the prompt said it
	early time.Duration // a rounded limit ("about 2 minutes") may end this much sooner
	agent string
}

// newDeadline makes the time limit of a prompt read at now.
func newDeadline(now time.Time, left time.Duration, rough bool, agent string) alertDeadline {
	dl := alertDeadline{at: now.Add(left), limit: left, agent: agent}
	if rough {
		dl.early = min(30*time.Second, left/4)
	}
	return dl
}

// evAlertDeadline reports the time limit of a new alert.
type evAlertDeadline struct {
	alert int64
	d     alertDeadline
}

// deadlineLine is the line of an alert that says by when to answer.
func deadlineLine(dl alertDeadline) string {
	return fmt.Sprintf("⏳ <b>Answer within %s</b> (by %s), or %s blocks it.",
		clockText(dl.limit), dl.at.Format("15:04:05"), format.Escape(dl.agent))
}

// endedOutcome is what an alert whose prompt went away without an answer
// from Telegram says (core): blocked, when its time limit ran out, else
// answered on the Mac. Only promptEnded asks: a topic that closes is no sign
// of either.
func (d *daemon) endedOutcome(al store.Alert) string {
	dl, ok := d.deadlines[al.ID]
	delete(d.deadlines, al.ID)
	if ok && !d.now().Before(dl.at.Add(-min(deadlineSlack, dl.limit/10)-dl.early)) {
		return fmt.Sprintf("⌛ <b>Blocked</b>: no answer within %s, so %s denied the request.", clockText(dl.limit), format.Escape(dl.agent))
	}
	return "✔ Answered on the Mac"
}

// evAlertFollow asks for a look at the screen after an answer: a question
// of several shows the next one, or its review page, with the pane still
// blocked and no state change to post it.
type evAlertFollow struct {
	thread int64
	hash   string // the prompt that was answered
}

// alertFollow is a look at the screen that is due (core).
type alertFollow struct {
	hash  string
	due   time.Time
	tries int
}

func (d *daemon) onAlertFollow(e evAlertFollow) {
	d.follows[e.thread] = alertFollow{hash: e.hash, due: d.now().Add(d.timing.AlertFollow), tries: 2}
}

// tickFollows posts the alert of a prompt that came after an answered one
// (core): when the pane is still blocked, no alert is open, and the screen
// shows another prompt with answers. It looks twice: after AlertFollow and
// after three times that, since an agent may take a moment to redraw.
func (d *daemon) tickFollows(now time.Time) {
	for tid, f := range d.follows {
		if now.Before(f.due) {
			continue
		}
		if f.tries--; f.tries > 0 {
			f.due = now.Add(2 * d.timing.AlertFollow)
			d.follows[tid] = f
		} else {
			delete(d.follows, tid)
		}
		if d.alertBusy[tid] {
			continue
		}
		th, err := d.st.Thread(d.ctx, tid)
		if err != nil || th.Status != store.ThreadLive {
			continue
		}
		p, ok, err := d.st.PaneByConversation(d.ctx, th.CurrentConversationID)
		if err != nil || !ok || p.State != "blocked" {
			continue
		}
		if als, err := d.st.OpenAlerts(d.ctx, tid); err != nil || len(als) > 0 {
			continue
		}
		kind, hash := p.StateKind, f.hash
		if kind != store.AlertQuestion {
			kind = store.AlertPermission
		}
		d.alertBusy[tid] = true
		d.spawn(func() {
			defer d.emit(evAlertDone{tid})
			d.createAlert(th, kind, hash)
		})
	}
}

// noteAnswer picks the answer that a note goes with: the one that refuses
// (denyAnswer).
func noteAnswer(al store.Alert) (int, bool) { return denyAnswer(al) }

// denyAnswer picks the answer that refuses a prompt and gives the turn back,
// for /deny and for a note: Esc, or the answer bound to it. Both agents read
// Esc as a refusal that waits for the user: Claude Code rejects ("What should
// Claude do instead?"), and in Codex Esc is "No, and tell Codex what to do
// differently". A label that starts with No is no safe guess: Claude Code's
// plan prompt has "No, refine with Ultraplan in a cloud session" (it starts
// a cloud session) before "No, keep planning" (a text field, which its digit
// only focuses), and Codex's network prompt has "No, and block this host in
// the future". Every alert has Esc (withEsc), so a plain No is only a
// fallback for a menu without it.
func denyAnswer(al store.Alert) (int, bool) {
	for i, o := range al.Options {
		if len(o.Keys) == 1 && o.Keys[0] == "escape" {
			return i, true
		}
	}
	if al.Kind == store.AlertPermission {
		for i, o := range al.Options {
			if answerWord(o.Label) == "no" && !notPlainNo(o.Label) {
				return i, true
			}
		}
	}
	return 0, false
}

// notPlainNo tells whether a No answer does more than refuse: makes a
// lasting rule, starts something else, or opens a text field.
func notPlainNo(label string) bool {
	l := strings.ToLower(label)
	for _, w := range []string{"block", "future", "always", "never", "don't ask", "do not ask", "ultraplan", "cloud", "keep planning", "start implementing"} {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// alertNote takes a text reply to an open alert as a note for the agent
// (core): rex-tg answers the prompt with No, then queues the note like a
// message, so it is typed when the agent waits for it. It reports whether
// it took the message.
func (d *daemon) alertNote(u telegram.Update, m *telegram.Message, th store.Thread) bool {
	rm := m.ReplyToMessage
	if rm == nil {
		return false
	}
	r := inbound.RouteText(m.Text, true, d.botUser)
	if r.Kind != inbound.RouteAgentText {
		return false
	}
	als, err := d.st.OpenAlerts(d.ctx, th.ID)
	if err != nil {
		return false
	}
	var al store.Alert
	for _, a := range als {
		if a.TGMessageID != 0 && a.TGMessageID == rm.MessageID {
			al = a
		}
	}
	if al.ID == 0 {
		return false
	}
	if !d.recorded(u, m, "other", "", th.ID, store.InboundDone) {
		return true
	}
	tag := fmt.Sprintf("%d:note", u.UpdateID)
	if al.Kind == store.AlertQuestion {
		// A question: the reply is the user's own answer, typed into the
		// question's text field ("Type something"), not a refusal.
		text := r.Text
		d.spawn(func() { d.answerWithText(th, al, text, tag) })
		return true
	}
	if approves(r.Text) {
		// A reply is a note that answers No; "yes, go ahead" must not deny.
		d.replyTopic(th, tag, "Nothing pressed: a reply answers <b>No</b> and sends your text as a note. To allow it, tap Yes or send /approve.")
		return true
	}
	idx, ok := noteAnswer(al)
	if !ok {
		d.replyTopic(th, tag, "This prompt has no No answer. Tap a button, then send your note.")
		return true
	}
	note, upd, msg, label := r.Text, u.UpdateID, m.MessageID, al.Options[idx].Label
	if label == "Esc" {
		label = "No (Esc)"
	}
	d.spawn(func() {
		var said string
		if !d.answerAlert(al.ID, idx, al.TGMessageID, func(text string, _ bool) { said = text }) {
			d.replyTopic(th, tag, format.Escape(said)+" Your note was not sent.")
			return
		}
		d.emit(evQueueText{threadID: th.ID, orig: upd, tgMsg: msg, text: note, kind: kindNote, action: inbound.ActText,
			note: &noteDone{tag: tag, label: label}})
	})
	return true
}

// noteDone is the reply to send once a note is queued (core).
type noteDone struct{ tag, label string }

// noteQueued tells the user what became of a note (core).
func (d *daemon) noteQueued(threadID int64, n *noteDone, queued bool) {
	th, err := d.st.Thread(d.ctx, threadID)
	if err != nil {
		return
	}
	if !queued {
		d.replyTopic(th, n.tag, "Answered <b>"+format.Escape(n.label)+"</b>, but your note could not be kept: the topic is closed.")
		return
	}
	d.replyTopic(th, n.tag, "✍ Answered <b>"+format.Escape(n.label)+"</b>. Your note goes to the agent next.")
}

// approves tells whether a reply starts like a yes, in English or
// Indonesian.
func approves(text string) bool {
	f := strings.Fields(strings.ToLower(text))
	if len(f) == 0 {
		return false
	}
	switch strings.Trim(f[0], ".,!?:;") {
	case "yes", "y", "yep", "yup", "yeah", "ok", "okay", "oke", "okey", "sure", "approve", "approved",
		"allow", "go", "proceed", "continue", "ya", "iya", "yoi", "boleh", "setuju", "lanjut", "lanjutkan", "gas", "sip":
		return true
	}
	return false
}

// promptEnded closes the alerts of a pane that left its prompt (core). Each
// one shrinks to its head and says what became of it: blocked at its time
// limit, or answered on the Mac.
func (d *daemon) promptEnded(th store.Thread) {
	d.closeAlertsWith(th, store.AlertGone, d.endedOutcome)
}

// answerWithText answers a question with the user's own words (a worker): it
// presses the digit of the question's text field ("Type something"), checks
// that the cursor is there, pastes the text as one line, and presses Enter.
// A question whose answers are ticked takes no text from Telegram: the
// buttons tick them.
func (d *daemon) answerWithText(th store.Thread, al store.Alert, text, tag string) {
	ctx := d.ctx
	say := func(html string) { d.replyTopic(th, tag, html) }
	if _, busy := d.answering.LoadOrStore(al.ID, struct{}{}); busy {
		say("Already sending an answer.")
		return
	}
	defer d.answering.Delete(al.ID)
	pane, lines, ok := d.alertReady(ctx, th, al, func(s string, _ bool) { say(format.Escape(s)) })
	if !ok {
		return
	}
	v := parsePromptView(lines, false)
	if v.multi() {
		say("This question takes ticks: tap the boxes, then <b>Next ➡</b>. Your own words go only on the Mac.")
		return
	}
	field := ""
	for _, o := range v.options {
		if o.input {
			field = o.num
		}
	}
	if field == "" {
		say("This question takes only its answers: tap one.")
		return
	}
	if err := d.rx.SendKeys(ctx, pane.Session, pane.Block, field); err != nil {
		say("Could not press the keys.")
		return
	}
	focused := false
	for range vimPolls {
		if !sleepCtx(ctx, d.timing.VimPoll) {
			return
		}
		s, err := d.rx.Capture(ctx, pane.Session, pane.Block)
		if err != nil {
			continue
		}
		rows, cur := promptRows(lastLines(s, alertLines))
		if cur >= 0 && cur < len(rows) && rows[cur] == field {
			focused = true
			break
		}
	}
	if !focused {
		say("Not sent: the answer field did not open. Answer on the Mac.")
		return
	}
	// Before the paste and before Enter: the same question, its field under
	// the cursor, and the agent in front, as for every key.
	onField := func(sameQuestion bool) bool {
		f, _, err := d.facts(ctx, th)
		if err != nil {
			return false
		}
		s, err := d.rx.Capture(ctx, pane.Session, pane.Block)
		if err != nil {
			return false
		}
		lines := lastLines(s, alertLines)
		f.PromptOpen = true
		f.AlertMatches = !sameQuestion || alertHash(lines) == al.ScreenHash
		if ok, _ := inbound.Check(inbound.ActPromptAnswer, f); !ok {
			return false
		}
		rows, cur := promptRows(lines)
		return cur >= 0 && cur < len(rows) && rows[cur] == field
	}
	if !onField(true) {
		say("Not sent: the question changed. Answer on the Mac.")
		return
	}
	line := strings.Join(strings.Fields(text), " ") // a newline would answer early
	if err := d.rx.Paste(ctx, pane.Session, pane.Block, line); err != nil {
		say("Could not type the answer.")
		return
	}
	if !sleepCtx(ctx, d.timing.VimPoll) {
		return
	}
	if !onField(false) { // the field now holds the text, so the screen differs
		say("Not sent: the question changed after your text was typed. Check it on the Mac.")
		return
	}
	if err := d.rx.SendKeys(ctx, pane.Session, pane.Block, "enter"); err != nil {
		say("Could not press Enter.")
		return
	}
	if err := d.st.SetAlertState(ctx, al.ID, store.AlertAnswered); err != nil {
		d.log.Warn("alert: save state", "err", err)
	}
	d.finishAlert(th.ChatID, al.TGMessageID, al.ID, "✍ "+format.Escape(format.Truncate(line, 200)))
	d.emit(evAlertFollow{thread: th.ID, hash: al.ScreenHash})
}

// A pane is blocked from the agent's prompt hook until a new prompt or the
// end of the turn; Claude Code and Codex report no hook when the prompt is
// answered. Messages that wait for the prompt would then wait for the whole
// turn. So when the session's transcript goes on (a tool call or result, or
// commentary) while the pane is blocked, rex-tg reads the screen: without a
// prompt on it, the prompt is over, and the pane is working again. The
// screen decides, not the transcript alone: a tool that runs in parallel
// can write its result while another one still waits for its answer.

// promptGoneDelay is how long after the transcript line the screen is read,
// so the agent has redrawn it.
const promptGoneDelay = 300 * time.Millisecond

// evPromptGone reports a blocked pane whose screen shows no prompt.
type evPromptGone struct {
	conv int64
	at   time.Time
}

// checkPromptGone asks for a look at the screen of a session whose
// transcript went on (core).
func (d *daemon) checkPromptGone(conv int64, now time.Time) {
	if _, ok := d.promptChecks[conv]; !ok {
		d.promptChecks[conv] = now.Add(promptGoneDelay)
	}
}

// tickPromptChecks reads the screens that are due, of the panes that are
// still blocked (core).
func (d *daemon) tickPromptChecks(now time.Time) {
	for conv, due := range d.promptChecks {
		if now.Before(due) {
			continue
		}
		delete(d.promptChecks, conv)
		p, ok, err := d.st.PaneByConversation(d.ctx, conv)
		if err != nil || !ok || p.State != "blocked" || !p.GoneAt.IsZero() {
			continue
		}
		var hashes []string // the open alerts' screens
		if th, ok := d.threadOf(conv); ok {
			if als, err := d.st.OpenAlerts(d.ctx, th.ID); err == nil {
				for _, a := range als {
					hashes = append(hashes, a.ScreenHash)
				}
			}
		}
		d.spawn(func() {
			screen, err := d.rx.Capture(d.ctx, p.Session, p.Block)
			if err != nil {
				return // the next transcript line asks again
			}
			if screenHasPrompt(lastLines(screen, alertLines), hashes) {
				return
			}
			d.emit(evPromptGone{conv: conv, at: d.now()})
		})
	}
}

// screenHasPrompt tells whether the bottom lines of a screen may still show
// a prompt: one that parsePromptView reads, key hints that only a prompt
// shows ("Esc to cancel", "Enter to select"), or the same screen as an open
// alert. A prompt in a layout that rex-tg cannot read still counts, so it is
// never taken for gone.
func screenHasPrompt(lines []string, alertHashes []string) bool {
	if parsePromptView(lines, true).ok {
		return true
	}
	for _, l := range lines {
		t := strings.ToLower(l)
		for _, w := range []string{"esc to cancel", "enter to select", "enter to confirm", "do you want to proceed", "would you like to run"} {
			if strings.Contains(t, w) {
				return true
			}
		}
	}
	h := alertHash(lines)
	return slices.Contains(alertHashes, h)
}

// onPromptGone moves a pane that is still blocked to working (core). The
// registry's change closes the topic's alerts and lets waiting messages go.
func (d *daemon) onPromptGone(e evPromptGone) {
	p, ok, err := d.st.PaneByConversation(d.ctx, e.conv)
	if err != nil || !ok || p.State != "blocked" {
		return
	}
	d.signal(e.conv, registry.Signal{State: "working", At: e.at})
}
