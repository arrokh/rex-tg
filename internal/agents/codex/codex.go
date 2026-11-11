// Package codex is the transcript adapter for the Codex CLI. It turns rollout
// JSONL lines into agents.Event values. See docs/agent-transcripts.md.
//
// Codex records each reply several times. This adapter reads replies only
// from response_item messages, prompts only from item_completed UserMessage
// items, and treats task_complete as a bare end-of-turn marker.
package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"rex-tg/internal/agents"
)

const maxMetaLine = 16 << 20 // longest session_meta line the adapter will read

type adapter struct{}

// New returns the Codex adapter.
func New() agents.Adapter { return adapter{} }

func (adapter) Runtime() agents.Runtime { return agents.Codex }

func (adapter) Input() agents.InputProfile {
	return agents.InputProfile{
		PasteEnterDelay: 300 * time.Millisecond,
		SubmitKey:       "enter",
		InterruptKeys:   []string{"escape"},
		WhileWorking:    "unknown",
	}
}

type meta struct {
	ID        string
	CWD       string
	Timestamp string
}

func readMeta(r io.Reader) (meta, error) {
	br := bufio.NewReader(r)
	line, over, err := readLine(br, maxMetaLine)
	if err != nil {
		return meta{}, fmt.Errorf("codex: read first line: %w", err)
	}
	if over {
		return meta{}, errors.New("codex: first line too long")
	}
	var l struct {
		Type    string `json:"type"`
		Payload struct {
			ID        string `json:"id"`
			CWD       string `json:"cwd"`
			Timestamp string `json:"timestamp"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &l); err != nil {
		return meta{}, fmt.Errorf("codex: decode session_meta: %w", err)
	}
	if l.Type != "session_meta" || l.Payload.ID == "" {
		return meta{}, errors.New("codex: first line is not a session_meta with an id")
	}
	return meta{l.Payload.ID, l.Payload.CWD, l.Payload.Timestamp}, nil
}

// ReadHeader reads the session_meta first line.
func (adapter) ReadHeader(r io.Reader) (agents.Header, error) {
	m, err := readMeta(r)
	if err != nil {
		return agents.Header{}, err
	}
	return agents.Header{SessionID: m.ID, CWD: m.CWD, StartedAt: parseTime(m.Timestamp)}, nil
}

type payload struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Role      string          `json:"role"`
	Phase     string          `json:"phase"`
	Name      string          `json:"name"`
	CallID    string          `json:"call_id"`
	TurnID    string          `json:"turn_id"`
	Content   json.RawMessage `json:"content"`
	Summary   json.RawMessage `json:"summary"`
	Arguments json.RawMessage `json:"arguments"`
	Input     json.RawMessage `json:"input"`
	Output    json.RawMessage `json:"output"`
	Item      *item           `json:"item"`
}

type item struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Content json.RawMessage `json:"content"`
}

type textBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ParseLine turns one rollout line into events.
func (adapter) ParseLine(raw []byte, ctx agents.LineContext) ([]agents.Event, error) {
	var l struct {
		Timestamp string          `json:"timestamp"`
		Type      string          `json:"type"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("codex: decode line: %w", err)
	}
	if l.Type != "response_item" && l.Type != "event_msg" {
		return nil, nil
	}
	if len(bytes.TrimSpace(l.Payload)) == 0 {
		return nil, nil
	}
	var p payload
	if err := json.Unmarshal(l.Payload, &p); err != nil {
		return nil, fmt.Errorf("codex: decode payload: %w", err)
	}
	at := parseTime(l.Timestamp)
	fallback := agents.FallbackKey(agents.Codex, ctx.SessionID, ctx.Offset)
	keyFor := func(id string) keyer {
		if id != "" {
			return keyer{base: agents.Key(agents.Codex, ctx.SessionID, id)}
		}
		return keyer{base: fallback}
	}

	if l.Type == "event_msg" {
		switch p.Type {
		case "item_completed":
			if p.Item == nil || p.Item.Type != "UserMessage" {
				return nil, nil // AgentMessage etc. duplicate response_item lines
			}
			text := joinText(p.Item.Content, "\n\n")
			if strings.TrimSpace(text) == "" {
				return nil, nil
			}
			k := keyFor(p.Item.ID)
			return []agents.Event{{Kind: agents.UserPrompt, Key: k.next(), At: at, Text: text}}, nil
		case "task_complete":
			k := keyer{base: fallback}
			if p.TurnID != "" {
				k.base = agents.Key(agents.Codex, ctx.SessionID, "turn-end:"+p.TurnID)
			}
			return []agents.Event{{Kind: agents.TurnEnd, Key: k.next(), At: at}}, nil
		}
		return nil, nil
	}

	k := keyFor(p.ID)
	switch p.Type {
	case "message":
		if p.Role != "assistant" {
			return nil, nil
		}
		text := joinText(p.Content, "\n\n")
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
		phase := agents.Commentary
		if p.Phase == "final_answer" {
			phase = agents.Final
		}
		return []agents.Event{{Kind: agents.AssistantText, Key: k.next(), At: at, Text: text, Phase: phase}}, nil
	case "reasoning":
		text := reasoningText(p)
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
		return []agents.Event{{Kind: agents.Thinking, Key: k.next(), At: at, Text: text}}, nil
	case "function_call", "custom_tool_call":
		args := p.Arguments
		if p.Type == "custom_tool_call" {
			args = p.Input
		}
		kind, lbl := toolLabel(p.Name, rawString(args))
		return []agents.Event{{Kind: agents.ToolCall, Key: k.next(), At: at,
			Tool: &agents.Tool{CallID: p.CallID, Name: p.Name, Kind: kind, Label: lbl}}}, nil
	case "function_call_output", "custom_tool_call_output":
		text, ok, took := parseOutput(p.Output)
		return []agents.Event{{Kind: agents.ToolResult, Key: k.next(), At: at, Tool: &agents.Tool{
			CallID: p.CallID, OK: ok, Took: took, Output: truncateBytes(text, agents.MaxToolOutput),
		}}}, nil
	}
	return nil, nil
}

// joinText joins the text blocks (input_text, output_text, text) of content.
func joinText(raw json.RawMessage, sep string) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	var bs []textBlock
	if json.Unmarshal(raw, &bs) != nil {
		return ""
	}
	var parts []string
	for _, b := range bs {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, sep)
}

