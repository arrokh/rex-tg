// Command fakerex is a fake `rex` executable for tests. Each test starts it
// through a symlink named rex in the test's own folder, and the files in that
// folder drive it (see package rextest).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
}

type call struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
}

func main() {
	// On macOS, os.Executable keeps the path of the symlink the fake was
	// started by, so its folder is the test's folder.
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakerex: find the executable:", err)
		os.Exit(98)
	}
	dir := filepath.Dir(exe)
	if _, err := os.Stat(filepath.Join(dir, "scenario.json")); err != nil {
		fmt.Fprintln(os.Stderr, "fakerex: start it through the symlink that rextest.New makes:", err)
		os.Exit(98)
	}
	args := os.Args[1:]
	stripped := stripGlobals(args)

	var sc scenario
	if b, err := os.ReadFile(filepath.Join(dir, "scenario.json")); err == nil {
		_ = json.Unmarshal(b, &sc)
	}

	isEvents := len(stripped) > 0 && stripped[0] == "events"
	var stdin []byte
	if !isEvents && stdinIsPipe() {
		stdin, _ = io.ReadAll(os.Stdin)
	}
	logCall(dir, call{Args: args, Stdin: string(stdin)})

	if isEvents {
		runEvents(dir, sc)
		return
	}
	if m := sc.Machine; m != nil && len(stripped) > 0 {
		switch stripped[0] {
		case "capture":
			fmt.Fprint(os.Stdout, m.Screens[currentScreen(dir, m)])
			os.Exit(0)
		case "send-key":
			if len(stripped) > 5 {
				cur := currentScreen(dir, m)
				for _, k := range stripped[5:] {
					if next, ok := m.Moves[cur][k]; ok {
						cur = next
					}
				}
				_ = writeAtomic(filepath.Join(dir, "screen.state"), []byte(cur))
			}
		}
	}

	best := -1
	bestLen := -1
	for i, r := range sc.Rules {
		if hasPrefix(stripped, r.Args) && len(r.Args) >= bestLen {
			best, bestLen = i, len(r.Args)
		}
	}
	if best < 0 {
		fmt.Fprintf(os.Stderr, "fakerex: no scenario rule for %q\n", strings.Join(stripped, " "))
		os.Exit(97)
	}
	r := sc.Rules[best]
	if r.DelayMS > 0 {
		waitOrExitWhenOrphaned(time.Duration(r.DelayMS) * time.Millisecond)
	}
	fmt.Fprint(os.Stdout, r.Stdout)
	fmt.Fprint(os.Stderr, r.Stderr)
	if r.EchoStdin {
		fmt.Fprint(os.Stderr, string(stdin))
	}
	os.Exit(r.Exit)
}

func currentScreen(dir string, m *screenMachine) string {
	if b, err := os.ReadFile(filepath.Join(dir, "screen.state")); err == nil && len(b) > 0 {
		return string(b)
	}
	return m.Start
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func runEvents(dir string, sc scenario) {
	_ = os.WriteFile(filepath.Join(dir, "events.pid"), []byte(fmt.Sprint(os.Getpid())), 0o600)
	for _, l := range sc.Events {
		fmt.Fprintln(os.Stdout, l)
	}
	if sc.EventsExit {
		fmt.Fprint(os.Stderr, sc.EventsStderr)
		os.Exit(sc.EventsExitCode)
	}
	// Wait until killed, like the real `rex events`.
	waitOrExitWhenOrphaned(time.Hour)
}

// waitOrExitWhenOrphaned waits for d, but exits at once when the parent is
// gone. A real `rex` ends when its reader is gone (its next write fails); the
// fake may print nothing more, so it watches for its parent instead. The e2e
// crash tests kill the daemon with SIGKILL, which would leave it behind.
func waitOrExitWhenOrphaned(d time.Duration) {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); {
		if os.Getppid() == 1 {
			os.Exit(0)
		}
		time.Sleep(min(200*time.Millisecond, time.Until(deadline)))
	}
}

func stdinIsPipe() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice == 0
}

// stripGlobals removes the leading global flags of rex.
func stripGlobals(args []string) []string {
	for len(args) > 0 {
		a := args[0]
		switch {
		case a == "-S" || a == "--server" || a == "--timeout" || a == "--color" || a == "-C" || a == "--client":
			if len(args) < 2 {
				return nil
			}
			args = args[2:]
		case strings.HasPrefix(a, "--autostart") || strings.HasPrefix(a, "--timeout=") || strings.HasPrefix(a, "--color=") || a == "-v" || a == "--verbose":
			args = args[1:]
		default:
			return args
		}
	}
	return args
}

func hasPrefix(args, prefix []string) bool {
	if len(prefix) > len(args) {
		return false
	}
	for i := range prefix {
		if args[i] != prefix[i] {
			return false
		}
	}
	return true
}

func logCall(dir string, c call) {
	b, _ := json.Marshal(c)
	f, err := os.OpenFile(filepath.Join(dir, "calls.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}
