package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"rex-tg/internal/agents"
)

// ---- Conversations ----

const convCols = `id, runtime, native_id, transcript, cwd, title, thread_id, first_seen_at`

func scanConv(r scanner) (Conversation, error) {
	var c Conversation
	var rt string
	var cwd, title sql.NullString
	var th, first sql.NullInt64
	if err := r.Scan(&c.ID, &rt, &c.NativeID, &c.Transcript, &cwd, &title, &th, &first); err != nil {
		return Conversation{}, err
	}
	c.Runtime = agents.Runtime(rt)
	c.CWD, c.Title, c.ThreadID = cwd.String, title.String, th.Int64
	c.FirstSeenAt = fromMs(first)
	return c, nil
}

func getConv(ctx context.Context, q querier, id int64) (Conversation, error) {
	c, err := scanConv(q.QueryRowContext(ctx, `SELECT `+convCols+` FROM conversation WHERE id=?`, id))
	if err != nil {
		return Conversation{}, notFound(err, fmt.Sprintf("store: conversation %d", id))
	}
	return c, nil
}

// UpsertConversation inserts a conversation or updates the one with the same
// (runtime, native_id). Empty CWD and Title keep the stored values; ThreadID
// and FirstSeenAt of an existing row do not change.
func (s *Store) UpsertConversation(ctx context.Context, c Conversation) (Conversation, error) {
	if !c.Runtime.Valid() {
		return Conversation{}, fmt.Errorf("store: invalid runtime %q", c.Runtime)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO conversation(runtime,native_id,transcript,cwd,title,first_seen_at)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(runtime,native_id) DO UPDATE SET
		  transcript=excluded.transcript,
		  cwd=COALESCE(excluded.cwd,cwd),
		  title=COALESCE(excluded.title,title)`,
		string(c.Runtime), c.NativeID, c.Transcript, nullStr(c.CWD), nullStr(c.Title), toMsNN(c.FirstSeenAt))
	if err != nil {
		return Conversation{}, fmt.Errorf("store: upsert conversation: %w", err)
	}
	got, ok, err := s.ConversationByNative(ctx, c.Runtime, c.NativeID)
	if err != nil {
		return Conversation{}, err
	}
	if !ok {
		return Conversation{}, fmt.Errorf("store: upsert conversation: %w", ErrNotFound)
	}
	return got, nil
}

// Conversation returns a conversation by ID.
func (s *Store) Conversation(ctx context.Context, id int64) (Conversation, error) {
	return getConv(ctx, s.db, id)
}

// ConversationByNative finds a conversation by runtime and the agent's session ID.
func (s *Store) ConversationByNative(ctx context.Context, rt agents.Runtime, nativeID string) (Conversation, bool, error) {
	c, err := scanConv(s.db.QueryRowContext(ctx, `SELECT `+convCols+` FROM conversation WHERE runtime=? AND native_id=?`, string(rt), nativeID))
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, false, nil
	}
	if err != nil {
		return Conversation{}, false, fmt.Errorf("store: conversation by native id: %w", err)
	}
	return c, true, nil
}

// RecentFolders returns the folders that agent sessions ran in, the most
// recently used first, at most limit of them. A folder counts as used when a
// session in it started, or when a pane that runs (or ran) one of its
// sessions was last seen.
func (s *Store) RecentFolders(ctx context.Context, limit int) ([]string, error) {
	uses, err := s.RecentFolderUses(ctx, limit)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, u := range uses {
		out = append(out, u.Path)
	}
	return out, nil
}

// FolderUse is a folder, when it was last used (see RecentFolders), and
// when a session last started in it.
type FolderUse struct {
	Path    string
	Used    time.Time // the newest of its sessions' first sight and its panes' last sight
	Started time.Time // its newest session's first sight
}

// RecentFolderUses is RecentFolders with the time each folder was last used.
func (s *Store) RecentFolderUses(ctx context.Context, limit int) ([]FolderUse, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.cwd, MAX(MAX(c.first_seen_at, COALESCE(p.seen_at, 0))) AS used,
		MAX(c.first_seen_at) AS started
		FROM conversation c
		LEFT JOIN pane p ON p.conversation_id = c.id OR p.last_conversation_id = c.id
		WHERE c.cwd IS NOT NULL AND c.cwd <> ''
		GROUP BY c.cwd ORDER BY used DESC, c.cwd LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: recent folders: %w", err)
	}
	defer rows.Close()
	var out []FolderUse
	for rows.Next() {
		var cwd string
		var used, started int64
		if err := rows.Scan(&cwd, &used, &started); err != nil {
			return nil, fmt.Errorf("store: recent folders: %w", err)
		}
		out = append(out, FolderUse{Path: cwd, Used: time.UnixMilli(used).UTC(), Started: time.UnixMilli(started).UTC()})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: recent folders: %w", err)
	}
	return out, nil
}

// SetConversationTitle sets a conversation's title.
func (s *Store) SetConversationTitle(ctx context.Context, id int64, title string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE conversation SET title=? WHERE id=?`, nullStr(title), id)
	if err != nil {
		return fmt.Errorf("store: set title: %w", err)
	}
	return affected(res, fmt.Sprintf("store: conversation %d", id))
}

