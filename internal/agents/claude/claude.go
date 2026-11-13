// Package claude is the transcript adapter for Claude Code. It turns session
// JSONL lines into agents.Event values. See docs/agent-transcripts.md.
package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"rex-tg/internal/agents"
)

const (
	maxHeaderLine = 8 << 20 // longest line ReadHeader will look at
	maxHeaderScan = 200     // lines ReadHeader scans for the identity
)

type adapter struct{}

// New returns the Claude Code adapter.
func New() agents.Adapter { return adapter{} }

func (adapter) Runtime() agents.Runtime { return agents.Claude }

func (adapter) Input() agents.InputProfile {
	return agents.InputProfile{
		PasteEnterDelay: 300 * time.Millisecond,
		SubmitKey:       "enter",
		InterruptKeys:   []string{"escape"},
		WhileWorking:    "steer",
	}
}

// ReadHeader scans the first lines for the session ID, the working folder,
// and (optionally) the title. Claude has no header line.
func (adapter) ReadHeader(r io.Reader) (agents.Header, error) {
	br := bufio.NewReader(r)
	var h agents.Header
	for i := 0; i < maxHeaderScan; i++ {
		line, over, err := readLine(br, maxHeaderLine)
		if err != nil {
			break
		}
		if over {
			continue
		}
		var l struct {
			Type        string `json:"type"`
			SessionID   string `json:"sessionId"`
			CWD         string `json:"cwd"`
			Timestamp   string `json:"timestamp"`
			CustomTitle string `json:"customTitle"`
		}
		if json.Unmarshal(line, &l) != nil {
			continue
		}
		if h.SessionID == "" && l.SessionID != "" {
			h.SessionID = l.SessionID
		}
		if h.CWD == "" && l.CWD != "" {
			h.CWD = l.CWD
		}
		if h.StartedAt.IsZero() {
			h.StartedAt = parseTime(l.Timestamp)
		}
		if l.Type == "custom-title" && l.CustomTitle != "" {
			h.Title = l.CustomTitle
		}
		if h.SessionID != "" && h.CWD != "" && h.Title != "" {
			break
		}
	}
	if h.SessionID == "" || h.CWD == "" {
		return agents.Header{}, errors.New("claude: session id or cwd not found in transcript start")
	}
	return h, nil
}

type line struct {
	Type             string          `json:"type"`
	UUID             string          `json:"uuid"`
	Timestamp        string          `json:"timestamp"`
	CWD              string          `json:"cwd"`
	Subtype          string          `json:"subtype"`
	CustomTitle      string          `json:"customTitle"`
	IsSidechain      bool            `json:"isSidechain"`
	IsMeta           bool            `json:"isMeta"`
	IsCompactSummary bool            `json:"isCompactSummary"`
	IsAPIError       bool            `json:"isApiErrorMessage"`
	Origin           *origin         `json:"origin"`
	Message          json.RawMessage `json:"message"`
	Attachment       json.RawMessage `json:"attachment"`
}

// origin says who wrote a user line: "human" for a person, or, for example,
// "task-notification" or "peer" (seen live). Older lines have none.
type origin struct {
	Kind string `json:"kind"`
}

// automatic reports whether a prompt line was written by Claude Code itself
// or another session, not typed by a person. commandMode is the queued
// line's mode ("prompt" when typed).
func automatic(o *origin, commandMode, text string) bool {
	if o != nil && o.Kind != "" && o.Kind != "human" {
		return true
	}
	if commandMode != "" && commandMode != "prompt" {
		return true
	}
	return strings.HasPrefix(text, "<task-notification>")
}

