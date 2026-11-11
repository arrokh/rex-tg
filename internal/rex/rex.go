// Package rex is a typed wrapper around the `rex` command line tool.
//
// Every call runs the CLI with --autostart=false, so rex-tg never starts a
// Rex server. Errors name the subcommand and carry the trimmed stderr.
package rex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultTimeout is the time limit of one non-streaming call.
const DefaultTimeout = 10 * time.Second

// Client runs the rex CLI.
type Client struct {
	bin     string
	server  string
	timeout time.Duration
	now     func() time.Time
}

// Option configures a Client.
type Option func(*Client)

// WithServer adds -S addr to every call.
func WithServer(addr string) Option { return func(c *Client) { c.server = addr } }

// WithTimeout sets the time limit of each non-streaming call. Zero or less keeps the default.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithClock sets the clock that stamps Event.Received.
func WithClock(now func() time.Time) Option {
	return func(c *Client) {
		if now != nil {
			c.now = now
		}
	}
}

// New returns a Client that runs the rex binary at bin.
func New(bin string, opts ...Option) *Client {
	c := &Client{bin: bin, timeout: DefaultTimeout, now: time.Now}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Whoami describes the server that the CLI talks to.
type Whoami struct {
	Account      string    `json:"account"`
	Endpoint     string    `json:"endpoint"`
	PID          int       `json:"pid"`
	InstanceID   string    `json:"instance_id"`
	HostID       string    `json:"host_id"`
	StartedAt    time.Time `json:"started_at"`
	RexVersion   string    `json:"rex_version"`
	APIRevision  int       `json:"api_revision"`
	SessionCount int       `json:"session_count"`
}

// Session is one Rex session.
type Session struct {
	ID    string `json:"session_id"`
	Label string `json:"label"`
}

// Block is one Rex block (a terminal pane).
type Block struct {
	ID          string `json:"block_id"`
	Label       string `json:"label"`
	CreatorName string `json:"creator_name"`
	Flavor      string `json:"flavor"`
	HostID      string `json:"host_id"`
	Placement   string `json:"placement"`
	WindowID    string `json:"window_id"`
	Owner       string `json:"owner"`
}

// ProcInfo describes one process.
type ProcInfo struct {
	PID         int    `json:"pid"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	InvokedPath string `json:"invoked_path"`
	Argv0       string `json:"argv0"`
	CWD         string `json:"cwd"`
}

// Process is the answer of the block method `process`.
// Foreground is the zero value when the server reports none.
type Process struct {
	Foreground ProcInfo   `json:"foreground"`
	Ancestors  []ProcInfo `json:"ancestors"`
	Child      *ProcInfo  `json:"child"`
	LastExit   *int       `json:"last_exit"`
}

// ProcOwner is the process that reported a status.
type ProcOwner struct {
	PID  int    `json:"pid"`
	Name string `json:"name"`
}

// StatusRecord is one program status report.
type StatusRecord struct {
	ID           string     `json:"id"`
	State        string     `json:"state"`
	Kind         string     `json:"kind"`
	App          string     `json:"app"`
	EffectiveApp string     `json:"effective_app"`
	Title        string     `json:"title"`
	Msg          string     `json:"msg"`
	UpdatedAt    time.Time  `json:"updated_at"`
	Owner        *ProcOwner `json:"owner"`
}

// Error is returned when the rex CLI fails.
type Error struct {
	Command string // for example "block call process"
	Stderr  string // trimmed
	Err     error
}

// IsGone reports an error that says the session or block is no longer there
// ("... target ... not found"). Panes come and go, so a call can race with a
// close; that is not a fault.
func IsGone(err error) bool {
	var re *Error
	return errors.As(err, &re) && strings.Contains(re.Stderr, "target") && strings.Contains(re.Stderr, "not found")
}

func (e *Error) Error() string {
	msg := "rex " + e.Command
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *Error) Unwrap() error { return e.Err }

func (c *Client) globals() []string {
	g := []string{"--autostart=false"}
	if c.server != "" {
		g = append(g, "-S", c.server)
	}
	return g
}

// run executes one non-streaming command and returns its stdout.
func (c *Client) run(ctx context.Context, name string, stdin []byte, args ...string) ([]byte, error) {
	return c.runScrub(ctx, name, stdin, nil, args...)
}

// maxStderr caps the stderr kept in an Error.
const maxStderr = 500

// cleanStderr removes every secret, trims, and caps the text at maxStderr bytes.
func cleanStderr(s string, secrets []string) string {
	for _, x := range secrets {
		if x != "" {
			s = strings.ReplaceAll(s, x, "[text]")
		}
	}
	s = strings.TrimSpace(s)
	if len(s) > maxStderr {
		s = strings.ToValidUTF8(s[:maxStderr], "") + "..."
	}
	return s
}

// runScrub is run, but removes the secrets from the stderr kept in errors.
func (c *Client) runScrub(ctx context.Context, name string, stdin []byte, secrets []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, append(c.globals(), args...)...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("%w (limit %s)", ctx.Err(), c.timeout)
		}
		return nil, &Error{Command: name, Stderr: cleanStderr(errb.String(), secrets), Err: err}
	}
	return out.Bytes(), nil
}

func (c *Client) runJSON(ctx context.Context, name string, v any, args ...string) error {
	out, err := c.run(ctx, name, nil, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, v); err != nil {
		return &Error{Command: name, Err: fmt.Errorf("parse output: %w", err)}
	}
	return nil
}

// Whoami runs `rex whoami --json`.
func (c *Client) Whoami(ctx context.Context) (Whoami, error) {
	var w Whoami
	err := c.runJSON(ctx, "whoami", &w, "whoami", "--json")
	return w, err
}

// Sessions runs `rex ls --json`.
func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	var r struct {
		Sessions []Session `json:"sessions"`
	}
	if err := c.runJSON(ctx, "ls", &r, "ls", "--json"); err != nil {
		return nil, err
	}
	return r.Sessions, nil
}

// Blocks runs `rex block ls -s session --json`.
func (c *Client) Blocks(ctx context.Context, session string) ([]Block, error) {
	var r struct {
		Blocks []Block `json:"blocks"`
	}
	if err := c.runJSON(ctx, "block ls", &r, "block", "ls", "-s", session, "--json"); err != nil {
		return nil, err
	}
	return r.Blocks, nil
}

// Process calls the block method `process`.
func (c *Client) Process(ctx context.Context, session, block string) (Process, error) {
	var p Process
	err := c.runJSON(ctx, "block call process", &p, "block", "call", "-s", session, "-b", block, "process")
	return p, err
}

// ProgramStatus calls the block method `program_status`.
func (c *Client) ProgramStatus(ctx context.Context, session, block string) ([]StatusRecord, error) {
	var r struct {
		Records []StatusRecord `json:"records"`
	}
	if err := c.runJSON(ctx, "block call program_status", &r, "block", "call", "-s", session, "-b", block, "program_status"); err != nil {
		return nil, err
	}
	return r.Records, nil
}

// Capture prints the screen and scrollback of a block, without trailing blank lines.
func (c *Client) Capture(ctx context.Context, session, block string) (string, error) {
	out, err := c.run(ctx, "capture", nil, "capture", "-s", session, "-b", block, "--trim")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Paste sends a paste event to a block. The JSON goes through stdin, never argv.
func (c *Client) Paste(ctx context.Context, session, block, text string) error {
	body, err := json.Marshal(map[string]any{
		"block_id": block,
		"event":    map[string]any{"type": "paste", "payload": map[string]any{"text": text}},
	})
	if err != nil {
		return fmt.Errorf("rex api call block.event: %w", err)
	}
	secrets := []string{text}
	if len(text) > 0 {
		if q, jerr := json.Marshal(text); jerr == nil {
			secrets = append([]string{string(q[1 : len(q)-1])}, secrets...)
		}
		secrets = append([]string{string(body)}, secrets...)
	}
	_, err = c.runScrub(ctx, "api call block.event", body, secrets, "api", "call", "-s", session, "block.event", "-")
	return err
}

// SendKeys presses keys such as "enter" or "ctrl+c" in a block.
func (c *Client) SendKeys(ctx context.Context, session, block string, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	args := append([]string{"send-key", "-s", session, "-b", block}, keys...)
	_, err := c.run(ctx, "send-key", nil, args...)
	return err
}
