package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"rex-tg/internal/registry"
)

const claudePayload = `{"hook_event_name":"SessionStart","session_id":"abc","transcript_path":"/x/y.jsonl","cwd":"/x","source":"startup"}`

func spoolFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out
}

func TestHookWritesSpoolFile(t *testing.T) {
	te := newTestEnv(t)
	te.setEnv(map[string]string{"REX_BLOCK": "block:1", "REX_SESSION": "session:9"})
	te.Stdin = strings.NewReader(claudePayload)
	if code := te.run("hook", "claude"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if te.stdout.Len() != 0 || te.stderr.Len() != 0 {
		t.Fatalf("hook printed: %q %q", te.stdout, te.stderr)
	}
	dir := filepath.Join(te.stateDir(), "spool")
	files := spoolFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("spool files = %v", files)
	}
	name := filepath.Base(files[0])
	if !strings.HasPrefix(name, "1794366774486000000-") || !strings.HasSuffix(name, ".json") {
		t.Errorf("name = %s", name)
	}
	st, _ := os.Stat(files[0])
	if st.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", st.Mode().Perm())
	}
	dst, _ := os.Stat(dir)
	if dst.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", dst.Mode().Perm())
	}
	data, _ := os.ReadFile(files[0])
	rep, err := registry.ParseSpool(data)
	if err != nil {
		t.Fatalf("ParseSpool: %v\n%s", err, data)
	}
	if rep.Runtime != "claude" || rep.Event != "SessionStart" || rep.RexBlock != "block:1" || rep.RexSession != "session:9" {
		t.Errorf("report = %+v", rep)
	}
	if rep.At.UnixNano() != te.Now().UnixNano() {
		t.Errorf("at = %v", rep.At)
	}
	if string(rep.Payload) != claudePayload {
		t.Errorf("payload = %s", rep.Payload)
	}
}

func TestHookAllRuntimes(t *testing.T) {
	for _, rt := range []string{"pi", "claude", "codex"} {
		te := newTestEnv(t)
		te.setEnv(map[string]string{"REX_BLOCK": "b"})
		te.Stdin = strings.NewReader(`{"hook_event_name":"session_start","reason":"startup"}`)
		te.run("hook", rt)
		files := spoolFiles(t, filepath.Join(te.stateDir(), "spool"))
		if len(files) != 1 {
			t.Fatalf("%s: files = %v", rt, files)
		}
		data, _ := os.ReadFile(files[0])
		rep, err := registry.ParseSpool(data)
		if err != nil || string(rep.Runtime) != rt {
			t.Errorf("%s: %+v %v", rt, rep, err)
		}
	}
}

func TestHookSilentAndZero(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		env   map[string]string
		stdin string
		setup func(te *testEnv)
		spool bool // expect a spool file
	}{
		{name: "no REX_BLOCK", args: []string{"hook", "claude"}, stdin: claudePayload},
		{name: "bad runtime", args: []string{"hook", "emacs"}, env: map[string]string{"REX_BLOCK": "b"}, stdin: claudePayload},
		{name: "no runtime", args: []string{"hook"}, env: map[string]string{"REX_BLOCK": "b"}, stdin: claudePayload},
		{name: "extra args", args: []string{"hook", "claude", "x"}, env: map[string]string{"REX_BLOCK": "b"}, stdin: claudePayload},
		{name: "empty stdin", args: []string{"hook", "claude"}, env: map[string]string{"REX_BLOCK": "b"}},
		{name: "bad json", args: []string{"hook", "claude"}, env: map[string]string{"REX_BLOCK": "b"}, stdin: "{nope"},
		{name: "broken config still spools", args: []string{"hook", "claude"}, env: map[string]string{"REX_BLOCK": "b"}, stdin: claudePayload,
			setup: func(te *testEnv) { te.writeConfig("this is [not toml") }, spool: true},
		{name: "unwritable folder", args: []string{"hook", "claude"}, env: map[string]string{"REX_BLOCK": "b"}, stdin: claudePayload,
			setup: func(te *testEnv) {
				// A file where the state folder should be.
				if err := os.MkdirAll(filepath.Dir(te.stateDir()), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(te.stateDir(), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			te := newTestEnv(t)
			te.setEnv(tc.env)
			te.Stdin = strings.NewReader(tc.stdin)
			if tc.setup != nil {
				tc.setup(te)
			}
			if code := te.run(tc.args...); code != 0 {
				t.Fatalf("exit %d", code)
			}
			if te.stdout.Len() != 0 || te.stderr.Len() != 0 {
				t.Fatalf("printed: %q %q", te.stdout, te.stderr)
			}
			files := spoolFiles(t, filepath.Join(te.stateDir(), "spool"))
			if tc.spool != (len(files) == 1) {
				t.Fatalf("spool files = %v, want file: %v", files, tc.spool)
			}
		})
	}
}

type failReader struct{ t *testing.T }

func (f failReader) Read([]byte) (int, error) {
	f.t.Error("stdin was read")
	return 0, errors.New("read")
}

func TestHookTerminalStdinNotRead(t *testing.T) {
	te := newTestEnv(t)
	te.setEnv(map[string]string{"REX_BLOCK": "b"})
	te.Stdin = failReader{t}
	te.StdinIsTerminal = func() bool { return true }
	if code := te.run("hook", "claude"); code != 0 {
		t.Fatal(code)
	}
	if te.stdout.Len()+te.stderr.Len() != 0 {
		t.Fatal("printed")
	}
}

func TestHookPanicIsRecovered(t *testing.T) {
	te := newTestEnv(t)
	te.Getenv = func(string) string { panic("boom") }
	if code := te.run("hook", "claude"); code != 0 {
		t.Fatal(code)
	}
}

func TestHookInputLimit(t *testing.T) {
	te := newTestEnv(t)
	te.setEnv(map[string]string{"REX_BLOCK": "b"})
	te.Stdin = strings.NewReader(`{"hook_event_name":"Stop","pad":"` + strings.Repeat("a", 2<<20) + `"}`)
	if code := te.run("hook", "claude"); code != 0 {
		t.Fatal(code)
	}
}

// countingReader reports whether all of it was read.
type countingReader struct {
	r    io.Reader
	done bool
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if err == io.EOF {
		c.done = true
	}
	return n, err
}

// Outside Rex the hook still reads its input, so the agent writing it never
// gets a broken pipe.
func TestHookReadsInputOutsideRex(t *testing.T) {
	te := newTestEnv(t)
	in := &countingReader{r: strings.NewReader(claudePayload)}
	te.Stdin = in
	if code := te.run("hook", "claude"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !in.done {
		t.Fatal("the hook ended without reading its input")
	}
}

// blockingReader never returns until it is closed.
type blockingReader struct{ stop chan struct{} }

func (b blockingReader) Read([]byte) (int, error) {
	<-b.stop
	return 0, io.EOF
}

// Outside Rex an agent that keeps stdin open must not hold the hook.
func TestHookOutsideRexDoesNotWaitLong(t *testing.T) {
	te := newTestEnv(t)
	stop := make(chan struct{})
	defer close(stop)
	te.Stdin = blockingReader{stop}
	start := time.Now()
	if code := te.run("hook", "claude"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("the hook waited %v", d)
	}
	if te.stdout.Len()+te.stderr.Len() != 0 {
		t.Fatal("printed")
	}
}