// ---- Panes ----

const paneCols = `id, rex_host, rex_block, rex_session, label, conversation_id, last_conversation_id,
	pid, process_name, source, state, state_kind, state_at, busy_until, seen_at, gone_at`

func scanPane(r scanner) (Pane, error) {
	var p Pane
	var label, pname, source, state, kind sql.NullString
	var conv, last, pid, stateAt, busy, seen, gone sql.NullInt64
	err := r.Scan(&p.ID, &p.Host, &p.Block, &p.Session, &label, &conv, &last,
		&pid, &pname, &source, &state, &kind, &stateAt, &busy, &seen, &gone)
	if err != nil {
		return Pane{}, err
	}
	p.Label, p.ProcessName, p.Source, p.State, p.StateKind = label.String, pname.String, source.String, state.String, kind.String
	p.ConversationID, p.LastConversationID, p.PID = conv.Int64, last.Int64, int(pid.Int64)
	p.StateAt, p.BusyUntil, p.SeenAt, p.GoneAt = fromMs(stateAt), fromMs(busy), fromMs(seen), fromMs(gone)
	return p, nil
}

func getPane(ctx context.Context, q querier, id int64) (Pane, error) {
	p, err := scanPane(q.QueryRowContext(ctx, `SELECT `+paneCols+` FROM pane WHERE id=?`, id))
	if err != nil {
		return Pane{}, notFound(err, fmt.Sprintf("store: pane %d", id))
	}
	return p, nil
}

// UpsertPane records that a pane exists. An existing pane is revived (gone_at
// cleared). An empty label keeps the stored label.
func (s *Store) UpsertPane(ctx context.Context, host, block, session, label string, now time.Time) (Pane, error) {
	_, err := s.db.ExecContext(ctx, `INSERT INTO pane(rex_host,rex_block,rex_session,label,seen_at) VALUES(?,?,?,?,?)
		ON CONFLICT(rex_host,rex_block) DO UPDATE SET
		  rex_session=excluded.rex_session,
		  label=COALESCE(excluded.label,label),
		  seen_at=excluded.seen_at,
		  gone_at=NULL`, host, block, session, nullStr(label), toMsNN(now))
	if err != nil {
		return Pane{}, fmt.Errorf("store: upsert pane: %w", err)
	}
	p, ok, err := s.PaneByBlock(ctx, host, block)
	if err != nil {
		return Pane{}, err
	}
	if !ok {
		return Pane{}, fmt.Errorf("store: upsert pane: %w", ErrNotFound)
	}
	return p, nil
}