func reasoningText(p payload) string {
	if s := joinText(p.Summary, "\n\n"); strings.TrimSpace(s) != "" {
		return s
	}
	return joinText(p.Content, "\n\n")
}

// rawString returns a JSON string's value, or the raw JSON for anything else.
func rawString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	return string(raw)
}

func toolLabel(name, args string) (agents.ToolKind, string) {
	var kind agents.ToolKind
	var l string
	switch name {
	case "exec_command", "shell", "shell_command", "local_shell", "container.exec":
		kind, l = agents.ToolShell, commandText(args)
	case "apply_patch":
		kind, l = agents.ToolEdit, patchFiles(args)
	case "update_plan":
		kind, l = agents.ToolPlan, "update plan"
	case "web_search":
		kind, l = agents.ToolWeb, argString(args, "query")
	default:
		kind = agents.ToolOther
	}
	l = label(l)
	if l == "" {
		l = label(name)
	}
	return kind, l
}

// commandText extracts the command from shell arguments JSON.
func commandText(args string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(args), &m) != nil {
		return ""
	}
	for _, key := range []string{"cmd", "command"} {
		v, ok := m[key]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(v, &s) == nil && s != "" {
			return s
		}
		var arr []string
		if json.Unmarshal(v, &arr) == nil && len(arr) > 0 {
			if len(arr) >= 3 && (arr[1] == "-lc" || arr[1] == "-c") && strings.HasSuffix(arr[0], "sh") {
				return arr[2]
			}
			return strings.Join(arr, " ")
		}
	}
	return ""
}

var patchLineRe = regexp.MustCompile(`(?m)^\*\*\* (?:Update|Add|Delete) File: (.+?)\s*$`)

// patchFiles lists the files an apply_patch call touches.
func patchFiles(args string) string {
	patch := args
	if !strings.Contains(patch, "*** ") {
		return ""
	}
	if strings.HasPrefix(strings.TrimSpace(args), "{") {
		patch = argString(args, "input", "patch", "cmd")
	}
	var files []string
	for _, m := range patchLineRe.FindAllStringSubmatch(patch, -1) {
		files = append(files, m[1])
	}
	return strings.Join(files, ", ")
}

func argString(args string, keys ...string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(args), &m) != nil {
		return ""
	}
	for _, key := range keys {
		var s string
		if v, ok := m[key]; ok && json.Unmarshal(v, &s) == nil && s != "" {
			return s
		}
	}
	return ""
}

