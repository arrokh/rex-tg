package sender_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
	"rex-tg/internal/telegram/tgtest"
)

const chat = int64(100)

var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

// clock is a fake clock; Sleep advances it at once and records the wait.
type clock struct {
	mu      sync.Mutex
	now     time.Time
	sleeps  []time.Duration
	onSleep func(d time.Duration) // set before the sender starts
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Sleep(ctx context.Context, d time.Duration) error {
	if c.onSleep != nil {
		c.onSleep(d)
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.sleeps = append(c.sleeps, d)
	c.mu.Unlock()
	return ctx.Err()
}

func (c *clock) slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

type env struct {
	t   *testing.T
	st  *store.Store
	srv *tgtest.Server
	clk *clock
	s   *sender.Sender
}

func newEnv(t *testing.T, opt sender.Options, clientOpts ...telegram.Option) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state", "rex-tg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := tgtest.New(t)
	clk := &clock{now: t0}
	opt.Now, opt.Sleep = clk.Now, clk.Sleep
	clientOpts = append([]telegram.Option{telegram.WithClock(clk.Now)}, clientOpts...)
	return &env{t: t, st: st, srv: srv, clk: clk, s: sender.New(st, srv.Client(clientOpts...), opt)}
}

func (e *env) topic(n int64) store.Thread {
	e.t.Helper()
	c, err := e.st.UpsertConversation(ctx, store.Conversation{
		Runtime: agents.Claude, NativeID: "s" + string(rune('a'+n)), Transcript: "/tmp/x.jsonl", FirstSeenAt: t0,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	th, err := e.st.LinkTopic(ctx, chat, n, c.ID, "topic", t0)
	if err != nil {
		e.t.Fatal(err)
	}
	return th
}

var ctx = context.Background()

func (e *env) enqueue(th store.Thread, key, format, content string) int64 {
	e.t.Helper()
	id, _, err := e.st.Enqueue(ctx, store.OutboxItem{
		ThreadID: th.ID, Key: key, Kind: "answer",
		Parts: []store.PartContent{{Content: content, Format: format, Filename: "f.txt"}},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// start runs the sender; the returned func stops it and waits.
func (e *env) start() func() {
	c, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = e.s.Run(c) }()
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); <-done }) }
	e.t.Cleanup(stop)
	return stop
}

// waitShown waits for the stored progress row to be "shown". Wait for this,
// not for the fake server's message, before stopping the sender: a stop while
// the reply is still in flight makes the send "unknown", and that is never
// retried.
func (e *env) waitShown(threadID int64) {
	e.t.Helper()
	waitFor(e.t, "progress shown", func() bool {
		p, ok, _ := e.st.Progress(ctx, threadID)
		return ok && p.State == store.ProgressShown
	})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (e *env) rowState(id int64) string {
	row, err := e.st.OutboxRow(ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return row.State
}

func (e *env) waitState(id int64, state string) {
	e.t.Helper()
	waitFor(e.t, "outbox state "+state, func() bool { return e.rowState(id) == state })
}

func (e *env) event(kind sender.EventKind) sender.Event {
	e.t.Helper()
	select {
	case ev := <-e.s.Events():
		if ev.Kind != kind {
			e.t.Fatalf("event = %+v, want kind %v", ev, kind)
		}
		return ev
	case <-time.After(5 * time.Second):
		e.t.Fatalf("no event of kind %v", kind)
	}
	return sender.Event{}
}

func texts(ms []tgtest.Message) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Text)
	}
	return out
}

func methods(srv *tgtest.Server) []string {
	var out []string
	for _, c := range srv.Calls("") {
		out = append(out, c.Method)
	}
	return out
}

func TestOrderedDelivery(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	var last int64
	for _, k := range []string{"a", "b", "c", "d"} {
		last = e.enqueue(th, k, store.FormatHTML, "<b>"+k+"</b>")
	}
	e.start()
	e.waitState(last, store.OutboxSent)
	if got := strings.Join(texts(e.srv.Messages(chat)), ","); got != "<b>a</b>,<b>b</b>,<b>c</b>,<b>d</b>" {
		t.Fatalf("order = %s", got)
	}
	if m := e.srv.Messages(chat)[0]; m.ParseMode != "HTML" || m.Silent || m.ThreadID != 1 {
		t.Fatalf("message = %+v", m)
	}
}

func TestTwoTopicsInterleave(t *testing.T) {
	e := newEnv(t, sender.Options{})
	a, b := e.topic(1), e.topic(2)
	var last int64
	for _, k := range []string{"1", "2", "3"} {
		e.enqueue(a, "a"+k, store.FormatPlain, "a"+k)
		last = e.enqueue(b, "b"+k, store.FormatPlain, "b"+k)
	}
	e.start()
	e.waitState(last, store.OutboxSent)
	if got := strings.Join(texts(e.srv.Messages(chat)), ","); got != "a1,b1,a2,b2,a3,b3" {
		t.Fatalf("order = %s", got)
	}
}

func TestParseErrorFallsBackToPlain(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	id := e.enqueue(th, "k", store.FormatHTML, "<b>Tom &amp; Jerry</b> &lt;3 &quot;hi&quot;")
	e.srv.FailNext("sendMessage", 1, 400, "Bad Request: can't parse entities: Unsupported start tag", 0)
	e.start()
	e.waitState(id, store.OutboxSent)
	ms := e.srv.Messages(chat)
	if len(ms) != 1 || ms[0].Text != `Tom & Jerry <3 "hi"` || ms[0].ParseMode != "" {
		t.Fatalf("messages = %+v", ms)
	}
	if n := len(e.srv.Calls("sendMessage")); n != 2 {
		t.Fatalf("sendMessage calls = %d, want 2", n)
	}
}

func TestRateLimitWaitsAndRetriesOnce(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	id := e.enqueue(th, "k", store.FormatPlain, "hello")
	e.srv.FailNext("sendMessage", 1, 429, "Too Many Requests: retry after 3", 3)
	e.start()
	e.waitState(id, store.OutboxSent)
	if n := len(e.srv.Messages(chat)); n != 1 {
		t.Fatalf("messages = %d, want 1", n)
	}
	if n := len(e.srv.Calls("sendMessage")); n != 2 {
		t.Fatalf("sendMessage calls = %d, want 2", n)
	}
	var long bool
	for _, d := range e.clk.slept() {
		if d >= 3*time.Second {
			long = true
		}
	}
	if !long {
		t.Fatalf("sleeps = %v, want one of at least 3s", e.clk.slept())
	}
}

