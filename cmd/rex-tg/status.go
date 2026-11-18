package main

import (
	"context"
	"fmt"
	"os"

	"rex-tg/internal/store"
)

func statusCmd(ctx context.Context, args []string, env Env) int {
	fs := newFlags("status", env)
	cfgPath := fs.String("config", "", "config file")
	all := fs.Bool("all", false, "include closed topics")
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
	unknownParts, err := st.UnknownParts(ctx)
	if err != nil {
		fmt.Fprintf(env.Stderr, "rex-tg: outbox: %v\n", err)
		return 1
	}
	unknownByThread := map[int64]int{}
	unknownTotal := len(unknownParts)
	for _, p := range unknownParts {
		unknownByThread[p.ThreadID]++
	}

	out := env.Stdout
	counts := map[string]int{}
	for _, th := range threads {
		counts[th.Status]++
	}
	fmt.Fprintf(out, "Topics: %d live, %d offline, %d closed\n", counts[store.ThreadLive], counts[store.ThreadOffline], counts[store.ThreadClosed])
	alertsTotal := 0
	queuedTotal := 0
	for _, th := range threads {
		if th.Status == store.ThreadClosed && !*all {
			continue
		}
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
	if counts[store.ThreadClosed] > 0 && !*all {
		fmt.Fprintf(out, "Closed topic details omitted (%d; use --all to show).\n", counts[store.ThreadClosed])
	}
	fmt.Fprintf(out, "Queued inbound messages: %d\n", queuedTotal)
	fmt.Fprintf(out, "Unknown parts: %d\n", unknownTotal)
	fmt.Fprintf(out, "Open alerts: %d\n", alertsTotal)
	if v, ok, err := st.Get(ctx, "paused"); err == nil && ok && v != "" && v != "0" && v != "false" {
		fmt.Fprintln(out, "Typing is paused (/unpause in Telegram).")
	}
	return 0
}
