// Package pi is the transcript adapter for the pi coding agent
// (@earendil-works/pi-coding-agent). It turns pi session JSONL lines into
// agents.Event values. See docs/agent-transcripts.md.
package pi

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
	"strings"
	"time"
	"unicode/utf8"

	"rex-tg/internal/agents"
)

const (
	maxHeaderLine = 8 << 20 // longest line ReadHeader will look at
	maxHeaderScan = 200     // lines after the header that may hold session_info
)

type adapter struct{}

// New returns the pi adapter.
func New() agents.Adapter { return adapter{} }

func (adapter) Runtime() agents.Runtime { return agents.Pi }

func (adapter) Input() agents.InputProfile {
	return agents.InputProfile{
		PasteEnterDelay: 300 * time.Millisecond,
		SubmitKey:       "enter",
		InterruptKeys:   []string{"escape"},
		WhileWorking:    "steer",
	}
}

// ReadHeader reads the session header line and, when present in the next
// lines, the session title.
func (adapter) ReadHeader(r io.Reader) (agents.Header, error) {
	br := bufio.NewReader(r)
	line, over, err := readLine(br, maxHeaderLine)
	if err != nil {
		return agents.Header{}, fmt.Errorf("pi: read header line: %w", err)
	}
	if over {
		return agents.Header{}, errors.New("pi: header line too long")
	}
	var h struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		Timestamp string `json:"timestamp"`
		CWD       string `json:"cwd"`
	}
	if err := json.Unmarshal(line, &h); err != nil {
		return agents.Header{}, fmt.Errorf("pi: decode header: %w", err)
	}
	if h.Type != "session" || h.ID == "" {
		return agents.Header{}, errors.New("pi: first line is not a session header")
	}
	hdr := agents.Header{SessionID: h.ID, CWD: h.CWD, StartedAt: parseTime(h.Timestamp)}
	for i := 0; i < maxHeaderScan; i++ {
		line, over, err := readLine(br, maxHeaderLine)
		if err != nil {
			break
		}
		if over || !bytes.Contains(line, []byte(`"session_info"`)) {
			continue
		}
		var e struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if json.Unmarshal(line, &e) == nil && e.Type == "session_info" && e.Name != "" {
			hdr.Title = e.Name
		}
	}
	return hdr, nil
}