type message struct {
	Content    json.RawMessage `json:"content"`
	StopReason string          `json:"stop_reason"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

// ParseLine turns one transcript line into events.
func (adapter) ParseLine(raw []byte, ctx agents.LineContext) ([]agents.Event, error) {
	var l line
	// Decode only the fields we need; the rest (images, usage) is skipped.
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("claude: decode line: %w", err)
	}
	switch l.Type {
	case "assistant", "user", "custom-title", "system", "attachment":
	case "", "api-request", "api-request-blob", "api-request-shape", "session-owner", "atis-latch",
		"last-prompt", "tools-used", "queue-operation", "mode", "permission-mode",
		"file-history-snapshot", "file-history-delta", "ai-title", "cost-state", "progress", "summary":
		return nil, nil
	default:
		return unknown(l.Type), nil
	}
	if l.IsSidechain {
		return nil, nil
	}
	base := agents.FallbackKey(agents.Claude, ctx.SessionID, ctx.Offset)
	if l.UUID != "" {
		base = agents.Key(agents.Claude, ctx.SessionID, l.UUID)
	}
	k := keyer{base: base}
	at := parseTime(l.Timestamp)

	cwd := l.CWD
	if cwd == "" {
		cwd = ctx.CWD
	}
	switch l.Type {
	case "attachment":
		var a struct {
			Type        string  `json:"type"`
			Prompt      string  `json:"prompt"`
			CommandMode string  `json:"commandMode"`
			Origin      *origin `json:"origin"`
		}
		if len(l.Attachment) == 0 || json.Unmarshal(l.Attachment, &a) != nil || a.Type != "queued_command" {
			return nil, nil
		}
		if text := strings.TrimSpace(unwrapPastes(a.Prompt)); isPrompt(text) {
			return []agents.Event{{Kind: agents.UserPrompt, Key: k.next(), At: at, Text: text,
				Auto: automatic(a.Origin, a.CommandMode, text)}}, nil
		}
		return nil, nil
	case "custom-title":
		if l.CustomTitle == "" {
			return nil, nil
		}
		return []agents.Event{{Kind: agents.Title, Key: k.next(), At: at, Text: l.CustomTitle}}, nil
	case "system":
		if l.Subtype != "compact_boundary" {
			return nil, nil
		}
		return []agents.Event{{Kind: agents.Boundary, Key: k.next(), At: at, Text: "compaction"}}, nil
	}

	msgRaw := bytes.TrimSpace(l.Message)
	if len(msgRaw) == 0 || msgRaw[0] != '{' {
		return nil, nil // not a conversation message
	}
	var m message
	if err := json.Unmarshal(msgRaw, &m); err != nil {
		return nil, fmt.Errorf("claude: decode message: %w", err)
	}
	blocks, err := parseBlocks(m.Content)
	if err != nil {
		return nil, fmt.Errorf("claude: decode content: %w", err)
	}

	var evs []agents.Event
	if l.Type == "assistant" {
		phase := agents.Commentary
		if m.StopReason == "end_turn" {
			phase = agents.Final
		}
		if l.IsAPIError {
			phase = agents.Error
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if strings.TrimSpace(b.Text) != "" {
					evs = append(evs, agents.Event{Kind: agents.AssistantText, Key: k.next(), At: at, Text: b.Text, Phase: phase})
				}
			case "thinking":
				if strings.TrimSpace(b.Thinking) != "" {
					evs = append(evs, agents.Event{Kind: agents.Thinking, Key: k.next(), At: at, Text: b.Thinking})
				}
			case "tool_use":
				kind, lbl := toolLabel(b.Name, b.Input, cwd)
				evs = append(evs, agents.Event{Kind: agents.ToolCall, Key: k.next(), At: at,
					Tool: &agents.Tool{CallID: b.ID, Name: b.Name, Kind: kind, Label: lbl}})
			}
		}
		return evs, nil
	}

	// type=user
	var texts []string
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			if b.ToolUseID == "" {
				continue
			}
			evs = append(evs, agents.Event{Kind: agents.ToolResult, Key: k.next(), At: at, Tool: &agents.Tool{
				CallID: b.ToolUseID,
				OK:     !b.IsError,
				Output: truncateBytes(resultText(b.Content), agents.MaxToolOutput),
			}})
		case "text":
			if strings.HasPrefix(strings.TrimSpace(b.Text), "[Request interrupted") {
				evs = append(evs, agents.Event{Kind: agents.TurnEnd, Key: k.next(), At: at})
				continue
			}
			// Claude Code adds <system-reminder> blocks to user lines; they
			// are context for the model, not what the user typed. It wraps
			// a long paste in tags, which the user did not type either.
			if t := strings.TrimSpace(unwrapPastes(stripReminders(b.Text))); t != "" {
				texts = append(texts, t)
			}
		}
	}
	if !l.IsMeta && !l.IsCompactSummary {
		if text := strings.Join(texts, "\n\n"); isPrompt(text) {
			evs = append(evs, agents.Event{Kind: agents.UserPrompt, Key: k.next(), At: at, Text: text,
				Auto: automatic(l.Origin, "", text)})
		}
	}
	return evs, nil
}

// stripReminders removes every complete <system-reminder>…</system-reminder>
// span from s and trims it. An unclosed span is kept as it is.
func stripReminders(s string) string {
	const open, closing = "<system-reminder>", "</system-reminder>"
	for {
		i := strings.Index(s, open)
		if i < 0 {
			break
		}
		j := strings.Index(s[i:], closing)
		if j < 0 {
			break
		}
		s = s[:i] + s[i+j+len(closing):]
	}
	return strings.TrimSpace(s)
}

// unwrapPastes replaces each complete span
// <pasted_content id="X">…</pasted_content id="X"> in s with the text inside
// it, without the newline after the opening tag and before the closing one.
// Claude Code saves a long paste that way (seen live). The closing tag must
// have the same id, so a tag inside the pasted text stays as it is, and so
// does a tag without its closing tag.
func unwrapPastes(s string) string {
	const openTag, closeTag = `<pasted_content id="`, `</pasted_content id="`
	var b strings.Builder
	for {
		i := strings.Index(s, openTag)
		if i < 0 {
			break
		}
		rest := s[i+len(openTag):]
		q := strings.Index(rest, `">`)
		id := ""
		if q >= 0 {
			id = rest[:q]
		}
		end := closeTag + id + `">`
		j := -1
		if q > 0 && !strings.ContainsAny(id, "\"<>\n") {
			j = strings.Index(rest[q+2:], end)
		}
		if j < 0 { // not a complete span: keep the text and look further on
			b.WriteString(s[:i+len(openTag)])
			s = rest
			continue
		}
		body := rest[q+2:]
		inner := strings.TrimSuffix(strings.TrimPrefix(body[:j], "\n"), "\n")
		b.WriteString(s[:i])
		b.WriteString(inner)
		s = body[j+len(end):]
	}
	b.WriteString(s)
	return b.String()
}

