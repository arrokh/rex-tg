package install

import (
	"bytes"
	"errors"
	"io/fs"
	"os"

	"github.com/tidwall/gjson"
)

// State says whether an installed item is present and up to date.
type State string

// Possible states.
const (
	StateMissing  State = "missing"  // not installed
	StateCurrent  State = "current"  // installed and matches this binary
	StateOutdated State = "outdated" // installed but differs (older binary path or content)
)

// HookReport is the state of one hook file.
type HookReport struct {
	Path   string
	Exists bool
	Events map[string]State // one entry per event rex-tg installs
	Err    string           // set when the file could not be read or parsed
}

// Installed reports whether every event is current.
func (h HookReport) Installed() bool {
	if h.Err != "" || len(h.Events) == 0 {
		return false
	}
	for _, s := range h.Events {
		if s != StateCurrent {
			return false
		}
	}
	return true
}

// FileReport is the state of a file that rex-tg owns completely.
type FileReport struct {
	Path  string
	State State
	Err   string
}

// Report is the result of Inspect.
type Report struct {
	Claude  HookReport
	Codex   HookReport
	Pi      FileReport
	Launchd FileReport
}

// Inspect reports which entries and files are installed and whether they are
// current. It never writes.
func Inspect(p Paths) (Report, error) {
	if err := p.validate(); err != nil {
		return Report{}, err
	}
	return Report{
		Claude:  p.inspectHooks(p.ClaudeSettings, "claude", claudeEvents),
		Codex:   p.inspectHooks(p.CodexHooks, "codex", codexEvents),
		Pi:      inspectFile(p.PiExtension, p.piSource()),
		Launchd: inspectFile(p.LaunchAgent, p.plist()),
	}, nil
}

func inspectFile(path string, want []byte) FileReport {
	r := FileReport{Path: path, State: StateMissing}
	got, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		r.Err = err.Error()
	case bytes.Equal(got, want):
		r.State = StateCurrent
	default:
		r.State = StateOutdated
	}
	return r
}

func (p Paths) inspectHooks(path, runtime string, events []string) HookReport {
	r := HookReport{Path: path, Events: map[string]State{}}
	for _, ev := range events {
		r.Events[ev] = StateMissing
	}
	data, _, exists, err := readState(path)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	r.Exists = exists
	if !exists {
		return r
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return r
	}
	if !gjson.ValidBytes(data) {
		r.Err = "not valid JSON"
		return r
	}
	for _, ev := range events {
		forEachHook(gjson.GetBytes(data, "hooks."+ev), func(_, _ int, h gjson.Result) {
			ours, exact := p.isOurs(h.Get("command").String(), runtime)
			switch {
			case !ours:
			case exact:
				r.Events[ev] = StateCurrent
			case r.Events[ev] == StateMissing:
				r.Events[ev] = StateOutdated
			}
		})
	}
	return r
}
