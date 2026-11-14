package daemon

import (
	"context"
	"strings"

	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// The reply keyboard: four buttons under the message box, the same in every
// topic and in the main chat. A tap sends the button's text as the user's
// message, so each button is a command: a command is never typed into a pane.
// Telegram keeps the keyboard until another one replaces it. Whether a
// client keeps one keyboard per topic or one for the whole chat is not
// settled, so each place that should show it gets it, without the user doing
// anything: after an update that changes the buttons, the main chat and every
// open topic get one short note that carries it; a new topic gets it with its
// first message; and /help brings it back anywhere.
var replyKeyboard = &telegram.ReplyKeyboardMarkup{
	Keyboard: [][]telegram.KeyboardButton{{
		{Text: "/rt"}, {Text: "/screen"}, {Text: "/files"}, {Text: "/changes"},
	}},
	IsPersistent:   true,
	ResizeKeyboard: true,
}

// keyboardVersion names the buttons above. A new value posts the keyboard
// note to the main chat once more, after an update that changes the buttons.
const keyboardVersion = "1"

const keyboardKey = "reply_keyboard"

const keyboardTopicNote = "⌨️ Buttons for /rt, /screen, /files and /changes are now under the message box. " +
	"Tap ⊞ in the message box to show or hide them."

const keyboardNote = "⌨️ Buttons for /rt, /screen, /files and /changes are now under the message box. " +
	"Tap ⊞ in the message box to show or hide them. /help brings them back."

// keyboardTopicKey is the outbox key of a topic's keyboard note. The outbox
// keeps one item per key and topic, so a topic gets the note at most once.
const keyboardTopicKey = "keyboard:" + keyboardVersion

// partKeyboard is the sender's Keyboard hook: a topic's first message, its
// keyboard note, and its /help answer carry the keyboard. None is edited
// later.
func partKeyboard(p store.Part) *telegram.ReplyKeyboardMarkup {
	if p.Kind != "notice" || p.Part != 0 {
		return nil
	}
	if strings.HasPrefix(p.Key, "intro:") || strings.HasPrefix(p.Key, "keyboard:") ||
		(strings.HasPrefix(p.Key, "reply:") && strings.HasSuffix(p.Key, ":help")) {
		return replyKeyboard
	}
	return nil
}

// sayKeys is say with the reply keyboard.
func (d *daemon) sayKeys(chatID, threadID int64, html string) {
	d.spawn(func() { _, _ = d.sendMsgKeys(chatID, threadID, html, nil, replyKeyboard, true) })
}

// showKeyboard posts the keyboard note to the main chat and to every open
// topic the first time this version of the buttons runs, so the buttons show
// everywhere without the user doing anything. Each topic's note goes through
// the outbox under keyboardTopicKey: at most once per topic, also after a
// crash. The main chat's note is marked as posted before it is sent, so it is
// never sent twice; /help shows the buttons again.
func (d *daemon) showKeyboard(ctx context.Context) {
	if d.ownerChat == 0 {
		return
	}
	if v, ok, _ := d.st.Get(ctx, keyboardKey); ok && v == keyboardVersion {
		return
	}
	threads, err := d.st.Threads(ctx)
	if err != nil {
		d.log.Warn("keyboard: list topics", "err", err)
	}
	for _, th := range threads {
		if th.Status == store.ThreadClosed {
			continue
		}
		d.enqueue(store.OutboxItem{
			ThreadID: th.ID, Key: keyboardTopicKey, Kind: "notice", Silent: true,
			Parts: []store.PartContent{{Content: keyboardTopicNote, Format: store.FormatHTML}},
		})
	}
	if err := d.st.Set(ctx, keyboardKey, keyboardVersion); err != nil {
		d.log.Warn("save keyboard version", "err", err)
		return
	}
	d.sayKeys(d.ownerChat, 0, keyboardNote)
}

// keyboardShown records that the owner's chat has the current keyboard, so
// that the next start does not post the note.
func (d *daemon) keyboardShown(ctx context.Context) {
	if err := d.st.Set(ctx, keyboardKey, keyboardVersion); err != nil {
		d.log.Warn("save keyboard version", "err", err)
	}
}

// KeyboardKV returns the key-value pair that marks the keyboard note as
// posted. Tests set it to start without the note.
func KeyboardKV() (key, value string) { return keyboardKey, keyboardVersion }
