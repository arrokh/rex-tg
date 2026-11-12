// Command dummyagent is a stand-in for the pi coding agent, used by the
// end-to-end test. Built under the file name "pi", it runs in a Rex pane,
// reports its state with OSC 7501, writes a spool hook report at start, and
// answers every submitted prompt with "ECHO: <prompt>" in a pi-format
// transcript.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

type agent struct {
	home, spool, logPath string
	think                time.Duration
	seed                 string
	format               string
	echoDelay            time.Duration
	exitFile             string
	fixedID              string
	ask                  string

	pending atomic.Int32 // prompts submitted and not yet answered
	waiting atomic.Bool  // a permission question is open: a digit answers it
	keys    chan string  // the digits that answer the questions

	logMu sync.Mutex

	sessionFile string
	sessionID   string
	lastEntry   string
	cwd         string
}

func main() {
	var a agent
	flag.StringVar(&a.home, "home", "", "fake HOME that holds .pi/agent/sessions")
	flag.StringVar(&a.spool, "spool", "", "hook spool folder")
	flag.StringVar(&a.logPath, "log", "", "JSON lines log of received prompts")
	flag.DurationVar(&a.think, "think", 2*time.Second, "simulated work time per prompt")
	flag.StringVar(&a.seed, "seed", "", "prompt of a turn that is already in the transcript at start")
	flag.DurationVar(&a.echoDelay, "echo-delay", 0, "time between a submit and the user line in the transcript")
	flag.StringVar(&a.exitFile, "exit-file", "", "exit at once when this file exists")
	flag.StringVar(&a.fixedID, "session-id", "", "session id; when its transcript exists, the agent resumes it")
	flag.StringVar(&a.format, "format", "pi", "transcript and hook format: pi, claude, or codex")
	flag.StringVar(&a.ask, "ask", "", "a question to ask at start, before the session starts, like a trust question")
	flag.Parse()
	switch a.format {
	case "pi", "claude", "codex":
	default:
		fmt.Fprintln(os.Stderr, "dummyagent: -format must be pi, claude, or codex")
		os.Exit(2)
	}
	if a.home == "" || a.spool == "" || a.logPath == "" {
		fmt.Fprintln(os.Stderr, "dummyagent: -home, -spool, and -log are required")
		os.Exit(2)
	}
	if err := a.run(); err != nil {
		fmt.Fprintln(os.Stderr, "dummyagent:", err)
		os.Exit(1)
	}
}

func (a *agent) run() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	a.cwd = cwd
	a.sessionID = uuid()
	if a.fixedID != "" {
		a.sessionID = a.fixedID
	}
	now := time.Now().UTC()
	a.sessionFile = a.transcriptPath(now)
	resumed := false
	if a.fixedID != "" {
		if old := a.findTranscript(); old != "" {
			a.sessionFile, resumed = old, true
			a.lastEntry = "resumed" // the header is already in the file
		}
	}
	if a.exitFile != "" {
		go func() {
			for {
				if _, err := os.Stat(a.exitFile); err == nil {
					os.Exit(0) // no clean-up, like a crash of the agent
				}
				time.Sleep(100 * time.Millisecond)
			}
		}()
	}

	// Raw input without echo, like a full-screen terminal program.
	stty := exec.Command("stty", "raw", "-echo")
	stty.Stdin = os.Stdin
	if err := stty.Run(); err != nil {
		return fmt.Errorf("stty raw: %w", err)
	}
	defer func() {
		restore := exec.Command("stty", "sane")
		restore.Stdin = os.Stdin
		_ = restore.Run()
	}()

	if a.ask != "" && !a.askFirst() {
		return nil // "No": exit before the session starts
	}
	fmt.Print("\x1b[?2004h") // bracketed paste on
	a.status("idle", "")
	fmt.Print("dummy pi ready\r\n")
	if a.seed != "" && !resumed {
		// A real pi creates its file at the first prompt, and rex-tg cannot
		// resolve the session before that. Start with one finished turn.
		if err := a.append("user", a.seed, ""); err != nil {
			return err
		}
		if err := a.append("assistant", "ECHO: "+a.seed, "stop"); err != nil {
			return err
		}
	}
	if err := a.writeHook(); err != nil {
		return err
	}

	prompts := make(chan string, 16)
	a.keys = make(chan string, 4)
	go a.readInput(prompts)
	for p := range prompts {
		if err := a.answer(p); err != nil {
			return err
		}
	}
	return nil
}

