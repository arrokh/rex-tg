package daemon

import (
	"os"
	"time"
)

// Restart after an update (design section 9.6). `brew upgrade rex-tg` and
// `make install` replace the binary, but the running daemon keeps the old
// one. So the daemon watches the binary that launchd starts (Options.SelfPath,
// a stable path that survives an upgrade). When it changed and the new file
// stayed the same for one more check (the copy is complete), the daemon
// waits until no message is being typed and no send is under way, and
// stops with ErrUpdated. launchd
// starts it again from the same path, now the new binary.
//
// Nothing of the topics is lost: the links of topics and sessions, the
// transcript cursors, the turn snapshots, the queue, and the outbox are in
// the database, the token is in the Keychain, and the proxy is in the
// config. The new daemon goes on where the old one stopped, as after any
// restart.

// selfWatch follows the daemon's own binary (core).
type selfWatch struct {
	path    string
	start   os.FileInfo // the binary at the start
	seen    os.FileInfo // the changed binary at the last check, nil while unchanged
	next    time.Time   // the next check
	pending bool        // the binary changed and settled: stop when safe
}

func newSelfWatch(path string) *selfWatch {
	if path == "" {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	return &selfWatch{path: path, start: fi}
}

// sameFile tells whether two stats show one unchanged file.
func sameFile(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// check reports whether the binary was replaced and has settled. A missing
// file (in the middle of an upgrade) counts as not settled.
func (w *selfWatch) check() bool {
	fi, err := os.Stat(w.path)
	switch {
	case err != nil:
		w.seen = nil
		return false
	case sameFile(fi, w.start):
		w.seen = nil
		return false
	case w.seen != nil && sameFile(fi, w.seen):
		return true
	}
	w.seen = fi
	return false
}

// checkUpdate runs from the tick (core). It stops the daemon with ErrUpdated
// once the binary was replaced and no message is being typed.
func (d *daemon) checkUpdate(now time.Time) {
	w := d.self
	if w == nil || d.fatal != nil {
		return
	}
	if !w.pending {
		if now.Before(w.next) {
			return
		}
		w.next = now.Add(d.timing.UpdateCheck)
		if !w.check() {
			return
		}
		w.pending = true
		d.log.Info("rex-tg was updated; restarting when no message is being typed", "path", w.path)
	}
	if len(d.delivering) > 0 || len(d.busy) > 0 {
		return // never stop between a paste and its Enter
	}
	if !d.snd.Idle() {
		return // nor in the middle of a send: it would look lost after the start
	}
	d.fatal = ErrUpdated
}
