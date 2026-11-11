package daemon

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
)

// A child process forked for a rex call holds a copy of every open file until
// it runs exec. The lock must be free once the daemon released it, even while
// such a copy is still open.
func TestLockIsFreeAfterReleaseWhileACopyIsOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	first, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLock(path); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second lock = %v, want ErrAlreadyRunning", err)
	}
	copyFD, err := syscall.Dup(int(first.Fd())) // what a forked child holds
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(copyFD)

	releaseLock(first)
	again, err := acquireLock(path)
	if err != nil {
		t.Fatalf("lock after release, with a copy open: %v", err)
	}
	releaseLock(again)
}
