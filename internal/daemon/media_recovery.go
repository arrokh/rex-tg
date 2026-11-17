package daemon

import (
	"rex-tg/internal/store"
)

const (
	voicePendingBody     = `{"v":1,"processing":"voice"}`
	voiceInterruptedText = "❌ Voice note processing was interrupted before it was queued. Send the voice note again."
)

// mediaWorkNotice stores the terminal notice and clears the work marker in
// one transaction. A failed write leaves the marker for startup recovery.
func (d *daemon) mediaWorkNotice(th store.Thread, updateID int64, key, text string) bool {
	it := store.OutboxItem{
		ThreadID: th.ID, Key: key, Kind: "notice", Silent: true,
		Parts: []store.PartContent{{Content: text, Format: store.FormatHTML}},
	}
	if _, _, err := d.st.EnqueueWithInboundBody(d.ctx, updateID, "", it); err != nil {
		d.log.Warn("queue media notice", "update", updateID, "err", err)
		return false
	}
	d.markBelow(th.ID)
	d.snd.Wake()
	return true
}
