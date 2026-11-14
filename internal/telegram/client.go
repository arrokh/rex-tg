// Package telegram is a small Telegram Bot API client. It covers the methods
// rex-tg needs, reports Telegram refusals as *APIError, reports uncertain
// outcomes as *UnknownOutcomeError (or *NotSentError when the request never
// left this machine), and never puts the bot token in an error.
package telegram

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// DefaultBaseURL is the public Bot API endpoint.
	DefaultBaseURL = "https://api.telegram.org"
	// DefaultTimeout is the per-call timeout.
	DefaultTimeout = 15 * time.Second
	// DefaultDownloadTimeout is the timeout of DownloadFile.
	DefaultDownloadTimeout = 60 * time.Second

	longPollSlack = 10 * time.Second

	dialTimeout       = 5 * time.Second
	handshakeTimeout  = 5 * time.Second
	keepAliveIdle     = 5 * time.Second
	keepAliveInterval = 3 * time.Second
	keepAliveCount    = 3
	maxBodyBytes      = 8 << 20
)

// ErrTooLarge is returned by DownloadFile when the file exceeds the limit.
var ErrTooLarge = errors.New("telegram: file exceeds the size limit")

// Client calls the Bot API. It is safe for concurrent use.
type Client struct {
	token           string
	base            string
	hc              *http.Client
	timeout         time.Duration
	downloadTimeout time.Duration
	onMessage       atomic.Pointer[func(chatID, messageID int64)]
	proxy           *url.URL
	fallback        bool
	log             *slog.Logger
	fo              *failover // nil unless a proxy and a fallback are set
	holds           holds     // methods that wait for a long 429 (hold.go)
	now             func() time.Time
}

// OnMessage sets a function that gets the chat and the number of every
// message that a call returns: each message the bot sends, and each message
// in an update. rex-tg records them to check sends whose outcome is unknown.
// A nil fn removes it.
func (c *Client) OnMessage(fn func(chatID, messageID int64)) {
	if fn == nil {
		c.onMessage.Store(nil)
		return
	}
	c.onMessage.Store(&fn)
}

// note passes a message's number to the OnMessage function.
func (c *Client) note(m *Message) {
	if f := c.onMessage.Load(); f != nil && m != nil && m.MessageID != 0 {
		(*f)(m.Chat.ID, m.MessageID)
	}
}

// newTransport is the HTTP transport of a Client: HTTP/1.1 only, with
// connections that drop the data they have not sent yet when they close
// (SO_LINGER 0). A request that timed out then cannot reach Telegram later,
// when the network comes back, because its connection is closed: the check
// of a send whose outcome is unknown relies on that (package sender). With
// HTTP/2, such a request could still be on its way in a shared connection.
//
// Dial and handshake time out after 5 s, and TCP keepalive finds a dead
// connection (an idle pooled one, or a long poll) in about 15 s, so rex-tg
// is back soon after a route to Telegram returns. A non-nil proxy replaces
// the proxy from the environment.
func newTransport(proxy *url.URL) *http.Transport {
	d := &net.Dialer{
		Timeout: dialTimeout,
		KeepAliveConfig: net.KeepAliveConfig{
			Enable:   true,
			Idle:     keepAliveIdle,
			Interval: keepAliveInterval,
			Count:    keepAliveCount,
		},
	}
	proxyFn := http.ProxyFromEnvironment
	if proxy != nil {
		proxyFn = http.ProxyURL(proxy)
	}
	return &http.Transport{
		Proxy: proxyFn,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			if tc, ok := conn.(*net.TCPConn); ok {
				_ = tc.SetLinger(0)
			}
			return conn, nil
		},
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{}, // no HTTP/2
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   handshakeTimeout,
		ExpectContinueTimeout: time.Second,
	}
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL sets the API base URL (default https://api.telegram.org).
func WithBaseURL(u string) Option { return func(c *Client) { c.base = strings.TrimRight(u, "/") } }

// WithProxy sends all requests through the proxy (http, https, socks5, or
// socks5h). nil keeps the proxy from the environment. It has no effect with
// WithHTTPClient.
func WithProxy(u *url.URL) Option { return func(c *Client) { c.proxy = u } }

// WithFallback lets the client switch between the proxy and the direct route
// when one of them keeps failing with "not sent" errors. It needs WithProxy
// and has no effect with WithHTTPClient.
func WithFallback(on bool) Option { return func(c *Client) { c.fallback = on } }

// WithLogger sets the logger of route switches (default: none).
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.log = l } }