// askFirst asks a.ask with a list of answers and a cursor, as the trust
// questions of Claude Code and Codex do: the cursor starts on "No, exit",
// Up and Down move it, and Enter picks. It reports whether the answer is
// Yes.
func (a *agent) askFirst() bool {
	answers := []string{"No, exit", "Yes, continue"}
	cur := 0
	draw := func() {
		fmt.Print("\x1b[2J\x1b[H " + a.ask + "\r\n\r\n")
		for i, s := range answers {
			mark := "  "
			if i == cur {
				mark = "❯ "
			}
			fmt.Print(" " + mark + s + "\r\n")
		}
		fmt.Print("\r\n Enter to confirm\r\n")
	}
	draw()
	buf := make([]byte, 64)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return false
		}
		for in := string(buf[:n]); in != ""; {
			switch {
			case strings.HasPrefix(in, "\x1b[A") || strings.HasPrefix(in, "\x1bOA"):
				cur, in = max(cur-1, 0), in[3:]
			case strings.HasPrefix(in, "\x1b[B") || strings.HasPrefix(in, "\x1bOB"):
				cur, in = min(cur+1, len(answers)-1), in[3:]
			case in[0] == '\r' || in[0] == '\n':
				fmt.Print("\x1b[2J\x1b[H")
				return cur == 1
			default:
				in = in[1:]
			}
		}
		draw()
	}
}

// findTranscript returns the existing transcript of the fixed session id.
func (a *agent) findTranscript() string {
	found := ""
	_ = filepath.WalkDir(a.home, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") && strings.Contains(filepath.Base(p), a.sessionID) {
			found = p
		}
		return nil
	})
	return found
}

// status reports the program state with OSC 7501, as pi's reporter does.
func (a *agent) status(state, msg string) {
	s := "state=" + state + ":app=pi"
	if msg != "" {
		s += ":msg=" + base64.StdEncoding.EncodeToString([]byte(msg))
	}
	fmt.Printf("\x1b]7501;%s\x1b\\", s)
}

// readInput parses raw input: bracketed pastes and typed characters. Enter
// (CR) submits the line.
func (a *agent) readInput(out chan<- string) {
	var pending []byte
	var line []byte
	inPaste := false
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
		}
		for len(pending) > 0 {
			if inPaste {
				i := bytes.Index(pending, []byte(pasteEnd))
				if i < 0 {
					// Keep a possible partial end marker.
					keep := len(pasteEnd) - 1
					if len(pending) <= keep {
						break
					}
					line = append(line, pending[:len(pending)-keep]...)
					pending = pending[len(pending)-keep:]
					break
				}
				line = append(line, pending[:i]...)
				pending = pending[i+len(pasteEnd):]
				inPaste = false
				continue
			}
			if bytes.HasPrefix(pending, []byte(pasteStart)) {
				pending = pending[len(pasteStart):]
				inPaste = true
				continue
			}
			if pending[0] == 0x1b {
				if len(pending) < len(pasteStart) && bytes.HasPrefix([]byte(pasteStart), pending) {
					break // possible partial marker
				}
				pending = pending[1:] // drop a lone escape
				continue
			}
			c := pending[0]
			pending = pending[1:]
			if a.waiting.Load() && c >= '0' && c <= '9' {
				a.record(map[string]any{"ev": "key", "text": string(c)})
				a.keys <- string(c)
				continue
			}
			if c == '\r' || c == '\n' {
				text := strings.ReplaceAll(string(line), "\r", "\n")
				line = line[:0]
				if strings.TrimSpace(text) == "" {
					continue
				}
				busy := a.pending.Add(1) > 1
				a.record(map[string]any{"ev": "prompt", "busy": busy, "text": text})
				out <- text
				continue
			}
			line = append(line, c)
		}
		if err != nil {
			close(out)
			return
		}
	}
}

// answer simulates a run: working, then the transcript lines, then done. A
// prompt "ask: <question>" first asks for permission, as Claude Code does
// before a tool runs, and waits for a digit; the answer then names it.
func (a *agent) answer(prompt string) error {
	a.status("working", "e2e")
	a.hook("UserPromptSubmit")
	time.Sleep(a.echoDelay)
	if err := a.append("user", prompt, ""); err != nil {
		return err
	}
	reply := "ECHO: " + prompt
	if q, ok := strings.CutPrefix(prompt, "ask: "); ok && a.format != "pi" {
		fmt.Print("\r\n " + q + "\r\n ❯ 1. Yes\r\n   2. No\r\n")
		a.waiting.Store(true)
		a.hook("PermissionRequest")
		k := <-a.keys
		a.waiting.Store(false)
		a.status("working", "e2e")
		reply += " (answer " + k + ")"
	}
	time.Sleep(a.think)
	if err := a.append("assistant", reply, "stop"); err != nil {
		return err
	}
	a.record(map[string]any{"ev": "done", "text": prompt})
	a.pending.Add(-1)
	a.hook("Stop")
	a.status("done", "e2e")
	return nil
}