var exitCodeRes = []*regexp.Regexp{
	regexp.MustCompile(`"exit_code"\s*:\s*(-?\d+)`),
	regexp.MustCompile(`(?i)exit code:\s*(-?\d+)`),
}

// parseOutput returns the readable text of a tool output, whether it looks
// successful, and the run time when the output reports one.
func parseOutput(raw json.RawMessage) (text string, ok bool, took time.Duration) {
	ok = true
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", true, 0
	}
	if raw[0] == '{' {
		var o struct {
			Content *string `json:"content"`
			Success *bool   `json:"success"`
		}
		if json.Unmarshal(raw, &o) == nil && (o.Content != nil || o.Success != nil) {
			if o.Success != nil && !*o.Success {
				ok = false
			}
			if o.Content != nil {
				text = *o.Content
			}
		} else {
			text = string(raw)
		}
	} else {
		text = rawString(raw)
	}
	for _, re := range exitCodeRes {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			if n, err := strconv.Atoi(m[1]); err == nil && n != 0 {
				ok = false
			}
		}
	}
	// exec output is often a JSON document with output and metadata.
	if strings.HasPrefix(text, "{") {
		var o struct {
			Output   *string `json:"output"`
			Metadata struct {
				Duration float64 `json:"duration_seconds"`
			} `json:"metadata"`
		}
		if json.Unmarshal([]byte(text), &o) == nil && o.Output != nil {
			text = *o.Output
			took = time.Duration(o.Metadata.Duration * float64(time.Second))
		}
	}
	return text, ok, took
}

type keyer struct {
	base string
	n    int
}

func (k *keyer) next() string {
	s := k.base
	if k.n > 0 {
		s = fmt.Sprintf("%s#%d", k.base, k.n)
	}
	k.n++
	return s
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func label(s string) string {
	if len(s) > 4*agents.MaxLabel {
		s = truncateBytes(s, 4*agents.MaxLabel)
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > agents.MaxLabel {
		s = string(r[:agents.MaxLabel-1]) + "…"
	}
	return s
}

func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// readLine reads one line of at most max bytes. A longer line is consumed
// and reported with over set.
func readLine(br *bufio.Reader, max int) (out []byte, over bool, err error) {
	var buf []byte
	got := false
	for {
		frag, e := br.ReadSlice('\n')
		if len(frag) > 0 {
			got = true
			if !over {
				if len(buf)+len(frag) > max {
					over, buf = true, nil
				} else {
					buf = append(buf, frag...)
				}
			}
		}
		if e == bufio.ErrBufferFull {
			continue
		}
		if e != nil && !got {
			return nil, false, e
		}
		return bytes.TrimRight(buf, "\r\n"), over, nil
	}
}

// IsSubagentTranscript is always false: Codex has no separate subagent files.
func (adapter) IsSubagentTranscript(string) bool { return false }

// TranscriptCandidates scans the day folders from one day before since up to
// tomorrow and keeps rollouts whose session_meta cwd equals cwd.
func (adapter) TranscriptCandidates(home, cwd string, since time.Time) ([]string, error) {
	root := filepath.Join(home, ".codex", "sessions")
	now := time.Now()
	first := since
	if first.IsZero() || first.Before(now.AddDate(-1, 0, 0)) {
		first = now.AddDate(-1, 0, 0) // bound the scan for a zero or very old since
	}
	day := dayStart(first.AddDate(0, 0, -1))
	last := dayStart(now.AddDate(0, 0, 1))

	type cand struct {
		path string
		mod  time.Time
	}
	var cs []cand
	for ; !day.After(last); day = day.AddDate(0, 0, 1) {
		dir := filepath.Join(root, day.Format("2006"), day.Format("01"), day.Format("02"))
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("codex: list %s: %w", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			fi, err := e.Info()
			if err != nil || fi.ModTime().Before(since) {
				continue
			}
			p := filepath.Join(dir, name)
			if !rolloutHasCWD(p, cwd) {
				continue
			}
			cs = append(cs, cand{p, fi.ModTime()})
		}
	}
	sort.Slice(cs, func(i, j int) bool {
		if !cs[i].mod.Equal(cs[j].mod) {
			return cs[i].mod.After(cs[j].mod)
		}
		return cs[i].path > cs[j].path
	})
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.path
	}
	return out, nil
}

func dayStart(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func rolloutHasCWD(path, cwd string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	m, err := readMeta(f)
	return err == nil && m.CWD == cwd
}
