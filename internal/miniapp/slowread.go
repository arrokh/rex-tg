package miniapp

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// macOS asks the user before a program reads the Desktop, Documents, or
// Downloads folder, iCloud Drive, or a network volume (Privacy & Security).
// The read does not fail while the question is open: it waits until someone
// answers on the Mac, and nothing can stop it. So the folder explorer and the
// file browser read in the background and answer the page after slowRead,
// whatever happens: the page says what may be wrong instead of loading
// forever. A read that is still waiting is not started again for the same
// folder: the next request waits for the same read.

// slowRead is how long a request waits for a read from the disk. The file
// browser's git calls take at most 5 seconds each.
const slowRead = 8 * time.Second

// errSlowRead is a read that did not end within slowRead. It goes on in the
// background.
var errSlowRead = errors.New("miniapp: the read did not end in time")

// slowReadText is the page's error for errSlowRead: what is "this
// folder" or "this file or folder".
func slowReadText(what string) string {
	return `{"error":"The Mac did not let rex-tg read ` + what + ` in time. macOS may be asking on the Mac whether rex-tg may read it: click Allow there, then try again."}`
}

// reads runs reads that may wait for macOS: one at a time for each key.
type reads struct {
	mu sync.Mutex
	on map[string]*read
}

type read struct {
	done chan struct{}
	v    any
	err  error
}

// do runs f for key, or joins the run of f for key that is still going, and
// waits for it at most wait. After wait it returns errSlowRead and f goes
// on; its result is dropped.
func (rs *reads) do(key string, wait time.Duration, f func() (any, error)) (any, error) {
	rs.mu.Lock()
	r := rs.on[key]
	if r == nil {
		r = &read{done: make(chan struct{})}
		if rs.on == nil {
			rs.on = map[string]*read{}
		}
		rs.on[key] = r
		go func() {
			// A panic here is not on the request's goroutine, where net/http
			// would recover it: it would stop the daemon.
			defer func() {
				if p := recover(); p != nil {
					r.v, r.err = nil, fmt.Errorf("miniapp: read panicked: %v", p)
				}
				rs.mu.Lock()
				delete(rs.on, key)
				rs.mu.Unlock()
				close(r.done)
			}()
			r.v, r.err = f()
		}()
	}
	rs.mu.Unlock()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-r.done:
		return r.v, r.err
	case <-t.C:
		return nil, errSlowRead
	}
}