// SetPaneOccupant sets the session that runs in a pane (convID 0 = none).
// A previous, different occupant becomes last_conversation_id. The session is
// removed from any other pane in the same transaction, because a session runs
// in one pane only; that pane keeps it as its last conversation.
func (s *Store) SetPaneOccupant(ctx context.Context, paneID, convID int64, pid int, processName, source string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := getPane(ctx, tx, paneID); err != nil {
			return err
		}
		if convID != 0 {
			_, err := tx.ExecContext(ctx, `UPDATE pane SET last_conversation_id=conversation_id, conversation_id=NULL,
				pid=NULL, process_name=NULL, source=NULL WHERE conversation_id=? AND id<>?`, convID, paneID)
			if err != nil {
				return fmt.Errorf("store: clear other pane: %w", err)
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE pane SET
			last_conversation_id = CASE WHEN conversation_id IS NOT NULL AND conversation_id IS NOT ?1
			                            THEN conversation_id ELSE last_conversation_id END,
			conversation_id=?1, pid=?2, process_name=?3, source=?4 WHERE id=?5`,
			nullInt(convID), nullInt(int64(pid)), nullStr(processName), nullStr(source), paneID)
		if err != nil {
			return fmt.Errorf("store: set pane occupant: %w", err)
		}
		return nil
	})
}

// SetPaneState records the pane's agent state ("" clears it).
func (s *Store) SetPaneState(ctx context.Context, paneID int64, state, kind string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE pane SET state=?, state_kind=?, state_at=? WHERE id=?`,
		nullStr(state), nullStr(kind), toMs(at), paneID)
	if err != nil {
		return fmt.Errorf("store: set pane state: %w", err)
	}
	return affected(res, fmt.Sprintf("store: pane %d", paneID))
}

// SetPaneBusyUntil sets (or with the zero time clears) the busy deadline.
func (s *Store) SetPaneBusyUntil(ctx context.Context, paneID int64, until time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE pane SET busy_until=? WHERE id=?`, toMs(until), paneID)
	if err != nil {
		return fmt.Errorf("store: set pane busy: %w", err)
	}
	return affected(res, fmt.Sprintf("store: pane %d", paneID))
}

// MarkPaneGone marks a pane closed and moves its occupant to last_conversation_id.
func (s *Store) MarkPaneGone(ctx context.Context, paneID int64, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE pane SET
		last_conversation_id=COALESCE(conversation_id,last_conversation_id),
		conversation_id=NULL, pid=NULL, process_name=NULL, source=NULL, gone_at=? WHERE id=?`,
		toMsNN(at), paneID)
	if err != nil {
		return fmt.Errorf("store: mark pane gone: %w", err)
	}
	return affected(res, fmt.Sprintf("store: pane %d", paneID))
}

// Pane returns a pane by ID.
func (s *Store) Pane(ctx context.Context, id int64) (Pane, error) { return getPane(ctx, s.db, id) }

// PaneByBlock finds a pane by Rex host and block.
func (s *Store) PaneByBlock(ctx context.Context, host, block string) (Pane, bool, error) {
	return s.onePane(ctx, `WHERE rex_host=? AND rex_block=?`, host, block)
}

// PaneByConversation finds the pane where a session runs now.
func (s *Store) PaneByConversation(ctx context.Context, convID int64) (Pane, bool, error) {
	return s.onePane(ctx, `WHERE conversation_id=?`, convID)
}

