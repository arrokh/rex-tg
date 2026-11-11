// Command fakerex is a fake `rex` executable for tests. It is driven by the
// directory named in REXTEST_DIR (see package rextest).
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
	Rules          []rule   `json:"rules"`
	Events         []string `json:"events"`
	EventsExit     bool     `json:"events_exit"`
	EventsExitCode int      `json:"events_exit_code"`
	EventsStderr   string   `json:"events_stderr"`
}

type call struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
}

func main() {
	dir := os.Getenv("REXTEST_DIR")
	if dir == "" {
		fmt.Fprintln(os.Stderr, "fakerex: REXTEST_DIR is not set")
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
		time.Sleep(time.Duration(r.DelayMS) * time.Millisecond)
	}
	fmt.Fprint(os.Stdout, r.Stdout)
	fmt.Fprint(os.Stderr, r.Stderr)
	if r.EchoStdin {
		fmt.Fprint(os.Stderr, string(stdin))
	}
	os.Exit(r.Exit)
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
	time.Sleep(time.Hour) // wait until killed
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
