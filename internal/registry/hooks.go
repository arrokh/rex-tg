package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/rex"
	"rex-tg/internal/store"
)

type hookPayload struct {
	HookEventName    string `json:"hook_event_name"`
	SessionID        string `json:"session_id"`
	TranscriptPath   string `json:"transcript_path"`
	CWD              string `json:"cwd"`
	Source           string `json:"source"`
	NotificationType string `json:"notification_type"`
	Reason           string `json:"reason"`
	SessionFile      string `json:"session_file"`
}

// hookState maps a hook event to a state signal.
func hookState(rt agents.Runtime, event string, p hookPayload) (state, kind string, prompt, ok bool) {
	if rt == agents.Pi {
		switch event {
		case "agent_start":
			return "working", "", true, true
		case "agent_end":
			return "idle", "", false, true
		}
		return "", "", false, false
	}
	switch event {
	case "UserPromptSubmit":
		return "working", "", true, true
	case "Stop":
		return "idle", "", false, true
	case "PermissionRequest":
		return "blocked", "permission", false, true
	case "Notification":
		if p.NotificationType == "permission_prompt" {
			return "blocked", "permission", false, true
		}
	}
	return "", "", false, false
}

// Hook applies one hook report. The report is accepted only when the pane's
// foreground process belongs to the reporting runtime, which rejects reports
// from nested agents that inherit REX_BLOCK. Reports older than the pane's
// current process are ignored, and session changes are applied in time order.
func (r *Registry) Hook(ctx context.Context, rep HookReport) ([]Change, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rep.RexBlock == "" || !rep.Runtime.Valid() {
		return nil, nil
	}
	if rep.RexSession == "" {
		session, err := r.sessionForBlock(ctx, rep.RexBlock)
		if err != nil {
			return nil, err
		}
		if session == "" {
			return nil, nil
		}
		rep.RexSession = session
	}
	now := r.opt.Now()
	at := rep.At
	if at.IsZero() {
		at = now
	}
	proc, err := r.rx.Process(ctx, rep.RexSession, rep.RexBlock)
	if err != nil {
		return nil, fmt.Errorf("registry: process of %s: %w", rep.RexBlock, err)
	}
	fg := proc.Foreground
	rt, isAgent := Detect(r.opt.Rules, fg)
	if !isAgent || rt != rep.Runtime {
		r.opt.Logger.Debug("hook rejected: foreground is not the reporting runtime",
			"runtime", rep.Runtime, "event", rep.Event, "block", rep.RexBlock, "foreground", fg.Name)
		return nil, nil
	}
	var p hookPayload
	if len(rep.Payload) > 0 {
		if err := json.Unmarshal(rep.Payload, &p); err != nil {
			return nil, fmt.Errorf("registry: hook payload: %w", err)
		}
	}
	event := rep.Event
	if event == "" {
		event = p.HookEventName
	}

	pane, exists, err := r.st.PaneByBlock(ctx, r.opt.Host, rep.RexBlock)
	if err != nil {
		return nil, fmt.Errorf("registry: find pane: %w", err)
	}
	if !exists || !pane.GoneAt.IsZero() || pane.Session != rep.RexSession {
		if pane, err = r.st.UpsertPane(ctx, r.opt.Host, rep.RexBlock, rep.RexSession, "", now); err != nil {
			return nil, fmt.Errorf("registry: upsert pane: %w", err)
		}
	}
	mem := r.memFor(ctx, pane)
	var out []Change
	if mem.hasProc && (mem.proc.pid != fg.PID || mem.proc.name != fg.Name) {
		// A new process of the same runtime replaced the old one unseen.
		ch, err := r.stopOccupant(ctx, pane, mem)
		if err != nil {
			return nil, err
		}
		out = append(out, ch...)
		if pane, err = r.st.Pane(ctx, pane.ID); err != nil {
			return nil, fmt.Errorf("registry: reload pane: %w", err)
		}
	}
	if !mem.hasProc {
		fallback := time.Time{}
		if exists {
			fallback = pane.SeenAt
		}
		mem.hasProc = true
		mem.proc = procIdent{pid: fg.PID, name: fg.Name, rt: rt, cwd: fg.CWD, start: r.startOf(fg.PID, fallback)}
	}
	if at.Before(mem.proc.start) {
		r.opt.Logger.Debug("hook ignored: older than the process", "event", event, "at", at)
		return out, nil
	}

	s, source, ok := r.hookSession(ctx, rep.Runtime, event, p)
	if ok {
		if a, have := r.adapter(rt); have && a.IsSubagentTranscript(s.Transcript) {
			return out, nil
		}
		if !at.Before(mem.hookAt) {
			mem.hookAt = at
			ch, err := r.adopt(ctx, pane, mem, rt, s, source, store.SourceHook, now)
			if err != nil {
				return nil, err
			}
			out = append(out, ch...)
			if pane, err = r.st.Pane(ctx, pane.ID); err != nil {
				return nil, fmt.Errorf("registry: reload pane: %w", err)
			}
		}
	}

	if state, kind, prompt, ok := hookState(rep.Runtime, event, p); ok {
		ch, err := r.signal(ctx, pane, mem, state, kind, "", at, prompt, now)
		if err != nil {
			return nil, err
		}
		out = append(out, ch...)
	}
	return out, nil
}

