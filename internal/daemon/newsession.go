package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"rex-tg/internal/rex"
)

// The Rex session for /new (rex.new_session, "rex-tg") holds the tabs that
// rex-tg starts. An agent in one of its tabs may rename the session, as Rex's
// own hint tells agents to name their session after their task. So rex-tg
// keeps the session's ID once it knows it (kv "new_session_id"), still finds
// the session by that ID when its name changed, and names it back at the
// 30-second name check. It does so only while the session holds an agent
// that rex-tg follows, and at most once in renameBackEvery, so it never
// fights a person who renames it on purpose (set rex.new_session then). Tabs
// of other sessions stay the user's.

const (
	// RexNewSessionKey stores the ID of the Rex session where new agents go.
	RexNewSessionKey = "new_session_id"
	newSessionKey    = RexNewSessionKey
)

// NewAgentTabLabel is the tab label shared by the Telegram and CLI launchers.
func NewAgentTabLabel(dir, agent string) string { return tabLabel(dir, agent) }

// WithNewSessionLock serializes session lookup and creation between the daemon
// and `rex-tg new`, so simultaneous first launches still share one session.
func WithNewSessionLock(ctx context.Context, stateDir string, f func() error) error {
	if f == nil {
		return errors.New("new session: no operation")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("new session: create state folder: %w", err)
	}
	path := filepath.Join(stateDir, "new-session.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("new session: open lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("new session: secure lock: %w", err)
	}
	defer file.Close()

	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("new session: lock: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return err
	}
	return f()
}

// renameBackEvery is the least time between two renames back of the session.
const renameBackEvery = 10 * time.Minute

// ownSession finds the Rex session for /new among sessions: by its name, else
// by the ID rex-tg kept.
func (d *daemon) ownSession(sessions []rex.Session) (rex.Session, bool) {
	want := strings.TrimSpace(d.cfg.Rex.NewSession)
	if want == "" {
		return rex.Session{}, false
	}
	if s, ok := findSession(sessions, want); ok {
		d.keepSessionID(s.ID)
		return s, true
	}
	id, ok, err := d.st.Get(d.ctx, newSessionKey)
	if err != nil || !ok || id == "" {
		return rex.Session{}, false
	}
	for _, s := range sessions {
		if s.ID == id {
			return s, true
		}
	}
	return rex.Session{}, false
}

// keepSessionID stores the ID of the session for /new.
func (d *daemon) keepSessionID(id string) {
	if id == "" {
		return
	}
	if cur, ok, err := d.st.Get(d.ctx, newSessionKey); err == nil && ok && cur == id {
		return
	}
	if err := d.st.Set(d.ctx, newSessionKey, id); err != nil {
		d.log.Warn("new session: keep its id", "err", err)
	}
}

// keepSessionName names the session for /new back when an agent in it
// renamed it (a worker, at the name check).
func (d *daemon) keepSessionName() {
	if id, ok, err := d.st.Get(d.ctx, newSessionKey); err != nil || !ok || id == "" {
		return // not known yet: nothing to name back
	}
	d.tabMu.Lock()
	defer d.tabMu.Unlock()
	// A fresh list, not the cache of syncTab: a list cached now, before
	// /new makes its session, would hide that session from syncTab.
	sessions, err := d.rx.Sessions(d.ctx)
	if err != nil {
		return
	}
	s, ok := d.ownSession(sessions)
	if !ok || strings.EqualFold(strings.TrimSpace(s.Label), strings.TrimSpace(d.cfg.Rex.NewSession)) {
		return
	}
	if !d.holdsAgents(s.ID) || time.Since(d.renamedBack) < renameBackEvery {
		return
	}
	if d.renameSessionBack(s) {
		d.renamedBack = time.Now()
		d.tabSessions = nil // listed again next time
	}
}

// holdsAgents tells whether a Rex session holds a pane with an agent that
// rex-tg follows.
func (d *daemon) holdsAgents(session string) bool {
	panes, err := d.st.Panes(d.ctx)
	if err != nil {
		return false
	}
	for _, p := range panes {
		if p.Session == session && p.ConversationID != 0 && p.GoneAt.IsZero() {
			return true
		}
	}
	return false
}

// renameSessionBack gives the session for /new its name again, and reports
// whether it did.
func (d *daemon) renameSessionBack(s rex.Session) bool {
	want := strings.TrimSpace(d.cfg.Rex.NewSession)
	if err := d.rx.RenameSession(d.ctx, s.ID, want); err != nil {
		d.log.Warn("new session: name it back", "was", s.Label, "err", err)
		return false
	}
	d.log.Info("the Rex session for /new was renamed; named it back", "was", s.Label, "now", want)
	return true
}