type message struct {
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content"`
	StopReason   string          `json:"stopReason"`
	ErrorMessage string          `json:"errorMessage"`
	ToolCallID   string          `json:"toolCallId"`
	IsError      bool            `json:"isError"`
	DurationMs   float64         `json:"durationMs"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ParseLine turns one transcript line into events.
func (adapter) ParseLine(line []byte, ctx agents.LineContext) ([]agents.Event, error) {
	var head struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return nil, fmt.Errorf("pi: decode line: %w", err)
	}
	switch head.Type {
	case "message", "session_info", "compaction":
	case "", "session", "custom", "custom_message", "thinking_level_change", "model_change", "context_edit", "label", "branch_summary":
		return nil, nil
	default:
		return unknown(head.Type), nil
	}
	base := agents.FallbackKey(agents.Pi, ctx.SessionID, ctx.Offset)
	if head.ID != "" {
		base = agents.Key(agents.Pi, ctx.SessionID, head.ID)
	}
	k := keyer{base: base}
	at := parseTime(head.Timestamp)

	switch head.Type {
	case "session_info":
		var e struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("pi: decode session_info: %w", err)
		}
		if e.Name == "" {
			return nil, nil
		}
		return []agents.Event{{Kind: agents.Title, Key: k.next(), At: at, Text: e.Name}}, nil
	case "compaction":
		return []agents.Event{{Kind: agents.Boundary, Key: k.next(), At: at, Text: "compaction"}}, nil
	}

	var m struct {
		Message *message `json:"message"`
	}
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, fmt.Errorf("pi: decode message: %w", err)
	}
	if m.Message == nil {
		return nil, nil
	}
	msg := m.Message
	blocks, err := parseBlocks(msg.Content)
	if err != nil {
		return nil, fmt.Errorf("pi: decode content: %w", err)
	}
	var evs []agents.Event
	switch msg.Role {
	case "user":
		if text := joinText(blocks, "\n\n"); strings.TrimSpace(text) != "" {
			evs = append(evs, agents.Event{Kind: agents.UserPrompt, Key: k.next(), At: at, Text: text})
		}
	case "toolResult":
		if msg.ToolCallID == "" {
			return nil, nil
		}
		tool := &agents.Tool{
			CallID: msg.ToolCallID,
			OK:     !msg.IsError,
			Took:   time.Duration(msg.DurationMs * float64(time.Millisecond)),
			Output: truncateBytes(joinText(blocks, "\n"), agents.MaxToolOutput),
		}
		evs = append(evs, agents.Event{Kind: agents.ToolResult, Key: k.next(), At: at, Tool: tool})
	case "assistant":
		phase := agents.Commentary
		switch msg.StopReason {
		case "stop":
			phase = agents.Final
		case "error":
			phase = agents.Error
		}
		text := joinText(blocks, "\n\n")
		textDone := false
		for _, b := range blocks {
			switch b.Type {
			case "thinking":
				if strings.TrimSpace(b.Thinking) != "" {
					evs = append(evs, agents.Event{Kind: agents.Thinking, Key: k.next(), At: at, Text: b.Thinking})
				}
			case "text":
				if !textDone && strings.TrimSpace(text) != "" {
					evs = append(evs, agents.Event{Kind: agents.AssistantText, Key: k.next(), At: at, Text: text, Phase: phase})
				}
				textDone = true
			case "toolCall":
				kind, lbl := toolLabel(b.Name, b.Arguments, ctx.CWD)
				evs = append(evs, agents.Event{Kind: agents.ToolCall, Key: k.next(), At: at,
					Tool: &agents.Tool{CallID: b.ID, Name: b.Name, Kind: kind, Label: lbl}})
			}
		}
		if phase == agents.Error && strings.TrimSpace(text) == "" && msg.ErrorMessage != "" {
			evs = append(evs, agents.Event{Kind: agents.AssistantText, Key: k.next(), At: at, Text: msg.ErrorMessage, Phase: agents.Error})
		}
	}
	return evs, nil
}

func toolLabel(name string, args json.RawMessage, cwd string) (agents.ToolKind, string) {
	var kind agents.ToolKind
	var l string
	switch name {
	case "bash":
		kind, l = agents.ToolShell, argString(args, "command")
	case "read", "ls":
		kind, l = agents.ToolRead, argString(args, "path", "file_path", "pattern")
	case "edit", "write":
		kind, l = agents.ToolEdit, argString(args, "path", "file_path")
	case "grep", "find":
		kind, l = agents.ToolSearch, argString(args, "pattern", "path")
	default:
		kind = agents.ToolOther
	}
	if kind == agents.ToolRead || kind == agents.ToolEdit {
		l = relPath(l, cwd)
	}
	l = label(l)
	if l == "" {
		l = label(name)
	}
	return kind, l
}

func parseBlocks(raw json.RawMessage) ([]block, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		return []block{{Type: "text", Text: s}}, nil
	}
	var bs []block
	if err := json.Unmarshal(raw, &bs); err != nil {
		return nil, err
	}
	return bs, nil
}

func joinText(bs []block, sep string) string {
	var parts []string
	for _, b := range bs {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, sep)
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

func argString(raw json.RawMessage, keys ...string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
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
func readLine(br *bufio.Reader, max int) (line []byte, over bool, err error) {
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

var runDirRe = regexp.MustCompile(`/run-\d+/session\.jsonl$`)

// IsSubagentTranscript reports nested-run and subagent-artifact transcripts.
func (adapter) IsSubagentTranscript(path string) bool {
	p := "/" + filepath.ToSlash(path)
	return runDirRe.MatchString(p) || strings.Contains(p, "/subagent-artifacts/")
}

// TranscriptCandidates lists session files of cwd changed at or after since,
// newest first.
func (a adapter) TranscriptCandidates(home, cwd string, since time.Time) ([]string, error) {
	name := "--" + strings.ReplaceAll(strings.TrimPrefix(cwd, "/"), "/", "-") + "--"
	dir := filepath.Join(home, ".pi", "agent", "sessions", name)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pi: list %s: %w", dir, err)
	}
	type cand struct {
		path string
		mod  time.Time
	}
	var cs []cand
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		fi, err := e.Info()
		if err != nil || fi.ModTime().Before(since) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if a.IsSubagentTranscript(p) {
			continue
		}
		cs = append(cs, cand{p, fi.ModTime()})
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

func relPath(p, cwd string) string {
	base := strings.TrimRight(cwd, "/")
	if base != "" && strings.HasPrefix(p, base+"/") {
		return p[len(base)+1:]
	}
	return p
}

func unknown(name string) []agents.Event {
	return []agents.Event{{Kind: agents.KindUnknown, Text: name}}
}
