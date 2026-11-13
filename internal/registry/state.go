package registry

import (
	"context"
	"fmt"
	"time"

	"rex-tg/internal/store"
)

func validState(s string) bool {
	switch s {
	case "idle", "working", "done", "blocked", "error":
		return true
	}
	return false
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// signal merges one state signal into a pane's state. The newest signal wins.
// A prompt (prompt true) always means working, even when a newer-looking but
// stale done is on record. A working signal clears the busy window.
func (r *Registry) signal(ctx context.Context, pane store.Pane, mem *paneMem, state, kind, msg string, at time.Time, prompt bool, now time.Time) ([]Change, error) {
	if !validState(state) {
		return nil, nil
	}
	if at.IsZero() {
		at = now
	}
	if at.Before(mem.stateAt) {
		return nil, nil // older than what we know, also a replayed prompt line
	}
	if state != "blocked" {
		kind = ""
	}
	prevState, prevKind := mem.state, mem.kind
	switch state {
	case "working":
		if prompt {
			// Never earlier than the idle we saw, so Ready stays false.
			mem.lastWorking = maxTime(at, mem.lastIdle)
			at = maxTime(at, mem.stateAt)
		} else {
			mem.lastWorking = at
		}
		if !pane.BusyUntil.IsZero() {
			if err := r.st.SetPaneBusyUntil(ctx, pane.ID, time.Time{}); err != nil {
				return nil, fmt.Errorf("registry: clear busy: %w", err)
			}
		}
	case "idle", "done":
		mem.lastIdle = at
	}
	mem.state, mem.kind, mem.stateAt = state, kind, at
	if err := r.st.SetPaneState(ctx, pane.ID, state, kind, at); err != nil {
		return nil, fmt.Errorf("registry: save pane state: %w", err)
	}
	if prevState == state && prevKind == kind {
		return nil, nil
	}
	return []Change{{Kind: StateChanged, PaneID: pane.ID, ConversationID: pane.ConversationID,
		State: state, StateKind: kind, Msg: msg}}, nil
}

// clearState forgets a pane's state (its agent left).
func (r *Registry) clearState(ctx context.Context, paneID int64, mem *paneMem) error {
	mem.state, mem.kind = "", ""
	mem.stateAt, mem.lastWorking, mem.lastIdle = time.Time{}, time.Time{}, time.Time{}
	if err := r.st.SetPaneState(ctx, paneID, "", "", time.Time{}); err != nil {
		return fmt.Errorf("registry: clear pane state: %w", err)
	}
	if err := r.st.SetPaneBusyUntil(ctx, paneID, time.Time{}); err != nil {
		return fmt.Errorf("registry: clear busy: %w", err)
	}
	return nil
}

// TranscriptSignal merges a state signal from the daemon's tailers. A Signal
// with State working and Kind "prompt" is a prompt line.
func (r *Registry) TranscriptSignal(ctx context.Context, convID int64, sig Signal) ([]Change, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pane, ok, err := r.st.PaneByConversation(ctx, convID)
	if err != nil || !ok {
		return nil, err
	}
	mem := r.memFor(ctx, pane)
	prompt := sig.State == "working" && sig.Kind == "prompt"
	kind := sig.Kind
	if prompt {
		kind = ""
	}
	return r.signal(ctx, pane, mem, sig.State, kind, "", sig.At, prompt, r.opt.Now())
}

// Ready tells whether a waiting message may be typed into the pane now. When
// not, the reason says why. Ready needs an idle or done signal newer than the
// last working signal. Silence never counts as idle. A blocked pane or a pane
// that was typed into a moment ago is not ready.
// Reasons of Ready that the daemon tells apart.
const (
	// ReasonWorking: the agent works and nothing else stops typing, so a
	// message may steer the run (input.while_busy).
	ReasonWorking = "the agent is working"
	// ReasonBlocked: the agent waits for an answer to a prompt.
	ReasonBlocked = "the agent is waiting for an answer to a prompt"
	// ReasonJustTyped: a message was typed and the agent has not taken it.
	ReasonJustTyped = "a message was just typed; waiting for the agent to take it"
)

func (r *Registry) Ready(ctx context.Context, paneID int64, now time.Time) (bool, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pane, err := r.st.Pane(ctx, paneID)
	if err != nil {
		return false, "", fmt.Errorf("registry: ready: %w", err)
	}
	if !pane.GoneAt.IsZero() {
		return false, "the pane is closed", nil
	}
	if pane.ConversationID == 0 {
		return false, "no agent session runs in the pane", nil
	}
	mem := r.memFor(ctx, pane)
	switch {
	case mem.state == "blocked":
		return false, ReasonBlocked, nil
	case pane.BusyUntil.After(now):
		return false, ReasonJustTyped, nil
	case mem.state == "working":
		return false, ReasonWorking, nil
	case mem.lastIdle.IsZero() || !mem.lastIdle.After(mem.lastWorking):
		return false, "no idle signal yet", nil
	case mem.state == "error":
		return false, "the agent is in an error state", nil
	}
	return true, "", nil
}

// MarkTyped starts the busy window after rex-tg typed a message into the
// pane. A working signal or a prompt clears it.
func (r *Registry) MarkTyped(ctx context.Context, paneID int64, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.st.SetPaneBusyUntil(ctx, paneID, now.Add(r.opt.BusyAfterTyping)); err != nil {
		return fmt.Errorf("registry: mark typed: %w", err)
	}
	return nil
}

// Agents lists the panes that run an agent, including unresolved ones.
func (r *Registry) Agents(ctx context.Context) ([]AgentView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	panes, err := r.st.Panes(ctx)
	if err != nil {
		return nil, fmt.Errorf("registry: agents: %w", err)
	}
	var out []AgentView
	for _, p := range panes {
		if p.Host != r.opt.Host {
			continue
		}
		mem := r.memFor(ctx, p)
		v := AgentView{Pane: p, Resolution: "unresolved"}
		switch {
		case p.ConversationID != 0:
			c, err := r.st.Conversation(ctx, p.ConversationID)
			if err != nil {
				return nil, fmt.Errorf("registry: agents: %w", err)
			}
			v.Conversation, v.Runtime = &c, c.Runtime
			v.Resolution = p.Source
			if v.Resolution == "" {
				v.Resolution = "scan"
			}
			if t, ok, err := r.st.ThreadByConversation(ctx, c.ID); err != nil {
				return nil, fmt.Errorf("registry: agents: %w", err)
			} else if ok {
				v.Thread = &t
			}
		case mem.hasProc:
			v.Runtime = mem.proc.rt
		default:
			continue
		}
		out = append(out, v)
	}
	return out, nil
}
