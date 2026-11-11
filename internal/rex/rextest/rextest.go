// Package rextest provides a fake `rex` CLI for tests.
//
// The fake is a small Go program (see fakerex) that is built once per test
// run. Each Fake gets its own folder with a scenario file, a call log, and a
// wrapper script, so tests do not share state and need no environment changes.
package rextest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var (
	buildOnce sync.Once
	buildPath string
	buildErr  error
)

func buildFake() (string, error) {
	buildOnce.Do(func() {
		_, file, _, ok := runtime.Caller(0)
		if !ok {
			buildErr = fmt.Errorf("rextest: cannot find source folder")
			return
		}
		src := filepath.Join(filepath.Dir(file), "fakerex")
		out, err := os.MkdirTemp("", "rextest-build-")
		if err != nil {
			buildErr = err
			return
		}
		bin := filepath.Join(out, "fakerex")
		goBin, err := exec.LookPath("go")
		if err != nil {
			buildErr = fmt.Errorf("rextest: find the go command: %w", err)
			return
		}
		cmd := exec.Command(goBin, "build", "-o", bin, ".")
		cmd.Dir = src
		if b, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("rextest: build fakerex: %w\n%s", err, b)
			return
		}
		buildPath = bin
	})
	return buildPath, buildErr
}

type rule struct {
	Args      []string `json:"args"`
	Stdout    string   `json:"stdout"`
	Stderr    string   `json:"stderr"`
	Exit      int      `json:"exit"`
	DelayMS   int      `json:"delay_ms"`
	EchoStdin bool     `json:"echo_stdin"`
}

type scenario struct {
	Rules          []rule         `json:"rules"`
	Events         []string       `json:"events"`
	EventsExit     bool           `json:"events_exit"`
	EventsExitCode int            `json:"events_exit_code"`
	EventsStderr   string         `json:"events_stderr"`
	Machine        *screenMachine `json:"machine"`
}

// screenMachine makes `capture` print a screen that changes when keys are
// pressed: Moves[screen][key] names the next screen. The current screen's
// name is kept in the file screen.state next to the scenario.
type screenMachine struct {
	Screens map[string]string            `json:"screens"`
	Start   string                       `json:"start"`
	Moves   map[string]map[string]string `json:"moves"`
	// Processes holds the answer of the block method `process` while a
	// screen shows, for screens where it differs from the rules.
	Processes map[string]string `json:"processes"`
}

// Call is one recorded invocation of the fake.
type Call struct {
	Args  []string // all arguments, including global flags
	Stdin string
}

// Fake is a fake `rex` executable with a scripted scenario.
type Fake struct {
	t   testing.TB
	dir string
	bin string

	mu sync.Mutex
	sc scenario
}

// New builds (once per test run) and installs a fake rex in a temporary folder.
func New(t testing.TB) *Fake {
	t.Helper()
	fake, err := buildFake()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "rex")
	script := fmt.Sprintf("#!/bin/sh\nREXTEST_DIR=%s\nexport REXTEST_DIR\nexec %s \"$@\"\n", shellQuote(dir), shellQuote(fake))
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	f := &Fake{t: t, dir: dir, bin: wrapper}
	f.save()
	return f
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Bin returns the path of the fake rex executable.
func (f *Fake) Bin() string { return f.bin }

func (f *Fake) save() {
	b, err := json.Marshal(f.sc)
	if err != nil {
		f.t.Fatal(err)
	}
	tmp := filepath.Join(f.dir, "scenario.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(f.dir, "scenario.json")); err != nil {
		f.t.Fatal(err)
	}
}

// SetScreenMachine makes `capture` print screens[start] until keys move it:
// a `send-key` call applies moves[current screen][key] for each key, in order.
// A key without a move leaves the screen as it is. It replaces any earlier
// machine and starts again at start. Rules for `capture` are ignored while a
// machine is set; `send-key` calls still need a rule for their answer.
func (f *Fake) SetScreenMachine(start string, screens map[string]string, moves map[string]map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sc.Machine = &screenMachine{Screens: screens, Start: start, Moves: moves}
	_ = os.Remove(filepath.Join(f.dir, "screen.state"))
	f.save()
}