// WithClock sets the clock that times the hold of a method after a long 429
// (default time.Now). Tests use it.
func WithClock(now func() time.Time) Option { return func(c *Client) { c.now = now } }

// WithHTTPClient sets the HTTP client. Timeouts are applied through contexts.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.hc = hc
		}
	}
}

// WithTimeout sets the per-call timeout (default 15 s). GetUpdates adds its
// long-poll seconds plus 10 s instead.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithDownloadTimeout sets the timeout of DownloadFile (default 60 s).
func WithDownloadTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.downloadTimeout = d
		}
	}
}

// New returns a Client for the bot token.
func New(token string, opts ...Option) *Client {
	c := &Client{
		token:           token,
		base:            DefaultBaseURL,
		timeout:         DefaultTimeout,
		downloadTimeout: DefaultDownloadTimeout,
		now:             time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	if c.hc == nil {
		c.hc = &http.Client{Transport: newTransport(c.proxy)}
		if c.proxy != nil && c.fallback {
			direct := newTransport(nil)
			direct.Proxy = nil // really direct: not the proxy from the environment
			c.fo = newFailover(c.hc, &http.Client{Transport: direct}, c.log)
		}
	}
	return c
}

type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter float64 `json:"retry_after"`
	} `json:"parameters"`
}

// scrub removes the token from s.
func (c *Client) scrub(s string) string {
	if c.token == "" {
		return s
	}
	s = strings.ReplaceAll(s, c.token, "<token>")
	return strings.ReplaceAll(s, url.PathEscape(c.token), "<token>")
}

// wrapErr turns a transport error into an *UnknownOutcomeError without the token.
func (c *Client) wrapErr(method string, err error) error {
	cause := err
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		cause = ue.Err
	}
	msg := c.scrub(cause.Error())
	return &UnknownOutcomeError{Method: method, Err: &sanitizedError{msg: msg, cause: cause}}
}

// notSent turns a transport error that came before the whole request was
// written into a *NotSentError without the token.
func (c *Client) notSent(method string, err error) error {
	cause := err
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		cause = ue.Err
	}
	return &NotSentError{Method: method, Err: &sanitizedError{msg: c.scrub(cause.Error()), cause: cause}}
}

func (c *Client) unknown(method, format string, args ...any) error {
	return &UnknownOutcomeError{Method: method, Err: errors.New(c.scrub(fmt.Sprintf(format, args...)))}
}

// call sends one request and decodes the result into out (may be nil).
// With a fallback route, a request that was not sent is tried once more on
// the other route at once: nothing was posted, so this cannot send it twice.
// Any other failure is returned as it is.
func (c *Client) call(ctx context.Context, method string, timeout time.Duration, contentType string, body []byte, out any) (err error) {
	if herr := c.held(method); herr != nil {
		return herr
	}
	defer func() { c.noteLimit(err) }()
	if c.fo == nil {
		return c.attempt(ctx, c.hc, method, timeout, contentType, body, out)
	}
	route, hc := c.fo.current()
	err = c.attempt(ctx, hc, method, timeout, contentType, body, out)
	c.fo.report(ctx, route, err)
	if Classify(err) != ClassNotSent || ctx.Err() != nil {
		return err
	}
	other := 1 - route
	err = c.attempt(ctx, c.fo.clients[other], method, timeout, contentType, body, out)
	c.fo.report(ctx, other, err)
	return err
}

