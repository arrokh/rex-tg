package daemon

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"rex-tg/internal/rex"
)

type openEventsClient struct {
	rexClient
	events <-chan rex.Event
	errs   <-chan error
}

func (r openEventsClient) Events(context.Context) (<-chan rex.Event, <-chan error) {
	return r.events, r.errs
}

func TestRexEventLoopScansOnDroppedErrorWhileStreamIsOpen(t *testing.T) {
	events := make(chan rex.Event)
	errs := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	d := &daemon{
		rx:   openEventsClient{events: events, errs: errs},
		regq: newJobQueue(),
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:  time.Now,
	}
	go func() {
		d.rexEventLoop(ctx)
		close(done)
	}()
	closed := false
	t.Cleanup(func() {
		cancel()
		if !closed {
			close(events)
			close(errs)
		}
		<-done
	})

	errs <- &rex.DroppedError{Count: 1}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		d.regq.mu.Lock()
		queued := len(d.regq.items) > 0
		d.regq.mu.Unlock()
		if queued {
			cancel()
			close(events)
			close(errs)
			closed = true
			<-done
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("dropped event did not queue a scan while the event stream remained open")
}
