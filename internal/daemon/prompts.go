package daemon

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"rex-tg/internal/format"
	"rex-tg/internal/inbound"
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

// blockedNotice tells the topic that an alert's time ran out (core): the
// prompt went away unanswered at about its deadline. Only promptEnded calls
// it: a topic that closes is no sign of a block.
func (d *daemon) blockedNotice(th store.Thread, al store.Alert) {
	dl, ok := d.deadlines[al.ID]
	if !ok {
		return
	}
	delete(d.deadlines, al.ID)
	if d.now().Before(dl.at.Add(-min(deadlineSlack, dl.limit/10) - dl.early)) {
		return // it went before its time: answered on the Mac
	}
	d.replyTopic(th, fmt.Sprintf("blocked:%d", al.ID), fmt.Sprintf(
		"⌛ <b>Blocked</b>: no answer within %s, so %s denied the request.",
		clockText(dl.limit), format.Escape(dl.agent)))
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
		d.emit(evQueueText{threadID: th.ID, orig: upd, tgMsg: msg, text: note, kind: "text", action: inbound.ActText,
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

// promptEnded closes the alerts of a pane that left its prompt (core), and
// tells the topic when a prompt went away at its time limit.
func (d *daemon) promptEnded(th store.Thread) {
	if als, err := d.st.OpenAlerts(d.ctx, th.ID); err == nil {
		for _, a := range als {
			d.blockedNotice(th, a)
		}
	}
	d.closeAlerts(th, store.AlertGone)
}