// A long 429 holds only its method. A rich answer goes out as a plain
// message at once, the next rich answer does not ask Telegram again, and the
// sender does not pause for the hour.
func TestLongRateLimitHoldsOnlyItsMethod(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th1, th2 := e.topic(1), e.topic(2)
	a := e.enqueue(th1, "a", store.FormatRich, "first answer")
	b := e.enqueue(th1, "b", store.FormatRich, "second answer")
	c := e.enqueue(th2, "c", store.FormatPlain, "other topic")
	e.srv.FailNext("sendRichMessage", 1, 429, "Too Many Requests: retry after 3600", 3600)
	e.start()
	for _, id := range []int64{a, b, c} {
		e.waitState(id, store.OutboxSent)
	}
	if n := len(e.srv.Calls("sendRichMessage")); n != 1 {
		t.Errorf("sendRichMessage calls = %d, want 1: the hold refuses the second", n)
	}
	if n := len(e.srv.Calls("sendMessage")); n != 3 {
		t.Errorf("sendMessage calls = %d, want 3", n)
	}
	var texts []string
	for _, m := range e.visible() {
		texts = append(texts, m.Text)
	}
	if got := strings.Join(texts, "|"); !strings.Contains(got, "first answer") || !strings.Contains(got, "second answer") {
		t.Errorf("messages = %q", got)
	}
	for _, d := range e.clk.slept() {
		if d >= telegram.HoldAfter {
			t.Fatalf("slept %v: the hold paused every send", d)
		}
	}
}

// A Call whose method is held waits for the end of the hold, without using
// its attempts, and then goes out once.
func TestHeldCallWaitsForTheHold(t *testing.T) {
	e := newEnv(t, sender.Options{})
	e.srv.FailNext("sendMessage", 1, 429, "Too Many Requests: retry after 3600", 3600)
	e.start()
	start := e.clk.Now()
	err := e.s.Call(sender.PriorityAlert, func(ctx context.Context, tg *telegram.Client) error {
		_, err := tg.SendMessage(ctx, telegram.SendMessageParams{ChatID: chat, Text: "alert"})
		return err
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if n := len(e.srv.Calls("sendMessage")); n != 2 {
		t.Errorf("sendMessage calls = %d, want 2", n)
	}
	if waited := e.clk.Now().Sub(start); waited < time.Hour-time.Second {
		t.Errorf("waited %v, want the hour of the hold", waited)
	}
}

// A held method keeps back only the topic whose part met it: another topic
// goes on, and the held part goes out once after the hold, without asking
// Telegram during it.
func TestHeldPartWaitsWhileOtherTopicsSend(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th1, th2 := e.topic(1), e.topic(2)
	// The held part is older, so it goes first (NextParts orders by outbox
	// id): the other topic's part comes after the 429.
	held := e.enqueue(th1, "h", store.FormatPlain, "held")
	other := e.enqueue(th2, "o", store.FormatRich, "other topic")
	e.srv.FailNext("sendMessage", 1, 429, "Too Many Requests: retry after 3600", 3600)
	// The fake clock skips the hour at once, so the test looks at what was
	// sent when the sender went to sleep for it.
	var mu sync.Mutex
	richAtLongSleep := -1
	e.clk.onSleep = func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		if d >= 30*time.Minute && richAtLongSleep < 0 {
			richAtLongSleep = len(e.srv.Calls("sendRichMessage"))
		}
	}
	start := e.clk.Now()
	e.start()
	e.waitState(other, store.OutboxSent)
	e.waitState(held, store.OutboxSent)
	if n := len(e.srv.Calls("sendMessage")); n != 2 {
		t.Errorf("sendMessage calls = %d, want 2: none during the hold", n)
	}
	mu.Lock()
	if richAtLongSleep != 1 {
		t.Errorf("rich messages sent before the long sleep = %d, want 1: the other topic must not wait", richAtLongSleep)
	}
	mu.Unlock()
	if waited := e.clk.Now().Sub(start); waited < time.Hour-time.Minute {
		t.Errorf("the held part went out after %v, want the hour of the hold", waited)
	}
}

// A progress edit that meets a held method waits for the hold, once.
func TestHeldProgressEditWaits(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: "one"})
	e.start()
	e.waitShown(th.ID)
	e.srv.FailNext("editMessageText", 1, 429, "Too Many Requests: retry after 3600", 3600)
	start := e.clk.Now()
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Edit, HTML: "two"})
	waitFor(t, "the edit", func() bool { return e.srv.Messages(chat)[0].Text == "two" })
	if n := len(e.srv.Calls("editMessageText")); n != 2 {
		t.Errorf("editMessageText calls = %d, want 2", n)
	}
	if waited := e.clk.Now().Sub(start); waited < time.Hour-time.Minute {
		t.Errorf("the edit went out after %v, want the hour of the hold", waited)
	}
}

func noticeText(id int64, part int) string {
	return "⚠️ Part " + string(rune('0'+part+1)) + " of reply #" + string(rune('0'+id)) + " may not have arrived."
}

// visible returns the chat's messages that were not deleted.
func (e *env) visible() []tgtest.Message {
	var out []tgtest.Message
	for _, m := range e.srv.Messages(chat) {
		if !m.Deleted {
			out = append(out, m)
		}
	}
	return out
}

// A part whose send got no answer, and that Telegram did not post: the check
// finds no message number between the part's send and its note, so the part
// is sent again, once, and the note goes. Nothing is posted twice.
func TestUnknownPartThatDidNotArriveIsSentAgain(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	id := e.enqueue(th, "k", store.FormatPlain, "hello")
	e.srv.FailNext("sendMessage", 1, 500, "Internal Server Error", 0) // nothing posted
	e.start()
	ev := e.event(sender.PartUnknown)
	if ev.OutboxID != id || ev.Part != 0 || ev.ThreadID != th.ID {
		t.Fatalf("event = %+v", ev)
	}
	e.waitState(id, store.OutboxSent)
	waitFor(t, "the note to go", func() bool { return len(e.visible()) == 1 })
	if ms := e.visible(); ms[0].Text != "hello" {
		t.Fatalf("messages = %+v, want the reply once", ms)
	}
	if n := len(e.srv.Calls("sendMessage")); n != 3 {
		t.Fatalf("sendMessage calls = %d, want 3: the reply, the note, the reply again", n)
	}
	if note, _ := e.srv.Calls("sendMessage")[1].Params["text"].(string); !strings.Contains(note, "Checking whether it arrived") {
		t.Errorf("note = %q", note)
	}
}

