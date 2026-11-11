package sender_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
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
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Sleep(ctx context.Context, d time.Duration) error {
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

func noticeText(id int64, part int) string {
	return "⚠️ Part " + string(rune('0'+part+1)) + " of reply #" + string(rune('0'+id)) + " may not have arrived. /resend " + string(rune('0'+id))
}

func TestUnknownOutcomeNoRetryOneNotice(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	id := e.enqueue(th, "k", store.FormatPlain, "hello")
	e.srv.FailNext("sendMessage", 1, 500, "Internal Server Error", 0)
	e.start()
	e.waitState(id, store.OutboxUnknown)
	ev := e.event(sender.PartUnknown)
	if ev.OutboxID != id || ev.Part != 0 || ev.ThreadID != th.ID {
		t.Fatalf("event = %+v", ev)
	}
	waitFor(t, "notice", func() bool { return len(e.srv.Messages(chat)) == 1 })
	time.Sleep(30 * time.Millisecond)
	ms := e.srv.Messages(chat)
	if len(ms) != 1 || ms[0].Text != noticeText(id, 0) || ms[0].ParseMode != "HTML" {
		t.Fatalf("messages = %+v", ms)
	}
	if n := len(e.srv.Calls("sendMessage")); n != 2 {
		t.Fatalf("sendMessage calls = %d, want 2 (original and notice)", n)
	}
	if _, inserted, _ := e.st.Enqueue(ctx, store.OutboxItem{ThreadID: th.ID, Key: "notice:unknown:1:0", Kind: "notice",
		Parts: []store.PartContent{{Content: "x", Format: "plain"}}}); inserted {
		t.Fatal("notice key was not used")
	}
}

func TestTimeoutIsUnknown(t *testing.T) {
	e := newEnv(t, sender.Options{}, telegram.WithTimeout(200*time.Millisecond))
	th := e.topic(1)
	id := e.enqueue(th, "k", store.FormatPlain, "hello")
	e.srv.Delay("sendMessage", 2*time.Second)
	e.start()
	waitFor(t, "first call", func() bool { return len(e.srv.Calls("sendMessage")) >= 1 })
	e.srv.Delay("sendMessage", 0)
	e.waitState(id, store.OutboxUnknown)
	waitFor(t, "notice", func() bool { return len(e.srv.Messages(chat)) == 1 })
	time.Sleep(30 * time.Millisecond)
	if n := len(e.srv.Messages(chat)); n != 1 {
		t.Fatalf("messages = %d, want only the notice", n)
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
	first := e.enqueue(th, "a", store.FormatPlain, "one")
	second := e.enqueue(th, "b", store.FormatPlain, "two")
	e.srv.FailNext("sendMessage", 1, 403, "Forbidden: bot was blocked by the user", 0)
	stop := e.start()
	e.waitState(first, store.OutboxFailed)
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

	// Finish: last edit, then the row goes away.
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Finish, HTML: "<b>done</b>"})
	waitFor(t, "row deleted", func() bool {
		_, ok, _ := e.st.Progress(ctx, th.ID)
		return !ok
	})
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
	waitFor(t, "shown", func() bool { return len(e.srv.Messages(chat)) == 1 })
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
	waitFor(t, "shown", func() bool { return len(e.srv.Messages(chat)) == 1 })
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
	waitFor(t, "shown", func() bool { return len(e.srv.Messages(chat)) == 1 })
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
	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Show, HTML: "one"})
	stop := e.start()
	waitFor(t, "shown", func() bool { return len(e.srv.Messages(chat)) == 1 })
	stop()

	// The budget is spent and the clock is frozen: typing is dropped.
	e.s.Typing(chat, 1)

	e.s.Progress(th.ID, sender.ProgressOp{Kind: sender.Edit, HTML: "edited"})
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
	waitFor(t, "edit", func() bool { return len(e.srv.Calls("editMessageText")) == 1 })
	if err := <-alertDone; err != nil {
		t.Fatal(err)
	}
	got := strings.Join(methods(e.srv), ",")
	want := "sendMessage,sendMessage,sendMessage,sendMessage,editMessageText"
	if got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
	if got := strings.Join(texts(e.srv.Messages(chat)), ","); got != "edited,answer1,answer2,alert" {
		t.Fatalf("messages = %s", got)
	}
	if len(e.srv.Calls("sendChatAction")) != 0 {
		t.Fatal("typing was sent")
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

func TestRecover(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	id := e.enqueue(th, "k", store.FormatPlain, "hello")
	if ok, err := e.st.ClaimPart(ctx, id, 0); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := e.s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if st := e.rowState(id); st != store.OutboxUnknown {
		t.Fatalf("state = %s", st)
	}
	e.event(sender.PartUnknown)
	e.start()
	waitFor(t, "notice", func() bool { return len(e.srv.Messages(chat)) == 1 })
	if got := e.srv.Messages(chat)[0].Text; got != noticeText(id, 0) {
		t.Fatalf("notice = %q", got)
	}
	if err := e.s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
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
