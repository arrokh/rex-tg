package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rex-tg/internal/format"
	"rex-tg/internal/miniapp"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
	"rex-tg/internal/turn"
)

// File offers (design section 9.4a). A final answer that names files of the
// types in display.send_files, such as a PDF or a CSV, gets one silent
// message after it: "📎 Files in this answer" with a button per file. A file
// goes to Telegram only when the user taps its button, and it is read from
// the disk at that moment. Images still come by themselves.
//
// The offer is an outbox item (kind "files"), so it arrives once and after
// the answer. Its file paths are kept in the store (kv "files:<thread>:<key>")
// and its buttons carry the outbox id and the file's index ("fo:<id>:<i>"),
// because callback data holds at most 64 bytes. The sender asks for the
// buttons when it sends the offer (fileOfferMarkup).

const (
	fileOfferKind   = "files"
	fileOfferData   = "fo"
	fileOfferMaxAge = 30 * 24 * time.Hour // housekeeping drops older offers
)

func fileOfferKey(thread int64, key string) string { return fmt.Sprintf("files:%d:%s", thread, key) }

// fileOffer is what the store keeps of an offer.
type fileOffer struct {
	At       int64    `json:"at"` // unix seconds
	Paths    []string `json:"paths"`
	CWD      string   `json:"cwd,omitempty"`      // the agent's folder, for the check at the tap
	Previews []string `json:"previews,omitempty"` // private Mini App file pages, by path index
}

// fileOfferItem makes the offer of the files a final answer names, or
// reports that there is none (core). It keeps the paths in the store first.
func (d *daemon) fileOfferItem(th store.Thread, a turn.Post) (store.OutboxItem, bool) {
	exts := sendExts(false, d.cfg.Display.SendFiles)
	if len(exts) == 0 || th.CurrentConversationID == 0 {
		return store.OutboxItem{}, false
	}
	conv, err := d.st.Conversation(d.ctx, th.CurrentConversationID)
	if err != nil {
		return store.OutboxItem{}, false
	}
	home, _ := os.UserHomeDir()
	paths := findFiles(a.Markdown, home, conv.CWD, exts)
	if len(paths) == 0 {
		return store.OutboxItem{}, false
	}
	key := a.Key + ":files"
	o := fileOffer{At: d.now().Unix(), Paths: paths, CWD: conv.CWD}
	if d.miniAppReady() && d.cfg.MiniApp.Files && filepath.IsAbs(conv.CWD) && filepath.Clean(conv.CWD) != string(filepath.Separator) {
		o.Previews = make([]string, len(paths))
		for i, path := range paths {
			rel, err := filepath.Rel(conv.CWD, path)
			if err != nil || !filepath.IsLocal(rel) {
				continue // outside the Files browser root; attachment only
			}
			id, err := d.savePageOf(miniapp.Page{Kind: miniapp.KindFiles, Title: "📂 " + filepath.Base(conv.CWD), Thread: th.ID, Dir: conv.CWD, InitialFile: filepath.ToSlash(rel)})
			if err == nil {
				o.Previews[i] = id
			}
		}
	}
	b, _ := json.Marshal(o)
	if err := d.st.Set(d.ctx, fileOfferKey(th.ID, key), string(b)); err != nil {
		d.log.Warn("file offer: keep the paths", "err", err)
		return store.OutboxItem{}, false
	}
	var sb strings.Builder
	sb.WriteString("📎 <b>Files in this answer</b>. Open a preview or send a file as an attachment.")
	for _, p := range paths {
		sb.WriteString("\n• <code>" + esc(format.Truncate(d.tildePath(p), maxShownPath)) + "</code>")
		if fi, err := os.Stat(p); err == nil {
			sb.WriteString(" · " + sizeText(fi.Size()))
		}
	}
	return store.OutboxItem{ThreadID: th.ID, Key: key, Kind: fileOfferKind, Silent: true,
		Parts: []store.PartContent{{Content: sb.String(), Format: store.FormatHTML}}}, true
}

// sizeText is a file size for people: "812 B", "14 KB", "3.2 MB".
func sizeText(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%d KB", (n+1<<9)>>10)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
}

// loadFileOffer reads the paths of an offer, by its thread and outbox key.
func loadFileOffer(ctx context.Context, st *store.Store, thread int64, key string) (fileOffer, bool) {
	v, ok, err := st.Get(ctx, fileOfferKey(thread, key))
	if err != nil || !ok {
		return fileOffer{}, false
	}
	var o fileOffer
	if json.Unmarshal([]byte(v), &o) != nil || len(o.Paths) == 0 {
		return fileOffer{}, false
	}
	return o, true
}

