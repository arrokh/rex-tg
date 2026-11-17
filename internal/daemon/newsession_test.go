package daemon_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"rex-tg/internal/daemon"
)

func TestNewSessionLockSerializesCallers(t *testing.T) {
	stateDir := t.TempDir()
	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	defer release()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- daemon.WithNewSessionLock(context.Background(), stateDir, func() error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	secondDone := make(chan error, 1)
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		secondDone <- daemon.WithNewSessionLock(context.Background(), stateDir, func() error {
			close(secondEntered)
			return nil
		})
	}()
	<-secondStarted
	select {
	case <-secondEntered:
		t.Fatal("second caller entered while the first held the lock")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}
