package telegram

import (
	"errors"
	"sync"
	"time"
)

// HoldAfter is the shortest 429 wait that holds the whole chat in the client.
//
// A shorter wait is the chat's usual limit of about one message a second;
// the caller pauses its requests for it. A long wait is a penalty. On
// 2026-11-14 Telegram asked rex-tg to wait about 95 minutes, and
// sendMessage, sendRichMessage and editMessageText then all answered 429
// with the same time left: the wait covered the chat, not one method. During
// such a wait the client refuses every request that posts to the chat
// itself, without asking Telegram, because a request during the wait may
// make it longer. Requests that do not post (getUpdates, getMe, getFile, the
// command menu) still go. The refusal is an *APIError with code 429, Held
// set, and the time left as RetryAfter. Nothing was posted.
const HoldAfter = 5 * time.Minute

// Hold is a long wait that Telegram asked for: the request that met it, when
// rex-tg met it, and when it ends.
type Hold struct {
	Method string    `json:"method"`
	Since  time.Time `json:"since"`
	Until  time.Time `json:"until"`
}

// unheld are the methods that a hold lets through: they post nothing to the
// chat.
var unheld = map[string]bool{
	"getUpdates": true, "getMe": true, "getFile": true,
	"setMyCommands": true, "getMyCommands": true, "deleteMyCommands": true,
	"deleteWebhook": true, "getWebhookInfo": true,
}

type holds struct {
	mu sync.Mutex
	h  Hold
	on func(Hold)
}

// held returns the refusal for a call during a hold, or nil.
func (c *Client) held(method string) error {
	if unheld[method] {
		return nil
	}
	c.holds.mu.Lock()
	defer c.holds.mu.Unlock()
	left := c.holds.h.Until.Sub(c.now())
	if left <= 0 {
		return nil
	}
	return &APIError{Method: method, Code: 429, Description: "Too Many Requests: waiting for Telegram's retry_after", RetryAfter: left, Held: true, Local: true}
}

// noteLimit logs a 429 from Telegram and, when the wait is long, holds the
// chat until it ends.
func (c *Client) noteLimit(err error) {
	var api *APIError
	if !errors.As(err, &api) || api.Code != 429 || api.Local {
		return
	}
	var (
		fn      func(Hold)
		h       Hold
		changed bool
	)
	if api.RetryAfter >= HoldAfter && !unheld[api.Method] {
		api.Held = true
		now := c.now()
		until := now.Add(api.RetryAfter)
		c.holds.mu.Lock()
		// Two calls may overlap: the longer wait wins, and a hold that is
		// still on keeps its start.
		if until.After(c.holds.h.Until) {
			since := now
			if c.holds.h.Until.After(now) {
				since = c.holds.h.Since
			}
			c.holds.h = Hold{Method: api.Method, Since: since, Until: until}
			changed = true
		}
		h, fn = c.holds.h, c.holds.on
		c.holds.mu.Unlock()
	}
	if c.log != nil {
		c.log.Warn("Telegram rate limit", "method", api.Method, "retry_after", api.RetryAfter.String(), "held", api.Held)
	}
	if changed && fn != nil {
		fn(h)
	}
}

// CurrentHold returns the hold in force, if any.
func (c *Client) CurrentHold() (Hold, bool) {
	c.holds.mu.Lock()
	defer c.holds.mu.Unlock()
	h := c.holds.h
	return h, h.Until.After(c.now())
}

// SetHold puts back a hold that an earlier run met, so that a restart does
// not ask Telegram during the wait. A shorter or ended hold changes nothing.
func (c *Client) SetHold(h Hold) {
	c.holds.mu.Lock()
	defer c.holds.mu.Unlock()
	if h.Until.After(c.holds.h.Until) && h.Until.After(c.now()) {
		c.holds.h = h
	}
}

// OnHold sets a function that gets each new or longer hold, for example to
// keep it across a restart. It is called without locks held. nil removes it.
func (c *Client) OnHold(fn func(Hold)) {
	c.holds.mu.Lock()
	c.holds.on = fn
	c.holds.mu.Unlock()
}