func (s *Store) onePane(ctx context.Context, where string, args ...any) (Pane, bool, error) {
	p, err := scanPane(s.db.QueryRowContext(ctx, `SELECT `+paneCols+` FROM pane `+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Pane{}, false, nil
	}
	if err != nil {
		return Pane{}, false, fmt.Errorf("store: pane lookup: %w", err)
	}
	return p, true, nil
}

// Panes lists the panes that are not gone, by ID.
func (s *Store) Panes(ctx context.Context) ([]Pane, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+paneCols+` FROM pane WHERE gone_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list panes: %w", err)
	}
	defer rows.Close()
	var out []Pane
	for rows.Next() {
		p, err := scanPane(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list panes: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- Topics ----

const threadCols = `id, chat_id, message_thread_id, current_conversation_id, name, mode, verbose,
	reasoning, muted, status, created_at, closed_at`

func scanThread(r scanner) (Thread, error) {
	var t Thread
	var reasoning, muted int
	var created, closed sql.NullInt64
	err := r.Scan(&t.ID, &t.ChatID, &t.MessageThreadID, &t.CurrentConversationID, &t.Name, &t.Mode, &t.Verbose,
		&reasoning, &muted, &t.Status, &created, &closed)
	if err != nil {
		return Thread{}, err
	}
	t.Reasoning, t.Muted = reasoning != 0, muted != 0
	t.CreatedAt, t.ClosedAt = fromMs(created), fromMs(closed)
	return t, nil
}

func getThread(ctx context.Context, q querier, id int64) (Thread, error) {
	t, err := scanThread(q.QueryRowContext(ctx, `SELECT `+threadCols+` FROM thread WHERE id=?`, id))
	if err != nil {
		return Thread{}, notFound(err, fmt.Sprintf("store: thread %d", id))
	}
	return t, nil
}

// LinkTopic creates a topic for a conversation. It refuses a conversation that
// already has a topic that is not closed. In one transaction it writes the
// thread row, then conversation.thread_id.
func (s *Store) LinkTopic(ctx context.Context, chatID, messageThreadID, convID int64, name string, now time.Time) (Thread, error) {
	var out Thread
	err := s.tx(ctx, func(tx *sql.Tx) error {
		c, err := getConv(ctx, tx, convID)
		if err != nil {
			return err
		}
		if c.ThreadID != 0 {
			old, err := getThread(ctx, tx, c.ThreadID)
			if err != nil {
				return err
			}
			if old.Status != ThreadClosed {
				return fmt.Errorf("store: conversation %d belongs to topic %d: %w", convID, old.ID, ErrTopicTaken)
			}
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO thread(chat_id,message_thread_id,current_conversation_id,name,created_at)
			VALUES(?,?,?,?,?)`, chatID, messageThreadID, convID, name, toMsNN(now))
		if err != nil {
			return fmt.Errorf("store: insert thread: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE conversation SET thread_id=? WHERE id=?`, id, convID); err != nil {
			return fmt.Errorf("store: link conversation: %w", err)
		}
		out, err = getThread(ctx, tx, id)
		return err
	})
	if err != nil {
		return Thread{}, err
	}
	return out, nil
}

// TakeOver makes a conversation the current one of a topic (take-over and
// resume). The old current conversation keeps its thread_id as history. It
// refuses a closed topic, and a conversation that has another open topic.
func (s *Store) TakeOver(ctx context.Context, threadID, convID int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		t, err := getThread(ctx, tx, threadID)
		if err != nil {
			return err
		}
		if t.Status == ThreadClosed {
			return fmt.Errorf("store: thread %d is closed", threadID)
		}
		c, err := getConv(ctx, tx, convID)
		if err != nil {
			return err
		}
		if c.ThreadID != 0 && c.ThreadID != threadID {
			other, err := getThread(ctx, tx, c.ThreadID)
			if err != nil {
				return err
			}
			if other.Status != ThreadClosed {
				return fmt.Errorf("store: conversation %d belongs to topic %d: %w", convID, other.ID, ErrTopicTaken)
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE thread SET current_conversation_id=? WHERE id=?`, convID, threadID); err != nil {
			return fmt.Errorf("store: take over: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE conversation SET thread_id=? WHERE id=?`, threadID, convID); err != nil {
			return fmt.Errorf("store: take over: %w", err)
		}
		return nil
	})
}

// Thread returns a topic by ID.
func (s *Store) Thread(ctx context.Context, id int64) (Thread, error) {
	return getThread(ctx, s.db, id)
}

