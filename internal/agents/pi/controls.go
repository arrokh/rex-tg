package pi

import (
	"bytes"
	"encoding/json"

	"rex-tg/internal/agents"
)

// The effort control is pi's thinking level. It is called "effort" so that
// /effort means the same in every agent; the command pi takes is /thinking.
var controls = []agents.Control{
	// pi knows hundreds of models, so there are no buttons by default.
	{Name: "model", Title: "Model", Free: true},
	{Name: "effort", Title: "Thinking level", Choices: []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}},
}

// Controls lists the settings that /model and /effort change.
func (adapter) Controls() []agents.Control { return append([]agents.Control(nil), controls...) }

// Command returns the slash command that sets a control.
func (adapter) Command(control, value string, _ map[string]string) (string, error) {
	v, err := agents.CheckControl(controls, control, value)
	if err != nil {
		return "", err
	}
	if control == "effort" {
		return "/thinking " + v, nil
	}
	return "/model " + v, nil
}

// Seen reads the settings from three kinds of lines: model_change
// (provider, modelId), thinking_level_change (thinkingLevel), and assistant
// messages (message.provider, message.model, and thinkingLevel, which is read
// from the message or from the line itself).
func (adapter) Seen(line []byte) map[string]string {
	if !bytes.Contains(line, []byte(`"model`)) && !bytes.Contains(line, []byte(`"thinkingLevel"`)) {
		return nil
	}
	var l struct {
		Type          string `json:"type"`
		Provider      string `json:"provider"`
		ModelID       string `json:"modelId"`
		ThinkingLevel string `json:"thinkingLevel"`
		Message       *struct {
			Role          string `json:"role"`
			Provider      string `json:"provider"`
			Model         string `json:"model"`
			ThinkingLevel string `json:"thinkingLevel"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &l) != nil {
		return nil
	}
	out := map[string]string{}
	switch l.Type {
	case "model_change":
		out["model"] = join(l.Provider, l.ModelID)
	case "thinking_level_change":
		out["effort"] = l.ThinkingLevel
	case "message":
		if l.Message != nil && l.Message.Role == "assistant" {
			out["model"] = join(l.Message.Provider, l.Message.Model)
			out["effort"] = firstNonEmpty(l.Message.ThinkingLevel, l.ThinkingLevel)
		}
	}
	for k, v := range out {
		if v == "" {
			delete(out, k)
		}
	}
	return out
}

// join makes "provider/id", or just the id when the provider is unknown.
func join(provider, id string) string {
	if id == "" {
		return ""
	}
	if provider == "" {
		return id
	}
	return provider + "/" + id
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
