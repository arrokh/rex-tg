package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"rex-tg/internal/config"
	"rex-tg/internal/format"
	"rex-tg/internal/inbound"
	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// Scheduled messages (/at). A schedule is a text that rex-tg queues for a
// topic at a set time, as if the user had typed it then. It goes through
// queueText, so every normal check applies: the agent is in front, no prompt
// is open, input.while_busy, and the waiting notice.
//
// A schedule is kept in kv "sched:<id>" as JSON until it is queued, cancelled,
// or its topic closes. At most once: the row that is queued has an id made
// from the schedule's id, so a schedule that is due queues once, also when the
// daemon stops between the queueing and the delete of the kv key.

const (
	schedPrefix = "sched:"
	// schedBase moves the ids of the queued rows into a range that real
	// update ids (minus the update id) never reach.
	schedBase = int64(1) << 50
	// maxSchedules is the most schedules of one topic.
	maxSchedules = 20
	// schedLate: a schedule that came due while rex-tg was down is sent when
	// it is less late than this, and dropped with a note otherwise.
	schedLate = time.Hour
	// schedCheck is how often the due schedules are looked for.
	schedCheck = time.Second
	// schedMaxDelay is the furthest time that /at accepts.
	schedMaxDelay = 30 * 24 * time.Hour
)

// schedule is one scheduled message (kv "sched:<id>").
type schedule struct {
	Thread  int64  `json:"thread"`
	At      int64  `json:"at"` // unix seconds
	Text    string `json:"text"`
	Created int64  `json:"created"`
}

// atError is a message for the user, as the error of parseAt.
type atError string

func (e atError) Error() string { return string(e) }

func schedKey(id int64) string { return schedPrefix + strconv.FormatInt(id, 10) }

var errAtUsage = atError("Usage: /at 09:00 &lt;text&gt; or /at 30m &lt;text&gt;. /at lists, /at cancel &lt;n&gt; removes one.")

// parseAt splits the arguments of /at into the time and the text. The time
// is an HH:MM clock time (the next one, in now's zone) or a delay such as
// 30m, 2h, or 1h30m. The result is always after now.
func parseAt(args string, now time.Time) (at time.Time, text string, err error) {
	args = strings.TrimSpace(args)
	head, rest := args, ""
	if i := strings.IndexAny(args, " \t\r\n"); i >= 0 {
		head, rest = args[:i], strings.TrimSpace(args[i:])
	}
	if head == "" || rest == "" {
		return time.Time{}, "", errAtUsage
	}
	if h, m, ok := parseClock(head); ok {
		at = time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
		if !at.After(now) {
			at = time.Date(now.Year(), now.Month(), now.Day()+1, h, m, 0, 0, now.Location())
		}
		return at, rest, nil
	}
	if last := head[len(head)-1]; last == 's' || last == 'm' || last == 'h' {
		if dur, err := time.ParseDuration(strings.ToLower(head)); err == nil {
			if dur <= 0 {
				return time.Time{}, "", atError("The delay must be more than zero.")
			}
			if dur > schedMaxDelay {
				return time.Time{}, "", atError("The delay is at most 30 days.")
			}
			return now.Add(dur), rest, nil
		}
	}
	return time.Time{}, "", errAtUsage
}

func parseClock(s string) (h, m int, ok bool) {
	a, b, found := strings.Cut(s, ":")
	if !found || len(a) < 1 || len(a) > 2 || len(b) != 2 {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(a)
	m, err2 := strconv.Atoi(b)
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// inText says how long it is until a time, such as "7h 12m" or "30s".
func inText(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int((d+time.Second-1)/time.Second))
	}
	mins := int((d + time.Minute - 1) / time.Minute)
	if mins < 60 {
		return fmt.Sprintf("%dm", mins)
	}
	if mins < 24*60 {
		if mins%60 == 0 {
			return fmt.Sprintf("%dh", mins/60)
		}
		return fmt.Sprintf("%dh %dm", mins/60, mins%60)
	}
	return fmt.Sprintf("%dd %dh", mins/(24*60), mins%(24*60)/60)
}

// whenText is the time of a schedule: the clock time, with the date when it
// is not today.
func whenText(at, now time.Time) string {
	at = at.In(now.Location())
	y1, m1, d1 := at.Date()
	y2, m2, d2 := now.Date()
	if y1 == y2 && m1 == m2 && d1 == d2 {
		return at.Format("15:04")
	}
	return at.Format("Jan 2 15:04")
}

func schedPreview(text string) string {
	return format.Escape(format.Truncate(strings.Join(strings.Fields(text), " "), 60))
}

// loadSchedules reads the schedules from the store once (core).
func (d *daemon) loadSchedules() {
	if d.schedLoaded {
		return
	}
	keys, err := d.st.KeysWithPrefix(d.ctx, schedPrefix)
	if err != nil {
		d.log.Warn("schedules: list", "err", err)
		return
	}
	d.schedLoaded = true
	for _, k := range keys {
		id, err := strconv.ParseInt(strings.TrimPrefix(k, schedPrefix), 10, 64)
		if err != nil || id <= 0 || id >= schedBase {
			continue
		}
		v, ok, err := d.st.Get(d.ctx, k)
		if err != nil || !ok {
			continue
		}
		var s schedule
		if json.Unmarshal([]byte(v), &s) != nil || s.Text == "" {
			_ = d.st.Delete(d.ctx, k)
			continue
		}
		d.sched[id] = s
	}
}

