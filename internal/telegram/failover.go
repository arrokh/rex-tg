package telegram

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
)

// switchAfter is the number of "not sent" failures in a row on the current
// route that make the client use the other route.
const switchAfter = 3

const (
	routeProxy  = 0
	routeDirect = 1
)

// failover picks the route of the next call. Only calls after a run of
// "not sent" failures change route; an unknown outcome neither counts nor
// resets the run, because it proves nothing about the route.
type failover struct {
	clients [2]*http.Client // routeProxy, routeDirect
	cur     atomic.Int32
	fails   [2]atomic.Int32
	log     *slog.Logger
}

func newFailover(proxy, direct *http.Client, log *slog.Logger) *failover {
	return &failover{clients: [2]*http.Client{proxy, direct}, log: log}
}

// current returns the route to use now and its client.
func (f *failover) current() (int, *http.Client) {
	r := int(f.cur.Load())
	return r, f.clients[r]
}

// report records the outcome of a call that used route r. A call that got
// an answer from Telegram (even a refusal) shows that the route works. A
// call that was not sent counts as a failure, unless the caller gave up.
func (f *failover) report(ctx context.Context, r int, err error) {
	var api *APIError
	switch {
	case err == nil || errors.As(err, &api):
		f.fails[r].Store(0)
	case Classify(err) == ClassNotSent && ctx.Err() == nil:
		if f.fails[r].Add(1) >= switchAfter && f.cur.CompareAndSwap(int32(r), int32(1-r)) {
			f.fails[r].Store(0)
			f.fails[1-r].Store(0)
			if f.log != nil {
				name := "proxy"
				if r == routeProxy {
					name = "direct"
				}
				f.log.Info("Telegram route: "+name, "reason", ClassNotSent.String())
			}
		}
	}
}