// A part that Telegram did post, but whose answer was lost: the check finds
// a number that rex-tg never saw, so it does not send the part again. The
// note becomes the notice, whose button sends the reply again on request.
func TestUnknownPartThatMayHaveArrivedGetsTheNotice(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	id := e.enqueue(th, "k", store.FormatPlain, "hello")
	e.srv.LoseAnswerNext("sendMessage", 1) // posted, but the answer is lost
	e.start()
	waitFor(t, "the notice", func() bool {
		ms := e.visible()
		return len(ms) == 2 && ms[1].Text == noticeText(id, 0)
	})
	ms := e.visible()
	if ms[0].Text != "hello" {
		t.Fatalf("messages = %+v", ms)
	}
	if m := ms[1].Markup; m == nil || len(m.InlineKeyboard) != 1 || m.InlineKeyboard[0][0].CallbackData != sender.ResendButtonData(id) {
		t.Fatalf("notice buttons = %+v, want one Send again button for #%d", ms[1].Markup, id)
	}
	time.Sleep(30 * time.Millisecond)
	if st := e.rowState(id); st != store.OutboxUnknown || len(e.srv.Calls("sendMessage")) != 2 {
		t.Fatalf("state %s after %d sendMessage calls; want unknown after the reply and the note", st, len(e.srv.Calls("sendMessage")))
	}
	if p, _ := e.st.Part(ctx, id, 0); !p.Checked {
		t.Fatal("the part is not marked checked")
	}
}

// lostSend sets a part up as sent with an unknown outcome that never reached
// Telegram, while the newest known message number was 0. Then you write a
// message in the chat; noted says whether rex-tg recorded its number, as the
// daemon records every update.
func (e *env) lostSend(th store.Thread, noted bool) int64 {
	e.t.Helper()
	id := e.enqueue(th, "k", store.FormatPlain, "hello")
	if ok, err := e.st.ClaimPart(ctx, id, 0, 0); err != nil || !ok {
		e.t.Fatalf("claim: %v %v", ok, err)
	}
	if err := e.st.MarkPartUnknown(ctx, id, 0); err != nil {
		e.t.Fatal(err)
	}
	u := e.srv.PushMessage(chat, 1, 7, "a message from you")
	if noted {
		if err := e.st.NoteMessage(ctx, chat, u.Message.MessageID, t0); err != nil {
			e.t.Fatal(err)
		}
	}
	if err := e.s.Recover(ctx); err != nil { // the check of the unknown part
		e.t.Fatal(err)
	}
	return id
}

// Messages between the part and the note that rex-tg knows, such as yours,
// do not stop the check.
func TestCheckCountsKnownMessages(t *testing.T) {
	e := newEnv(t, sender.Options{})
	id := e.lostSend(e.topic(1), true)
	e.start()
	e.waitState(id, store.OutboxSent)
	waitFor(t, "the note to go", func() bool { return len(e.visible()) == 2 })
}

// A message whose number rex-tg did not record could be the part: no second
// send, the notice instead.
func TestCheckStopsAtAnUnknownNumber(t *testing.T) {
	e := newEnv(t, sender.Options{})
	id := e.lostSend(e.topic(1), false)
	e.start()
	waitFor(t, "the notice", func() bool {
		ms := e.visible()
		return len(ms) == 2 && ms[1].Text == noticeText(id, 0)
	})
	if n := len(e.srv.Calls("sendMessage")); n != 1 {
		t.Fatalf("sendMessage calls = %d, want only the note", n)
	}
}

// A note that could not be sent (no network) is tried again later.
func TestCheckWaitsForTheNetwork(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	id := e.enqueue(th, "k", store.FormatPlain, "hello")
	e.srv.FailNext("sendMessage", 1, 500, "Internal Server Error", 0)
	e.srv.FailNext("sendMessage", 2, 429, "Too Many Requests: retry after 1", 1) // the note waits
	e.start()
	e.waitState(id, store.OutboxSent)
	waitFor(t, "the note to go", func() bool { return len(e.visible()) == 1 })
}

// /resend before the check: the part is sent, and no note is posted.
func TestResendBeforeTheCheck(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	id := e.enqueue(th, "k", store.FormatPlain, "hello")
	if _, err := e.st.ClaimPart(ctx, id, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := e.st.MarkPartUnknown(ctx, id, 0); err != nil {
		t.Fatal(err)
	}
	if n, err := e.st.ResendUnknown(ctx, id); err != nil || n != 1 {
		t.Fatalf("resend: %d %v", n, err)
	}
	if err := e.s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	e.start()
	e.waitState(id, store.OutboxSent)
	time.Sleep(30 * time.Millisecond)
	if n := len(e.srv.Calls("sendMessage")); n != 1 {
		t.Fatalf("sendMessage calls = %d, want only the reply", n)
	}
}

func TestTimeoutIsUnknown(t *testing.T) {
	e := newEnv(t, sender.Options{}, telegram.WithTimeout(200*time.Millisecond))
	th := e.topic(1)
	id := e.enqueue(th, "k", store.FormatPlain, "hello")
	e.srv.Delay("sendMessage", 2*time.Second) // the fake drops a call whose client gave up
	e.start()
	e.event(sender.PartUnknown)
	e.srv.Delay("sendMessage", 0)
	e.waitState(id, store.OutboxSent) // the check found that it never arrived
	waitFor(t, "the note to go", func() bool { return len(e.visible()) == 1 })
	if ms := e.visible(); ms[0].Text != "hello" {
		t.Fatalf("messages = %+v", ms)
	}
}

func TestThreadGone(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	id := e.enqueue(th, "k", store.FormatPlain, "hello")
	e.srv.DeleteTopic(chat, 1)
	e.start()
	e.waitState(id, store.OutboxFailed)
	if ev := e.event(sender.ThreadGone); ev.ThreadID != th.ID {
		t.Fatalf("event = %+v", ev)
	}
}

func TestBlockedPausesUntilRestart(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	e.enqueue(th, "a", store.FormatPlain, "one")
	second := e.enqueue(th, "b", store.FormatPlain, "two")
	e.srv.FailNext("sendMessage", 1, 403, "Forbidden: bot was blocked by the user", 0)
	stop := e.start()
	e.event(sender.Blocked)
	e.s.Wake()
	time.Sleep(50 * time.Millisecond)
	if st := e.rowState(second); st != store.OutboxPending {
		t.Fatalf("second = %s while blocked", st)
	}
	stop()
	e.start()
	e.waitState(second, store.OutboxSent)
}

func TestDocument(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	id := e.enqueue(th, "k", store.FormatDocument, "file body")
	e.start()
	e.waitState(id, store.OutboxSent)
	ms := e.srv.Messages(chat)
	if len(ms) != 1 || ms[0].Filename != "f.txt" || string(ms[0].File) != "file body" || ms[0].ThreadID != 1 {
		t.Fatalf("messages = %+v", ms)
	}
}

func TestMutedAndSilent(t *testing.T) {
	e := newEnv(t, sender.Options{})
	loud, muted := e.topic(1), e.topic(2)
	if err := e.st.SetThreadSettings(ctx, muted.ID, store.ModeProgress, store.VerboseOff, false, true); err != nil {
		t.Fatal(err)
	}
	e.enqueue(loud, "a", store.FormatPlain, "loud")
	e.enqueue(muted, "b", store.FormatPlain, "muted")
	_, _, _ = e.st.Enqueue(ctx, store.OutboxItem{ThreadID: loud.ID, Key: "c", Kind: "answer", Silent: true,
		Parts: []store.PartContent{{Content: "quiet", Format: store.FormatPlain}}})
	last := e.enqueue(loud, "d", store.FormatPlain, "loud2")
	e.start()
	e.waitState(last, store.OutboxSent)
	waitFor(t, "all sent", func() bool { return len(e.srv.Messages(chat)) == 4 })
	got := map[string]bool{}
	for _, m := range e.srv.Messages(chat) {
		got[m.Text] = m.Silent
	}
	if got["loud"] || got["loud2"] || !got["muted"] || !got["quiet"] {
		t.Fatalf("silent flags = %v", got)
	}
}

func TestProgressLifecycle(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)

	// Show with a pending edit merges into one send of the latest text.
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: "<b>one</b>"})
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Edit, HTML: "<b>two</b>"})
	stop := e.start()
	waitFor(t, "shown", func() bool {
		p, ok, _ := e.st.Progress(ctx, th.ID)
		return ok && p.State == store.ProgressShown
	})
	stop()
	ms := e.srv.Messages(chat)
	if len(ms) != 1 || ms[0].Text != "<b>two</b>" || !ms[0].Silent || ms[0].ThreadID != 1 {
		t.Fatalf("after show: %+v", ms)
	}
	p, _, _ := e.st.Progress(ctx, th.ID)
	first := p.TGMessageID
	if first != ms[0].ID {
		t.Fatalf("row message id %d, want %d", first, ms[0].ID)
	}

	// Two edits merge into one call.
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Edit, HTML: "three"})
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Edit, HTML: "four"})
	stop = e.start()
	waitFor(t, "edit", func() bool { return len(e.srv.Calls("editMessageText")) == 1 })
	stop()
	if got := e.srv.Messages(chat)[0].Text; got != "four" {
		t.Fatalf("text = %q", got)
	}

	// Repost: new message first, then the old one is deleted.
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Repost, HTML: "five"})
	e.start()
	waitFor(t, "repost", func() bool { return len(e.srv.Calls("deleteMessage")) == 1 })
	ms = e.srv.Messages(chat)
	if len(ms) != 2 || !ms[0].Deleted || ms[1].Deleted || ms[1].Text != "five" || !ms[1].Silent {
		t.Fatalf("after repost: %+v", ms)
	}
	p, _, _ = e.st.Progress(ctx, th.ID)
	if p.TGMessageID != ms[1].ID || p.State != store.ProgressShown {
		t.Fatalf("row = %+v", p)
	}
	if m := methods(e.srv); m[len(m)-2] != "sendMessage" {
		t.Fatalf("calls = %v", m)
	}

	// Finish: last edit; the row stays so that a corrected summary can follow.
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Finish, HTML: "<b>done</b>"})
	waitFor(t, "finish edit", func() bool { return e.srv.Messages(chat)[1].Text == "<b>done</b>" })
	ms = e.srv.Messages(chat)
	if ms[1].Text != "<b>done</b>" || ms[1].Edits != 1 {
		t.Fatalf("after finish: %+v", ms[1])
	}
}

