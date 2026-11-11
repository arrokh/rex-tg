package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"rex-tg/internal/agents"
)

// HookReport is one spool file written by `rex-tg hook <runtime>`.
type HookReport struct {
	Runtime    agents.Runtime
	Event      string
	At         time.Time
	RexSession string
	RexBlock   string
	Payload    json.RawMessage // the hook's stdin JSON
}

// ParseSpool decodes the content of a spool file.
func ParseSpool(data []byte) (HookReport, error) {
	var raw struct {
		Runtime    string          `json:"runtime"`
		Event      string          `json:"event"`
		At         string          `json:"at"`
		RexSession string          `json:"rex_session"`
		RexBlock   string          `json:"rex_block"`
		Payload    json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return HookReport{}, fmt.Errorf("registry: parse spool file: %w", err)
	}
	rt := agents.Runtime(raw.Runtime)
	if !rt.Valid() {
		return HookReport{}, fmt.Errorf("registry: spool file has unknown runtime %q", raw.Runtime)
	}
	if raw.Event == "" {
		return HookReport{}, errors.New("registry: spool file has no event")
	}
	rep := HookReport{Runtime: rt, Event: raw.Event, RexSession: raw.RexSession, RexBlock: raw.RexBlock, Payload: raw.Payload}
	if raw.At != "" {
		t, err := time.Parse(time.RFC3339Nano, raw.At)
		if err != nil {
			return HookReport{}, fmt.Errorf("registry: spool file time %q: %w", raw.At, err)
		}
		rep.At = t
	}
	return rep, nil
}
