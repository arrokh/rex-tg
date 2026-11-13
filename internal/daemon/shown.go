package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/store"
	"rex-tg/internal/turn"
)

// Images that the agent looked at (design section 9.4a). A tool result may
// hold an image block, such as a screenshot that pi read. When the final
// answer names no image file, the last few of them come after the answer.
//
// The daemon writes each image to a private file as soon as its line is read
// and keeps only a small reference, so a turn never holds base64 in memory.

const (
	// maxCallsKept caps the call IDs remembered as reading an upload.
	maxCallsKept = 256
	// maxShownBytesPerTurn caps what one turn writes to the shown folder.
	maxShownBytesPerTurn = 40 << 20
)

// shownRef is one image file of the running turn.
type shownRef struct {
	path string
	hash string
	ext  string
}

// shownTurn is the images of one thread's running turn.
type shownTurn struct {
	refs    []shownRef      // oldest first, at most turn_images, no repeats
	uploads map[string]bool // call IDs whose tool read one of the user's own uploads
	written int             // bytes written for this turn
}

func (d *daemon) shownReset(thread int64) { delete(d.shown, thread) }

func (d *daemon) shownOn() bool { return d.cfg.Display.SendImages && d.cfg.Display.TurnImages > 0 }

func (d *daemon) shownOf(thread int64) *shownTurn {
	st := d.shown[thread]
	if st == nil {
		st = &shownTurn{uploads: map[string]bool{}}
		d.shown[thread] = st
	}
	return st
}

// noteShownCall remembers a call that read a photo that the user sent from
// Telegram: its result is the user's own image and must not come back.
func (d *daemon) noteShownCall(thread int64, e agents.Event) {
	if !d.shownOn() || e.Tool == nil || e.Tool.CallID == "" || e.Tool.Target == "" {
		return
	}
	if !strings.Contains(e.Tool.Target, d.cfg.UploadDir()) {
		return
	}
	st := d.shownOf(thread)
	if len(st.uploads) < maxCallsKept {
		st.uploads[e.Tool.CallID] = true
	}
}

// noteShownResult keeps the images of a tool result: each is written to the
// shown folder, and only the last turn_images stay in the turn.
func (d *daemon) noteShownResult(thread int64, e agents.Event) {
	if !d.shownOn() || len(e.Images) == 0 || e.Tool == nil {
		return
	}
	st := d.shownOf(thread)
	if st.uploads[e.Tool.CallID] {
		return
	}
	for _, im := range e.Images {
		ext, ok := sniffImage(im.Data)
		if !ok {
			continue
		}
		sum := sha256.Sum256(im.Data)
		hash := hex.EncodeToString(sum[:])
		var kept []shownRef
		found := shownRef{}
		for _, r := range st.refs {
			if r.hash == hash {
				found = r
			} else {
				kept = append(kept, r)
			}
		}
		if found.hash == "" { // new in this turn
			if st.written+len(im.Data) > maxShownBytesPerTurn {
				continue
			}
			path, err := d.writeShown(hash, ext, im.Data)
			if err != nil {
				d.log.Warn("save shown image", "err", err)
				continue
			}
			st.written += len(im.Data)
			found = shownRef{path: path, hash: hash, ext: ext}
		}
		st.refs = append(kept, found) // a repeat counts as the latest one
		if over := len(st.refs) - d.cfg.Display.TurnImages; over > 0 {
			st.refs = st.refs[over:]
		}
	}
}

// writeShown writes an image to <state>/shown/<sha256>.<ext>, private to the
// user, and returns its path. An existing file is kept and made fresh, so
// housekeeping does not remove it while a turn uses it.
func (d *daemon) writeShown(hash, ext string, data []byte) (string, error) {
	dir := d.cfg.ShownDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, hash+ext)
	if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() && fi.Size() == int64(len(data)) {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return path, nil
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr == nil && cerr == nil {
		werr = os.Chmod(tmp.Name(), 0o600)
	}
	if err := firstErr(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return path, nil
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// shownItem makes the outbox item for the images that the agent looked at
// during the turn, or reports that there are none. The item is silent and
// has the key <answer key>:shown, so it arrives once. The turn's images are
// used up either way.
func (d *daemon) shownItem(th store.Thread, a turn.Post) (store.OutboxItem, bool) {
	st := d.shown[th.ID]
	d.shownReset(th.ID)
	if st == nil || len(st.refs) == 0 || !d.shownOn() {
		return store.OutboxItem{}, false
	}
	parts := make([]store.PartContent, len(st.refs))
	for i, r := range st.refs {
		parts[i] = store.PartContent{Content: r.path, Format: store.FormatImage, Filename: fmt.Sprintf("image-%d%s", i+1, r.ext)}
	}
	return store.OutboxItem{ThreadID: th.ID, Key: a.Key + ":shown", Kind: "image", Parts: parts, Silent: true}, true
}

// sniffImage tells the file extension of data when it is an image that
// Telegram takes as a photo or an animation: PNG, JPEG, WebP, or GIF. The
// magic bytes decide, not the MIME type that the transcript claims.
func sniffImage(b []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return ".png", true
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return ".jpg", true
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return ".gif", true
	case len(b) >= 12 && bytes.HasPrefix(b, []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return ".webp", true
	}
	return "", false
}