// attempt sends one request through hc.
func (c *Client) attempt(ctx context.Context, hc *http.Client, method string, timeout time.Duration, contentType string, body []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return c.wrapErr(method, err)
	}
	req.Header.Set("Content-Type", contentType)
	// A failure before the whole request was written (no connection, a TLS
	// handshake timeout) cannot have reached Telegram, so it is "not sent"
	// rather than "unknown": the caller may send again safely.
	var wrote atomic.Bool
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				wrote.Store(true)
			}
		},
	}))
	resp, err := hc.Do(req)
	if err != nil {
		if !wrote.Load() {
			return c.notSent(method, err)
		}
		return c.wrapErr(method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return c.wrapErr(method, err)
	}
	var env envelope
	jsonErr := json.Unmarshal(raw, &env)
	if resp.StatusCode >= 500 {
		return c.unknown(method, "HTTP %d", resp.StatusCode)
	}
	if jsonErr == nil && !env.OK {
		code := env.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return &APIError{
			Method:      method,
			Code:        code,
			Description: c.scrub(env.Description),
			RetryAfter:  time.Duration(env.Parameters.RetryAfter * float64(time.Second)),
		}
	}
	if jsonErr != nil {
		if resp.StatusCode >= 400 {
			return &APIError{Method: method, Code: resp.StatusCode, Description: http.StatusText(resp.StatusCode)}
		}
		return c.unknown(method, "malformed response: %v", jsonErr)
	}
	if resp.StatusCode >= 400 {
		return c.unknown(method, "HTTP %d with ok body", resp.StatusCode)
	}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return c.unknown(method, "malformed result: %v", err)
		}
	}
	return nil
}

func (c *Client) callJSON(ctx context.Context, method string, timeout time.Duration, params any, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("telegram %s: encode request: %w", method, err)
	}
	return c.call(ctx, method, timeout, "application/json", body, out)
}

type linkPreview struct {
	IsDisabled bool `json:"is_disabled"`
}

type replyParameters struct {
	MessageID int64 `json:"message_id"`
	// AllowSendingWithoutReply sends the message also when the replied
	// message was deleted.
	AllowSendingWithoutReply bool `json:"allow_sending_without_reply"`
}

// GetMe returns the bot's own user.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var u User
	err := c.callJSON(ctx, "getMe", c.timeout, struct{}{}, &u)
	return u, err
}

// GetUpdates long-polls for updates. allowed lists update types; nil leaves
// Telegram's default. The HTTP timeout is timeoutSec plus 10 s.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSec int, allowed []string) ([]Update, error) {
	if timeoutSec < 0 {
		timeoutSec = 0
	}
	p := struct {
		Offset         int64    `json:"offset,omitempty"`
		Timeout        int      `json:"timeout"`
		AllowedUpdates []string `json:"allowed_updates,omitempty"`
	}{offset, timeoutSec, allowed}
	var ups []Update
	err := c.callJSON(ctx, "getUpdates", time.Duration(timeoutSec)*time.Second+longPollSlack, p, &ups)
	for i := range ups {
		c.note(ups[i].Message)
		c.note(ups[i].EditedMessage)
		if cb := ups[i].CallbackQuery; cb != nil {
			c.note(cb.Message)
		}
	}
	return ups, err
}

// SendMessage sends a text message.
func (c *Client) SendMessage(ctx context.Context, p SendMessageParams) (Message, error) {
	body := struct {
		ChatID              int64            `json:"chat_id"`
		MessageThreadID     int64            `json:"message_thread_id,omitempty"`
		Text                string           `json:"text"`
		ParseMode           string           `json:"parse_mode,omitempty"`
		DisableNotification bool             `json:"disable_notification,omitempty"`
		ReplyMarkup         any              `json:"reply_markup,omitempty"`
		ReplyParameters     *replyParameters `json:"reply_parameters,omitempty"`
		LinkPreviewOptions  linkPreview      `json:"link_preview_options"`
	}{ChatID: p.ChatID, MessageThreadID: p.MessageThreadID, Text: p.Text, ParseMode: p.ParseMode,
		DisableNotification: p.DisableNotification, LinkPreviewOptions: linkPreview{true}}
	// Only a nil interface is left out of the JSON, so a nil pointer is
	// never stored in it.
	switch {
	case p.ReplyMarkup != nil:
		body.ReplyMarkup = p.ReplyMarkup
	case p.Keyboard != nil:
		body.ReplyMarkup = p.Keyboard
	}
	if p.ReplyToMessageID != 0 {
		body.ReplyParameters = &replyParameters{MessageID: p.ReplyToMessageID, AllowSendingWithoutReply: true}
	}
	var m Message
	err := c.callJSON(ctx, "sendMessage", c.timeout, body, &m)
	if err == nil {
		c.note(&m)
	}
	return m, err
}