func TestEditTargetGoneShowsAgain(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: "one"})
	stop := e.start()
	e.waitShown(th.ID)
	stop()
	old := e.srv.Messages(chat)[0].ID
	e.srv.DeleteMessageOutside(chat, old)
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Edit, HTML: "two"})
	e.start()
	waitFor(t, "new message", func() bool {
		p, ok, _ := e.st.Progress(ctx, th.ID)
		return ok && p.State == store.ProgressShown && p.TGMessageID != old
	})
	ms := e.srv.Messages(chat)
	if len(ms) != 2 || ms[1].Text != "two" || !ms[1].Silent {
		t.Fatalf("messages = %+v", ms)
	}
}

func TestUnknownShowIsNeverRetried(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	e.srv.FailNext("sendMessage", 1, 500, "boom", 0)
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: "one"})
	e.start()
	waitFor(t, "unknown", func() bool {
		p, ok, _ := e.st.Progress(ctx, th.ID)
		return ok && p.State == store.ProgressUnknown
	})
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Edit, HTML: "two"})
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: "again"})
	time.Sleep(50 * time.Millisecond)
	if n := len(e.srv.Calls("sendMessage")); n != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", n)
	}
	if n := len(e.srv.Calls("editMessageText")); n != 0 {
		t.Fatalf("edit calls = %d", n)
	}
	// After Finish, a new Show works again.
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Finish, HTML: "end"})
	waitFor(t, "row gone", func() bool { _, ok, _ := e.st.Progress(ctx, th.ID); return !ok })
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: "next turn"})
	waitFor(t, "second turn", func() bool { return len(e.srv.Messages(chat)) == 1 })
}

func TestEditRetriesWithBackoff(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: "one"})
	stop := e.start()
	e.waitShown(th.ID)
	stop()
	e.srv.FailNext("editMessageText", 2, 500, "boom", 0)
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Edit, HTML: "two"})
	e.start()
	waitFor(t, "edited", func() bool { return e.srv.Messages(chat)[0].Text == "two" })
	if n := len(e.srv.Calls("editMessageText")); n != 3 {
		t.Fatalf("edit calls = %d, want 3", n)
	}
	s := e.clk.slept()
	if len(s) < 2 || s[len(s)-1] < 2*time.Second || s[len(s)-2] < time.Second {
		t.Fatalf("sleeps = %v, want 1s then 2s backoff", s)
	}
}

func TestEditNotModifiedIsSuccess(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: "same"})
	stop := e.start()
	e.waitShown(th.ID)
	stop()
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Edit, HTML: "same"})
	e.start()
	waitFor(t, "edit call", func() bool { return len(e.srv.Calls("editMessageText")) == 1 })
	time.Sleep(30 * time.Millisecond)
	if n := len(e.srv.Calls("editMessageText")); n != 1 {
		t.Fatalf("edit calls = %d, want 1", n)
	}
}

func TestPrioritiesWhenBudgetIsShort(t *testing.T) {
	e := newEnv(t, sender.Options{Burst: 1})
	th := e.topic(1)
	// Everything is queued before Run starts; the budget allows one request
	// at a time, so the order of calls shows the priorities.
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: "progress"})
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Edit, HTML: "progress2"})
	e.enqueue(th, "a1", store.FormatPlain, "answer1")
	last := e.enqueue(th, "a2", store.FormatPlain, "answer2")
	alertDone := make(chan error, 1)
	go func() {
		alertDone <- e.s.Call(sender.PriorityAlert, func(ctx context.Context, tg *telegram.Client) error {
			_, err := tg.SendMessage(ctx, telegram.SendMessageParams{ChatID: chat, MessageThreadID: 1, Text: "alert"})
			return err
		})
	}()
	time.Sleep(100 * time.Millisecond)
	e.start()
	e.waitState(last, store.OutboxSent)
	if err := <-alertDone; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "progress", func() bool { return len(e.srv.Messages(chat)) == 4 })
	if got := strings.Join(texts(e.srv.Messages(chat)), ","); got != "answer1,answer2,alert,progress2" {
		t.Fatalf("messages = %s", got)
	}
}