// topicSchedules returns the ids of a topic's schedules, by time.
func (d *daemon) topicSchedules(thread int64) []int64 {
	var ids []int64
	for id, s := range d.sched {
		if s.Thread == thread {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := d.sched[ids[i]], d.sched[ids[j]]
		if a.At != b.At {
			return a.At < b.At
		}
		return ids[i] < ids[j]
	})
	return ids
}

func (d *daemon) dropSchedule(id int64) {
	delete(d.sched, id)
	if err := d.st.Delete(d.ctx, schedKey(id)); err != nil {
		d.log.Warn("schedules: delete", "err", err)
	}
}

// dropSchedules removes all schedules of a topic (core).
func (d *daemon) dropSchedules(thread int64) {
	d.loadSchedules()
	for id, s := range d.sched {
		if s.Thread == thread {
			d.dropSchedule(id)
		}
	}
}

// atCommand runs /at in a topic (core).
func (d *daemon) atCommand(th store.Thread, args string, reply func(html string)) {
	d.loadSchedules()
	now := d.now().In(time.Local)
	args = strings.TrimSpace(args)
	if args == "" {
		ids := d.topicSchedules(th.ID)
		if len(ids) == 0 {
			reply("No scheduled messages. Use /at 09:00 &lt;text&gt; or /at 30m &lt;text&gt;.")
			return
		}
		var b strings.Builder
		b.WriteString("⏰ <b>Scheduled messages</b>")
		for i, id := range ids {
			s := d.sched[id]
			at := time.Unix(s.At, 0).In(time.Local)
			fmt.Fprintf(&b, "\n%d. %s (in %s): %s", i+1, whenText(at, now), inText(at.Sub(now)), schedPreview(s.Text))
		}
		b.WriteString("\n/at cancel &lt;n&gt; removes one.")
		reply(b.String())
		return
	}
	if head, rest, _ := strings.Cut(args, " "); strings.EqualFold(head, "cancel") {
		n, err := strconv.Atoi(strings.TrimSpace(rest))
		ids := d.topicSchedules(th.ID)
		if err != nil || n < 1 || n > len(ids) {
			reply("Usage: /at cancel &lt;n&gt;, with n from the list that /at shows.")
			return
		}
		s := d.sched[ids[n-1]]
		d.dropSchedule(ids[n-1])
		reply("✖ Cancelled: " + schedPreview(s.Text))
		return
	}
	at, text, err := parseAt(args, now)
	if err != nil {
		reply(err.Error())
		return
	}
	if len([]rune(text)) > config.MaxMessageLength {
		reply(fmt.Sprintf("The text is too long: at most %d characters.", config.MaxMessageLength))
		return
	}
	if len(d.topicSchedules(th.ID)) >= maxSchedules {
		reply(fmt.Sprintf("This topic has %d scheduled messages already. Use /at cancel &lt;n&gt; first.", maxSchedules))
		return
	}
	id := d.now().UnixMilli()
	for {
		if _, used := d.sched[id]; !used && id < schedBase {
			break
		}
		id++
	}
	s := schedule{Thread: th.ID, At: at.Unix(), Text: text, Created: d.now().Unix()}
	b, _ := json.Marshal(s)
	if err := d.st.Set(d.ctx, schedKey(id), string(b)); err != nil {
		d.log.Warn("schedules: save", "err", err)
		reply("Could not save the schedule. Try again.")
		return
	}
	d.sched[id] = s
	reply(fmt.Sprintf("⏰ Scheduled for %s (in %s): %s", whenText(at, now), inText(at.Sub(now)), schedPreview(text)))
}

// tickSchedules queues the schedules that are due (core).
func (d *daemon) tickSchedules(now time.Time) {
	if now.Sub(d.lastSched) < schedCheck {
		return
	}
	d.lastSched = now
	d.loadSchedules()
	for id, s := range d.sched {
		at := time.Unix(s.At, 0)
		if now.Before(at) {
			continue
		}
		th, err := d.st.Thread(d.ctx, s.Thread)
		if errors.Is(err, store.ErrNotFound) || (err == nil && th.Status == store.ThreadClosed) {
			d.dropSchedule(id)
			continue
		}
		if err != nil {
			continue
		}
		late := now.Sub(at)
		if late >= schedLate {
			d.dropSchedule(id)
			d.sayQuiet(th, fmt.Sprintf("⏰ Missed: %s (rex-tg was not running at %s)", schedPreview(s.Text), at.In(time.Local).Format("15:04")))
			continue
		}
		if !d.queueText(th.ID, schedBase+id, 0, s.Text, "text", inbound.ActText) {
			continue // the store failed: try again at the next look
		}
		d.dropSchedule(id)
		note := "⏰ Sending the scheduled message: " + schedPreview(s.Text)
		if late >= time.Minute {
			note += fmt.Sprintf(" (%s late, rex-tg was not running at %s)", inText(late), at.In(time.Local).Format("15:04"))
		}
		d.sayQuiet(th, note)
	}
}

// sayQuiet posts a note in a topic without sound (worker).
func (d *daemon) sayQuiet(th store.Thread, html string) {
	chat, topic := th.ChatID, th.MessageThreadID
	d.spawn(func() {
		err := d.snd.Call(sender.PriorityAnswer, func(ctx context.Context, tg *telegram.Client) error {
			_, err := tg.SendMessage(ctx, telegram.SendMessageParams{
				ChatID: chat, MessageThreadID: topic, Text: html, ParseMode: "HTML", DisableNotification: true,
			})
			return err
		})
		if err != nil && d.ctx.Err() == nil {
			d.log.Warn("schedules: note", "err", err)
		}
	})
}
