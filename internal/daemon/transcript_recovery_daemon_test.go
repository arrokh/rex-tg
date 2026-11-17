package daemon_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"rex-tg/internal/turn"
)

type transcriptLogCapture struct {
	mu sync.Mutex
	b  strings.Builder
}

func (c *transcriptLogCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.Write(p)
}

func (c *transcriptLogCapture) contains(text string) bool {
	return c.count(text) > 0
}

func (c *transcriptLogCapture) count(text string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Count(c.b.String(), text)
}

func (c *transcriptLogCapture) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(c, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func sqliteExec(t *testing.T, path, query string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

func TestBackfillFailureDoesNotAdvanceCursorAndRetries(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	p := e.newPi("pi-backfill-recovery")
	p.User("first question")
	p.Final("first backfill answer")
	p.User("second question")
	p.Final("second backfill answer")
	sqliteExec(t, e.cfg.DBPath(), `CREATE TRIGGER fail_backfill BEFORE INSERT ON outbox
		WHEN NEW.kind='backfill' BEGIN SELECT RAISE(ABORT, 'forced backfill failure'); END`)

	logs := &transcriptLogCapture{}
	opt := e.options()
	opt.Logger = logs.logger()
	e.startWith(opt)
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitFor("the backfill transaction to fail", func() bool { return logs.contains("commit transcript backfill") })
	if cur, ok, err := e.peek().Cursor(context.Background(), p.Path); err != nil || ok {
		t.Fatalf("cursor advanced after a failed backfill transaction: %+v, %v, %v", cur, ok, err)
	}
	if e.hasText(thread, "first backfill answer") || e.hasText(thread, "second backfill answer") {
		t.Fatal("backfill was posted even though its transaction failed")
	}

	// A restart loses the in-memory pending-tail intent. With no cursor,
	// startup must retry the configured backfill rather than skip to the end.
	e.stop()
	failuresBeforeRestart := logs.count("commit transcript backfill")
	e.startWith(opt)
	e.waitFor("the restarted backfill transaction to fail", func() bool {
		return logs.count("commit transcript backfill") > failuresBeforeRestart
	})
	if cur, ok, err := e.peek().Cursor(context.Background(), p.Path); err != nil || ok {
		t.Fatalf("cursor advanced after restarted backfill failure: %+v, %v, %v", cur, ok, err)
	}

	sqliteExec(t, e.cfg.DBPath(), `DROP TRIGGER fail_backfill`)
	e.waitText(thread, "first backfill answer")
	e.waitText(thread, "second backfill answer")
	for _, answer := range []string{"first backfill answer", "second backfill answer"} {
		if n := e.countText(thread, answer); n != 1 {
			t.Errorf("%q posted %d times, want 1", answer, n)
		}
	}
	if cur, ok, err := e.peek().Cursor(context.Background(), p.Path); err != nil || !ok || cur.Offset == 0 {
		t.Fatalf("cursor after backfill retry = %+v, %v, %v", cur, ok, err)
	}
}

func TestFailedBatchKeepsAcknowledgedEchoAfterRestart(t *testing.T) {
	e := newEnv(t)
	e.cfg.Display.MacNotice = true
	e.seedOwner()
	p := e.newPi("pi-echo-batch-recovery")
	p.User("initial prompt")
	p.Final("ready")
	logs := &transcriptLogCapture{}
	opt := e.options()
	opt.Logger = logs.logger()
	e.startWith(opt)
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "ready")

	input := e.say(thread, "this request came from Telegram")
	e.waitFor("the input to be delivered", func() bool { m, _ := e.message(input); return m.Reaction == "👀" })
	sqliteExec(t, e.cfg.DBPath(), `CREATE TRIGGER reject_echo_snapshot BEFORE INSERT ON kv
		WHEN NEW.key LIKE 'turn:%' BEGIN SELECT RAISE(ABORT, 'blocked echo snapshot'); END`)
	p.User("this request came from Telegram")
	e.waitFor("the acknowledgment before the failed transaction", func() bool { m, _ := e.message(input); return m.Reaction == "👍" })
	e.waitFor("the transcript transaction to fail", func() bool { return logs.contains("commit batch; reading the lines again") })
	e.stop()
	sqliteExec(t, e.cfg.DBPath(), `DROP TRIGGER reject_echo_snapshot`)
	e.startWith(opt)
	e.waitFor("the restarted daemon", func() bool { return len(e.tg.Calls("getMe")) >= 2 })
	p.Final("answer remains visible after echo replay")
	e.waitText(thread, "answer remains visible after echo replay")
}

func TestBatchFailureReplaysTrackerOnceAndRestoresSnapshot(t *testing.T) {
	e := newEnv(t)
	e.seedOwner()
	p := e.newPi("pi-batch-recovery")
	p.User("start")
	p.Final("ready")
	logs := &transcriptLogCapture{}
	opt := e.options()
	opt.Logger = logs.logger()
	e.startWith(opt)
	e.hookPi(p, "startup")
	thread := e.openTopic()
	e.waitText(thread, "ready")

	ctx := context.Background()
	e.say(thread, "/verbose on")
	e.waitText(thread, "/verbose is now on.")
	st := e.peek()
	th, ok, err := st.ThreadByTopic(ctx, chatID, thread)
	if err != nil || !ok {
		t.Fatalf("thread lookup: %+v, %v, %v", th, ok, err)
	}
	before, ok, err := st.Cursor(ctx, p.Path)
	if err != nil || !ok {
		t.Fatalf("initial cursor: %+v, %v, %v", before, ok, err)
	}
	insertTrigger := fmt.Sprintf("fail_turn_insert_%d", th.ID)
	updateTrigger := fmt.Sprintf("fail_turn_update_%d", th.ID)
	sqliteExec(t, e.cfg.DBPath(), fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON kv
		WHEN NEW.key='turn:%d' BEGIN SELECT RAISE(ABORT, 'forced snapshot failure'); END`, insertTrigger, th.ID))
	sqliteExec(t, e.cfg.DBPath(), fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE ON kv
		WHEN NEW.key='turn:%d' BEGIN SELECT RAISE(ABORT, 'forced snapshot failure'); END`, updateTrigger, th.ID))

	p.User("run the test suite")
	p.ToolCall("retry-call", "go test ./internal/daemon")
	e.waitFor("the transcript commit to fail", func() bool { return logs.contains("commit batch; reading the lines again") })
	if cur, ok, err := st.Cursor(ctx, p.Path); err != nil || !ok || cur.Offset != before.Offset {
		t.Fatalf("cursor advanced on a failed snapshot transaction: %+v, %v, %v", cur, ok, err)
	}

	sqliteExec(t, e.cfg.DBPath(), "DROP TRIGGER "+insertTrigger)
	sqliteExec(t, e.cfg.DBPath(), "DROP TRIGGER "+updateTrigger)
	key := fmt.Sprintf("turn:%d", th.ID)
	readSnapshot := func() (turn.Snapshot, bool) {
		value, found, err := st.Get(ctx, key)
		if err != nil || !found {
			return turn.Snapshot{}, false
		}
		var snap turn.Snapshot
		if json.Unmarshal([]byte(value), &snap) != nil {
			return turn.Snapshot{}, false
		}
		return snap, true
	}
	var snapshot turn.Snapshot
	e.waitFor("the replayed snapshot with one tool call", func() bool {
		var found bool
		snapshot, found = readSnapshot()
		return found && len(snapshot.Calls) == 1 && snapshot.Calls[0].ID == "retry-call"
	})

	e.stop()
	e.startWith(opt)
	e.waitFor("the daemon restart", func() bool { return len(e.tg.Calls("getMe")) >= 2 })
	p.ToolResult("retry-call")
	p.Final("answer after recovery")
	e.waitText(thread, "answer after recovery")
	snapshot, ok = readSnapshot()
	if !ok || len(snapshot.Calls) != 1 || snapshot.Calls[0].ID != "retry-call" {
		t.Fatalf("restored tracker snapshot = %+v, found %v", snapshot, ok)
	}
}
