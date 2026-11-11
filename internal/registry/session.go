package registry

import (
	"context"
	"fmt"
	"os"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/rex"
	"rex-tg/internal/store"
)

// session is what a hook or the guess resolver knows about a session.
type session struct {
	ID, Transcript, CWD, Title string
	Started                    time.Time
}

// syncPane compares a pane's foreground process with what the registry knows.
// It stops a session whose process left, tracks a new agent process, and, when
// guess is true, tries to resolve a pane that has an agent but no session.
func (r *Registry) syncPane(ctx context.Context, rexSession, block, label string, proc rex.Process, guess bool, now time.Time) ([]Change, error) {
	fg := proc.Foreground
	rt, isAgent := Detect(r.opt.Rules, fg)
	pane, exists, err := r.st.PaneByBlock(ctx, r.opt.Host, block)
	if err != nil {
		return nil, fmt.Errorf("registry: find pane: %w", err)
	}
	if !exists && !isAgent {
		return nil, nil
	}
	if !exists || !pane.GoneAt.IsZero() || pane.Session != rexSession {
		if pane, err = r.st.UpsertPane(ctx, r.opt.Host, block, rexSession, label, now); err != nil {
			return nil, fmt.Errorf("registry: upsert pane: %w", err)
		}
	}
	mem := r.memFor(ctx, pane)
	var out []Change
	if mem.hasProc && (!isAgent || mem.proc.pid != fg.PID || mem.proc.name != fg.Name) {
		ch, err := r.stopOccupant(ctx, pane, mem)
		if err != nil {
			return nil, err
		}
		out = append(out, ch...)
		if pane, err = r.st.Pane(ctx, pane.ID); err != nil {
			return nil, fmt.Errorf("registry: reload pane: %w", err)
		}
	}
	if !isAgent {
		return out, nil
	}
	if !mem.hasProc {
		mem.hasProc = true
		mem.proc = procIdent{pid: fg.PID, name: fg.Name, rt: rt, cwd: fg.CWD, start: r.startOf(fg.PID, now)}
	}
	if guess && pane.ConversationID == 0 {
		ch, err := r.guess(ctx, pane, mem, now)
		if err != nil {
			return nil, err
		}
		out = append(out, ch...)
	}
	return out, nil
}

// stopOccupant handles an agent process that left the pane. The session
// stops; its topic goes offline when it was the topic's current session.
func (r *Registry) stopOccupant(ctx context.Context, pane store.Pane, mem *paneMem) ([]Change, error) {
	conv := pane.ConversationID
	mem.hasProc = false
	mem.proc = procIdent{}
	if err := r.clearState(ctx, pane.ID, mem); err != nil {
		return nil, err
	}
	if conv == 0 {
		return nil, nil
	}
	if err := r.st.SetPaneOccupant(ctx, pane.ID, 0, 0, "", ""); err != nil {
		return nil, fmt.Errorf("registry: clear occupant: %w", err)
	}
	out := []Change{{Kind: SessionStopped, PaneID: pane.ID, ConversationID: conv}}
	off, err := r.topicOffline(ctx, conv, pane.ID)
	return append(out, off...), err
}

// topicOffline sets the topic offline when conv is its current session and it
// is live.
func (r *Registry) topicOffline(ctx context.Context, conv, paneID int64) ([]Change, error) {
	th, ok, err := r.st.ThreadByConversation(ctx, conv)
	if err != nil {
		return nil, fmt.Errorf("registry: find topic: %w", err)
	}
	if !ok || th.CurrentConversationID != conv || th.Status != store.ThreadLive {
		return nil, nil
	}
	if err := r.st.SetThreadStatus(ctx, th.ID, store.ThreadOffline); err != nil {
		return nil, fmt.Errorf("registry: topic offline: %w", err)
	}
	return []Change{{Kind: TopicOffline, PaneID: paneID, ConversationID: conv, ThreadID: th.ID}}, nil
}

// paneGone handles a closed Rex block.
func (r *Registry) paneGone(ctx context.Context, pane store.Pane, now time.Time) ([]Change, error) {
	conv := pane.ConversationID
	if err := r.st.MarkPaneGone(ctx, pane.ID, now); err != nil {
		return nil, fmt.Errorf("registry: mark pane gone: %w", err)
	}
	delete(r.panes, pane.ID)
	out := []Change{{Kind: PaneGone, PaneID: pane.ID, ConversationID: conv}}
	if conv == 0 {
		return out, nil
	}
	off, err := r.topicOffline(ctx, conv, pane.ID)
	return append(out, off...), err
}

// readHeader reads the session identity from a transcript file.
func (r *Registry) readHeader(rt agents.Runtime, path string) (agents.Header, error) {
	a, ok := r.adapter(rt)
	if !ok {
		return agents.Header{}, fmt.Errorf("registry: no adapter for %s", rt)
	}
	f, err := os.Open(path)
	if err != nil {
		return agents.Header{}, err
	}
	defer f.Close()
	return a.ReadHeader(f)
}

