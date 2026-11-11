package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"rex-tg/internal/store"
)

// maxIDGap is how many missing outbox ids in a row end the scan for unknown
// parts. The store has no list query, so status probes ids one by one.
const maxIDGap = 1000

func statusCmd(ctx context.Context, args []string, env Env) int {
	fs := newFlags("status", env)
	cfgPath := fs.String("config", "", "config file")
	if code, stop := parseFlags(fs, args); stop {
		return code
	}
	cfg, err := env.loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: config: %v\n", err)
		return 1
	}
	if _, err := os.Stat(cfg.DBPath()); err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: no database at %s (the daemon has not run yet)\n", cfg.DBPath())
		return 1
	}
	st, err := store.OpenReadOnly(cfg.DBPath())
	if err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: open database: %v\n", err)
		return 1
	}
	defer st.Close()

	threads, err := st.Threads(ctx)
	if err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: topics: %v\n", err)
		return 1
	}
	unknownByThread := map[int64]int{}
	unknownTotal := 0
	misses := 0
	for id := int64(1); misses < maxIDGap; id++ {
		row, err := st.OutboxRow(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				misses++
				continue
			}
			fmt.Fprintf(env.Stderr, "rex-tg: outbox: %v\n", err)
			return 1
		}
		misses = 0
		for _, p := range row.Parts {
			if p.State == store.OutboxUnknown {
				unknownByThread[row.ThreadID]++
				unknownTotal++
			}
		}
	}

	out := env.Stdout
	fmt.Fprintf(out, "Topics: %d\n", len(threads))
	alertsTotal := 0
	queuedTotal := 0
	for _, th := range threads {
		runtime := "-"
		if th.CurrentConversationID != 0 {
			if c, err := st.Conversation(ctx, th.CurrentConversationID); err == nil {
				runtime = string(c.Runtime)
			}
		}
		queued, err := st.QueuedInbound(ctx, th.ID)
		if err != nil {
			fmt.Fprintf(env.Stderr, "rex-tg: queue: %v\n", err)
			return 1
		}
		alerts, err := st.OpenAlerts(ctx, th.ID)
		if err != nil {
			fmt.Fprintf(env.Stderr, "rex-tg: alerts: %v\n", err)
			return 1
		}
		queuedTotal += len(queued)
		alertsTotal += len(alerts)
		fmt.Fprintf(out, "  %-8s %-7s %s  queued=%d unknown-parts=%d open-alerts=%d\n",
			th.Status, runtime, th.Name, len(queued), unknownByThread[th.ID], len(alerts))
	}
	fmt.Fprintf(out, "Queued inbound messages: %d\n", queuedTotal)
	fmt.Fprintf(out, "Unknown parts: %d\n", unknownTotal)
	fmt.Fprintf(out, "Open alerts: %d\n", alertsTotal)
	if v, ok, err := st.Get(ctx, "paused"); err == nil && ok && v != "" && v != "0" && v != "false" {
		fmt.Fprintln(out, "Typing is paused (/unpause in Telegram).")
	}
	return 0
}