// sessionForBlock finds the Rex session of a block when a hook process has no
// REX_SESSION environment variable. The stored session is a fast-path hint,
// but the block may have moved since the last scan. The Rex scan also covers
// the first hook, which can arrive before the daemon's pane scan.
func (r *Registry) sessionForBlock(ctx context.Context, block string) (string, error) {
	pane, ok, err := r.st.PaneByBlock(ctx, r.opt.Host, block)
	if err != nil {
		return "", fmt.Errorf("registry: find pane for hook: %w", err)
	}
	if ok && pane.GoneAt.IsZero() && pane.Session != "" {
		blocks, err := r.rx.Blocks(ctx, pane.Session)
		if err == nil {
			for _, b := range blocks {
				if b.ID == block {
					return pane.Session, nil
				}
			}
		}
	}
	sessions, err := r.rx.Sessions(ctx)
	if err != nil {
		return "", fmt.Errorf("registry: list sessions for hook: %w", err)
	}
	found := ""
	for _, s := range sessions {
		blocks, err := r.rx.Blocks(ctx, s.ID)
		if err != nil {
			return "", fmt.Errorf("registry: list blocks for hook in %s: %w", s.ID, err)
		}
		for _, b := range blocks {
			if b.ID != block {
				continue
			}
			if found != "" && found != s.ID {
				return "", nil // an ambiguous block is never adopted
			}
			found = s.ID
		}
	}
	return found, nil
}

// hookSession extracts the session that a hook names. It returns false when
// the event does not identify one.
func (r *Registry) hookSession(ctx context.Context, rt agents.Runtime, event string, p hookPayload) (session, string, bool) {
	if rt == agents.Pi {
		switch event {
		case "session_start", "agent_start", "agent_end":
		default:
			return session{}, "", false
		}
		if p.SessionFile == "" {
			return session{}, "", false
		}
		s := session{ID: p.SessionID, Transcript: p.SessionFile, CWD: p.CWD}
		if s.ID == "" {
			if h, err := r.readHeader(rt, p.SessionFile); err == nil && h.SessionID != "" {
				s.ID, s.CWD, s.Title, s.Started = h.SessionID, firstNonEmpty(s.CWD, h.CWD), h.Title, h.StartedAt
			} else {
				// pi writes the file at the first prompt: use <time>_<id>.jsonl.
				name := strings.TrimSuffix(filepath.Base(p.SessionFile), ".jsonl")
				if i := strings.LastIndex(name, "_"); i >= 0 {
					name = name[i+1:]
				}
				s.ID = name
			}
			if s.ID == "" {
				return session{}, "", false
			}
		}
		source := ""
		if event == "session_start" {
			source = p.Reason
		}
		return s, source, true
	}
	if event == "SessionEnd" || p.SessionID == "" {
		return session{}, "", false
	}
	s := session{ID: p.SessionID, Transcript: p.TranscriptPath, CWD: p.CWD}
	if s.Transcript == "" {
		// PermissionRequest carries no path. Use the one we know.
		c, found, err := r.st.ConversationByNative(ctx, rt, p.SessionID)
		if err != nil || !found {
			return session{}, "", false
		}
		s.Transcript = c.Transcript
	}
	source := ""
	if event == "SessionStart" {
		source = p.Source
	}
	return s, source, true
}

// RexEvent applies one event from `rex events`.
func (r *Registry) RexEvent(ctx context.Context, ev rex.Event) ([]Change, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.opt.Now()
	switch ev.Type {
	case rex.BlockClosed:
		pane, ok, err := r.st.PaneByBlock(ctx, r.opt.Host, ev.BlockID)
		if err != nil || !ok || !pane.GoneAt.IsZero() {
			return nil, err
		}
		return r.paneGone(ctx, pane, now)
	case rex.ProcessChanged:
		proc, err := r.rx.Process(ctx, ev.SessionID, ev.BlockID)
		if err != nil {
			return nil, fmt.Errorf("registry: process of %s: %w", ev.BlockID, err)
		}
		return r.syncPane(ctx, ev.SessionID, ev.BlockID, "", proc, true, now)
	case rex.ProgramStatusChanged:
		if ev.Status == nil {
			return nil, nil
		}
		var out []Change
		pane, ok, err := r.st.PaneByBlock(ctx, r.opt.Host, ev.BlockID)
		if err != nil {
			return nil, fmt.Errorf("registry: find pane: %w", err)
		}
		if !ok || !pane.GoneAt.IsZero() || !r.memFor(ctx, pane).hasProc {
			// A status can arrive before the process change.
			proc, err := r.rx.Process(ctx, ev.SessionID, ev.BlockID)
			if err != nil {
				return nil, fmt.Errorf("registry: process of %s: %w", ev.BlockID, err)
			}
			if out, err = r.syncPane(ctx, ev.SessionID, ev.BlockID, "", proc, true, now); err != nil {
				return nil, err
			}
			if pane, ok, err = r.st.PaneByBlock(ctx, r.opt.Host, ev.BlockID); err != nil || !ok {
				return out, err
			}
		}
		ch, err := r.applyRecord(ctx, pane, *ev.Status, ev.Received, now)
		return append(out, ch...), err
	}
	return nil, nil
}

