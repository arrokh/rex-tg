package agents

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Control is a session setting that the user can change from Telegram, for
// example the model or the thinking effort. Its Name is also the Telegram
// command (/model, /effort), so the same name means the same setting for
// every agent.
type Control struct {
	Name    string   // Telegram command and key of the seen values: "model", "effort"
	Title   string   // shown to the user: "Model", "Thinking effort"
	Choices []string // values offered as buttons; may be empty (free text only)
	Free    bool     // a value outside Choices is allowed when it passes CheckValue
}

// Controller is implemented by adapters whose agent takes typed commands for
// its settings. Adding a setting or an agent means adding data or a method
// here, not a branch in the daemon.
type Controller interface {
	// Controls lists the settings of this agent.
	Controls() []Control
	// Command returns the text to type into the pane to set control to value.
	// cur holds the latest values seen in the transcript, by control name; it
	// may lack a key. The value is checked again here: the text goes to a
	// terminal.
	Command(control, value string, cur map[string]string) (string, error)
	// Seen returns the setting values that one transcript line shows, by
	// control name. Most lines show none. It must not panic on any input.
	Seen(line []byte) map[string]string
}

// ChoiceSource is implemented by adapters that can list more choices from the
// agent's own files (for example Codex's model cache). home is the user's
// home folder. An empty answer means "use the adapter's own Choices".
type ChoiceSource interface {
	Choices(home, control string) []string
}

// maxFreeValue is the longest free value.
const maxFreeValue = 80

// freeValueRe is what a free value (a model name) may look like. It has no
// space, quote, or control character, because the value is typed into a
// terminal. It cannot start with "-": the agent could read that as an option.
var freeValueRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/\[\]-]{0,79}$`)

// ValidValue tells whether v has the form of a free value.
func ValidValue(v string) bool { return freeValueRe.MatchString(v) }

// FindControl returns the control with the given name.
func FindControl(cs []Control, name string) (Control, bool) {
	for _, c := range cs {
		if c.Name == name {
			return c, true
		}
	}
	return Control{}, false
}

// CheckValue checks a value for the control and returns it in its canonical
// form. A value that equals a choice (ignoring case) returns that choice.
func (c Control) CheckValue(value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", errors.New("no value")
	}
	for _, ch := range c.Choices {
		if strings.EqualFold(ch, v) {
			return ch, nil
		}
	}
	if c.Free {
		if !freeValueRe.MatchString(v) {
			return "", fmt.Errorf("%q is not a valid %s: use letters, digits, and . _ : / [ ] - only (at most %d)", clip(v), strings.ToLower(c.Title), maxFreeValue)
		}
		return v, nil
	}
	return "", fmt.Errorf("%q is not a %s. Use: %s", clip(v), strings.ToLower(c.Title), strings.Join(c.Choices, ", "))
}

func clip(s string) string {
	if r := []rune(s); len(r) > 30 {
		return string(r[:30]) + "…"
	}
	return s
}

// CheckControl finds the control and checks the value. Adapters call it first
// in Command.
func CheckControl(cs []Control, control, value string) (string, error) {
	c, ok := FindControl(cs, control)
	if !ok {
		return "", fmt.Errorf("no %s setting", control)
	}
	return c.CheckValue(value)
}

// MergeSeen copies the values of add into cur and tells whether cur changed.
func MergeSeen(cur, add map[string]string) bool {
	changed := false
	for k, v := range add {
		if v != "" && cur[k] != v {
			cur[k] = v
			changed = true
		}
	}
	return changed
}
