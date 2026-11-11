package rex_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/rex"
	"rex-tg/internal/rex/rextest"
)

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

const (
	evClientJoined = `{"type":"client_joined","payload":{"client":{"client_id":"client:1","info":{"kind":"cli"}}}}`
	evClientLeft   = `{"type":"client_left","payload":{"client_id":"client:1"}}`
	evConnected    = `{"type":"session_event","payload":{"session_id":"session:a","event":{"type":"client_connected","payload":{"client_id":"client:2"}}}}`
	evDisconnected = `{"type":"session_event","payload":{"session_id":"session:a","event":{"type":"client_disconnected","payload":{"client_id":"client:2"}}}}`
	evProgress     = `{"type":"session_event","payload":{"session_id":"session:a","event":{"type":"block_event","payload":{"block_id":"block:1","creator_name":"c","name":"progress_report","payload":{"state":"indeterminate"}}}}}`
	evOwner        = `{"type":"session_event","payload":{"session_id":"session:a","event":{"type":"block_owner_changed","payload":{"block_id":"block:1","owner":"client:1","reason":"create"}}}}`
	evView         = `{"type":"session_event","payload":{"session_id":"session:a","event":{"type":"session_view_changed","payload":{"revision":2}}}}`

	evSessionCreated = `{"type":"session_created","payload":{"session":{"session_id":"session:new","label":"spike"}}}`
	evBlockCreated   = `{"type":"session_event","payload":{"session_id":"session:new","event":{"type":"block_created","payload":{"block_id":"block:n"}}}}`
	evBlockClosed    = `{"type":"session_event","payload":{"session_id":"session:new","event":{"type":"block_closed","payload":{"block_id":"block:n"}}}}`
	evStatus         = `{"type":"session_event","payload":{"session_id":"session:a","event":{"type":"block_event","payload":{"block_id":"block:1","creator_name":"c","name":"program_status_changed","payload":{"id":"","record":{"state":"done","app":"pi","effective_app":"pi","msg":"finished","updated_at":"2026-11-10T22:50:23.039012+07:00","owner":{"pid":7,"name":"node"}},"reason":"report"}}}}}`
	evProcess        = `{"type":"session_event","payload":{"session_id":"session:a","event":{"type":"block_event","payload":{"block_id":"block:1","creator_name":"c","name":"process_changed","payload":{"generation":1,"foreground":null}}}}}`
	evPwd            = `{"type":"session_event","payload":{"session_id":"session:a","event":{"type":"block_event","payload":{"block_id":"block:1","name":"pwd_changed","payload":{"pwd":"file://host/Users/x"}}}}}`
	evTitle          = `{"type":"session_event","payload":{"session_id":"session:a","event":{"type":"block_event","payload":{"block_id":"block:1","name":"title_changed","payload":{"title":"zsh"}}}}}`
	evUnknownBlock   = `{"type":"session_event","payload":{"session_id":"session:a","event":{"type":"block_event","payload":{"block_id":"block:1","name":"something_new","payload":{}}}}}`
	evUnknownInner   = `{"type":"session_event","payload":{"session_id":"session:a","event":{"type":"layout_changed","payload":{}}}}`
	evUnknownTop     = `{"type":"server_status","payload":{"x":1}}`
	evDestroyed      = `{"type":"session_destroyed","payload":{"session_id":"session:old"}}`
)

func collect(t *testing.T, ch <-chan rex.Event, n int) []rex.Event {
	t.Helper()
	var out []rex.Event
	timeout := time.After(10 * time.Second)
	for len(out) < n {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed after %d of %d events", len(out), n)
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatalf("timeout after %d of %d events", len(out), n)
		}
	}
	return out
}

