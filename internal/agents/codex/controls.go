package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"rex-tg/internal/agents"
)

var controls = []agents.Control{
	{Name: "model", Title: "Model", Free: true},
	{Name: "effort", Title: "Reasoning effort", Choices: []string{"low", "medium", "high", "xhigh", "max"}},
}

// Controls lists the settings that /model and /effort change.
func (adapter) Controls() []agents.Control { return append([]agents.Control(nil), controls...) }

// Command returns the /model command. Codex sets the model and the reasoning
// effort together (`/model <slug> --reasoning=<level>`), without a picker. So
// each change needs the other value from the transcript (cur).
func (adapter) Command(control, value string, cur map[string]string) (string, error) {
	v, err := agents.CheckControl(controls, control, value)
	if err != nil {
		return "", err
	}
	model, effort := cur["model"], cur["effort"]
	switch control {
	case "model":
		model = v
		if effort == "" {
			return "", errors.New("send a message first, so rex-tg sees the reasoning effort")
		}
	case "effort":
		effort = v
		if model == "" {
			return "", errors.New("send a message first, so rex-tg sees the model")
		}
	}
	// The value that came from the transcript is typed too, so it must have
	// a safe form. It need not be in the list: Codex knows more levels (such
	// as minimal) than the buttons offer.
	if !agents.ValidValue(model) || !agents.ValidValue(effort) {
		return "", errors.New("the current model or effort looks wrong; set both, for example /model <name> after a message")
	}
	return "/model " + model + " --reasoning=" + effort, nil
}

// Seen reads the model and the effort from a turn_context line
// (payload.model and payload.effort).
func (adapter) Seen(line []byte) map[string]string {
	if !bytes.Contains(line, []byte(`turn_context`)) {
		return nil
	}
	var l struct {
		Type    string `json:"type"`
		Payload struct {
			Model  string `json:"model"`
			Effort string `json:"effort"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &l) != nil || l.Type != "turn_context" {
		return nil
	}
	out := map[string]string{}
	if l.Payload.Model != "" {
		out["model"] = l.Payload.Model
	}
	if l.Payload.Effort != "" {
		out["effort"] = l.Payload.Effort
	}
	return out
}

// Choices lists the models that Codex shows in its picker, from
// ~/.codex/models_cache.json. It gives nothing for the other controls or when
// the file is missing or unreadable.
func (adapter) Choices(home, control string) []string {
	if control != "model" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(home, ".codex", "models_cache.json"))
	if err != nil {
		return nil
	}
	return modelChoices(b)
}

// modelChoices returns the slugs of the visible models, in the file's order.
func modelChoices(cache []byte) []string {
	var f struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if json.Unmarshal(cache, &f) != nil {
		return nil
	}
	var out []string
	for _, m := range f.Models {
		if m.Visibility == "list" && m.Slug != "" {
			out = append(out, m.Slug)
		}
	}
	return out
}