// SendRichMessage sends markdown as a rich message (Bot API 10.1,
// sendRichMessage). There is no plain-text fallback field: when Telegram
// rejects the markdown the caller must send the text another way.
func (c *Client) SendRichMessage(ctx context.Context, chatID, threadID int64, markdown string, silent bool, markup *InlineKeyboardMarkup) (Message, error) {
	type richMessage struct {
		Markdown string `json:"markdown"`
	}
	body := struct {
		ChatID              int64                 `json:"chat_id"`
		MessageThreadID     int64                 `json:"message_thread_id,omitempty"`
		RichMessage         richMessage           `json:"rich_message"`
		DisableNotification bool                  `json:"disable_notification,omitempty"`
		ReplyMarkup         *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
	}{chatID, threadID, richMessage{markdown}, silent, markup}
	var m Message
	err := c.callJSON(ctx, "sendRichMessage", c.timeout, body, &m)
	if err == nil {
		c.note(&m)
	}
	return m, err
}

// EditMessageText replaces the text (and keyboard) of a message.
func (c *Client) EditMessageText(ctx context.Context, p EditMessageTextParams) (Message, error) {
	body := struct {
		ChatID             int64                 `json:"chat_id"`
		MessageID          int64                 `json:"message_id"`
		Text               string                `json:"text"`
		ParseMode          string                `json:"parse_mode,omitempty"`
		ReplyMarkup        *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
		LinkPreviewOptions linkPreview           `json:"link_preview_options"`
	}{p.ChatID, p.MessageID, p.Text, p.ParseMode, p.ReplyMarkup, linkPreview{true}}
	var m Message
	err := c.callJSON(ctx, "editMessageText", c.timeout, body, &m)
	return m, err
}

// SendMessageDraft shows a partial message in a private chat while it is
// being written (Bot API 9.3). The draft is a temporary preview of about 30
// seconds; drafts with the same draftID (non-zero) are animated, and a real
// message from the bot ends it. It never becomes a message.
func (c *Client) SendMessageDraft(ctx context.Context, chatID, threadID, draftID int64, text, parseMode string) error {
	body := struct {
		ChatID          int64  `json:"chat_id"`
		MessageThreadID int64  `json:"message_thread_id,omitempty"`
		DraftID         int64  `json:"draft_id"`
		Text            string `json:"text"`
		ParseMode       string `json:"parse_mode,omitempty"`
	}{chatID, threadID, draftID, text, parseMode}
	return c.callJSON(ctx, "sendMessageDraft", c.timeout, body, nil)
}

// EditMessageReplyMarkup replaces the keyboard of a message; nil removes it.
func (c *Client) EditMessageReplyMarkup(ctx context.Context, chatID, messageID int64, markup *InlineKeyboardMarkup) error {
	body := struct {
		ChatID      int64                 `json:"chat_id"`
		MessageID   int64                 `json:"message_id"`
		ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
	}{chatID, messageID, markup}
	return c.callJSON(ctx, "editMessageReplyMarkup", c.timeout, body, nil)
}

// DeleteMessage deletes a message.
func (c *Client) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	body := struct {
		ChatID    int64 `json:"chat_id"`
		MessageID int64 `json:"message_id"`
	}{chatID, messageID}
	return c.callJSON(ctx, "deleteMessage", c.timeout, body, nil)
}

// SetMessageReaction sets one emoji reaction; "" clears reactions.
func (c *Client) SetMessageReaction(ctx context.Context, chatID, messageID int64, emoji string) error {
	type reaction struct {
		Type  string `json:"type"`
		Emoji string `json:"emoji"`
	}
	r := []reaction{}
	if emoji != "" {
		r = append(r, reaction{"emoji", emoji})
	}
	body := struct {
		ChatID    int64      `json:"chat_id"`
		MessageID int64      `json:"message_id"`
		Reaction  []reaction `json:"reaction"`
	}{chatID, messageID, r}
	return c.callJSON(ctx, "setMessageReaction", c.timeout, body, nil)
}

// SendChatAction shows an action such as "typing".
func (c *Client) SendChatAction(ctx context.Context, chatID, threadID int64, action string) error {
	body := struct {
		ChatID          int64  `json:"chat_id"`
		MessageThreadID int64  `json:"message_thread_id,omitempty"`
		Action          string `json:"action"`
	}{chatID, threadID, action}
	return c.callJSON(ctx, "sendChatAction", c.timeout, body, nil)
}

