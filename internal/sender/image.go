package sender

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/jpeg" // image.DecodeConfig
	_ "image/png"  // image.DecodeConfig
	"os"
	"path/filepath"
	"strings"

	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// Limits of sendPhoto (Bot API): 10 MB, width plus height 10000, and a side
// ratio of at most 20. Files that break them go out as documents. A document
// can be 50 MB.
const (
	maxPhotoBytes = 10 << 20
	maxPhotoSum   = 10000
	maxPhotoRatio = 20
	maxImageBytes = 50 << 20
)

// readImage reads the file of an image part. The error text is the short
// reason that the "could not be sent" notice shows.
func readImage(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, errors.New("the file is gone")
	case err != nil:
		return nil, errors.New("the file cannot be read")
	case !fi.Mode().IsRegular():
		return nil, errors.New("not a regular file")
	case fi.Size() > maxImageBytes:
		return nil, errors.New("the file is larger than 50 MB")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("the file cannot be read")
	}
	if len(b) > maxImageBytes {
		return nil, errors.New("the file is larger than 50 MB")
	}
	return b, nil
}

// photoOK reports whether Telegram is likely to take the file as a photo.
func photoOK(name string, data []byte) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg":
	default:
		return false
	}
	if len(data) > maxPhotoBytes {
		return false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width+cfg.Height > maxPhotoSum {
		return false
	}
	long, short := max(cfg.Width, cfg.Height), min(cfg.Width, cfg.Height)
	return long <= maxPhotoRatio*short
}

// sendImage sends the file that an image part names, as a photo when
// Telegram takes it as one, otherwise as a document. When Telegram refuses
// the photo with 400, nothing was posted, so the same file goes out as a
// document in the same attempt. Any other outcome is the caller's verdict.
func (s *Sender) sendImage(ctx context.Context, p store.Part, silent bool) (int64, verdict, error) {
	data, err := readImage(p.Content)
	if err != nil {
		return 0, vFailed, err
	}
	name := p.Filename
	if name == "" {
		name = filepath.Base(p.Content)
	}
	if photoOK(name, data) {
		if err := s.token(ctx); err != nil {
			return 0, vUnknown, err
		}
		m, err := s.tg.SendPhoto(ctx, p.ChatID, p.MessageThreadID, name, data, name, silent)
		if telegram.Classify(err) != telegram.ClassBadRequest {
			return m.MessageID, s.judge(err, p.ThreadID), err
		}
		s.log.Warn("photo rejected; sending a document", "outbox", p.OutboxID, "part", p.Part, "err", err)
	}
	if err := s.token(ctx); err != nil {
		return 0, vUnknown, err
	}
	m, err := s.tg.SendDocument(ctx, p.ChatID, p.MessageThreadID, name, data, name, silent)
	return m.MessageID, s.judge(err, p.ThreadID), err
}
