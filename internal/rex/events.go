package rex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// EventType classifies an Event.
type EventType string

// Event types. Events that rex-tg does not need are dropped, not mapped.
const (
	SessionCreated       EventType = "session_created"
	SessionRemoved       EventType = "session_removed"
	BlockCreated         EventType = "block_created"
	BlockClosed          EventType = "block_closed"
	ProgramStatusChanged EventType = "program_status_changed"
	ProcessChanged       EventType = "process_changed"
	PwdChanged           EventType = "pwd_changed"
	TitleChanged         EventType = "title_changed"
	OtherEvent           EventType = "other"
)

// Event is one parsed item of `rex events --all --json`.
type Event struct {
	Type      EventType
	SessionID string
	BlockID   string
	Status    *StatusRecord // set for ProgramStatusChanged
	Received  time.Time
	Raw       json.RawMessage // the original line
}

// DroppedError is sent on the error channel when the consumer was too slow
// and events were discarded.
type DroppedError struct{ Count int }

func (e *DroppedError) Error() string {
	return fmt.Sprintf("rex events: dropped %d events (consumer too slow)", e.Count)
}

// ParseError is sent on the error channel for a line that is not valid JSON.
type ParseError struct {
	Line string
	Err  error
}

func (e *ParseError) Error() string { return "rex events: bad line: " + e.Err.Error() }

// Unwrap returns the JSON error.
func (e *ParseError) Unwrap() error { return e.Err }

const (
	eventBuffer = 256
	errBuffer   = 16
	maxLine     = 4 << 20
)

// Events starts `rex events --all --json` and streams parsed events.
//
// Both channels are closed when the stream ends. The process is killed when
// ctx is cancelled; that is not reported as an error. If the process exits by
// itself, or the output cannot be read, one error is sent before the close.
// A slow consumer never blocks the reader: when the event buffer is full the
// event is dropped and a *DroppedError is reported.
func (c *Client) Events(ctx context.Context) (<-chan Event, <-chan error) {
	events := make(chan Event, eventBuffer)
	errs := make(chan error, errBuffer)
	go c.streamEvents(ctx, events, errs)
	return events, errs
}

func (c *Client) streamEvents(ctx context.Context, events chan<- Event, errs chan<- error) {
	defer close(errs)
	defer close(events)

	cmd := exec.CommandContext(ctx, c.bin, append(c.globals(), "events", "--all", "--json")...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = time.Second
	out, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		errs <- &Error{Command: "events", Err: err}
		return
	}

	dropped := 0
	flushDropped := func() {
		if dropped == 0 {
			return
		}
		select {
		case errs <- &DroppedError{Count: dropped}:
			dropped = 0
		default:
		}
	}

	var scanErr error
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), maxLine)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		ev, ok, perr := parseEvent(line, c.now())
		if perr != nil {
			select {
			case errs <- &ParseError{Line: string(line), Err: perr}:
			default:
			}
			continue
		}
		if !ok {
			continue
		}
		select {
		case events <- ev:
			flushDropped()
		default:
			dropped++
			flushDropped()
		}
	}
	scanErr = sc.Err()
	if scanErr != nil {
		// Stop the child so Wait cannot hang on a full pipe.
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()

	if dropped > 0 {
		select {
		case errs <- &DroppedError{Count: dropped}:
		default:
		}
	}
	if ctx.Err() != nil {
		return
	}
	switch {
	case scanErr != nil:
		errs <- &Error{Command: "events", Stderr: strings.TrimSpace(stderr.String()), Err: fmt.Errorf("read output: %w", scanErr)}
	case waitErr != nil:
		errs <- &Error{Command: "events", Stderr: strings.TrimSpace(stderr.String()), Err: waitErr}
	default:
		errs <- &Error{Command: "events", Stderr: strings.TrimSpace(stderr.String()), Err: fmt.Errorf("stream ended")}
	}
}

type rawEnvelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// parseEvent converts one line. ok is false for events that are dropped.
func parseEvent(line []byte, now time.Time) (ev Event, ok bool, err error) {
	var top rawEnvelope
	if err := json.Unmarshal(line, &top); err != nil {
		return Event{}, false, err
	}
	raw := append(json.RawMessage(nil), line...)
	ev = Event{Received: now, Raw: raw}

	switch top.Type {
	case "client_joined", "client_left":
		return Event{}, false, nil
	case "session_created":
		var p struct {
			Session struct {
				ID string `json:"session_id"`
			} `json:"session"`
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(top.Payload, &p)
		ev.Type = SessionCreated
		ev.SessionID = firstNonEmpty(p.Session.ID, p.SessionID)
		return ev, true, nil
	case "session_destroyed", "session_removed", "session_closed":
		var p struct {
			Session struct {
				ID string `json:"session_id"`
			} `json:"session"`
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(top.Payload, &p)
		ev.Type = SessionRemoved
		ev.SessionID = firstNonEmpty(p.Session.ID, p.SessionID)
		return ev, true, nil
	case "session_event":
		var p struct {
			SessionID string      `json:"session_id"`
			Event     rawEnvelope `json:"event"`
		}
		if err := json.Unmarshal(top.Payload, &p); err != nil {
			ev.Type = OtherEvent
			return ev, true, nil
		}
		ev.SessionID = p.SessionID
		return parseSessionEvent(ev, p.Event)
	default:
		ev.Type = OtherEvent
		return ev, true, nil
	}
}

func parseSessionEvent(ev Event, inner rawEnvelope) (Event, bool, error) {
	switch inner.Type {
	case "client_connected", "client_disconnected", "block_owner_changed", "session_view_changed":
		return Event{}, false, nil
	case "block_created", "block_closed":
		var p struct {
			BlockID string `json:"block_id"`
		}
		_ = json.Unmarshal(inner.Payload, &p)
		ev.BlockID = p.BlockID
		ev.Type = BlockCreated
		if inner.Type == "block_closed" {
			ev.Type = BlockClosed
		}
		return ev, true, nil
	case "block_event":
		var p struct {
			BlockID string          `json:"block_id"`
			Name    string          `json:"name"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(inner.Payload, &p); err != nil {
			ev.Type = OtherEvent
			return ev, true, nil
		}
		ev.BlockID = p.BlockID
		switch p.Name {
		case "progress_report":
			return Event{}, false, nil
		case "program_status_changed":
			var sp struct {
				ID     string       `json:"id"`
				Record StatusRecord `json:"record"`
			}
			if err := json.Unmarshal(p.Payload, &sp); err == nil {
				if sp.Record.ID == "" {
					sp.Record.ID = sp.ID
				}
				ev.Status = &sp.Record
			}
			ev.Type = ProgramStatusChanged
		case "process_changed":
			ev.Type = ProcessChanged
		case "pwd_changed":
			ev.Type = PwdChanged
		case "title_changed":
			ev.Type = TitleChanged
		default:
			ev.Type = OtherEvent
		}
		return ev, true, nil
	default:
		ev.Type = OtherEvent
		return ev, true, nil
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