// CreateForumTopic creates a topic.
func (c *Client) CreateForumTopic(ctx context.Context, chatID int64, name string) (ForumTopic, error) {
	body := struct {
		ChatID int64  `json:"chat_id"`
		Name   string `json:"name"`
	}{chatID, name}
	var t ForumTopic
	err := c.callJSON(ctx, "createForumTopic", c.timeout, body, &t)
	return t, err
}

// EditForumTopic renames a topic.
func (c *Client) EditForumTopic(ctx context.Context, chatID, threadID int64, name string) error {
	body := struct {
		ChatID          int64  `json:"chat_id"`
		MessageThreadID int64  `json:"message_thread_id"`
		Name            string `json:"name"`
	}{chatID, threadID, name}
	return c.callJSON(ctx, "editForumTopic", c.timeout, body, nil)
}

// CloseForumTopic closes a topic.
func (c *Client) CloseForumTopic(ctx context.Context, chatID, threadID int64) error {
	body := struct {
		ChatID          int64 `json:"chat_id"`
		MessageThreadID int64 `json:"message_thread_id"`
	}{chatID, threadID}
	return c.callJSON(ctx, "closeForumTopic", c.timeout, body, nil)
}

// AnswerCallbackQuery answers a button press.
func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string, showAlert bool) error {
	body := struct {
		CallbackQueryID string `json:"callback_query_id"`
		Text            string `json:"text,omitempty"`
		ShowAlert       bool   `json:"show_alert,omitempty"`
	}{id, text, showAlert}
	return c.callJSON(ctx, "answerCallbackQuery", c.timeout, body, nil)
}

// SetMyCommands sets the bot's command list for a scope. A nil scope is the
// default scope.
func (c *Client) SetMyCommands(ctx context.Context, cmds []BotCommand, scope *BotCommandScope) error {
	if cmds == nil {
		cmds = []BotCommand{}
	}
	body := struct {
		Commands []BotCommand     `json:"commands"`
		Scope    *BotCommandScope `json:"scope,omitempty"`
	}{cmds, scope}
	return c.callJSON(ctx, "setMyCommands", c.timeout, body, nil)
}