func TestEventsParsing(t *testing.T) {
	now := time.Date(2026, 11, 11, 1, 2, 3, 0, time.UTC)
	c, f := newClient(t, rex.WithClock(func() time.Time { return now }))
	f.SetEvents(
		evClientJoined, evClientLeft, evConnected, evDisconnected, evProgress, evOwner, evView, // dropped
		evSessionCreated, evBlockCreated, evStatus, evProcess, evPwd, evTitle, evBlockClosed,
		evUnknownBlock, evUnknownInner, evUnknownTop, evDestroyed,
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, errs := c.Events(ctx)
	got := collect(t, events, 11)

	type row struct {
		T       rex.EventType
		Session string
		Block   string
	}
	want := []row{
		{rex.SessionCreated, "session:new", ""},
		{rex.BlockCreated, "session:new", "block:n"},
		{rex.ProgramStatusChanged, "session:a", "block:1"},
		{rex.ProcessChanged, "session:a", "block:1"},
		{rex.PwdChanged, "session:a", "block:1"},
		{rex.TitleChanged, "session:a", "block:1"},
		{rex.BlockClosed, "session:new", "block:n"},
		{rex.OtherEvent, "session:a", "block:1"},
		{rex.OtherEvent, "session:a", ""},
		{rex.OtherEvent, "", ""},
		{rex.SessionRemoved, "session:old", ""},
	}
	for i, ev := range got {
		if (row{ev.Type, ev.SessionID, ev.BlockID}) != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, row{ev.Type, ev.SessionID, ev.BlockID}, want[i])
		}
		if !ev.Received.Equal(now) {
			t.Errorf("event %d received %v", i, ev.Received)
		}
		if !json.Valid(ev.Raw) {
			t.Errorf("event %d raw invalid", i)
		}
	}
	st := got[2].Status
	if st == nil || st.State != "done" || st.App != "pi" || st.Msg != "finished" || st.Owner == nil || st.Owner.PID != 7 {
		t.Fatalf("status %+v", st)
	}
	if got[3].Status != nil {
		t.Error("only status events carry a status")
	}
	if !strings.Contains(string(got[2].Raw), `"program_status_changed"`) {
		t.Errorf("raw %s", got[2].Raw)
	}

	wantArgs := []string{"--autostart=false", "events", "--all", "--json"}
	if args := f.Calls()[0].Args; !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args %q", args)
	}

	// Nothing else may arrive: every dropped line produced no event.
	select {
	case ev := <-events:
		t.Fatalf("unexpected event %+v", ev)
	case err := <-errs:
		t.Fatalf("unexpected error %v", err)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestEventsCancelKillsProcess(t *testing.T) {
	c, f := newClient(t)
	f.SetEvents(evSessionCreated)
	ctx, cancel := context.WithCancel(context.Background())
	events, errs := c.Events(ctx)
	collect(t, events, 1)
	pid := f.EventsPID()
	if !rextest.ProcessAlive(pid) {
		t.Fatalf("events process %d not running", pid)
	}
	cancel()

	deadline := time.After(10 * time.Second)
	for events != nil || errs != nil {
		select {
		case _, ok := <-events:
			if !ok {
				events = nil
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
			} else {
				t.Errorf("cancel must not report an error, got %v", err)
			}
		case <-deadline:
			t.Fatal("channels not closed after cancel")
		}
	}
	if rextest.ProcessAlive(pid) {
		t.Fatalf("process %d still alive", pid)
	}
}

func TestEventsExitReported(t *testing.T) {
	c, f := newClient(t)
	f.SetEvents(evSessionCreated)
	f.SetEventsExit(3, "server went away\n")
	events, errs := c.Events(context.Background())
	collect(t, events, 1)
	select {
	case err := <-errs:
		var re *rex.Error
		if !errors.As(err, &re) || re.Command != "events" || re.Stderr != "server went away" {
			t.Fatalf("err %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no error")
	}
	if _, ok := <-events; ok {
		t.Fatal("events should be closed")
	}
}

func TestEventsBadLineReported(t *testing.T) {
	c, f := newClient(t)
	f.SetEvents("garbage{", evSessionCreated)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, errs := c.Events(ctx)
	collect(t, events, 1)
	select {
	case err := <-errs:
		var pe *rex.ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no error")
	}
}

func TestEventsSlowConsumerDrops(t *testing.T) {
	c, f := newClient(t)
	const total = 1000
	lines := make([]string, total)
	for i := range lines {
		lines[i] = evPwd
	}
	f.SetEvents(lines...)
	f.SetEventsExit(0, "")
	events, errs := c.Events(context.Background())

	// Do not read until the stream has ended: the reader must not block.
	dropped := 0
	var streamEnded bool
	deadline := time.After(10 * time.Second)
	for !streamEnded {
		select {
		case err, ok := <-errs:
			if !ok {
				streamEnded = true
				break
			}
			var de *rex.DroppedError
			if errors.As(err, &de) {
				dropped += de.Count
			}
		case <-deadline:
			t.Fatal("reader blocked on a slow consumer")
		}
	}
	received := 0
	for range events {
		received++
	}
	if dropped == 0 {
		t.Fatal("expected drops to be reported")
	}
	if received+dropped != total {
		t.Fatalf("received %d + dropped %d != %d", received, dropped, total)
	}
}

// A consumer that reads all events and then all errors must not hang, even
// when the error buffer filled up with reports before the stream ended.
func TestEventsEndWithFullErrorBuffer(t *testing.T) {
	c, f := newClient(t)
	lines := make([]string, 0, 40)
	for range 40 {
		lines = append(lines, "garbage{")
	}
	f.SetEvents(append(lines, evSessionCreated)...)
	f.SetEventsExit(3, "server went away\n")
	events, errs := c.Events(context.Background())
	done := make(chan []error)
	go func() {
		for range events {
		}
		var all []error
		for err := range errs {
			all = append(all, err)
		}
		done <- all
	}()
	select {
	case all := <-done:
		var re *rex.Error
		if len(all) == 0 || !errors.As(all[len(all)-1], &re) || re.Stderr != "server went away" {
			t.Fatalf("errors = %v, want the exit last", all)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the consumer hung: the last error was never sent")
	}
}