var wrapperPrefixes = []string{
	"<command-name>", "<command-message>", "<command-args>",
	"<local-command-stdout>", "<local-command-stderr>", "<local-command-caveat>",
}

// isPrompt reports whether text is something the user typed, not a command
// wrapper or a system reminder.
func isPrompt(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	for _, p := range wrapperPrefixes {
		if strings.HasPrefix(t, p) {
			return false
		}
	}
	// Content made only of <system-reminder> blocks.
	for strings.HasPrefix(t, "<system-reminder>") {
		end := strings.Index(t, "</system-reminder>")
		if end < 0 {
			return false
		}
		t = strings.TrimSpace(t[end+len("</system-reminder>"):])
	}
	return t != ""
}

func resultText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return ""
		}
		return s
	}
	var bs []block
	if json.Unmarshal(raw, &bs) != nil {
		return ""
	}
	var parts []string
	for _, b := range bs {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func toolLabel(name string, input json.RawMessage, cwd string) (agents.ToolKind, string) {
	var kind agents.ToolKind
	var l string
	switch name {
	case "Bash":
		kind, l = agents.ToolShell, argString(input, "description", "command")
	case "Read":
		kind, l = agents.ToolRead, relPath(argString(input, "file_path"), cwd)
	case "Edit", "Write", "MultiEdit":
		kind, l = agents.ToolEdit, relPath(argString(input, "file_path"), cwd)
	case "NotebookEdit":
		kind, l = agents.ToolEdit, relPath(argString(input, "file_path", "notebook_path"), cwd)
	case "Grep", "Glob":
		kind, l = agents.ToolSearch, argString(input, "pattern")
	case "WebFetch":
		kind, l = agents.ToolWeb, argString(input, "url")
	case "WebSearch":
		kind, l = agents.ToolWeb, argString(input, "query")
	case "Task", "Agent":
		kind, l = agents.ToolSubagent, argString(input, "description")
	case "TodoWrite":
		kind, l = agents.ToolPlan, "update plan"
	default:
		kind = agents.ToolOther
	}
	l = label(l)
	if l == "" {
		l = label(name)
	}
	return kind, l
}

func relPath(p, cwd string) string {
	if cwd != "" && strings.HasPrefix(p, strings.TrimRight(cwd, "/")+"/") {
		return p[len(strings.TrimRight(cwd, "/"))+1:]
	}
	return p
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

// IsSubagentTranscript reports transcripts under a session's subagents folder.
func (adapter) IsSubagentTranscript(path string) bool {
	return strings.Contains("/"+filepath.ToSlash(path), "/subagents/")
}

// projectDirName encodes cwd the way Claude Code names project folders: every
// UTF-16 unit that is not [A-Za-z0-9] becomes "-".
func projectDirName(cwd string) string {
	var b strings.Builder
	for _, u := range utf16.Encode([]rune(cwd)) {
		if u < 128 && (u >= 'a' && u <= 'z' || u >= 'A' && u <= 'Z' || u >= '0' && u <= '9') {
			b.WriteByte(byte(u))
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// TranscriptCandidates lists session files of cwd changed at or after since,
// newest first.
func (a adapter) TranscriptCandidates(home, cwd string, since time.Time) ([]string, error) {
	dir := filepath.Join(home, ".claude", "projects", projectDirName(cwd))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claude: list %s: %w", dir, err)
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

func unknown(name string) []agents.Event {
	return []agents.Event{{Kind: agents.KindUnknown, Text: name}}
}
