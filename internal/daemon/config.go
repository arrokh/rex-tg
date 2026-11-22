package daemon

import (
	"context"
	"errors"
	"strconv"

	"rex-tg/internal/agents"
	"rex-tg/internal/miniapp"
	"rex-tg/internal/store"
)

const configActionBase = int64(1) << 59 // separate queued-row range for Config actions

// configRowID makes a stable queued row id for a page action, so a retried
// action cannot type the setting twice.
func configRowID(id string) int64 {
	n, _ := strconv.ParseInt(id[:15], 16, 64)
	return configActionBase + n
}

// State reads only the controls of the session named by a Config page.
func (d *daemon) State(ctx context.Context, thread, convID int64) (miniapp.ConfigState, error) {
	th, err := d.st.Thread(ctx, thread)
	if err != nil || th.Status == store.ThreadClosed || convID <= 0 || th.CurrentConversationID != convID {
		return miniapp.ConfigState{}, errors.New("config page is stale")
	}
	conv, err := d.st.Conversation(ctx, convID)
	if err != nil || conv.ThreadID != thread {
		return miniapp.ConfigState{}, errors.New("config page is stale")
	}
	a, ok := d.adapters[conv.Runtime]
	if !ok {
		return miniapp.ConfigState{}, errors.New("agent has no settings")
	}
	ctrl, ok := a.(agents.Controller)
	if !ok {
		return miniapp.ConfigState{}, errors.New("agent has no settings")
	}
	values := d.currentValues(conv, ctrl)
	out := miniapp.ConfigState{Controls: make([]miniapp.ConfigItem, 0)}
	for _, name := range []string{"model", "effort"} {
		c, _, ok := d.controlOf(conv.Runtime, name)
		if !ok {
			continue
		}
		out.Controls = append(out.Controls, miniapp.ConfigItem{
			Name: c.Name, Title: c.Title, Value: values[c.Name], Choices: append([]string(nil), c.Choices...), Free: c.Free,
		})
	}
	if len(out.Controls) == 0 {
		return miniapp.ConfigState{}, errors.New("agent has no model or effort controls")
	}
	return out, nil
}