// GetMyCommands returns the bot's command list for a scope. A nil scope is
// the default scope.
func (c *Client) GetMyCommands(ctx context.Context, scope *BotCommandScope) ([]BotCommand, error) {
	body := struct {
		Scope *BotCommandScope `json:"scope,omitempty"`
	}{scope}
	var out []BotCommand
	if err := c.callJSON(ctx, "getMyCommands", c.timeout, body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SendDocument uploads data as a document (multipart/form-data).
func (c *Client) SendDocument(ctx context.Context, chatID, threadID int64, filename string, data []byte, caption string, silent bool) (Message, error) {
	return c.upload(ctx, "sendDocument", "document", chatID, threadID, filename, data, caption, silent, nil)
}

// SendPhoto uploads data as a photo (multipart/form-data). Telegram checks
// the size and the shape of the picture and may answer 400 (for example
// PHOTO_INVALID_DIMENSIONS); then nothing was posted.
func (c *Client) SendPhoto(ctx context.Context, chatID, threadID int64, filename string, data []byte, caption string, silent bool) (Message, error) {
	return c.upload(ctx, "sendPhoto", "photo", chatID, threadID, filename, data, caption, silent, nil)
}

// SendPhotoMarkup is SendPhoto with buttons under the photo.
func (c *Client) SendPhotoMarkup(ctx context.Context, chatID, threadID int64, filename string, data []byte, caption string, silent bool, markup *InlineKeyboardMarkup) (Message, error) {
	return c.upload(ctx, "sendPhoto", "photo", chatID, threadID, filename, data, caption, silent, markup)
}

// uploadRate is the slowest upload speed that an upload is given time for:
// 256 KiB/s, so 10 MB get 40 s on top of the call timeout. A request that
// times out has an unknown outcome, so a large file on a slow link must not
// hit the timeout of a short message.
const uploadRate = 256 << 10

// uploadTimeout is the timeout of an upload of n bytes.
func uploadTimeout(base time.Duration, n int) time.Duration {
	return base + time.Duration(n)*time.Second/uploadRate
}

// upload sends a multipart request with one file in the given form field.
func (c *Client) upload(ctx context.Context, method, fileField string, chatID, threadID int64, filename string, data []byte, caption string, silent bool, markup *InlineKeyboardMarkup) (Message, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	field := func(k, v string) { _ = mw.WriteField(k, v) }
	field("chat_id", strconv.FormatInt(chatID, 10))
	if threadID != 0 {
		field("message_thread_id", strconv.FormatInt(threadID, 10))
	}
	if caption != "" {
		field("caption", caption)
	}
	if silent {
		field("disable_notification", "true")
	}
	if markup != nil {
		mk, err := json.Marshal(markup)
		if err != nil {
			return Message{}, fmt.Errorf("telegram %s: build request: %w", method, err)
		}
		field("reply_markup", string(mk))
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fileField,
		strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", "", "\n", "").Replace(filename)))
	h.Set("Content-Type", "application/octet-stream")
	part, err := mw.CreatePart(h)
	if err != nil {
		return Message{}, fmt.Errorf("telegram %s: build request: %w", method, err)
	}
	if _, err := part.Write(data); err != nil {
		return Message{}, fmt.Errorf("telegram %s: build request: %w", method, err)
	}
	if err := mw.Close(); err != nil {
		return Message{}, fmt.Errorf("telegram %s: build request: %w", method, err)
	}
	var m Message
	err = c.call(ctx, method, uploadTimeout(c.timeout, buf.Len()), mw.FormDataContentType(), buf.Bytes(), &m)
	if err == nil {
		c.note(&m)
	}
	return m, err
}

// GetFile returns the download path of a file.
func (c *Client) GetFile(ctx context.Context, fileID string) (File, error) {
	var f File
	err := c.callJSON(ctx, "getFile", c.timeout, struct {
		FileID string `json:"file_id"`
	}{fileID}, &f)
	return f, err
}

// DownloadFile copies the file at filePath (from GetFile) to w. It fails with
// ErrTooLarge when the file is longer than maxBytes; w may then hold a partial
// copy of at most maxBytes. maxBytes <= 0 means no limit.
func (c *Client) DownloadFile(ctx context.Context, filePath string, w io.Writer, maxBytes int64) error {
	const method = "downloadFile"
	ctx, cancel := context.WithTimeout(ctx, c.downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/file/bot"+c.token+"/"+strings.TrimLeft(filePath, "/"), nil)
	if err != nil {
		return c.wrapErr(method, err)
	}
	hc := c.hc
	if c.fo != nil {
		_, hc = c.fo.current() // follows the route, but a download does not count
	}
	resp, err := hc.Do(req)
	if err != nil {
		return c.wrapErr(method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return c.unknown(method, "HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return &APIError{Method: method, Code: resp.StatusCode, Description: http.StatusText(resp.StatusCode)}
	}
	if maxBytes > 0 && resp.ContentLength > maxBytes {
		return ErrTooLarge
	}
	var src io.Reader = resp.Body
	if maxBytes > 0 {
		src = io.LimitReader(resp.Body, maxBytes+1)
	}
	n, err := io.Copy(&limitedWriter{w: w, max: maxBytes}, src)
	if errors.Is(err, ErrTooLarge) || (maxBytes > 0 && n > maxBytes) {
		return ErrTooLarge
	}
	if err != nil {
		var we *writeError
		if errors.As(err, &we) {
			return fmt.Errorf("telegram %s: write: %w", method, we.err)
		}
		return c.wrapErr(method, err)
	}
	return nil
}

type writeError struct{ err error }

func (e *writeError) Error() string { return e.err.Error() }
func (e *writeError) Unwrap() error { return e.err }

// limitedWriter never passes more than max bytes (when max > 0) to w and
// marks errors from w so they are not mistaken for network errors.
type limitedWriter struct {
	w       io.Writer
	max     int64
	written int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.max > 0 && l.written+int64(len(p)) > l.max {
		p = p[:l.max-l.written]
		n, err := l.write(p)
		if err != nil {
			return n, err
		}
		return n, ErrTooLarge
	}
	return l.write(p)
}

func (l *limitedWriter) write(p []byte) (int, error) {
	n, err := l.w.Write(p)
	l.written += int64(n)
	if err != nil {
		return n, &writeError{err}
	}
	return n, nil
}
