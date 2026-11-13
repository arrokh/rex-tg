package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"rex-tg/internal/store"
)

// A queued control command belongs to the agent it was sent to.
func TestControlStale(t *testing.T) {
	d := &daemon{ctlPending: map[int64]ctlSet{5: {conv: 1, name: "model", value: "opus"}}}
	if d.controlStale(store.Thread{CurrentConversationID: 1}, 5) {
		t.Error("stale for the same agent")
	}
	if !d.controlStale(store.Thread{CurrentConversationID: 2}, 5) {
		t.Error("not stale after another agent took the pane")
	}
	if d.controlStale(store.Thread{CurrentConversationID: 2}, 6) {
		t.Error("a message that is no control command counts as stale")
	}
}

// Housekeeping drops old card claims (also those without a time, from
// before) and the values of conversations that are gone.
func TestPruneControlRecords(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now()
	d := &daemon{st: st, ctx: ctx, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for k, v := range map[string]string{
		"cf_card:1:2": "1",
		"cf_card:1:3": fmt.Sprint(now.Add(-time.Hour).Unix()),
		"cf_card:1:4": fmt.Sprint(now.Add(-8 * 24 * time.Hour).Unix()),
		"ctl:999":     `{"model":"opus"}`,
	} {
		if err := st.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	d.pruneControlRecords(now)
	cards, _ := st.KeysWithPrefix(ctx, "cf_card:")
	ctls, _ := st.KeysWithPrefix(ctx, "ctl:")
	if len(cards) != 1 || cards[0] != "cf_card:1:3" || len(ctls) != 0 {
		t.Errorf("left: %v %v, want only cf_card:1:3", cards, ctls)
	}
}
