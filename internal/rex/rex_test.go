package rex_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/rex"
	"rex-tg/internal/rex/rextest"
)

func newClient(t *testing.T, opts ...rex.Option) (*rex.Client, *rextest.Fake) {
	t.Helper()
	f := rextest.New(t)
	return rex.New(f.Bin(), opts...), f
}

func lastArgs(t *testing.T, f *rextest.Fake) []string {
	t.Helper()
	calls := f.Calls()
	if len(calls) == 0 {
		t.Fatal("no calls recorded")
	}
	return calls[len(calls)-1].Args
}

func TestWhoami(t *testing.T) {
	c, f := newClient(t)
	f.On([]string{"whoami"}, `{"principal":{"kind":"peer","uid":501},"account":"tester","endpoint":"unix:///tmp/x.sock","pid":42,"instance_id":"i-1","host_id":"h-1","started_at":"2026-11-09T12:20:52.553438Z","rex_version":"0.0.1","api_revision":11,"session_count":2}`, 0)
	w, err := c.Whoami(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if w.Endpoint != "unix:///tmp/x.sock" || w.PID != 42 || w.APIRevision != 11 || w.SessionCount != 2 || w.Account != "tester" || w.StartedAt.IsZero() {
		t.Fatalf("got %+v", w)
	}
	want := []string{"--autostart=false", "whoami", "--json"}
	if got := lastArgs(t, f); !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestSessionsAndBlocks(t *testing.T) {
	c, f := newClient(t)
	f.On([]string{"ls"}, `{"sessions":[{"session_id":"session:a","label":"T3 Code"},{"session_id":"session:b","label":"Personal"}]}`, 0)
	f.On([]string{"block", "ls"}, `{"blocks":[{"block_id":"block:1","label":"shell","creator_name":"com.example.terminal","flavor":"com.example.terminal.shell","host_id":"h","placement":"placed","window_id":"window:1","owner":"client:1"},{"block_id":"block:2","label":"x"}]}`, 0)

	ss, err := c.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 || ss[0].ID != "session:a" || ss[1].Label != "Personal" {
		t.Fatalf("sessions %+v", ss)
	}
	if got, want := lastArgs(t, f), []string{"--autostart=false", "ls", "--json"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args %q", got)
	}

	bs, err := c.Blocks(context.Background(), "session:a")
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 2 || bs[0].ID != "block:1" || bs[0].WindowID != "window:1" || bs[0].Owner != "client:1" || bs[1].Owner != "" {
		t.Fatalf("blocks %+v", bs)
	}
	if got, want := lastArgs(t, f), []string{"--autostart=false", "block", "ls", "-s", "session:a", "--json"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args %q", got)
	}
}

func TestProcess(t *testing.T) {
	c, f := newClient(t)
	f.On([]string{"block", "call"}, `{"last_exit":null,"child":{"pid":2,"user_id":0,"user":"root","name":"login","path":"/usr/bin/login"},"foreground":{"pid":3,"user_id":501,"user":"u","name":"node","path":"/bin/node","invoked_path":"/bin/node","argv0":"pi","cwd":"/work"},"ancestors":[{"pid":4,"name":"zsh","argv0":"-zsh","cwd":"/work"}],"home":"/h"}`, 0)
	p, err := c.Process(context.Background(), "session:a", "block:1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Foreground.PID != 3 || p.Foreground.Argv0 != "pi" || p.Foreground.CWD != "/work" || p.Foreground.Name != "node" {
		t.Fatalf("foreground %+v", p.Foreground)
	}
	if len(p.Ancestors) != 1 || p.Ancestors[0].Name != "zsh" || p.Child == nil || p.Child.Name != "login" || p.LastExit != nil {
		t.Fatalf("got %+v", p)
	}
	want := []string{"--autostart=false", "block", "call", "-s", "session:a", "-b", "block:1", "process"}
	if got := lastArgs(t, f); !reflect.DeepEqual(got, want) {
		t.Fatalf("args %q", got)
	}

	// A pane that stays open after its program ended (the shape Rex sends).
	f.On([]string{"block", "call"}, `{"foreground":null,"child":null,"home":"/h","last_exit":{"exit_code":127,"error":"exit status 127"}}`, 0)
	p, err = c.Process(context.Background(), "session:a", "block:1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Foreground.PID != 0 || p.LastExit == nil || p.LastExit.Code != 127 || p.LastExit.Error != "exit status 127" {
		t.Fatalf("got %+v", p)
	}
}

func TestNewSession(t *testing.T) {
	c, f := newClient(t)
	f.On([]string{"new"}, `{"session_id":"session:n","initial_windows":[{"window_id":"window:w","block_ids":["block:b"],"revision":1}]}`, 0)
	got, err := c.NewSession(context.Background(), "my app", rex.Tab{Label: "pi", CWD: "/work/my app", Command: []string{"pi", "--flag", "a b"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := (rex.Started{SessionID: "session:n", WindowID: "window:w", BlockID: "block:b"}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	want := []string{"--autostart=false", "new", "--cwd=/work/my app", "--window=pi", "--interactive", "--json", "my app", "--", "pi", "--flag", "a b"}
	if args := lastArgs(t, f); !reflect.DeepEqual(args, want) {
		t.Fatalf("args %q, want %q", args, want)
	}
}

func TestNewWindow(t *testing.T) {
	c, f := newClient(t)
	f.On([]string{"window", "new"}, `{"window_id":"window:w","block_ids":["block:b"],"revision":2}`, 0)
	got, err := c.NewWindow(context.Background(), "session:s", rex.Tab{Label: "codex", CWD: "/work/app", Command: []string{"codex"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := (rex.Started{SessionID: "session:s", WindowID: "window:w", BlockID: "block:b"}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	want := []string{"--autostart=false", "window", "new", "--session=session:s", "--cwd=/work/app", "--interactive", "--focus=false", "--json", "codex", "--", "codex"}
	if args := lastArgs(t, f); !reflect.DeepEqual(args, want) {
		t.Fatalf("args %q, want %q", args, want)
	}
}

// A name that starts with "-" would be read as an option, so it never
// reaches rex; neither does a relative folder or an empty command.
func TestNewTabRejectsWhatRexWouldMisread(t *testing.T) {
	c, f := newClient(t)
	f.On([]string{"new"}, `{"session_id":"session:n","initial_windows":[{"window_id":"w","block_ids":["b"]}]}`, 0)
	f.On([]string{"window", "new"}, `{"window_id":"w","block_ids":["b"]}`, 0)
	ok := rex.Tab{Label: "pi", CWD: "/work", Command: []string{"pi"}}
	ctx := context.Background()
	cases := map[string]func() error{
		"session name": func() error { _, err := c.NewSession(ctx, "-rf", ok); return err },
		"empty name":   func() error { _, err := c.NewSession(ctx, "", ok); return err },
		"tab name": func() error {
			_, err := c.NewWindow(ctx, "s", rex.Tab{Label: "--json", CWD: "/work", Command: []string{"pi"}})
			return err
		},
		"relative folder": func() error {
			_, err := c.NewWindow(ctx, "s", rex.Tab{Label: "pi", CWD: "work", Command: []string{"pi"}})
			return err
		},
		"no command": func() error { _, err := c.NewSession(ctx, "app", rex.Tab{Label: "pi", CWD: "/work"}); return err },
		"no session": func() error { _, err := c.NewWindow(ctx, "", ok); return err },
	}
	for name, call := range cases {
		if err := call(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if n := len(f.Calls()); n != 0 {
		t.Fatalf("%d rex calls, want none", n)
	}
}

// An answer without a pane is an error: the caller needs the pane's ID.
func TestNewTabNeedsAPane(t *testing.T) {
	c, f := newClient(t)
	f.On([]string{"new"}, `{"session_id":"session:n","initial_windows":[]}`, 0)
	f.On([]string{"window", "new"}, `{"window_id":"window:w","block_ids":[]}`, 0)
	tab := rex.Tab{Label: "pi", CWD: "/work", Command: []string{"pi"}}
	if _, err := c.NewSession(context.Background(), "app", tab); err == nil {
		t.Error("NewSession: no error")
	}
	if _, err := c.NewWindow(context.Background(), "session:s", tab); err == nil {
		t.Error("NewWindow: no error")
	}
}

func TestProgramStatus(t *testing.T) {
	c, f := newClient(t)
	f.On([]string{"block", "call"}, `{"records":[{"id":"r1","state":"blocked","kind":"permission","app":"pi","effective_app":"pi","msg":"Run ls?","updated_at":"2026-11-10T22:50:23.039012+07:00","owner":{"pid":80735,"name":"node"}}]}`, 0)
	rs, err := c.ProgramStatus(context.Background(), "session:a", "block:1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 {
		t.Fatalf("got %+v", rs)
	}
	r := rs[0]
	if r.ID != "r1" || r.State != "blocked" || r.Kind != "permission" || r.EffectiveApp != "pi" || r.Msg != "Run ls?" ||
		r.Owner == nil || r.Owner.PID != 80735 || r.Owner.Name != "node" ||
		!r.UpdatedAt.Equal(time.Date(2026, 11, 10, 15, 50, 23, 39012000, time.UTC)) {
		t.Fatalf("record %+v", r)
	}
	if got := lastArgs(t, f); got[len(got)-1] != "program_status" {
		t.Fatalf("args %q", got)
	}

	f.On([]string{"block", "call"}, `{"records":[]}`, 0)
	rs, err = c.ProgramStatus(context.Background(), "s", "b")
	if err != nil || len(rs) != 0 {
		t.Fatalf("got %v %v", rs, err)
	}
}

func TestCapture(t *testing.T) {
	c, f := newClient(t)
	f.On([]string{"capture"}, "line 1\nline 2\n", 0)
	out, err := c.Capture(context.Background(), "session:a", "block:1")
	if err != nil {
		t.Fatal(err)
	}
	if out != "line 1\nline 2\n" {
		t.Fatalf("out %q", out)
	}
	want := []string{"--autostart=false", "capture", "-s", "session:a", "-b", "block:1", "--trim"}
	if got := lastArgs(t, f); !reflect.DeepEqual(got, want) {
		t.Fatalf("args %q", got)
	}
}

func TestPasteUsesStdin(t *testing.T) {
	c, f := newClient(t)
	f.On([]string{"api", "call"}, `{}`, 0)
	text := "hello \"world\"\nsecond line with $HOME and 'quotes' — ünï"
	if err := c.Paste(context.Background(), "session:a", "block:1", text); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls %+v", calls)
	}
	call := calls[0]
	wantArgs := []string{"--autostart=false", "api", "call", "-s", "session:a", "block.event", "-"}
	if !reflect.DeepEqual(call.Args, wantArgs) {
		t.Fatalf("args %q", call.Args)
	}
	for _, a := range call.Args {
		if strings.Contains(a, "hello") {
			t.Fatalf("text leaked into argv: %q", call.Args)
		}
	}
	var body struct {
		BlockID string `json:"block_id"`
		Event   struct {
			Type    string `json:"type"`
			Payload struct {
				Text string `json:"text"`
			} `json:"payload"`
		} `json:"event"`
	}
	if err := jsonUnmarshal(call.Stdin, &body); err != nil {
		t.Fatalf("stdin %q: %v", call.Stdin, err)
	}
	if body.BlockID != "block:1" || body.Event.Type != "paste" || body.Event.Payload.Text != text {
		t.Fatalf("body %+v", body)
	}
}

func TestSendKeys(t *testing.T) {
	c, f := newClient(t)
	f.On([]string{"send-key"}, "", 0)
	if err := c.SendKeys(context.Background(), "session:a", "block:1", "ctrl+c", "enter"); err != nil {
		t.Fatal(err)
	}
	want := []string{"--autostart=false", "send-key", "-s", "session:a", "-b", "block:1", "ctrl+c", "enter"}
	if got := lastArgs(t, f); !reflect.DeepEqual(got, want) {
		t.Fatalf("args %q", got)
	}
	n := len(f.Calls())
	if err := c.SendKeys(context.Background(), "s", "b"); err != nil || len(f.Calls()) != n {
		t.Fatalf("no keys should be a no-op, err=%v", err)
	}
}

func TestWithServer(t *testing.T) {
	c, f := newClient(t, rex.WithServer("unix:///tmp/test.sock"))
	f.On([]string{"ls"}, `{"sessions":[]}`, 0)
	if _, err := c.Sessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"--autostart=false", "-S", "unix:///tmp/test.sock", "ls", "--json"}
	if got := lastArgs(t, f); !reflect.DeepEqual(got, want) {
		t.Fatalf("args %q", got)
	}
}

func TestErrorCarriesSubcommandAndStderr(t *testing.T) {
	c, f := newClient(t)
	f.OnError([]string{"block", "call"}, "  block not found: block:9\n\n", 1)
	_, err := c.Process(context.Background(), "session:a", "block:9")
	if err == nil {
		t.Fatal("want error")
	}
	var re *rex.Error
	if !errors.As(err, &re) {
		t.Fatalf("error type %T", err)
	}
	if re.Stderr != "block not found: block:9" || re.Command != "block call process" {
		t.Fatalf("got %+v", re)
	}
	if msg := err.Error(); !strings.Contains(msg, "block call process") || !strings.HasSuffix(msg, "block not found: block:9") {
		t.Fatalf("message %q", msg)
	}
}

func TestUnmatchedCommandFails(t *testing.T) {
	c, _ := newClient(t)
	if _, err := c.Sessions(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

func TestBadJSON(t *testing.T) {
	c, f := newClient(t)
	f.On([]string{"ls"}, `not json`, 0)
	_, err := c.Sessions(context.Background())
	var re *rex.Error
	if !errors.As(err, &re) || re.Command != "ls" {
		t.Fatalf("err %v", err)
	}
}

func TestTimeout(t *testing.T) {
	c, f := newClient(t, rex.WithTimeout(150*time.Millisecond))
	f.OnDelay([]string{"ls"}, `{"sessions":[]}`, 5*time.Second)
	start := time.Now()
	_, err := c.Sessions(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %s", d)
	}
}

func TestContextCancel(t *testing.T) {
	c, f := newClient(t)
	f.OnDelay([]string{"ls"}, `{"sessions":[]}`, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := c.Sessions(ctx); err == nil {
		t.Fatal("want error")
	}
}

func TestMissingBinary(t *testing.T) {
	c := rex.New("/nonexistent/rex")
	if _, err := c.Sessions(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

func TestPasteErrorDoesNotLeakText(t *testing.T) {
	c, f := newClient(t)
	f.OnEchoStdin([]string{"api", "call"}, 1)
	text := "secret \"quoted\"\nline two"
	err := c.Paste(context.Background(), "session:a", "block:1", text)
	var re *rex.Error
	if !errors.As(err, &re) {
		t.Fatalf("err %v", err)
	}
	for _, bad := range []string{"secret", "line two", `\"quoted\"`} {
		if strings.Contains(re.Stderr, bad) || strings.Contains(err.Error(), bad) {
			t.Fatalf("leaked %q in %q", bad, re.Stderr)
		}
	}
	if !strings.Contains(re.Stderr, "[text]") {
		t.Fatalf("stderr %q", re.Stderr)
	}
}

func TestStderrCapped(t *testing.T) {
	c, f := newClient(t)
	f.OnError([]string{"ls"}, strings.Repeat("x", 5000), 1)
	_, err := c.Sessions(context.Background())
	var re *rex.Error
	if !errors.As(err, &re) || len(re.Stderr) > 510 {
		t.Fatalf("stderr len %d", len(re.Stderr))
	}
}

func TestIsGone(t *testing.T) {
	gone := &rex.Error{Command: "block call process", Stderr: `Error: block target "block:01a1" not found`, Err: errors.New("exit status 3")}
	if !rex.IsGone(gone) || !rex.IsGone(fmt.Errorf("registry: process of block: %w", gone)) {
		t.Fatal("a closed block is not reported as gone")
	}
	for _, err := range []error{
		&rex.Error{Command: "ls", Stderr: "Error: connection refused", Err: errors.New("exit status 1")},
		errors.New(`block target "x" not found`), // not a Rex CLI error
		nil,
	} {
		if rex.IsGone(err) {
			t.Errorf("IsGone(%v) = true", err)
		}
	}
}
