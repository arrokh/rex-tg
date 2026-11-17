package turn

// Clone returns an independent tracker with the same configuration and state.
// It lets callers stage transcript events and publish the tracker only after
// the durable commit succeeds.
func (t *Tracker) Clone() *Tracker {
	clone := *t
	if t.calls != nil {
		clone.calls = make([]call, len(t.calls))
		copy(clone.calls, t.calls)
	}
	return &clone
}
