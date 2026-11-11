package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rex-tg/internal/store"
)

const fakeSecurityScript = `#!/bin/bash
DIR="$(dirname "$0")"
mkdir -p "$DIR/items"
echo "$*" >> "$DIR/argv.log"
run() {
  local cmd="$1"; shift
  local s="" a="" w=""
  while [ $# -gt 0 ]; do
    case "$1" in
      -s) s="$2"; shift 2;;
      -a) a="$2"; shift 2;;
      -w) if [ $# -ge 2 ]; then w="$2"; shift 2; else IFS= read -r w; shift; fi;;
      *) shift;;
    esac
  done
  f="$DIR/items/$s.$a"
  case "$cmd" in
    add-generic-password) printf '%s' "$w" > "$f";;
    find-generic-password)
      if [ ! -f "$f" ]; then echo "security: SecItemCopyMatching: The specified item could not be found in the keychain." >&2; return 44; fi
      cat "$f"; echo;;
    delete-generic-password)
      if [ ! -f "$f" ]; then return 44; fi
      rm "$f";;
  esac
}
if [ "$1" = "-i" ]; then
  while IFS= read -r line; do
    eval "set -- $line"
    run "$@"
  done
  exit 0
fi
run "$@"
`

// testEnv is an Env that touches only a temporary folder.
type testEnv struct {
	Env
	t      *testing.T
	home   string
	secDir string
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	runner *runnerLog
	exe    string
}

type runnerLog struct {
	mu    sync.Mutex
	calls []string
	fail  func(call string) bool
}

func (r *runnerLog) run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
	if r.fail != nil && r.fail(call) {
		return []byte("not loaded"), fmt.Errorf("exit status 113")
	}
	return nil, nil
}

func (r *runnerLog) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	secDir := filepath.Join(root, "sec")
	for _, d := range []string{home, secDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sec := filepath.Join(secDir, "security")
	if err := os.WriteFile(sec, []byte(fakeSecurityScript), 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "bin", "rex-tg")
	te := &testEnv{t: t, home: home, secDir: secDir, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, runner: &runnerLog{}, exe: exe}
	te.Env = Env{
		Stdin:           strings.NewReader(""),
		Stdout:          te.stdout,
		Stderr:          te.stderr,
		Getenv:          func(string) string { return "" },
		Home:            home,
		Security:        sec,
		Now:             func() time.Time { return time.Date(2026, 11, 11, 3, 12, 54, 486_000_000, time.UTC) },
		Executable:      func() (string, error) { return exe, nil },
		StdinIsTerminal: func() bool { return false },
		Runner:          te.runner.run,
		UID:             "501",
		PairingCode: func(context.Context, *store.Store, time.Time) (string, error) {
			return "ABC-123", nil
		},
		Interactive: func(context.Context, string, ...string) error { return fmt.Errorf("interactive not expected") },
	}
	return te
}

func (te *testEnv) run(args ...string) int {
	te.t.Helper()
	te.stdout.Reset()
	te.stderr.Reset()
	return run(context.Background(), args, te.Env)
}

func (te *testEnv) setEnv(m map[string]string) {
	te.Env.Getenv = func(k string) string { return m[k] }
}

func (te *testEnv) writeConfig(content string) {
	te.t.Helper()
	path := filepath.Join(te.home, ".config", "rex-tg", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		te.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		te.t.Fatal(err)
	}
}

func (te *testEnv) stateDir() string { return filepath.Join(te.home, ".local", "state", "rex-tg") }

func (te *testEnv) storedToken() string {
	b, _ := os.ReadFile(filepath.Join(te.secDir, "items", "rex-tg.bot-token"))
	return string(b)
}

func (te *testEnv) securityArgv() string {
	b, _ := os.ReadFile(filepath.Join(te.secDir, "argv.log"))
	return string(b)
}

func realSecurityInteractive(stdin string) func(ctx context.Context, name string, args ...string) error {
	return func(ctx context.Context, name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Stdin = strings.NewReader(stdin)
		return cmd.Run()
	}
}