// A turn that ends before its progress message went out (the Show was starved
// behind answers) posts no progress message at all.
func TestShortTurnFinishedBeforeShowPostsNothing(t *testing.T) {
	e := newEnv(t, sender.Options{Burst: 1})
	th := e.topic(1)
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: "working"})
	e.enqueue(th, "a1", store.FormatPlain, "answer1")
	last := e.enqueue(th, "a2", store.FormatPlain, "answer2")
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Finish, HTML: "done"})
	e.start()
	e.waitState(last, store.OutboxSent)
	// A later turn still works, which also proves the queue was fully drained.
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: "next"})
	waitFor(t, "next turn", func() bool { return len(e.srv.Messages(chat)) == 3 })
	if got := strings.Join(texts(e.srv.Messages(chat)), ","); got != "answer1,answer2,next" {
		t.Fatalf("messages = %s", got)
	}
	if n := len(e.srv.Calls("editMessageText")); n != 0 {
		t.Fatalf("edit calls = %d, want 0", n)
	}
}

func TestTypingDroppedWhenBudgetIsShort(t *testing.T) {
	e := newEnv(t, sender.Options{Burst: 1})
	th := e.topic(1)
	id := e.enqueue(th, "a", store.FormatPlain, "answer")
	stop := e.start()
	e.waitState(id, store.OutboxSent)
	stop() // the budget is spent and the clock is frozen
	e.s.Typing(chat, 1)
	e.start()
	time.Sleep(50 * time.Millisecond)
	if n := len(e.srv.Calls("sendChatAction")); n != 0 {
		t.Fatalf("typing calls = %d, want 0", n)
	}
}

func TestTypingSentWhenBudgetFree(t *testing.T) {
	e := newEnv(t, sender.Options{})
	e.topic(1)
	e.start()
	e.s.Typing(chat, 1)
	waitFor(t, "typing", func() bool { return len(e.srv.Calls("sendChatAction")) == 1 })
}

func TestCallReturnsError(t *testing.T) {
	e := newEnv(t, sender.Options{})
	e.start()
	want := errors.New("boom")
	if err := e.s.Call(sender.PriorityAlert, func(context.Context, *telegram.Client) error { return want }); err != want {
		t.Fatalf("err = %v", err)
	}
	if err := e.s.Call(sender.PriorityAlert, func(ctx context.Context, tg *telegram.Client) error {
		_, err := tg.CreateForumTopic(ctx, chat, "x")
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReact(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	id := e.enqueue(th, "k", store.FormatPlain, "hi")
	e.start()
	e.waitState(id, store.OutboxSent)
	e.s.React(chat, e.srv.Messages(chat)[0].ID, "👍")
	waitFor(t, "reaction", func() bool { return e.srv.Messages(chat)[0].Reaction == "👍" })
}

func TestNewerReactionWinsOverRetriedOlderOne(t *testing.T) {
	e := newEnv(t, sender.Options{})
	msg := e.srv.PushMessage(chat, 0, 7, "hi").Message.MessageID
	e.s.React(chat, msg, "👀")
	e.s.React(chat, msg, "👍")
	e.start()
	waitFor(t, "reaction", func() bool { return e.srv.Messages(chat)[0].Reaction == "👍" })
	time.Sleep(30 * time.Millisecond)
	if n := len(e.srv.Calls("setMessageReaction")); n != 1 {
		t.Fatalf("setMessageReaction calls = %d, want exactly 1", n)
	}
}

func TestRecover(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	id := e.enqueue(th, "k", store.FormatPlain, "hello")
	if ok, err := e.st.ClaimPart(ctx, id, 0, -1); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := e.s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if st := e.rowState(id); st != store.OutboxUnknown {
		t.Fatalf("state = %s", st)
	}
	e.event(sender.PartUnknown)
	stop := e.start()
	waitFor(t, "notice", func() bool { return len(e.srv.Messages(chat)) == 1 })
	if got := e.srv.Messages(chat)[0].Text; got != noticeText(id, 0) {
		t.Fatalf("notice = %q", got)
	}
	// Recover runs at start, before Run. Simulate a restart: wait until the
	// notice (the next outbox row) is recorded as sent, stop, recover again.
	e.waitState(id+1, store.OutboxSent)
	stop()
	if err := e.s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	e.start()
	time.Sleep(30 * time.Millisecond)
	if n := len(e.srv.Messages(chat)); n != 1 {
		t.Fatalf("messages = %d", n)
	}
}

func TestPlainFromHTML(t *testing.T) {
	got := sender.PlainFromHTML(`<a href="x">a &amp;lt; b</a> &lt;i&gt; <pre><code>x&quot;y</code></pre>`)
	if want := `a &lt; b <i> x"y`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRichPartSent(t *testing.T) {
	e := newEnv(t, sender.Options{})
	loud, muted := e.topic(1), e.topic(2)
	if err := e.st.SetThreadSettings(ctx, muted.ID, store.ModeProgress, store.VerboseOff, false, true); err != nil {
		t.Fatal(err)
	}
	a := e.enqueue(loud, "a", store.FormatRich, "# Hi\n\n**bold**")
	b := e.enqueue(muted, "b", store.FormatRich, "muted *rich*")
	e.start()
	e.waitState(a, store.OutboxSent)
	e.waitState(b, store.OutboxSent)
	if n := len(e.srv.Calls("sendMessage")); n != 0 {
		t.Fatalf("sendMessage calls = %d, want 0", n)
	}
	got := map[string]bool{}
	for _, m := range e.srv.Messages(chat) {
		got[m.Text] = m.Silent
	}
	if len(got) != 2 || got["# Hi\n\n**bold**"] || !got["muted *rich*"] {
		t.Fatalf("messages = %v", got)
	}
}

// A rejected rich message comes again in rex-tg's own format: Telegram HTML,
// so the answer keeps its bold text instead of showing "**" marks.
func TestRichFallbackToHTML(t *testing.T) {
	for _, desc := range []string{"Bad Request: can't parse entities: bad", "Bad Request: something else"} {
		e := newEnv(t, sender.Options{})
		th := e.topic(1)
		id := e.enqueue(th, "k", store.FormatRich, "**raw** markdown")
		e.srv.FailNext("sendRichMessage", 1, 400, desc, 0)
		e.start()
		e.waitState(id, store.OutboxSent)
		ms := e.srv.Messages(chat)
		if len(ms) != 1 || ms[0].Text != "<b>raw</b> markdown" || ms[0].ParseMode != "HTML" || ms[0].File != nil {
			t.Fatalf("%q: messages = %+v", desc, ms)
		}
		if n := len(e.srv.Calls("sendRichMessage")); n != 1 {
			t.Fatalf("sendRichMessage calls = %d, want 1", n)
		}
	}
}

func TestRichFallbackToDocument(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	long := strings.Repeat("é", 4001)
	id := e.enqueue(th, "k", store.FormatRich, long)
	e.srv.FailNext("sendRichMessage", 1, 400, "Bad Request: can't parse entities: bad", 0)
	e.start()
	e.waitState(id, store.OutboxSent)
	ms := e.srv.Messages(chat)
	if len(ms) != 1 || string(ms[0].File) != long || !strings.HasSuffix(ms[0].Filename, ".md") {
		t.Fatalf("messages = %+v", ms)
	}
	if n := len(e.srv.Calls("sendMessage")); n != 0 {
		t.Fatalf("sendMessage calls = %d, want 0", n)
	}
}

func TestRichFallbackAtLimitIsText(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	text := strings.Repeat("é", 4000)
	id := e.enqueue(th, "k", store.FormatRich, text)
	e.srv.FailNext("sendRichMessage", 1, 400, "Bad Request: bad", 0)
	e.start()
	e.waitState(id, store.OutboxSent)
	if ms := e.srv.Messages(chat); len(ms) != 1 || ms[0].Text != text || ms[0].File != nil {
		t.Fatalf("messages = %d", len(ms))
	}
}

func TestRichOtherErrorsFollowPartRules(t *testing.T) {
	// Thread gone: failed, no fallback.
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	id := e.enqueue(th, "k", store.FormatRich, "hello")
	e.srv.DeleteTopic(chat, 1)
	e.start()
	e.waitState(id, store.OutboxFailed)
	if n := len(e.srv.Calls("sendMessage")) + len(e.srv.Calls("sendDocument")); n != 0 {
		t.Fatalf("fallback calls after thread gone = %d", n)
	}

	// Rate limit: retried as a rich message.
	e = newEnv(t, sender.Options{})
	th = e.topic(1)
	id = e.enqueue(th, "k", store.FormatRich, "hello")
	e.srv.FailNext("sendRichMessage", 1, 429, "Too Many Requests: retry after 3", 3)
	e.start()
	e.waitState(id, store.OutboxSent)
	if n := len(e.srv.Calls("sendRichMessage")); n != 2 {
		t.Fatalf("sendRichMessage calls = %d, want 2", n)
	}
	if n := len(e.srv.Calls("sendDocument")); n != 0 {
		t.Fatalf("sendDocument calls = %d, want 0", n)
	}

	// Unknown outcome: no fallback; the check finds that it never arrived
	// and sends it again as a rich message.
	e = newEnv(t, sender.Options{})
	th = e.topic(1)
	id = e.enqueue(th, "k", store.FormatRich, "hello")
	e.srv.FailNext("sendRichMessage", 1, 502, "Bad Gateway", 0)
	e.start()
	e.event(sender.PartUnknown)
	e.waitState(id, store.OutboxSent)
	if n := len(e.srv.Calls("sendRichMessage")); n != 2 || len(e.srv.Calls("sendDocument")) != 0 {
		t.Fatalf("rich calls = %d, want 2 and no fallback", n)
	}
}

func TestSecondFinishCorrectsSummary(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: "one"})
	e.start()
	e.waitShown(th.ID) // a Finish queued before the Show went out would drop both
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Finish, HTML: "interrupted"})
	waitFor(t, "first finish", func() bool { return len(e.srv.Messages(chat)) == 1 && e.srv.Messages(chat)[0].Text == "interrupted" })
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Finish, HTML: "done"})
	waitFor(t, "second finish", func() bool { return e.srv.Messages(chat)[0].Text == "done" })
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: "next"})
	waitFor(t, "new show", func() bool { return len(e.srv.Messages(chat)) == 2 })
	if ms := e.srv.Messages(chat); ms[0].Text != "done" || ms[1].Text != "next" {
		t.Fatalf("messages = %+v", ms)
	}
	waitFor(t, "row", func() bool {
		p, ok, _ := e.st.Progress(ctx, th.ID)
		return ok && p.State == store.ProgressShown && p.TGMessageID == e.srv.Messages(chat)[1].ID
	})
}