// fileOfferMarkup gives the sender the buttons of an offer: one per file.
// It runs in the sender.
func fileOfferMarkup(st *store.Store, base string) func(store.Part) *telegram.InlineKeyboardMarkup {
	return func(p store.Part) *telegram.InlineKeyboardMarkup {
		if p.Kind != fileOfferKind {
			return nil
		}
		o, ok := loadFileOffer(context.Background(), st, p.ThreadID, p.Key)
		if !ok {
			return nil
		}
		var rows [][]telegram.InlineKeyboardButton
		for i, path := range o.Paths {
			var row []telegram.InlineKeyboardButton
			if strings.TrimSpace(base) != "" && i < len(o.Previews) && o.Previews[i] != "" {
				row = append(row, telegram.InlineKeyboardButton{
					Text: "👁 " + format.Truncate(filepath.Base(path), 32), WebApp: &telegram.WebAppInfo{URL: miniAppURL(base, o.Previews[i])},
				})
			}
			row = append(row, telegram.InlineKeyboardButton{
				Text:         "📎 Attach " + format.Truncate(filepath.Base(path), 32),
				CallbackData: fmt.Sprintf("%s:%d:%d", fileOfferData, p.OutboxID, i),
			})
			rows = append(rows, row)
		}
		return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
	}
}

// fileOfferButton sends the file of a tapped button ("fo:<outbox id>:<i>")
// (core). The file goes out once per button: its outbox key comes from the
// offer and the index, so a second tap sends nothing.
func (d *daemon) fileOfferButton(cb *telegram.CallbackQuery, parts []string) {
	id, err1 := strconv.ParseInt(parts[1], 10, 64)
	idx, err2 := strconv.Atoi(parts[2])
	th, ok := d.threadOfMessage(cb.Message)
	if err1 != nil || err2 != nil || !ok {
		d.answerCallback(cb.ID, "This topic is closed.", false)
		return
	}
	row, err := d.st.OutboxRow(d.ctx, id)
	if err != nil || row.ThreadID != th.ID || row.Kind != fileOfferKind {
		d.answerCallback(cb.ID, "This offer is gone. Ask the agent again.", false)
		return
	}
	o, ok := loadFileOffer(d.ctx, d.st, th.ID, row.Key)
	if !ok || idx < 0 || idx >= len(o.Paths) {
		d.answerCallback(cb.ID, "This offer is gone. Ask the agent again.", false)
		return
	}
	path := o.Paths[idx]
	name := filepath.Base(path)
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 || fi.Size() > maxImageBytes {
		d.answerCallback(cb.ID, name+" is gone or too large now.", false)
		return
	}
	// The offer may be days old: the path may lead somewhere else now.
	home, _ := os.UserHomeDir()
	cwd := o.CWD
	if cwd == "" { // an offer of v0.1.10 or older: the topic's agent folder
		if conv, err := d.st.Conversation(d.ctx, th.CurrentConversationID); err == nil {
			cwd = conv.CWD
		}
	}
	real, ok := placeOK(path, home, cwd)
	if !ok {
		d.answerCallback(cb.ID, name+" is not in a place rex-tg sends files from.", false)
		return
	}
	item := store.OutboxItem{ThreadID: th.ID, Key: fmt.Sprintf("file:%d:%d", id, idx), Kind: "image",
		Parts: []store.PartContent{{Content: real, Format: store.FormatImage, Filename: name}}}
	_, created, err := d.st.Enqueue(d.ctx, item)
	switch {
	case err != nil:
		d.log.Warn("enqueue", "thread", th.ID, "kind", "file", "err", err)
		d.answerCallback(cb.ID, "Could not send "+name+".", false)
	case !created:
		d.answerCallback(cb.ID, name+" is sent already.", false)
	default:
		d.snd.Wake()
		d.answerCallback(cb.ID, "Sending "+name+"…", false)
	}
}

// pruneFileOffers drops the paths of offers older than fileOfferMaxAge
// (housekeeping, a worker).
func (d *daemon) pruneFileOffers(now time.Time) {
	keys, err := d.st.KeysWithPrefix(d.ctx, "files:")
	if err != nil {
		return
	}
	for _, k := range keys {
		v, ok, err := d.st.Get(d.ctx, k)
		var o fileOffer
		if err != nil || !ok || json.Unmarshal([]byte(v), &o) != nil || now.Sub(time.Unix(o.At, 0)) > fileOfferMaxAge {
			if err == nil {
				_ = d.st.Delete(d.ctx, k)
			}
		}
	}
}
