package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rex-tg/internal/inbound"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

const (
	onlyTextText      = "Only text is supported for now."
	voiceDisabledText = "Voice transcription is disabled in the [voice] config."
	voicePausedText   = "Automatic voice transcription is disabled in the [voice] config."
	voiceFailText     = "❌ Could not turn the voice note into text."
	voiceTooLargeText = "❌ This audio is too long to transcribe. Send a shorter clip."
	fileFailText      = "❌ Could not download the file."
	mediaTimeout      = 2 * time.Minute
	defaultMaxMB      = 20
	maxMediaWorkers   = 4 // downloads and local Hex transcriptions share this budget
)

// media handles a topic message without text (flow 9). Photos and documents
// are downloaded, and their path is typed like text. Voice notes are turned
// into text by Hex when local STT is enabled. Other kinds get a short answer.
// It runs in the core loop; downloads and speech to text run in workers.
func (d *daemon) media(u telegram.Update, m *telegram.Message, th store.Thread) {
	switch {
	case len(m.Photo) > 0 || m.Document != nil:
		fileID, name, size := fileOf(m)
		update, msg, caption := u.UpdateID, m.MessageID, strings.TrimSpace(m.Caption)
		f := fileRetry{FileID: fileID, Name: name, Size: size, Caption: caption, Quote: d.replyQuote(m, th)}
		body, err := encodeFileRetry(f)
		if err != nil {
			d.log.Warn("encode file download", "err", err)
			return
		}
		if size > d.maxUpload() {
			body = "" // oversized files need no recoverable download
		}
		if !d.recorded(u, m, "file", body, th.ID, store.InboundDone) {
			return
		}
		if limit := d.maxUpload(); size > limit {
			d.replyTopic(th, fmt.Sprintf("file:%d", u.UpdateID),
				fmt.Sprintf("❌ The file is too large. The limit is %d MB.", limit>>20))
			return
		}
		dir := filepath.Join(d.cfg.UploadDir(), fmt.Sprint(th.ID))
		if !d.tryMediaSlot() {
			d.fileDownloadFailed(th, update, f, errors.New("media workers are busy"))
			return
		}
		d.spawn(func() {
			defer d.releaseMediaSlot()
			path, err := d.download(fileID, dir, fmt.Sprintf("%d-%s", msg, name))
			if err != nil {
				if d.ctx.Err() == nil {
					d.log.Warn("download file", "thread", th.ID, "err", err)
					d.fileDownloadFailed(th, update, f, err)
				}
				return
			}
			d.emit(evQueueText{threadID: th.ID, orig: update, tgMsg: msg, text: filePrompt(path, f), kind: "file", action: inbound.ActText})
		})
	case m.Voice != nil || m.Audio != nil:
		fileID, size := voiceFile(m.Voice, m.Audio)
		if !d.cfg.Voice.EnableSTT {
			if d.recorded(u, m, "voice", "", th.ID, store.InboundDone) {
				d.replyTopic(th, fmt.Sprintf("voice:%d", u.UpdateID), voiceDisabledText)
			}
			return
		}
		cli := d.hexCLI()
		if d.cfg.Voice.DisableSTT && cli != "" {
			if d.recorded(u, m, "voice", "", th.ID, store.InboundDone) {
				d.replyTopic(th, fmt.Sprintf("voice:%d", u.UpdateID), voiceDisabledText)
			}
			return
		}
		if !d.cfg.Voice.AutoProcessSTT {
			if d.recorded(u, m, "voice", "", th.ID, store.InboundDone) {
				d.replyTopic(th, fmt.Sprintf("voice:%d", u.UpdateID), voicePausedText)
			}
			return
		}
		if limit := d.maxUpload(); size > limit {
			if d.recorded(u, m, "voice", "", th.ID, store.InboundDone) {
				d.replyTopic(th, fmt.Sprintf("voice:%d", u.UpdateID),
					fmt.Sprintf("❌ The voice note is too large. The limit is %d MB.", limit>>20))
			}
			return
		}
		pending := ""
		if cli != "" {
			pending = voicePendingBody
		}
		if !d.recorded(u, m, "voice", pending, th.ID, store.InboundDone) {
			return
		}
		if cli == "" {
			d.promptHexSetup(th)
			return
		}
		dir := filepath.Join(d.cfg.UploadDir(), fmt.Sprint(th.ID))
		update, msg := u.UpdateID, m.MessageID
		quoted := d.replyQuote(m, th)
		if !d.tryMediaSlot() {
			d.mediaWorkNotice(th, update, fmt.Sprintf("reply:voice:%d", update), "❌ The Mac is busy processing uploads. Send the voice note again shortly.")
			return
		}
		d.spawn(func() {
			defer d.releaseMediaSlot()
			text, err := d.transcribe(fileID, dir, fmt.Sprintf("%d-voice", msg), cli)
			if err != nil || strings.TrimSpace(text) == "" {
				if d.ctx.Err() == nil {
					d.log.Warn("voice note", "thread", th.ID, "err", err)
					if err != nil && isVoiceSetupError(err) {
						d.voiceSetupReady.Store(false)
						if clearErr := d.st.SetInboundBody(d.ctx, update, ""); clearErr != nil {
							d.log.Warn("clear interrupted voice marker", "update", update, "err", clearErr)
							d.mediaWorkNotice(th, update, fmt.Sprintf("reply:voice:%d", update), voiceFailText)
							return
						}
						d.promptHexSetup(th)
						return
					}
					if errors.Is(err, errVoiceWAVTooLarge) {
						if clearErr := d.st.SetInboundBody(d.ctx, update, ""); clearErr != nil {
							d.log.Warn("clear oversized voice marker", "update", update, "err", clearErr)
							d.mediaWorkNotice(th, update, fmt.Sprintf("reply:voice:%d", update), voiceFailText)
							return
						}
						d.mediaWorkNotice(th, update, fmt.Sprintf("reply:voice:%d", update), voiceTooLargeText)
						return
					}
					d.mediaWorkNotice(th, update, fmt.Sprintf("reply:voice:%d", update), voiceFailText)
				}
				return
			}
			text = strings.TrimSpace(text)
			d.replyTopic(th, fmt.Sprintf("voice:%d", update), "🎤 "+esc(text))
			text = inbound.WithReply(text, agentQuote(text, quoted))
			d.emit(evQueueText{threadID: th.ID, orig: update, tgMsg: msg, text: text, kind: "voice", action: inbound.ActText})
		})
	default:
		if d.recorded(u, m, "other", "", th.ID, store.InboundDone) {
			d.replyTopic(th, fmt.Sprintf("other:%d", u.UpdateID), onlyTextText)
		}
	}
}

