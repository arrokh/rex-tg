package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"rex-tg/internal/rex/rextest"
	"rex-tg/internal/store"
)

func TestNewCommandStartsEachAgentInTheCurrentFolder(t *testing.T) {
	for _, tc := range []struct {
		name      string
		command   []string
		agentArgs []string
	}{
		{name: "pi", command: []string{"pi"}},
		{name: "cux", command: []string{"cux"}, agentArgs: []string{"--resume", "session with spaces", "--config", "agent.toml", "--", "-v"}},
		{name: "claude", command: []string{"claude"}},
		{name: "codex", command: []string{"codex"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			te := newTestEnv(t)
			folder := filepath.Join(te.home, "work", "app")
			if err := os.MkdirAll(folder, 0o755); err != nil {
				t.Fatal(err)
			}
			te.Env.Getwd = func() (string, error) { return folder, nil }
			fake := rextest.New(t)
			fake.On([]string{"ls"}, `{"sessions":[]}`, 0)
			fake.On([]string{"new"}, `{"session_id":"S1","initial_windows":[{"window_id":"W1","block_ids":["B1"]}]}`, 0)
			if tc.name == "cux" {
				bin, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(te.home, ".local", "bin", "cux")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(bin, path); err != nil {
					t.Fatal(err)
				}
			}
			te.writeConfig(fmt.Sprintf("[rex]\ncli = %q\n", fake.Bin()))

			runArgs := append([]string{"new", tc.name}, tc.agentArgs...)
			if code := te.run(runArgs...); code != 0 {
				t.Fatalf("exit %d; stderr: %s", code, te.stderr.String())
			}
			if !strings.Contains(te.stdout.String(), "Started "+tc.name+" in "+folder) {
				t.Fatalf("stdout %q does not report the current folder", te.stdout.String())
			}
			if got := te.stderr.String(); got != "" {
				t.Fatalf("stderr %q, want empty", got)
			}

			var createCall []string
			for _, call := range fake.Calls() {
				if i := indexOf(call.Args, "new"); i >= 0 && i+1 < len(call.Args) && strings.HasPrefix(call.Args[i+1], "--cwd=") {
					createCall = call.Args
					break
				}
			}
			if createCall == nil {
				t.Fatalf("no Rex session creation call: %+v", fake.Calls())
			}
			if !contains(createCall, "--cwd="+folder) || !contains(createCall, "--window=app · "+tc.name) {
				t.Fatalf("Rex new args %q do not use folder/tab", createCall)
			}
			sep := indexOf(createCall, "--")
			wantCommand := append(append([]string(nil), tc.command...), tc.agentArgs...)
			if sep < 0 || !reflect.DeepEqual(createCall[sep+1:], wantCommand) {
				t.Fatalf("command after -- = %q, want %q", createCall[sep+1:], wantCommand)
			}

			st, err := store.Open(filepath.Join(te.stateDir(), "rex-tg.db"))
			if err != nil {
				t.Fatal(err)
			}
			id, ok, err := st.Get(context.Background(), "new_session_id")
			_ = st.Close()
			if err != nil || !ok || id != "S1" {
				t.Fatalf("stored session ID = %q, %v, %v; want S1", id, ok, err)
			}
		})
	}
}

func TestNewCommandAddsTabToTheOwnedRexSession(t *testing.T) {
	te := newTestEnv(t)
	folder := filepath.Join(te.home, "work", "app")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	te.Env.Getwd = func() (string, error) { return folder, nil }
	fake := rextest.New(t)
	fake.On([]string{"ls"}, `{"sessions":[{"session_id":"S1","label":"renamed session"}]}`, 0)
	fake.On([]string{"window", "new"}, `{"window_id":"W2","block_ids":["B2"]}`, 0)
	te.writeConfig(fmt.Sprintf("[rex]\ncli = %q\n", fake.Bin()))

	st, err := store.Open(filepath.Join(te.stateDir(), "rex-tg.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Set(context.Background(), "new_session_id", "S1"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	if code := te.run("new", "Pi"); code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, te.stderr.String())
	}
	if !strings.Contains(te.stdout.String(), `Rex session "rex-tg"`) {
		t.Fatalf("stdout %q does not identify the configured session", te.stdout.String())
	}
	calls := fake.Calls()
	if len(calls) != 2 {
		t.Fatalf("Rex calls = %+v, want list and window new", calls)
	}
	if !reflect.DeepEqual(calls[1].Args[len(calls[1].Args)-8:], []string{"--session=S1", "--cwd=" + folder, "--interactive", "--focus=false", "--json", "app · pi", "--", "pi"}) {
		t.Fatalf("window new args = %q", calls[1].Args)
	}
}

func TestNewCommandFindsTheRexSessionByConfiguredName(t *testing.T) {
	te := newTestEnv(t)
	folder := filepath.Join(te.home, "work", "app")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	te.Env.Getwd = func() (string, error) { return folder, nil }
	fake := rextest.New(t)
	fake.On([]string{"ls"}, `{"sessions":[{"session_id":"S2","label":"  REX-TG  "}]}`, 0)
	fake.On([]string{"window", "new"}, `{"window_id":"W2","block_ids":["B2"]}`, 0)
	te.writeConfig(fmt.Sprintf("[rex]\ncli = %q\n", fake.Bin()))

	if code := te.run("new", "pi"); code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, te.stderr.String())
	}
	calls := fake.Calls()
	if len(calls) != 2 || !contains(calls[1].Args, "--session=S2") {
		t.Fatalf("Rex calls = %+v, want window new in S2", calls)
	}
}

func TestNewCommandDoesNotRetryAnUnconfirmedTabCreation(t *testing.T) {
	te := newTestEnv(t)
	folder := filepath.Join(te.home, "work", "app")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	te.Env.Getwd = func() (string, error) { return folder, nil }
	fake := rextest.New(t)
	fake.On([]string{"ls"}, `{"sessions":[]}`, 0)
	fake.OnError([]string{"new"}, "answer lost", 1)
	te.writeConfig(fmt.Sprintf("[rex]\ncli = %q\n", fake.Bin()))

	if code := te.run("new", "pi"); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(te.stderr.String(), "may exist already") {
		t.Fatalf("stderr %q does not warn about an unknown outcome", te.stderr.String())
	}
	creates := 0
	for _, call := range fake.Calls() {
		if indexOf(call.Args, "new") >= 0 {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("Rex session creation attempts = %d, want 1", creates)
	}
}

func TestNewCommandRejectsUnknownAgentWithoutCallingRex(t *testing.T) {
	te := newTestEnv(t)
	fake := rextest.New(t)
	te.writeConfig(fmt.Sprintf("[rex]\ncli = %q\n", fake.Bin()))

	if code := te.run("new", "vim"); code != 2 {
		t.Fatalf("exit %d, want 2; stderr: %s", code, te.stderr.String())
	}
	if len(fake.Calls()) != 0 {
		t.Fatalf("Rex calls = %+v, want none", fake.Calls())
	}
}

func indexOf(xs []string, want string) int {
	for i, x := range xs {
		if x == want {
			return i
		}
	}
	return -1
}

func contains(xs []string, want string) bool { return indexOf(xs, want) >= 0 }