func (r *Registry) applyRecord(ctx context.Context, pane store.Pane, rec rex.StatusRecord, received, now time.Time) ([]Change, error) {
	at := rec.UpdatedAt
	if at.IsZero() {
		at = received
	}
	mem := r.memFor(ctx, pane)
	if !mem.hasProc {
		return nil, nil
	}
	return r.signal(ctx, pane, mem, rec.State, rec.Kind, rec.Msg, at, false, now)
}

// Scan reads all Rex sessions and blocks. It finds agents that started while
// the daemon was away, retries the guess for unresolved panes, takes in the
// current Rex status of every agent, and marks panes gone that Rex no longer
// lists. When Rex cannot be read completely, no pane is marked gone.
// logScanErr logs a failed call of a scan. A session or block that closed
// during the scan is normal (the next scan sees it gone), so it is a debug
// line, not a warning.
func (r *Registry) logScanErr(msg, key, id string, err error) {
	if rex.IsGone(err) {
		r.opt.Logger.Debug(msg, key, id, "err", err)
		return
	}
	r.opt.Logger.Warn(msg, key, id, "err", err)
}

func (r *Registry) Scan(ctx context.Context) ([]Change, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.opt.Now()
	sessions, err := r.rx.Sessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("registry: list sessions: %w", err)
	}
	var out []Change
	seen := map[string]bool{}
	complete := true
	for _, sess := range sessions {
		blocks, err := r.rx.Blocks(ctx, sess.ID)
		if err != nil {
			complete = false
			r.logScanErr("scan: list blocks", "session", sess.ID, err)
			continue
		}
		for _, b := range blocks {
			seen[b.ID] = true
			proc, err := r.rx.Process(ctx, sess.ID, b.ID)
			if err != nil {
				complete = false
				r.logScanErr("scan: process", "block", b.ID, err)
				continue
			}
			ch, err := r.syncPane(ctx, sess.ID, b.ID, b.Label, proc, false, now)
			if err != nil {
				return out, err
			}
			out = append(out, ch...)
			pane, ok, err := r.st.PaneByBlock(ctx, r.opt.Host, b.ID)
			if err != nil {
				return out, fmt.Errorf("registry: find pane: %w", err)
			}
			if !ok || !r.memFor(ctx, pane).hasProc {
				continue
			}
			recs, err := r.rx.ProgramStatus(ctx, sess.ID, b.ID)
			if err != nil {
				r.logScanErr("scan: program status", "block", b.ID, err)
				continue
			}
			if len(recs) == 0 {
				continue
			}
			newest := recs[0]
			for _, rec := range recs[1:] {
				if rec.UpdatedAt.After(newest.UpdatedAt) {
					newest = rec
				}
			}
			ch, err = r.applyRecord(ctx, pane, newest, time.Time{}, now)
			if err != nil {
				return out, err
			}
			out = append(out, ch...)
		}
	}
	// Resolve only after every pane is known, so that two unresolved agents
	// of one runtime in one folder see each other.
	panes, err := r.st.Panes(ctx)
	if err != nil {
		return out, fmt.Errorf("registry: list panes: %w", err)
	}
	for _, p := range panes {
		mem := r.memFor(ctx, p)
		if p.Host != r.opt.Host || !seen[p.Block] || p.ConversationID != 0 || !mem.hasProc {
			continue
		}
		ch, err := r.guess(ctx, p, mem, now)
		if err != nil {
			return out, err
		}
		out = append(out, ch...)
	}
	if !complete {
		return out, nil
	}
	panes, err = r.st.Panes(ctx)
	if err != nil {
		return out, fmt.Errorf("registry: list panes: %w", err)
	}
	for _, p := range panes {
		if p.Host != r.opt.Host || seen[p.Block] {
			continue
		}
		ch, err := r.paneGone(ctx, p, now)
		if err != nil {
			return out, err
		}
		out = append(out, ch...)
	}
	return out, nil
}