func TestFinishedSummaryIsKeptAcrossRestart(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: "one"})
	stop := e.start()
	e.waitShown(th.ID) // a Finish queued before the Show went out would drop both
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Finish, HTML: "summary"})
	waitFor(t, "finish", func() bool {
		p, ok, _ := e.st.Progress(ctx, th.ID)
		return len(e.srv.Messages(chat)) == 1 && e.srv.Messages(chat)[0].Text == "summary" && ok && p.TurnKey != ""
	})
	stop()

	// A new sender on the same store has lost the in-memory mark. The next
	// turn's Show must post a new message, not edit the old summary.
	s2 := sender.New(e.st, e.srv.Client(), sender.Options{})
	c, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = s2.Run(c) }()
	t.Cleanup(func() { cancel(); <-done })
	s2.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: "next turn"})
	waitFor(t, "new progress message", func() bool { return len(e.srv.Messages(chat)) == 2 })
	if ms := e.srv.Messages(chat); ms[0].Text != "summary" || ms[1].Text != "next turn" {
		t.Fatalf("messages = %q, %q; want the old summary kept and a new progress message", ms[0].Text, ms[1].Text)
	}
}

func TestCallAfterRunReturnsErrStopped(t *testing.T) {
	e := newEnv(t, sender.Options{})
	stop := e.start()
	stop()
	err := e.s.Call(sender.PriorityAlert, func(context.Context, *telegram.Client) error { return nil })
	if !errors.Is(err, sender.ErrStopped) {
		t.Fatalf("err = %v", err)
	}
}

func TestReactAndTypingGoneEmitNoEvent(t *testing.T) {
	e := newEnv(t, sender.Options{})
	e.topic(1)
	e.start()
	e.s.React(chat, 99999, "👍")
	e.srv.DeleteTopic(chat, 5)
	e.s.Typing(chat, 5)
	waitFor(t, "typing", func() bool { return len(e.srv.Calls("sendChatAction")) == 1 })
	time.Sleep(50 * time.Millisecond)
	select {
	case ev := <-e.s.Events():
		t.Fatalf("unexpected event %+v", ev)
	default:
	}
}

// failFirst fails the first n requests for a method before writing them, as
// when the network is down.
type failFirst struct {
	method string
	n      atomic.Int32
}

func (f *failFirst) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/"+f.method) && f.n.Add(-1) >= 0 {
		return nil, errors.New("dial tcp: connect: network is unreachable")
	}
	return http.DefaultTransport.RoundTrip(r)
}

