// Package tail follows a JSONL file by byte offset and delivers complete
// lines in batches. It survives a missing file, truncation, and replacement.
package tail

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	defaultPoll    = time.Second
	defaultMaxLine = 8 << 20
	chunkSize      = 64 << 10
	maxBatchBytes  = 4 << 20
)

// Position is a place in a file: a byte offset plus the file identity.
type Position struct {
	Offset int64
	DevIno string
}

// Line is one complete line without its line ending.
type Line struct {
	Data   []byte
	Offset int64 // byte offset of the first byte of the line
}

// Batch is a group of lines read together.
type Batch struct {
	Lines   []Line
	End     Position // right after the last complete line (or skipped line)
	Reset   bool     // reading restarted at byte 0 (file replaced or shortened)
	Skipped int      // lines dropped because they were longer than MaxLine
}

// Options tunes a Follower. Zero values pick the defaults.
type Options struct {
	Poll    time.Duration // default 1 s
	MaxLine int           // default 8 MiB
}

// DevIno returns the device and inode of fi as a string, or "" when unknown.
func DevIno(fi os.FileInfo) string {
	if fi == nil {
		return ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return ""
	}
	return strconv.FormatUint(uint64(st.Dev), 10) + ":" + strconv.FormatUint(uint64(st.Ino), 10)
}

// Follower follows one file. Create it with Follow.
type Follower struct {
	out    chan Batch
	cancel context.CancelFunc
	done   chan struct{}

	mu  sync.Mutex
	err error
}

// Batches returns the channel of batches. It is closed when the follower stops.
func (f *Follower) Batches() <-chan Batch { return f.out }

// Err returns the terminal error, if any. Cancellation is not an error.
func (f *Follower) Err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

// Close stops the follower and waits for its goroutine to end.
func (f *Follower) Close() {
	f.cancel()
	<-f.done
}

// Follow starts following path from start.
func Follow(ctx context.Context, path string, start Position, opts Options) *Follower {
	if opts.Poll <= 0 {
		opts.Poll = defaultPoll
	}
	if opts.MaxLine <= 0 {
		opts.MaxLine = defaultMaxLine
	}
	cctx, cancel := context.WithCancel(ctx)
	f := &Follower{out: make(chan Batch), cancel: cancel, done: make(chan struct{})}
	r := &reader{
		path:   path,
		opts:   opts,
		pos:    start.Offset,
		readAt: start.Offset,
		devino: start.DevIno,
	}
	go func() {
		defer close(f.done)
		defer close(f.out)
		defer cancel()
		err := r.run(cctx, f.out)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			f.mu.Lock()
			f.err = err
			f.mu.Unlock()
		}
	}()
	return f
}

type reader struct {
	path string
	opts Options

	devino   string // identity of the file being followed ("" until known)
	pos      int64  // offset of the start of the first unfinished line
	readAt   int64  // offset of the next byte to read
	buf      []byte // bytes of the unfinished line read so far
	skipping bool   // inside an over-long line
	reset    bool   // Reset flag pending for the next batch
}

func (r *reader) run(ctx context.Context, out chan<- Batch) error {
	wake := make(chan struct{}, 1)
	var w *fsnotify.Watcher
	if nw, err := fsnotify.NewWatcher(); err == nil {
		w = nw
		defer w.Close()
	}
	dir := filepath.Dir(r.path)
	clean := filepath.Clean(r.path)
	watching := false
	addWatch := func() {
		if w != nil && !watching && w.Add(dir) == nil {
			watching = true
		}
	}
	addWatch()

	var events <-chan fsnotify.Event
	var werrs <-chan error
	if w != nil {
		events, werrs = w.Events, w.Errors
	}
	ticker := time.NewTicker(r.opts.Poll)
	defer ticker.Stop()

	poke := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	poke()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		case <-ticker.C:
			addWatch()
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if filepath.Clean(ev.Name) != clean {
				continue
			}
		case _, ok := <-werrs:
			if !ok {
				werrs = nil
			}
		}
		if err := r.drain(ctx, out); err != nil {
			return err
		}
	}
}

