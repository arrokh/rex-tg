package daemon

import (
	"context"
	"encoding/json"
	"time"

	"rex-tg/internal/agents"
)

const (
	unknownKey           = "unknown_line_types"
	unknownFlushInterval = time.Minute
)

// unknownCounts counts transcript lines of types the adapters do not know,
// per runtime and type. Only the core goroutine touches it. The counts are
// merged into the KV value `unknown_line_types` for `rex-tg doctor`.
type unknownCounts struct {
	delta     map[agents.Runtime]map[string]int
	lastFlush time.Time
}

func (u *unknownCounts) add(rt agents.Runtime, typ string, now time.Time) {
	if u.delta == nil {
		u.delta = map[agents.Runtime]map[string]int{}
	}
	if u.delta[rt] == nil {
		u.delta[rt] = map[string]int{}
	}
	if u.lastFlush.IsZero() {
		u.lastFlush = now
	}
	u.delta[rt][typ]++
}

func (d *daemon) maybeFlushUnknown(now time.Time) {
	if len(d.unknown.delta) > 0 && now.Sub(d.unknown.lastFlush) >= unknownFlushInterval {
		d.flushUnknown(d.ctx)
	}
}

// flushUnknown merges the new counts into the stored value.
func (d *daemon) flushUnknown(ctx context.Context) {
	u := &d.unknown
	if len(u.delta) == 0 {
		return
	}
	stored := d.knownPruned(ctx)
	for rt, m := range u.delta {
		if stored[string(rt)] == nil {
			stored[string(rt)] = map[string]int{}
		}
		for typ, n := range m {
			stored[string(rt)][typ] += n
		}
	}
	b, err := json.Marshal(stored)
	if err != nil {
		return
	}
	if err := d.st.Set(ctx, unknownKey, string(b)); err != nil {
		d.log.Warn("save unknown line types", "err", err)
		return
	}
	u.delta = nil
	u.lastFlush = d.now()
}

// knownPruned reads the stored counts without the types that the adapters
// know now (after an update taught them), so doctor stops reporting them.
func (d *daemon) knownPruned(ctx context.Context) map[string]map[string]int {
	stored := map[string]map[string]int{}
	if v, ok, err := d.st.Get(ctx, unknownKey); err == nil && ok {
		_ = json.Unmarshal([]byte(v), &stored)
	}
	for rt, m := range stored {
		a, ok := d.adapters[agents.Runtime(rt)]
		if !ok {
			continue
		}
		for typ := range m {
			if agents.KnowsType(a, typ) {
				delete(m, typ)
			}
		}
		if len(m) == 0 {
			delete(stored, rt)
		}
	}
	return stored
}

// pruneKnownTypes drops the counted types that the adapters know now (at
// the start).
func (d *daemon) pruneKnownTypes(ctx context.Context) {
	v, ok, err := d.st.Get(ctx, unknownKey)
	if err != nil || !ok {
		return
	}
	stored := d.knownPruned(ctx)
	b, err := json.Marshal(stored)
	if err != nil || string(b) == v {
		return
	}
	if err := d.st.Set(ctx, unknownKey, string(b)); err != nil {
		d.log.Warn("save unknown line types", "err", err)
	}
}
