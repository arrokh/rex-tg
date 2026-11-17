package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rex-tg/internal/inbound"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

const (
	fileRetryKind     = "file-retry"
	fileRetryCallback = "fr"
	fileRetryMaxAge   = 30 * 24 * time.Hour
)

// fileRetry is kept in the original inbound row before a download starts,
// until its text row is durable. The file ID lets a later button tap repeat
// the safe, read-only download after a failure or interrupted worker.
type fileRetry struct {
	Version int    `json:"v"`
	FileID  string `json:"id"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Caption string `json:"caption,omitempty"`
	Quote   string `json:"quote,omitempty"`
}

// fileRetryMarkup adds a button to a failed media download. The callback only
// carries the original update ID; the file ID and text stay in the store.
func fileRetryMarkup(p store.Part) *telegram.InlineKeyboardMarkup {
	if p.Kind != fileRetryKind {
		return nil
	}
	parts := strings.Split(p.Key, ":")
	if len(parts) != 3 || parts[0] != fileRetryKind {
		return nil
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{
		{Text: "↻ Retry download", CallbackData: fmt.Sprintf("%s:%d", fileRetryCallback, id)},
	}}}
}

func encodeFileRetry(f fileRetry) (string, error) {
	f.Version = 1
	body, err := json.Marshal(f)
	if err != nil {
		return "", fmt.Errorf("encode retry: %w", err)
	}
	return string(body), nil
}

func decodeFileRetry(body string) (fileRetry, bool) {
	var f fileRetry
	if body == "" || json.Unmarshal([]byte(body), &f) != nil {
		return fileRetry{}, false
	}
	if f.Version != 1 || f.FileID == "" || f.Name == "" || f.Size < 0 {
		return fileRetry{}, false
	}
	f.Name = safeName(f.Name, "file")
	return f, true
}

func filePrompt(path string, f fileRetry) string {
	text := path
	if f.Caption != "" {
		text = f.Caption + "\n" + path
	}
	return inbound.WithReply(text, agentQuote(f.Caption, f.Quote))
}

// fileRetryNotice describes one failed-download notice. attempt is zero
// for the first failure, then the callback update ID for each later failure.
func fileRetryNotice(th store.Thread, updateID, attempt int64) store.OutboxItem {
	return store.OutboxItem{
		ThreadID: th.ID,
		Key:      fmt.Sprintf("%s:%d:%d", fileRetryKind, updateID, attempt),
		Kind:     fileRetryKind,
		Silent:   true,
		Parts:    []store.PartContent{{Content: fileFailText, Format: store.FormatHTML}},
	}
}

func (d *daemon) enqueueFileRetryNotice(th store.Thread, updateID, attempt int64) bool {
	if _, _, err := d.st.Enqueue(d.ctx, fileRetryNotice(th, updateID, attempt)); err != nil {
		d.log.Warn("enqueue file retry", "thread", th.ID, "err", err)
		return false
	}
	d.markBelow(th.ID)
	d.snd.Wake()
	return true
}

func (d *daemon) clearFileRetry(updateID int64) {
	if err := d.st.SetInboundBody(d.ctx, updateID, ""); err != nil && d.ctx.Err() == nil {
		d.log.Warn("clear file retry", "update", updateID, "err", err)
	}
}

func (d *daemon) fileDownloadFailed(th store.Thread, updateID int64, f fileRetry, err error) {
	if errors.Is(err, telegram.ErrTooLarge) {
		d.mediaWorkNotice(th, updateID, fmt.Sprintf("reply:file-too-large:%d", updateID),
			fmt.Sprintf("❌ The file is too large. The limit is %d MB.", d.maxUpload()>>20))
		return
	}
	if f.FileID == "" {
		d.replyTopic(th, fmt.Sprintf("file:%d", updateID), fileFailText)
		return
	}
	body, err := encodeFileRetry(f)
	if err != nil {
		d.log.Warn("encode file retry", "thread", th.ID, "err", err)
		d.replyTopic(th, fmt.Sprintf("file:%d", updateID), fileFailText)
		return
	}
	if _, _, err := d.st.EnqueueWithInboundBody(d.ctx, updateID, body, fileRetryNotice(th, updateID, 0)); err != nil {
		d.log.Warn("enqueue file retry", "thread", th.ID, "err", err)
		d.replyTopic(th, fmt.Sprintf("file:%d", updateID), fileFailText)
		return
	}
	d.markBelow(th.ID)
	d.snd.Wake()
}

// recoverMediaWork restores a retry button for interrupted file workers and
// warns about interrupted transcription. It never automatically retypes;
// an existing negative text row proves that the update was already queued.
func (d *daemon) recoverMediaWork(ctx context.Context) error {
	rows, err := d.st.MediaWork(ctx)
	if err != nil {
		return fmt.Errorf("recover file downloads: %w", err)
	}
	for _, in := range rows {
		if in.Kind == "file" {
			if _, valid := decodeFileRetry(in.Body); !valid {
				continue
			}
		} else if in.Body != voicePendingBody {
			continue
		}
		_, queued, err := d.st.InboundByID(ctx, -in.UpdateID)
		if err != nil {
			return fmt.Errorf("recover file download: %w", err)
		}
		th, err := d.st.Thread(ctx, in.ThreadID)
		if err != nil {
			return fmt.Errorf("recover file topic: %w", err)
		}
		if queued || th.Status == store.ThreadClosed || fileRetryExpiry(d.now(), in.CreatedAt) {
			d.clearFileRetry(in.UpdateID)
			continue
		}
		if in.Kind == "voice" {
			if !d.mediaWorkNotice(th, in.UpdateID, fmt.Sprintf("voice-interrupted:%d", in.UpdateID), voiceInterruptedText) {
				return errors.New("recover voice note: could not queue interruption notice")
			}
			continue
		}
		if !d.enqueueFileRetryNotice(th, in.UpdateID, 0) {
			return errors.New("recover file download: could not queue retry notice")
		}
	}
	return nil
}

// retryFileButton handles a retry button on a failed media notice. It runs on
// the core loop; one active download per source update prevents duplicate taps.
func (d *daemon) retryFileButton(callbackUpdateID int64, cb *telegram.CallbackQuery, updateID int64) {
	if cb.Message == nil {
		d.answerCallback(cb.ID, "This retry is no longer available.", false)
		return
	}
	th, ok := d.threadOfMessage(cb.Message)
	if !ok {
		d.answerCallback(cb.ID, "This topic is closed.", false)
		d.removeButtons(cb.Message.Chat.ID, cb.Message.MessageID)
		return
	}
	if d.mediaRetryBusy[updateID] {
		d.answerCallback(cb.ID, "A download is already in progress.", false)
		return
	}
	in, found, err := d.st.InboundByID(d.ctx, updateID)
	if err != nil {
		d.log.Warn("read file retry", "update", updateID, "err", err)
		d.answerCallback(cb.ID, "Please try again in a moment.", false)
		return
	}
	f, valid := decodeFileRetry(in.Body)
	if !found || !valid || in.Kind != "file" || in.State != store.InboundDone || in.ThreadID != th.ID ||
		in.ChatID != cb.Message.Chat.ID || in.MessageThreadID != cb.Message.MessageThreadID ||
		in.FromID != d.ownerUser || in.TGMessageID == 0 || fileRetryExpiry(d.now(), in.CreatedAt) {
		d.answerCallback(cb.ID, "This retry is no longer available.", false)
		d.removeButtons(cb.Message.Chat.ID, cb.Message.MessageID)
		return
	}
	if f.Size > d.maxUpload() {
		d.answerCallback(cb.ID, fmt.Sprintf("The file exceeds the current %d MB limit.", d.maxUpload()>>20), true)
		return
	}
	_, alreadyQueued, err := d.st.InboundByID(d.ctx, -updateID)
	if err != nil {
		d.log.Warn("check file retry", "update", updateID, "err", err)
		d.answerCallback(cb.ID, "Please try again in a moment.", false)
		return
	}
	if alreadyQueued {
		d.clearFileRetry(updateID)
		d.answerCallback(cb.ID, "This file was already sent.", false)
		d.removeButtons(cb.Message.Chat.ID, cb.Message.MessageID)
		return
	}
	if !d.tryMediaSlot() {
		d.answerCallback(cb.ID, "The Mac is busy processing uploads. Try again shortly.", false)
		return
	}
	d.mediaRetryBusy[updateID] = true
	d.answerCallback(cb.ID, "Retrying download…", false)
	chatID, retryMessageID := cb.Message.Chat.ID, cb.Message.MessageID
	d.spawn(func() {
		defer d.releaseMediaSlot()
		dir := filepath.Join(d.cfg.UploadDir(), fmt.Sprint(th.ID))
		path, err := d.download(f.FileID, dir, fmt.Sprintf("%d-%s", in.TGMessageID, f.Name))
		d.emit(evFileRetryDone{
			updateID: updateID, callbackUpdateID: callbackUpdateID, thread: th,
			chatID: chatID, messageID: retryMessageID, tgMessageID: in.TGMessageID,
			file: f, path: path, err: err,
		})
	})
}

// fileRetryDone finishes a worker download on the core loop. A failed retry
// gets a fresh notice before the old button goes away. A successful retry
// queues the original update's text row, whose negative ID prevents a second
// tap from typing the same file twice.
func (d *daemon) fileRetryDone(e evFileRetryDone) {
	delete(d.mediaRetryBusy, e.updateID)
	if d.ctx.Err() != nil {
		return
	}
	if e.err != nil {
		d.log.Warn("retry download file", "thread", e.thread.ID, "err", e.err)
		if errors.Is(e.err, telegram.ErrTooLarge) {
			if d.mediaWorkNotice(e.thread, e.updateID, fmt.Sprintf("reply:file-too-large:%d:%d", e.updateID, e.callbackUpdateID),
				fmt.Sprintf("❌ The file is too large. The limit is %d MB.", d.maxUpload()>>20)) {
				d.removeButtons(e.chatID, e.messageID)
			}
			return
		}
		if d.enqueueFileRetryNotice(e.thread, e.updateID, e.callbackUpdateID) {
			d.removeButtons(e.chatID, e.messageID)
		}
		return
	}
	if !d.queueText(e.thread.ID, e.updateID, e.tgMessageID, filePrompt(e.path, e.file), "file", inbound.ActText) {
		return
	}
	d.clearFileRetry(e.updateID)
	d.removeButtons(e.chatID, e.messageID)
}

// fileRetryExpiry reports whether the inbound row has outlived its retention.
func fileRetryExpiry(now, created time.Time) bool {
	return now.Sub(created) > fileRetryMaxAge
}