func (a *agent) record(v map[string]any) {
	v["at"] = time.Now().UTC().Format(time.RFC3339Nano)
	b, _ := json.Marshal(v)
	a.logMu.Lock()
	defer a.logMu.Unlock()
	f, err := os.OpenFile(a.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// transcriptPath is where this runtime keeps its session file.
func (a *agent) transcriptPath(now time.Time) string {
	stamp := fmt.Sprintf("%s-%03dZ", now.Format("2006-01-02T15-04-05"), now.Nanosecond()/1e6)
	switch a.format {
	case "claude":
		dir := strings.NewReplacer("/", "-", ".", "-").Replace(a.cwd)
		return filepath.Join(a.home, ".claude", "projects", dir, a.sessionID+".jsonl")
	case "codex":
		return filepath.Join(a.home, ".codex", "sessions", now.Format("2006"), now.Format("01"), now.Format("02"),
			fmt.Sprintf("rollout-%s-%s.jsonl", now.Format("2006-01-02T15-04-05"), a.sessionID))
	}
	dir := "--" + strings.ReplaceAll(strings.TrimPrefix(a.cwd, "/"), "/", "-") + "--"
	return filepath.Join(a.home, ".pi", "agent", "sessions", dir, stamp+"_"+a.sessionID+".jsonl")
}

// append writes the transcript lines of one message in the runtime's format.
// The first call writes the header first.
func (a *agent) append(role, text, stop string) error {
	if err := os.MkdirAll(filepath.Dir(a.sessionFile), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(a.sessionFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	id := shortID()
	var parent any
	if a.lastEntry != "" {
		parent = a.lastEntry
	}
	first := a.lastEntry == ""
	a.lastEntry = id
	var lines []any
	switch a.format {
	case "claude":
		line := map[string]any{"type": role, "uuid": id, "parentUuid": parent, "sessionId": a.sessionID,
			"isSidechain": false, "cwd": a.cwd, "timestamp": ts}
		if role == "user" {
			line["message"] = map[string]any{"role": "user", "content": text}
		} else {
			line["message"] = map[string]any{"id": "msg_" + id, "role": "assistant", "stop_reason": "end_turn",
				"content": []any{map[string]any{"type": "text", "text": text}}}
		}
		lines = append(lines, line)
	case "codex":
		if first {
			lines = append(lines, map[string]any{"timestamp": ts, "type": "session_meta", "payload": map[string]any{
				"id": a.sessionID, "cwd": a.cwd, "timestamp": ts, "originator": "codex-tui", "source": "cli"}})
		}
		if role == "user" {
			lines = append(lines, map[string]any{"timestamp": ts, "type": "event_msg", "payload": map[string]any{
				"type": "item_completed", "turn_id": "turn-" + id,
				"item": map[string]any{"type": "UserMessage", "id": id,
					"content": []any{map[string]any{"type": "text", "text": text}}}}})
		} else {
			lines = append(lines,
				map[string]any{"timestamp": ts, "type": "response_item", "payload": map[string]any{
					"type": "message", "id": "msg-" + id, "role": "assistant", "phase": "final_answer",
					"content": []any{map[string]any{"type": "output_text", "text": text}}}},
				map[string]any{"timestamp": ts, "type": "event_msg", "payload": map[string]any{
					"type": "task_complete", "turn_id": "turn-" + id, "last_agent_message": text}})
		}
	default:
		if first {
			lines = append(lines, map[string]any{"type": "session", "version": 3, "id": a.sessionID, "timestamp": ts, "cwd": a.cwd})
		}
		msg := map[string]any{"role": role, "content": []any{map[string]any{"type": "text", "text": text}}}
		if stop != "" {
			msg["stopReason"] = stop
		}
		lines = append(lines, map[string]any{"type": "message", "id": id, "parentId": parent, "timestamp": ts, "message": msg})
	}
	var out bytes.Buffer
	for _, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			return err
		}
		out.Write(b)
		out.WriteByte('\n')
	}
	_, err = f.Write(out.Bytes())
	return err
}

// writeHook writes the session start report.
func (a *agent) writeHook() error {
	if a.format == "pi" {
		return a.writeSpool("session_start", map[string]any{
			"hook_event_name": "session_start", "reason": "startup",
			"session_file": a.sessionFile, "cwd": a.cwd, "session_id": a.sessionID})
	}
	return a.writeSpool("SessionStart", map[string]any{
		"hook_event_name": "SessionStart", "source": "startup",
		"session_id": a.sessionID, "transcript_path": a.sessionFile, "cwd": a.cwd})
}

// hook writes a later report. Pi has no per-prompt hook here.
func (a *agent) hook(ev string) {
	if a.format == "pi" {
		return
	}
	_ = a.writeSpool(ev, map[string]any{"hook_event_name": ev,
		"session_id": a.sessionID, "transcript_path": a.sessionFile, "cwd": a.cwd})
}

// writeSpool writes one spool file, atomically.
func (a *agent) writeSpool(event string, payload map[string]any) error {
	rep := map[string]any{
		"runtime":     a.format,
		"event":       event,
		"at":          time.Now().UTC().Format(time.RFC3339Nano),
		"rex_session": os.Getenv("REX_SESSION"),
		"rex_block":   os.Getenv("REX_BLOCK"),
		"payload":     payload,
	}
	b, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.spool, 0o700); err != nil {
		return err
	}
	name := fmt.Sprintf("%d-%d-%s.json", time.Now().UnixNano(), os.Getpid(), shortID())
	tmp := filepath.Join(a.spool, "."+name+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(a.spool, name))
}

func shortID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func uuid() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