// A request that never left the machine cannot have posted anything, so the
// part is sent again after a growing pause, with no "may not have arrived"
// notice.
func TestNotSentIsRetriedWithoutNotice(t *testing.T) {
	ft := &failFirst{method: "sendMessage"}
	ft.n.Store(3)
	e := newEnv(t, sender.Options{}, telegram.WithHTTPClient(&http.Client{Transport: ft}))
	th := e.topic(1)
	id := e.enqueue(th, "k", store.FormatPlain, "hello")
	e.start()
	e.waitState(id, store.OutboxSent)
	ms := e.srv.Messages(chat)
	if len(ms) != 1 || ms[0].Text != "hello" {
		t.Fatalf("messages = %+v, want only the answer", ms)
	}
	var waited time.Duration
	for _, d := range e.clk.slept() {
		waited += d
	}
	// 1 s + 2 s + 3 s: the pause grows but stops at 3 s.
	if waited < 6*time.Second {
		t.Errorf("waited %v in total, want the growing backoff (%v)", waited, e.clk.slept())
	}
	for _, d := range e.clk.slept() {
		if d > 3500*time.Millisecond {
			t.Errorf("slept %v, want at most the 3 s cap (%v)", d, e.clk.slept())
		}
	}
}

// A part that Telegram refuses for good gets a notice, but a notice never
// gets a notice of its own: they cannot pile up.
func TestRefusedPartGetsOneNotice(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	id := e.enqueue(th, "k", store.FormatPlain, "hello")
	e.srv.FailNext("sendMessage", 2, 400, "Bad Request: message is too long", 0)
	e.start()
	e.waitState(id, store.OutboxFailed)
	waitFor(t, "the notice attempt", func() bool { return len(e.srv.Calls("sendMessage")) == 2 })
	time.Sleep(50 * time.Millisecond)
	if n := len(e.srv.Calls("sendMessage")); n != 2 {
		t.Fatalf("sendMessage calls = %d, want the part and one notice", n)
	}
	text, _ := e.srv.Calls("sendMessage")[1].Params["text"].(string)
	if !strings.Contains(text, "could not be sent") || !strings.Contains(text, "message is too long") {
		t.Errorf("notice = %q", text)
	}
}

// A notice that itself may not have arrived gets no notice of its own, also
// when it was stored before notices had their own kind (seen live: a chain of
// notices about notices, one per timeout).
func TestNoNoticeAboutANotice(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	old, _, err := e.st.Enqueue(ctx, store.OutboxItem{ThreadID: th.ID, Key: "notice:unknown:7:0", Kind: "notice",
		Parts: []store.PartContent{{Content: "⚠️ Part 1 of reply #7 may not have arrived.", Format: store.FormatHTML}}})
	if err != nil {
		t.Fatal(err)
	}
	e.srv.FailNext("sendMessage", 1, 500, "Internal Server Error", 0)
	e.start()
	e.waitState(old, store.OutboxUnknown)
	time.Sleep(50 * time.Millisecond)
	if n := len(e.srv.Calls("sendMessage")); n != 1 {
		t.Fatalf("sendMessage calls = %d, want only the notice itself", n)
	}
	if id, ok := sender.NoticeTarget("notice:unknown:7:0"); !ok || id != 7 {
		t.Fatalf("NoticeTarget = %d, %v", id, ok)
	}
}

// The daemon's own notices (for example a reply to a command) are checked
// like replies: one that did not arrive is sent again, with no notice.
func TestDaemonNoticeIsCheckedLikeAReply(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	id, _, err := e.st.Enqueue(ctx, store.OutboxItem{ThreadID: th.ID, Key: "reply:42:resend", Kind: "notice",
		Parts: []store.PartContent{{Content: "Sending 1 part(s) of #7 again.", Format: store.FormatHTML}}})
	if err != nil {
		t.Fatal(err)
	}
	e.srv.FailNext("sendMessage", 1, 500, "Internal Server Error", 0)
	e.start()
	e.waitState(id, store.OutboxSent)
	waitFor(t, "the note to go", func() bool { return len(e.visible()) == 1 })
	if ms := e.visible(); !strings.Contains(ms[0].Text, "Sending 1 part") {
		t.Fatalf("messages = %+v", ms)
	}
}

// The check compares the numbers only once a getUpdates call finished after
// its note: a message of the user's sent just before the note may not be
// fetched yet, and its number would look like the part's. (The test clock
// jumps on each wait, so the poll answers "not yet" a few times.)
func TestCheckWaitsForAPollAfterItsNote(t *testing.T) {
	var asked atomic.Int32
	e := newEnv(t, sender.Options{PolledSince: func(time.Time) bool { return asked.Add(1) > 3 }})
	th := e.topic(1)
	id := e.enqueue(th, "k", store.FormatPlain, "hello")
	e.srv.FailNext("sendMessage", 1, 500, "Internal Server Error", 0) // nothing posted
	e.start()
	e.event(sender.PartUnknown)
	e.waitState(id, store.OutboxSent)
	if n := asked.Load(); n < 4 {
		t.Fatalf("the check judged after %d looks at the poll, want it to wait for one", n)
	}
	var texts []string
	for _, c := range e.srv.Calls("sendMessage") {
		s, _ := c.Params["text"].(string)
		texts = append(texts, s)
	}
	if len(texts) != 3 || texts[2] != "hello" {
		t.Fatalf("sends %q, want the reply, the note, the reply again (no notice)", texts)
	}
}

// A check cut by a stop between its note and its verdict goes on after the
// start with the same note: no second note, and the note does not stay.
func TestCheckResumesWithItsNoteAfterARestart(t *testing.T) {
	var polled, stopping atomic.Bool
	stopCh := make(chan func(), 1)
	e := newEnv(t, sender.Options{PolledSince: func(time.Time) bool {
		if polled.Load() {
			return true
		}
		if stopping.CompareAndSwap(false, true) {
			go func() { (<-stopCh)() }() // stop while the check waits for getUpdates
		}
		// Real time, so the stop lands long before the test clock (which
		// jumps on each wait) reaches the check's 3-minute limit.
		time.Sleep(20 * time.Millisecond)
		return false
	}})
	th := e.topic(1)
	id := e.lostSend(th, true)
	stop := e.start()
	stopCh <- stop
	waitFor(t, "the note", func() bool { return len(e.srv.Calls("sendMessage")) == 1 })
	waitFor(t, "the stop", func() bool { return stopping.Load() })
	stop()
	if v, ok, _ := e.st.Get(ctx, fmt.Sprintf("check_note:%d:0", id)); !ok || v == "" {
		t.Fatal("the note is not kept for the next start")
	}
	polled.Store(true)
	// The next start: a new sender over the same store and chat.
	e.s = sender.New(e.st, e.srv.Client(), sender.Options{Now: e.clk.Now, Sleep: e.clk.Sleep, PolledSince: func(time.Time) bool { return true }})
	if err := e.s.Recover(ctx); err != nil { // the start checks the part again
		t.Fatal(err)
	}
	e.start()
	e.waitState(id, store.OutboxSent)
	waitFor(t, "the note to go", func() bool { return len(e.visible()) == 2 })
	notes := 0
	for _, c := range e.srv.Calls("sendMessage") {
		if s, _ := c.Params["text"].(string); strings.Contains(s, "Checking whether it arrived") {
			notes++
		}
	}
	if notes != 1 {
		t.Errorf("%d notes, want 1: the check went on with its note", notes)
	}
}