// ThreadByTopic finds a topic by its Telegram IDs.
func (s *Store) ThreadByTopic(ctx context.Context, chatID, messageThreadID int64) (Thread, bool, error) {
	return s.oneThread(ctx, `SELECT `+threadCols+` FROM thread WHERE chat_id=? AND message_thread_id=?`, chatID, messageThreadID)
}

// ThreadByConversation returns the conversation's topic when it is not closed.
func (s *Store) ThreadByConversation(ctx context.Context, convID int64) (Thread, bool, error) {
	return s.oneThread(ctx, `SELECT `+threadCols+` FROM thread WHERE status<>'closed' AND id=
		(SELECT thread_id FROM conversation WHERE id=?)`, convID)
}

func (s *Store) oneThread(ctx context.Context, q string, args ...any) (Thread, bool, error) {
	t, err := scanThread(s.db.QueryRowContext(ctx, q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Thread{}, false, nil
	}
	if err != nil {
		return Thread{}, false, fmt.Errorf("store: thread lookup: %w", err)
	}
	return t, true, nil
}

// Threads lists topics with any of the given statuses (all when none), by ID.
func (s *Store) Threads(ctx context.Context, statuses ...string) ([]Thread, error) {
	q := `SELECT ` + threadCols + ` FROM thread`
	var args []any
	if len(statuses) > 0 {
		q += ` WHERE status IN (?` + strings.Repeat(`,?`, len(statuses)-1) + `)`
		for _, st := range statuses {
			args = append(args, st)
		}
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list threads: %w", err)
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list threads: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetThreadStatus changes a topic's status. Closing records the close time.
func (s *Store) SetThreadStatus(ctx context.Context, id int64, status string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE thread SET status=?1,
		closed_at = CASE WHEN ?1='closed' THEN COALESCE(closed_at, ?2) ELSE NULL END WHERE id=?3`,
		status, s.now().UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("store: set thread status: %w", err)
	}
	return affected(res, fmt.Sprintf("store: thread %d", id))
}

// SetThreadName changes a topic's name.
func (s *Store) SetThreadName(ctx context.Context, id int64, name string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE thread SET name=? WHERE id=?`, name, id)
	if err != nil {
		return fmt.Errorf("store: set thread name: %w", err)
	}
	return affected(res, fmt.Sprintf("store: thread %d", id))
}

// SetThreadSettings changes a topic's display settings.
func (s *Store) SetThreadSettings(ctx context.Context, id int64, mode, verbose string, reasoning, muted bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE thread SET mode=?, verbose=?, reasoning=?, muted=? WHERE id=?`,
		mode, verbose, b2i(reasoning), b2i(muted), id)
	if err != nil {
		return fmt.Errorf("store: set thread settings: %w", err)
	}
	return affected(res, fmt.Sprintf("store: thread %d", id))
}

// CheckInvariants reports any topic, not closed, whose current conversation
// does not belong to it.
func (s *Store) CheckInvariants(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT t.id, t.current_conversation_id, c.thread_id
		FROM thread t JOIN conversation c ON c.id=t.current_conversation_id
		WHERE t.status<>'closed' AND c.thread_id IS NOT t.id ORDER BY t.id`)
	if err != nil {
		return fmt.Errorf("store: check invariants: %w", err)
	}
	defer rows.Close()
	var bad []string
	for rows.Next() {
		var tid, cid int64
		var cth sql.NullInt64
		if err := rows.Scan(&tid, &cid, &cth); err != nil {
			return fmt.Errorf("store: check invariants: %w", err)
		}
		bad = append(bad, fmt.Sprintf("thread %d: current conversation %d has thread_id %v", tid, cid, nullable(cth)))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(bad) > 0 {
		return fmt.Errorf("store: invariant broken: %s", strings.Join(bad, "; "))
	}
	return nil
}

func nullable(n sql.NullInt64) any {
	if !n.Valid {
		return "NULL"
	}
	return n.Int64
}