// guess resolves a pane that has an agent but no hook report: the newest
// transcript of the runtime in the same folder that changed after the process
// started, when it is the only one that fits.
func (r *Registry) guess(ctx context.Context, pane store.Pane, mem *paneMem, now time.Time) ([]Change, error) {
	rt, cwd := mem.proc.rt, mem.proc.cwd
	a, ok := r.adapter(rt)
	if !ok || cwd == "" {
		return nil, nil
	}
	cands, err := a.TranscriptCandidates(r.opt.Home, cwd, mem.proc.start)
	if err != nil {
		return nil, fmt.Errorf("registry: transcript candidates: %w", err)
	}
	// Another unresolved agent of the same runtime in the same folder makes
	// every file ambiguous.
	for id, other := range r.panes {
		if id == pane.ID || !other.hasProc || other.proc.rt != rt || other.proc.cwd != cwd {
			continue
		}
		op, err := r.st.Pane(ctx, id)
		if err == nil && op.GoneAt.IsZero() && op.ConversationID == 0 {
			return nil, nil
		}
	}
	var fit []session
	for _, path := range cands {
		if a.IsSubagentTranscript(path) {
			continue
		}
		h, err := r.readHeader(rt, path)
		if err != nil || h.SessionID == "" {
			continue // not readable yet, or not a session file
		}
		if c, found, err := r.st.ConversationByNative(ctx, rt, h.SessionID); err == nil && found {
			if p, running, err := r.st.PaneByConversation(ctx, c.ID); err == nil && running && p.ID != pane.ID {
				continue // runs in another pane
			}
		}
		fit = append(fit, session{ID: h.SessionID, Transcript: path, CWD: firstNonEmpty(h.CWD, cwd), Title: h.Title, Started: h.StartedAt})
	}
	if len(fit) != 1 {
		return nil, nil // none, or unresolved because several fit
	}
	return r.adopt(ctx, pane, mem, rt, fit[0], "", store.SourceGuess, now)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// classify decides whether a session was resumed, and the word for the
// divider. Without a hook source it uses the saved cursor, then the age of
// the file against the process (design section 6).
func classify(source string, hasCursor bool, started, procStart time.Time) (resume bool, label string) {
	switch source {
	case "resume":
		return true, ""
	case "startup", "clear", "new", "fork", "compact", "reload":
		return false, source
	}
	if hasCursor {
		return true, ""
	}
	if !started.IsZero() && !procStart.IsZero() && started.Before(procStart) {
		return true, ""
	}
	return false, "startup"
}

func startFor(resume, hasCursor bool) TailStart {
	switch {
	case !resume:
		return TailStart{FromStart: true}
	case hasCursor:
		return TailStart{}
	}
	return TailStart{AtEnd: true, Backfill: true}
}

// adopt records that session s runs in the pane and applies the topic rules of
// design section 7. how is store.SourceHook or store.SourceGuess; source is the
// hook's source or reason ("" when unknown).
func (r *Registry) adopt(ctx context.Context, pane store.Pane, mem *paneMem, rt agents.Runtime, s session, source, how string, now time.Time) ([]Change, error) {
	if s.ID == "" || s.Transcript == "" {
		return nil, nil
	}
	if s.Started.IsZero() && source == "" {
		if h, err := r.readHeader(rt, s.Transcript); err == nil {
			s.Started = h.StartedAt
		}
	}
	old, found, err := r.st.ConversationByNative(ctx, rt, s.ID)
	if err != nil {
		return nil, fmt.Errorf("registry: find conversation: %w", err)
	}
	if found && pane.ConversationID == old.ID {
		// Same session as before. Keep pid, name, and source current.
		if pane.PID != mem.proc.pid || pane.ProcessName != mem.proc.name || (how == store.SourceHook && pane.Source != how) {
			src := pane.Source
			if how == store.SourceHook {
				src = how
			}
			if err := r.st.SetPaneOccupant(ctx, pane.ID, old.ID, mem.proc.pid, mem.proc.name, src); err != nil {
				return nil, fmt.Errorf("registry: update occupant: %w", err)
			}
		}
		return nil, nil
	}
	conv, err := r.st.UpsertConversation(ctx, store.Conversation{
		Runtime: rt, NativeID: s.ID, Transcript: s.Transcript, CWD: s.CWD, Title: s.Title, FirstSeenAt: now,
	})
	if err != nil {
		return nil, fmt.Errorf("registry: save conversation: %w", err)
	}
	_, hasCursor, err := r.st.Cursor(ctx, conv.Transcript)
	if err != nil {
		return nil, fmt.Errorf("registry: read cursor: %w", err)
	}
	prevOcc := pane.ConversationID
	prevLast := pane.LastConversationID
	oldPane, movedFrom, err := r.st.PaneByConversation(ctx, conv.ID)
	if err != nil {
		return nil, fmt.Errorf("registry: find pane of session: %w", err)
	}
	if err := r.st.SetPaneOccupant(ctx, pane.ID, conv.ID, mem.proc.pid, mem.proc.name, how); err != nil {
		return nil, fmt.Errorf("registry: set occupant: %w", err)
	}

	var out []Change
	if prevOcc != 0 {
		out = append(out, Change{Kind: SessionStopped, PaneID: pane.ID, ConversationID: prevOcc})
	}
	if movedFrom && oldPane.ID != pane.ID {
		out = append(out, Change{Kind: SessionStopped, PaneID: oldPane.ID, ConversationID: conv.ID})
		if om, ok := r.panes[oldPane.ID]; ok {
			om.hasProc = false
			if err := r.clearState(ctx, oldPane.ID, om); err != nil {
				return nil, err
			}
		}
	}

	resumeHint, label := classify(source, hasCursor, s.Started, mem.proc.start)
	newDivider := func(resume bool) string {
		if resume {
			return fmt.Sprintf("%s · resumed", rt)
		}
		return fmt.Sprintf("%s · new session (%s)", rt, label)
	}
	var taken int64 // topic that now shows conv

	th, hasTopic, err := r.st.ThreadByConversation(ctx, conv.ID)
	if err != nil {
		return nil, fmt.Errorf("registry: find topic: %w", err)
	}
	if hasTopic {
		busy := false
		if th.CurrentConversationID != conv.ID && th.Status == store.ThreadLive {
			cp, running, err := r.st.PaneByConversation(ctx, th.CurrentConversationID)
			if err != nil {
				return nil, fmt.Errorf("registry: find pane of topic: %w", err)
			}
			busy = running && cp.ID != pane.ID
		}
		if busy {
			out = append(out, Change{Kind: ResumeBusy, PaneID: pane.ID, ConversationID: conv.ID, ThreadID: th.ID})
		} else {
			if th.CurrentConversationID != conv.ID {
				if err := r.st.TakeOver(ctx, th.ID, conv.ID); err != nil {
					return nil, fmt.Errorf("registry: resume: %w", err)
				}
			}
			taken = th.ID
			out = append(out, Change{Kind: Resume, PaneID: pane.ID, ConversationID: conv.ID, ThreadID: th.ID,
				Divider: newDivider(true), Start: startFor(true, hasCursor)})
			if th.Status == store.ThreadOffline {
				if err := r.st.SetThreadStatus(ctx, th.ID, store.ThreadLive); err != nil {
					return nil, fmt.Errorf("registry: topic live: %w", err)
				}
				out = append(out, Change{Kind: TopicLive, PaneID: pane.ID, ConversationID: conv.ID, ThreadID: th.ID})
			}
		}
	} else {
		// A new session. It takes over the topic of the session that ran in
		// this pane just before, when that topic is not live elsewhere.
		prev := prevOcc
		if prev == 0 {
			prev = prevLast
		}
		var tx store.Thread
		takeOver := false
		if prev != 0 && prev != conv.ID {
			t, ok, err := r.st.ThreadByConversation(ctx, prev)
			if err != nil {
				return nil, fmt.Errorf("registry: find topic: %w", err)
			}
			if ok && t.CurrentConversationID == prev {
				pp, running, err := r.st.PaneByConversation(ctx, prev)
				if err != nil {
					return nil, fmt.Errorf("registry: find pane of topic: %w", err)
				}
				tx, takeOver = t, !running || pp.ID == pane.ID
			}
		}
		if takeOver {
			if err := r.st.TakeOver(ctx, tx.ID, conv.ID); err != nil {
				return nil, fmt.Errorf("registry: take over: %w", err)
			}
			taken = tx.ID
			out = append(out, Change{Kind: TakeOver, PaneID: pane.ID, ConversationID: conv.ID, ThreadID: tx.ID,
				Divider: newDivider(resumeHint), Start: startFor(resumeHint, hasCursor)})
			if tx.Status == store.ThreadOffline {
				if err := r.st.SetThreadStatus(ctx, tx.ID, store.ThreadLive); err != nil {
					return nil, fmt.Errorf("registry: topic live: %w", err)
				}
				out = append(out, Change{Kind: TopicLive, PaneID: pane.ID, ConversationID: conv.ID, ThreadID: tx.ID})
			}
		} else {
			out = append(out, Change{Kind: SessionStarted, PaneID: pane.ID, ConversationID: conv.ID,
				Start: startFor(resumeHint, hasCursor)})
		}
	}

	// The session that was replaced is not running any more. Its topic goes
	// offline unless another session took it over.
	if prevOcc != 0 && prevOcc != conv.ID {
		if t, ok, err := r.st.ThreadByConversation(ctx, prevOcc); err != nil {
			return nil, fmt.Errorf("registry: find topic: %w", err)
		} else if ok && t.ID != taken {
			off, err := r.topicOffline(ctx, prevOcc, pane.ID)
			if err != nil {
				return nil, err
			}
			out = append(out, off...)
		}
	}
	return out, nil
}
