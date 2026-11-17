package miniapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A read that waits (for macOS's question on the Mac) answers after the
// wait, goes on in the background, and is not started twice for one key.
func TestReadThatWaitsIsJoined(t *testing.T) {
	var rs reads
	release := make(chan struct{})
	var runs atomic.Int32
	slow := func() (any, error) {
		runs.Add(1)
		<-release
		return "listed", nil
	}
	for i := 0; i < 2; i++ {
		if _, err := rs.do("k", 10*time.Millisecond, slow); !errors.Is(err, errSlowRead) {
			t.Fatalf("call %d: %v, want errSlowRead", i, err)
		}
	}
	if n := runs.Load(); n != 1 {
		t.Fatalf("the read ran %d times while it waited, want 1", n)
	}
	// Another key does not wait for it.
	if v, err := rs.do("other", time.Second, func() (any, error) { return "fast", nil }); v != "fast" || err != nil {
		t.Fatalf("another key: %v %v", v, err)
	}
	// Once the read ends, the next request reads again, at once.
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		rs.mu.Lock()
		n := len(rs.on)
		rs.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the read did not end")
		}
		time.Sleep(time.Millisecond)
	}
	if v, err := rs.do("k", time.Second, slow); v != "listed" || err != nil || runs.Load() != 2 {
		t.Fatalf("after the wait: %v %v, runs %d", v, err, runs.Load())
	}
}

// The folder explorer says what may be wrong when a folder read waits, and
// lists the folder once the read can end.
func TestFolderThatWaitsForMacOS(t *testing.T) {
	home := t.TempDir()
	docs := filepath.Join(home, "Documents")
	if err := os.Mkdir(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) {
		return Page{Kind: KindControl, Title: "rex-tg"}, id == "c1"
	}, nil).WithControl(&fakeControl{}, home)
	s.wait = 20 * time.Millisecond
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	owner := sign(t, fakeToken, fields(42, time.Now()))
	get := func() (int, map[string]any) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/control/folders?id=c1&path="+docs, nil)
		req.Header.Set("X-Telegram-Init-Data", owner)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}

	// A read of Documents is already waiting, as when macOS asks.
	release := make(chan struct{})
	_, _ = s.reads.do(folderKey(home, docs, false), 0, func() (any, error) {
		<-release
		return ListFolders(home, docs, false)
	})
	code, body := get()
	if msg, _ := body["error"].(string); code != http.StatusServiceUnavailable || !strings.Contains(msg, "click Allow") {
		t.Fatalf("while the read waits: %d %v", code, body)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if code, body = get(); code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the wait: %d %v", code, body)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if body["shown"] != "~/Documents" {
		t.Errorf("listed: %v", body)
	}
}

// A read that panics answers with an error and leaves the daemon running.
func TestReadThatPanics(t *testing.T) {
	var rs reads
	_, err := rs.do("k", time.Second, func() (any, error) { panic("boom") })
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if v, err := rs.do("k", time.Second, func() (any, error) { return 1, nil }); v != 1 || err != nil {
		t.Fatalf("after the panic: %v %v", v, err)
	}
}

// Timed-out reads keep their global slots until the blocked work completes;
// a duplicate still joins, while a new key gets a retryable limit error.
func TestReadOutstandingLimit(t *testing.T) {
	var rs reads
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
		waitForNoReads(t, &rs)
	}()
	completed := make(chan struct{}, maxOutstandingReads)
	for i := 0; i < maxOutstandingReads; i++ {
		started := make(chan struct{})
		result := make(chan error, 1)
		key := fmt.Sprintf("blocked-%d", i)
		go func() {
			_, err := rs.do(key, 20*time.Millisecond, func() (any, error) {
				close(started)
				<-release
				completed <- struct{}{}
				return "done", nil
			})
			result <- err
		}()
		<-started
		if err := <-result; !errors.Is(err, errSlowRead) {
			t.Fatalf("read %d returned %v, want errSlowRead", i, err)
		}
	}
	if _, err := rs.do("blocked-0", time.Millisecond, func() (any, error) {
		return nil, errors.New("duplicate unexpectedly started")
	}); !errors.Is(err, errSlowRead) {
		t.Fatalf("duplicate returned %v, want errSlowRead", err)
	}
	if _, err := rs.do("excess", time.Second, func() (any, error) {
		return nil, errors.New("excess read unexpectedly started")
	}); !errors.Is(err, errReadLimit) {
		t.Fatalf("excess read returned %v, want errReadLimit", err)
	}
	if got := activeReadCount(&rs); got != maxOutstandingReads {
		t.Fatalf("active reads = %d, want %d", got, maxOutstandingReads)
	}

	close(release)
	released = true
	for i := 0; i < maxOutstandingReads; i++ {
		<-completed
	}
	waitForNoReads(t, &rs)
	if v, err := rs.do("after-release", time.Second, func() (any, error) { return "ok", nil }); v != "ok" || err != nil {
		t.Fatalf("read after release: %v, %v", v, err)
	}
}

func activeReadCount(rs *reads) int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return len(rs.on)
}

func waitForNoReads(t *testing.T, rs *reads) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for activeReadCount(rs) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("outstanding reads did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

// Capacity checks happen only after the existing owner and page checks.
func TestFolderReadLimitKeepsAuthAndCanRetry(t *testing.T) {
	home := t.TempDir()
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) {
		return Page{Kind: KindControl, Title: "rex-tg"}, id == "c1"
	}, nil).WithControl(&fakeControl{}, home)
	release := make(chan struct{})
	defer func() {
		close(release)
		waitForNoReads(t, &s.reads)
	}()
	for i := 0; i < maxOutstandingReads; i++ {
		_, _ = s.reads.do(fmt.Sprintf("busy-%d", i), 0, func() (any, error) {
			<-release
			return nil, nil
		})
	}
	call := func(user int64) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/control/folders?id=c1", nil)
		req.Header.Set("X-Telegram-Init-Data", sign(t, fakeToken, fields(user, time.Now())))
		w := httptest.NewRecorder()
		s.controlFolders(w, req)
		return w
	}
	if w := call(7); w.Code != http.StatusForbidden {
		t.Fatalf("non-owner request: %d %s", w.Code, w.Body.String())
	}
	if got := activeReadCount(&s.reads); got != maxOutstandingReads {
		t.Fatalf("a refused request changed active reads to %d", got)
	}
	if w := call(42); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "Try again shortly") {
		t.Fatalf("owner at capacity: %d %s", w.Code, w.Body.String())
	}
}

// The file browser says the same when its read waits.
func TestFilesThatWaitForMacOS(t *testing.T) {
	root := t.TempDir()
	s := New(fakeToken, func() int64 { return 42 }, func(id string) (Page, bool) {
		return Page{Kind: KindFiles, Title: "📂 repo", Thread: 7, Dir: root}, id == "f1"
	}, nil).WithFiles(func() bool { return true }, nil)
	s.wait = 20 * time.Millisecond
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	release := make(chan struct{})
	defer close(release)
	_, _ = s.reads.do(filesKey(root, ""), 0, func() (any, error) {
		<-release
		return View{}, nil
	})
	req, _ := http.NewRequest("GET", srv.URL+"/api/files?id=f1", nil)
	req.Header.Set("X-Telegram-Init-Data", sign(t, fakeToken, fields(42, time.Now())))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	if msg, _ := body["error"].(string); res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(msg, "this file or folder") {
		t.Fatalf("while the read waits: %d %v", res.StatusCode, body)
	}
}
