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
	echoDelay            time.Duration

	pending atomic.Int32 // prompts submitted and not yet answered

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
	flag.Parse()
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
	now := time.Now().UTC()
	dir := filepath.Join(a.home, ".pi", "agent", "sessions", "--"+strings.ReplaceAll(strings.TrimPrefix(cwd, "/"), "/", "-")+"--")
	a.sessionFile = filepath.Join(dir, fmt.Sprintf("%s-%03dZ_%s.jsonl", now.Format("2006-01-02T15-04-05"), now.Nanosecond()/1e6, a.sessionID))

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

	fmt.Print("\x1b[?2004h") // bracketed paste on
	a.status("idle", "")
	fmt.Print("dummy pi ready\r\n")
	if a.seed != "" {
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
	go a.readInput(prompts)
	for p := range prompts {
		if err := a.answer(p); err != nil {
			return err
		}
	}
	return nil
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

// answer simulates a run: working, then the transcript lines, then done.
func (a *agent) answer(prompt string) error {
	a.status("working", "e2e")
	time.Sleep(a.echoDelay)
	if err := a.append("user", prompt, ""); err != nil {
		return err
	}
	time.Sleep(a.think)
	if err := a.append("assistant", "ECHO: "+prompt, "stop"); err != nil {
		return err
	}
	a.record(map[string]any{"ev": "done", "text": prompt})
	a.pending.Add(-1)
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

// append writes pi-format lines. The first call writes the header first.
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
	var lines []any
	if a.lastEntry == "" {
		lines = append(lines, map[string]any{"type": "session", "version": 3, "id": a.sessionID, "timestamp": ts, "cwd": a.cwd})
	}
	id := shortID()
	msg := map[string]any{"role": role, "content": []any{map[string]any{"type": "text", "text": text}}}
	if stop != "" {
		msg["stopReason"] = stop
	}
	var parent any
	if a.lastEntry != "" {
		parent = a.lastEntry
	}
	lines = append(lines, map[string]any{"type": "message", "id": id, "parentId": parent, "timestamp": ts, "message": msg})
	a.lastEntry = id
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

// writeHook writes the session_start spool report, atomically.
func (a *agent) writeHook() error {
	payload := map[string]any{
		"hook_event_name": "session_start",
		"reason":          "startup",
		"session_file":    a.sessionFile,
		"cwd":             a.cwd,
		"session_id":      a.sessionID,
	}
	rep := map[string]any{
		"runtime":     "pi",
		"event":       "session_start",
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
