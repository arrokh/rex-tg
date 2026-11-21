package daemon

import (
	"strconv"

	"rex-tg/internal/format"
	"rex-tg/internal/miniapp"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

const templateBase = int64(1) << 60 // separate from scheduled rows and Annotate actions

// templateRowID derives a durable queued-row id from an action id. Repeating
// the same page action therefore finds the existing row instead of typing it
// twice; this range is separate from /at and Annotate rows.
func templateRowID(id string) int64 {
	n, _ := strconv.ParseInt(id[:15], 16, 64) // 60 stable bits from the random action id
	return templateBase + n
}

// templatesCommand is the text-button fallback when Telegram did not open
// the Mini App directly.
func (d *daemon) templatesCommand(chat, thread int64) {
	if !d.miniAppReady() {
		d.say(chat, thread, "🧩 Templates need the Mini App. Set <code>[miniapp] url</code> to turn it on.", nil)
		return
	}
	s := d.kb.snap.Load()
	if s == nil || s.url == "" {
		d.say(chat, thread, "🧩 Templates are not ready yet. Try again in a moment.", nil)
		return
	}
	markup := &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{
		{Text: "🧩 Open Templates", WebApp: &telegram.WebAppInfo{URL: keyboardURL(s.url, s.key, miniapp.ViewTemplates, thread)}},
	}}}
	d.say(chat, thread, "Create and send reusable message templates.", markup)
}

// templateSent queues a silent copy in the topic. The template remains saved
// locally so the owner can use it again.
func (d *daemon) templateSent(thread int64, act, text string) {
	d.enqueue(store.OutboxItem{
		ThreadID: thread, Key: "template:" + act, Kind: "notice", Silent: true,
		Parts: []store.PartContent{{Content: templateSentNote(text), Format: store.FormatHTML}},
	})
}

func templateSentNote(text string) string {
	const head = "🧩 Template sent to the agent\n<blockquote expandable>"
	const tail = "</blockquote>"
	room := 4096 - len(head) - len(tail)
	text = format.Redact(text)
	body := format.Escape(text)
	if len(body) <= room {
		return head + body + tail
	}
	r := []rune(text)
	n := len(r)
	for n > 0 {
		body = format.Escape(string(r[:n])) + "…"
		if len(body) <= room {
			break
		}
		n = n * 9 / 10
	}
	return head + body + tail
}
