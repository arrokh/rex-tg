package claude

import (
	"bytes"
	"encoding/json"

	"rex-tg/internal/agents"
)

var controls = []agents.Control{
	{Name: "model", Title: "Model", Choices: []string{"opus", "sonnet", "haiku", "fable"}, Free: true},
	{Name: "effort", Title: "Thinking effort", Choices: []string{"low", "medium", "high", "xhigh", "max"}},
}

// Controls lists the settings that /model and /effort change.
func (adapter) Controls() []agents.Control { return append([]agents.Control(nil), controls...) }

// Command returns the slash command that sets a control. Claude Code takes
// the new value at once, without a picker.
func (adapter) Command(control, value string, _ map[string]string) (string, error) {
	v, err := agents.CheckControl(controls, control, value)
	if err != nil {
		return "", err
	}
	return "/" + control + " " + v, nil
}

// Seen reads the model and the effort from an assistant line: the model is
// message.model and the effort is the top-level "effort" string. Both field
// names come from the transcript survey in docs/agent-transcripts.md; a
// non-string effort is ignored. Lines of nested agents and the synthetic
// model that Claude Code writes for errors are skipped.
func (adapter) Seen(line []byte) map[string]string {
	// Cheap checks first: this runs for every line of the transcript.
	if !bytes.Contains(line, []byte(`"type":"assistant"`)) || (!bytes.Contains(line, []byte(`"model"`)) && !bytes.Contains(line, []byte(`"effort"`))) {
		return nil
	}
	var l struct {
		Type        string `json:"type"`
		IsSidechain bool   `json:"isSidechain"`
		Effort      any    `json:"effort"`
		Message     *struct {
			Model string `json:"model"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &l) != nil || l.Type != "assistant" || l.IsSidechain {
		return nil
	}
	out := map[string]string{}
	if l.Message != nil && l.Message.Model != "" && l.Message.Model[0] != '<' {
		out["model"] = l.Message.Model
	}
	if e, ok := l.Effort.(string); ok && e != "" {
		out["effort"] = e
	}
	return out
}
