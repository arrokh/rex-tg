package telegram

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// HoldAfter is the shortest 429 wait that holds only its method.
//
// A shorter wait is the chat's limit of about one message a second, which
// covers every method, and the caller pauses all its requests. But Telegram can also ask a bot to wait an
// hour or more for one method, such as rich messages, while it still takes
// the others (seen live). Then the client refuses that method itself until
// the wait ends, without asking Telegram again: a request during the wait
// could make it longer. The refusal is an *APIError with code 429, Held set,
// and the time left as RetryAfter. Nothing was posted.
const HoldAfter = 5 * time.Minute

// Hold is a method that waits for Telegram.
type Hold struct {
	Method string
	Until  time.Time
}

type holds struct {
	mu    sync.Mutex
	until map[string]time.Time
}

// held returns the refusal for a call of a held method, or nil.
func (c *Client) held(method string) error {
	c.holds.mu.Lock()
	defer c.holds.mu.Unlock()
	until, ok := c.holds.until[method]
	if !ok {
		return nil
	}
	left := until.Sub(c.now())
	if left <= 0 {
		delete(c.holds.until, method)
		return nil
	}
	return &APIError{Method: method, Code: 429, Description: "Too Many Requests: waiting for Telegram's retry_after", RetryAfter: left, Held: true}
}

// noteLimit logs a 429 from Telegram and holds its method when the wait is
// long.
func (c *Client) noteLimit(err error) {
	var api *APIError
	if !errors.As(err, &api) || api.Code != 429 || api.Held {
		return
	}
	if api.RetryAfter >= HoldAfter {
		api.Held = true
		c.holds.mu.Lock()
		if c.holds.until == nil {
			c.holds.until = map[string]time.Time{}
		}
		// Two calls may overlap: the longer wait wins.
		if until := c.now().Add(api.RetryAfter); until.After(c.holds.until[api.Method]) {
			c.holds.until[api.Method] = until
		}
		c.holds.mu.Unlock()
	}
	if c.log != nil {
		c.log.Warn("Telegram rate limit", "method", api.Method, "retry_after", api.RetryAfter.String(), "held", api.Held)
	}
}

// Holds returns the methods that wait for Telegram, the soonest end first.
func (c *Client) Holds() []Hold {
	c.holds.mu.Lock()
	defer c.holds.mu.Unlock()
	var out []Hold
	now := c.now()
	for m, until := range c.holds.until {
		if until.After(now) {
			out = append(out, Hold{Method: m, Until: until})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Until.Before(out[j].Until) })
	return out
}
