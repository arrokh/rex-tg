package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

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

var configPiID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// configControl keeps the Mini App picker closed to values from the agent's
// catalog. The Telegram /model command retains its existing free-text path.
func (d *daemon) configControl(conv store.Conversation, name string) (agents.Control, agents.Controller, bool) {
	c, ctrl, ok := d.controlOf(conv.Runtime, name)
	if !ok || name != "model" {
		return c, ctrl, ok
	}
	if len(d.cfg.Input(conv.Runtime).Choices[name]) == 0 {
		switch conv.Runtime {
		case agents.Pi:
			c.Choices = piConfigModels(d.cfg.LiveDir(), conv.NativeID)
		case agents.Codex:
			c.Choices = codexConfigModels(conv)
		case agents.Claude:
			c.Choices = claudeModelChoices(conv, "/Library/Application Support/ClaudeCode/managed-settings.json", c.Choices)
		}
	}
	choices := make([]string, 0, len(c.Choices))
	seen := map[string]bool{}
	// Check the adapter's free-value syntax even for catalog entries: an
	// extension or local model configuration may contain unusable IDs.
	base := c
	base.Choices = nil
	for _, choice := range c.Choices {
		if v, err := base.CheckValue(choice); err == nil && !seen[strings.ToLower(v)] {
			choices = append(choices, v)
			seen[strings.ToLower(v)] = true
		}
	}
	c.Choices, c.Free = choices, false
	return c, ctrl, true
}

func piConfigModels(dir, id string) []string {
	if !configPiID.MatchString(id) {
		return nil
	}
	var choices []string
	if b, err := readCatalogFile(filepath.Join(dir, "models", id+".json")); err != nil || json.Unmarshal(b, &choices) != nil || len(choices) > 2048 {
		return nil
	}
	return choices
}

func readCatalogFile(path string) ([]byte, error) {
	const limit = 4 << 20
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("invalid model catalog file")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("invalid model catalog file")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if len(data) > limit {
		return nil, errors.New("model catalog is too large")
	}
	return data, err
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
		c, _, ok := d.configControl(conv, name)
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