// TestStaleCheckNoteIsSweptAtStart: a check stops while it waits, and its
// part is settled while rex-tg is down. The next start deletes that note
// (by the number stored with it) and its row, and checks nothing.
func TestStaleCheckNoteIsSweptAtStart(t *testing.T) {
	var stopping atomic.Bool
	stopCh := make(chan func(), 1)
	e := newEnv(t, sender.Options{PolledSince: func(time.Time) bool {
		if stopping.CompareAndSwap(false, true) {
			go func() { (<-stopCh)() }()
		}
		time.Sleep(20 * time.Millisecond) // real time: see the test above
		return false
	}})
	th := e.topic(1)
	id := e.lostSend(th, true)
	stop := e.start()
	stopCh <- stop
	waitFor(t, "the note", func() bool { return len(e.srv.Calls("sendMessage")) == 1 })
	waitFor(t, "the stop", func() bool { return stopping.Load() })
	stop()
	key := fmt.Sprintf("check_note:%d:0", id)
	v, ok, _ := e.st.Get(ctx, key)
	var noteID, at int64
	if _, err := fmt.Sscanf(v, "%d:%d", &noteID, &at); !ok || err != nil || noteID == 0 {
		t.Fatalf("no note kept: %q", v)
	}
	// While rex-tg is down, the part is settled (here: its notice is out).
	if err := e.st.MarkPartChecked(ctx, id, 0); err != nil {
		t.Fatal(err)
	}
	e.s = sender.New(e.st, e.srv.Client(), sender.Options{Now: e.clk.Now, Sleep: e.clk.Sleep, PolledSince: func(time.Time) bool { return true }})
	if err := e.s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	e.start()
	waitFor(t, "the note to be deleted", func() bool { return len(e.srv.Calls("deleteMessage")) == 1 })
	if got := fmt.Sprint(e.srv.Calls("deleteMessage")[0].Params["message_id"]); got != fmt.Sprint(noteID) {
		t.Errorf("deleted message %s, want the note %d", got, noteID)
	}
	if _, ok, _ := e.st.Get(ctx, key); ok {
		t.Error("the note's row is left")
	}
	if n := len(e.srv.Calls("sendMessage")); n != 1 {
		t.Errorf("%d sends, want only the first note", n)
	}
}

// TestCheckWaitsAgainAfterALongStop: a check stops while it waits for a
// poll, and rex-tg stays down for 10 minutes. After the start, the wait for
// a poll begins again: the part is judged when Telegram answers, instead of
// giving the notice at once because the note is old.
func TestCheckWaitsAgainAfterALongStop(t *testing.T) {
	var stopping atomic.Bool
	stopCh := make(chan func(), 1)
	e := newEnv(t, sender.Options{PolledSince: func(time.Time) bool {
		if stopping.CompareAndSwap(false, true) {
			go func() { (<-stopCh)() }()
		}
		time.Sleep(20 * time.Millisecond) // real time: see the tests above
		return false
	}})
	th := e.topic(1)
	id := e.lostSend(th, true)
	stop := e.start()
	stopCh <- stop
	waitFor(t, "the note", func() bool { return len(e.srv.Calls("sendMessage")) == 1 })
	waitFor(t, "the stop", func() bool { return stopping.Load() })
	stop()
	e.clk.mu.Lock()
	e.clk.now = e.clk.now.Add(10 * time.Minute) // rex-tg is down
	e.clk.mu.Unlock()
	var asks atomic.Int32
	e.s = sender.New(e.st, e.srv.Client(), sender.Options{Now: e.clk.Now, Sleep: e.clk.Sleep, PolledSince: func(time.Time) bool {
		return asks.Add(1) > 3 // Telegram answers after a few tries
	}})
	if err := e.s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	e.start()
	e.waitState(id, store.OutboxSent)
	for _, c := range e.srv.Calls("sendMessage") {
		if s, _ := c.Params["text"].(string); strings.Contains(s, "may not have arrived") {
			t.Fatalf("a notice came at once: %q", s)
		}
	}
}

// TestNoteOfAGonePartIsDeletedAtStart: a note's row whose part is gone (an
// old reply pruned) still deletes its note, by the chat kept with it. A row
// in the old form, without the chat, is only forgotten.
func TestNoteOfAGonePartIsDeletedAtStart(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	if err := e.st.Set(ctx, "check_note:9999:0", fmt.Sprintf("77:0:%d:%d", th.ChatID, th.ID)); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Set(ctx, "check_note:9998:0", "78:0"); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	e.start()
	waitFor(t, "the note deleted", func() bool { return len(e.srv.Calls("deleteMessage")) == 1 })
	if got := fmt.Sprint(e.srv.Calls("deleteMessage")[0].Params["message_id"]); got != "77" {
		t.Errorf("deleted message %s, want the note 77", got)
	}
	for _, k := range []string{"check_note:9999:0", "check_note:9998:0"} {
		if _, ok, _ := e.st.Get(ctx, k); ok {
			t.Errorf("row %s is left", k)
		}
	}
}

// Remove deletes the running turn's progress message and forgets it; a kept
// summary of an earlier turn stays.
func TestRemoveDeletesOnlyTheRunningProgress(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: "one"})
	e.start()
	e.waitShown(th.ID)
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Finish, HTML: "summary of a long turn"})
	waitFor(t, "the summary", func() bool { ms := e.srv.Messages(chat); return len(ms) == 1 && ms[0].Text == "summary of a long turn" })
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Remove}) // no running progress: the summary stays

	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: "two"})
	waitFor(t, "the next progress", func() bool { return len(e.srv.Messages(chat)) == 2 })
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Edit, HTML: "two, edited"})
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Remove})
	waitFor(t, "the progress deleted", func() bool {
		ms := e.srv.Messages(chat)
		return len(ms) == 2 && ms[1].Deleted
	})
	if ms := e.srv.Messages(chat); ms[0].Deleted {
		t.Error("the summary of the earlier turn was deleted")
	}
	if _, ok, _ := e.st.Progress(ctx, th.ID); ok {
		t.Error("the removed progress row is left")
	}
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: "three"})
	waitFor(t, "a new progress after the removal", func() bool { return len(e.srv.Messages(chat)) == 3 })
}