// drain reads everything currently available and sends batches.
func (r *reader) drain(ctx context.Context, out chan<- Batch) error {
	f, err := os.Open(r.path)
	if err != nil {
		return nil // missing (or unreadable for now): wait
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	id := DevIno(fi)
	switch {
	case r.devino != "" && id != "" && id != r.devino, fi.Size() < r.readAt:
		r.pos, r.readAt, r.buf, r.skipping = 0, 0, nil, false
		r.reset = true
	}
	if id != "" {
		r.devino = id
	}
	if _, err := f.Seek(r.readAt, io.SeekStart); err != nil {
		return fmt.Errorf("tail: seek %s: %w", r.path, err)
	}

	var b Batch
	size := 0
	flush := func() error {
		if len(b.Lines) == 0 && !r.reset && b.Skipped == 0 {
			return nil
		}
		b.Reset = r.reset
		b.End = Position{Offset: r.pos, DevIno: r.devino}
		select {
		case out <- b:
		case <-ctx.Done():
			return ctx.Err()
		}
		r.reset = false
		b, size = Batch{}, 0
		return nil
	}

	chunk := make([]byte, chunkSize)
	for {
		n, rerr := f.Read(chunk)
		if n > 0 {
			r.consume(chunk[:n], &b, &size)
			if size >= maxBatchBytes {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				return fmt.Errorf("tail: read %s: %w", r.path, rerr)
			}
			break
		}
	}
	return flush()
}

// consume splits data into lines and updates the reader state and batch.
func (r *reader) consume(data []byte, b *Batch, size *int) {
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if r.skipping {
			if i < 0 {
				r.readAt += int64(len(data))
				return
			}
			r.readAt += int64(i + 1)
			r.pos = r.readAt
			r.skipping = false
			b.Skipped++
			data = data[i+1:]
			continue
		}
		if i < 0 {
			r.readAt += int64(len(data))
			if len(r.buf)+len(data) > r.opts.MaxLine {
				r.buf, r.skipping = nil, true
			} else {
				r.buf = append(r.buf, data...)
			}
			return
		}
		line := data[:i]
		r.readAt += int64(i + 1)
		start := r.pos
		data = data[i+1:]
		if len(r.buf)+len(line) > r.opts.MaxLine {
			r.buf = nil
			r.pos = r.readAt
			b.Skipped++
			continue
		}
		full := make([]byte, 0, len(r.buf)+len(line))
		full = append(full, r.buf...)
		full = append(full, line...)
		r.buf = nil
		r.pos = r.readAt
		if n := len(full); n > 0 && full[n-1] == '\r' {
			full = full[:n-1]
		}
		b.Lines = append(b.Lines, Line{Data: full, Offset: start})
		*size += len(full)
	}
}

// Scan reads the whole current file once, calls fn for every complete line,
// and returns the position after the last complete line. Lines over the
// default maximum are skipped. A partial last line is ignored.
func Scan(path string, fn func(Line) error) (Position, error) {
	f, err := os.Open(path)
	if err != nil {
		return Position{}, fmt.Errorf("tail: scan: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Position{}, fmt.Errorf("tail: scan: %w", err)
	}
	pos := Position{DevIno: DevIno(fi)}
	br := bufio.NewReaderSize(f, chunkSize)
	var acc []byte
	skipping := false
	var off int64 // offset of the next byte to read
	for {
		frag, err := br.ReadSlice('\n')
		off += int64(len(frag))
		complete := len(frag) > 0 && frag[len(frag)-1] == '\n'
		if !skipping {
			if len(acc)+len(frag) > defaultMaxLine {
				acc, skipping = nil, true
			} else {
				acc = append(acc, frag...)
			}
		}
		if complete {
			if !skipping {
				line := acc[:len(acc)-1]
				if n := len(line); n > 0 && line[n-1] == '\r' {
					line = line[:n-1]
				}
				start := pos.Offset
				if err := fn(Line{Data: line, Offset: start}); err != nil {
					return pos, err
				}
			}
			pos.Offset = off
			acc, skipping = nil, false
			continue
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if err == io.EOF {
				return pos, nil
			}
			return pos, fmt.Errorf("tail: scan %s: %w", path, err)
		}
	}
}
