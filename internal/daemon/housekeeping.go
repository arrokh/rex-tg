package daemon

import (
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// uploadMaxAge is how long downloaded files stay. spoolMaxAge is in
// lifecycle.go.
const uploadMaxAge = 7 * 24 * time.Hour

// housekeeping runs once a day in a worker (flow 8): it prunes old database
// rows, spool files that were never read, and old uploads. It touches only
// the store and the file system, so it is safe outside the core loop.
func (d *daemon) housekeeping(now time.Time) {
	if err := d.st.Prune(d.ctx, now); err != nil && d.ctx.Err() == nil {
		d.log.Warn("housekeeping: prune", "err", err)
	}
	if n := removeOlder(d.cfg.SpoolDir(), now.Add(-spoolMaxAge)); n > 0 {
		d.log.Info("housekeeping: removed old spool files", "count", n)
	}
	if n := removeOlder(d.cfg.UploadDir(), now.Add(-uploadMaxAge)); n > 0 {
		d.log.Info("housekeeping: removed old uploads", "count", n)
	}
}

// removeOlder deletes regular files under root that changed before cutoff,
// then the folders that became empty (but not root). It returns the number of
// files removed. A missing root is not an error.
func removeOlder(root string, cutoff time.Time) int {
	n := 0
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			if path != root {
				dirs = append(dirs, path)
			}
			return nil
		}
		if !e.Type().IsRegular() {
			return nil
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			return nil
		}
		if os.Remove(path) == nil {
			n++
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // fails, harmlessly, when the folder is not empty
	}
	return n
}