// SetScreenProcess makes the block method `process` answer stdout while the
// screen machine shows screen, as if another program had come to the front.
func (f *Fake) SetScreenProcess(screen, stdout string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sc.Machine == nil {
		f.t.Fatal("rextest: SetScreenProcess needs a screen machine")
	}
	if f.sc.Machine.Processes == nil {
		f.sc.Machine.Processes = map[string]string{}
	}
	f.sc.Machine.Processes[screen] = stdout
	f.save()
}

// SetScreenState moves the screen machine to a screen, as if the program had
// changed it without a key.
func (f *Fake) SetScreenState(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tmp := filepath.Join(f.dir, "screen.state.tmp")
	if err := os.WriteFile(tmp, []byte(name), 0o600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(f.dir, "screen.state")); err != nil {
		f.t.Fatal(err)
	}
}

// ScreenState returns the name of the screen machine's current screen.
func (f *Fake) ScreenState() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, err := os.ReadFile(filepath.Join(f.dir, "screen.state")); err == nil && len(b) > 0 {
		return string(b)
	}
	if f.sc.Machine != nil {
		return f.sc.Machine.Start
	}
	return ""
}

func (f *Fake) add(r rule) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sc.Rules = append(f.sc.Rules, r)
	f.save()
}

// On answers every call whose arguments start with prefix (global flags such
// as --autostart=false and -S are ignored) with stdout and exit code. Of
// several matching rules, the longest prefix wins; among equal ones, the latest.
func (f *Fake) On(prefix []string, stdout string, exit int) {
	f.add(rule{Args: prefix, Stdout: stdout, Exit: exit})
}

// OnError is like On with a failing exit code and a stderr text.
func (f *Fake) OnError(prefix []string, stderr string, exit int) {
	f.add(rule{Args: prefix, Stderr: stderr, Exit: exit})
}

// OnEchoStdin makes the fake print its stdin to stderr and exit with code.
func (f *Fake) OnEchoStdin(prefix []string, exit int) {
	f.add(rule{Args: prefix, Exit: exit, EchoStdin: true})
}

// OnDelay is like On, but the fake waits for delay before it answers.
func (f *Fake) OnDelay(prefix []string, stdout string, delay time.Duration) {
	f.add(rule{Args: prefix, Stdout: stdout, DelayMS: int(delay / time.Millisecond)})
}

// SetEvents sets the lines that `rex events` prints. After them the fake
// waits until it is killed.
func (f *Fake) SetEvents(lines ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sc.Events = lines
	f.save()
}

// SetEventsExit makes `rex events` exit after its lines with a code and stderr text.
func (f *Fake) SetEventsExit(code int, stderr string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sc.EventsExit, f.sc.EventsExitCode, f.sc.EventsStderr = true, code, stderr
	f.save()
}

// Calls returns every recorded invocation, oldest first.
func (f *Fake) Calls() []Call {
	file, err := os.Open(filepath.Join(f.dir, "calls.jsonl"))
	if err != nil {
		return nil
	}
	defer file.Close()
	var calls []Call
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		var c struct {
			Args  []string `json:"args"`
			Stdin string   `json:"stdin"`
		}
		if json.Unmarshal(sc.Bytes(), &c) == nil {
			calls = append(calls, Call{Args: c.Args, Stdin: c.Stdin})
		}
	}
	return calls
}

// EventsPID returns the process ID of the last `rex events` run, or 0.
func (f *Fake) EventsPID() int {
	b, err := os.ReadFile(filepath.Join(f.dir, "events.pid"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

// ProcessAlive reports whether a process with this ID still exists.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