// tryMediaSlot refuses excess work instead of starting goroutines that wait
// indefinitely. Files keep their durable retry metadata; voice notes ask the
// owner to try again. A slot is held until the real worker finishes.
func (d *daemon) tryMediaSlot() bool {
	select {
	case d.mediaSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (d *daemon) releaseMediaSlot() { <-d.mediaSlots }

// maxUpload is the download limit in bytes.
func (d *daemon) maxUpload() int64 {
	mb := d.cfg.InputSettings.MaxUploadMB
	if mb <= 0 {
		mb = defaultMaxMB
	}
	return int64(mb) << 20
}

// fileOf picks the file of a photo or document message: the largest photo
// size, or the document.
func fileOf(m *telegram.Message) (fileID, name string, size int64) {
	if m.Document != nil {
		return m.Document.FileID, safeName(m.Document.FileName, "file"), m.Document.FileSize
	}
	best := m.Photo[len(m.Photo)-1]
	for _, p := range m.Photo {
		if p.FileSize > best.FileSize {
			best = p
		}
	}
	return best.FileID, "photo.jpg", best.FileSize
}

// safeName keeps a file name's base, without folders or unusual characters.
func safeName(name, fallback string) string {
	name = filepath.Base(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	s := strings.Trim(b.String(), "._")
	if s == "" {
		return fallback
	}
	if len(s) > 100 {
		s = s[len(s)-100:]
	}
	return s
}

// download fetches a Telegram file into dir/name and returns the path. The
// folder is only readable by the user.
func (d *daemon) download(fileID, dir, name string) (string, error) {
	ctx, cancel := context.WithTimeout(d.ctx, mediaTimeout)
	defer cancel()
	f, err := d.tg.GetFile(ctx, fileID)
	if err != nil {
		return "", fmt.Errorf("get file: %w", err)
	}
	if f.FilePath == "" {
		return "", errors.New("get file: no file path")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if ext := filepath.Ext(f.FilePath); filepath.Ext(name) == "" && ext != "" {
		name += ext
	}
	path := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, ".dl-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := d.tg.DownloadFile(ctx, f.FilePath, tmp, d.maxUpload()); err != nil {
		tmp.Close()
		return "", fmt.Errorf("download: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

// transcribe downloads Telegram audio. Hex requires WAV input, so FFmpeg
// converts the file to mono 16 kHz PCM before local transcription.
func (d *daemon) transcribe(fileID, dir, name, cli string) (string, error) {
	src, err := d.download(fileID, dir, name)
	if err != nil {
		return "", err
	}
	defer os.Remove(src)
	ctx, cancel := context.WithTimeout(d.ctx, mediaTimeout)
	defer cancel()
	if !executable(cli) {
		return "", errHexUnavailable
	}
	ffmpeg := d.ffmpegCLI()
	if ffmpeg == "" {
		return "", errFFmpegUnavailable
	}
	wav, err := d.transcodeVoiceToWAV(ctx, ffmpeg, src)
	if err != nil {
		return "", err
	}
	defer os.Remove(wav)
	return d.transcribeHex(ctx, cli, wav)
}

// voiceFile returns the file of a voice note, or of an audio file.
func voiceFile(v *telegram.Voice, a *telegram.Audio) (string, int64) {
	if v != nil {
		return v.FileID, v.FileSize
	}
	if a != nil {
		return a.FileID, a.FileSize
	}
	return "", 0
}
