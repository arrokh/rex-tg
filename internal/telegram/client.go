// Package telegram is a small Telegram Bot API client. It covers the methods
// rex-tg needs, reports Telegram refusals as *APIError, reports uncertain
// outcomes as *UnknownOutcomeError, and never puts the bot token in an error.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
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
	maxBodyBytes  = 8 << 20
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
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL sets the API base URL (default https://api.telegram.org).
func WithBaseURL(u string) Option { return func(c *Client) { c.base = strings.TrimRight(u, "/") } }

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
		hc:              &http.Client{},
		timeout:         DefaultTimeout,
		downloadTimeout: DefaultDownloadTimeout,
	}
	for _, o := range opts {
		o(c)
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

func (c *Client) unknown(method, format string, args ...any) error {
	return &UnknownOutcomeError{Method: method, Err: errors.New(c.scrub(fmt.Sprintf(format, args...)))}
}

// call sends one request and decodes the result into out (may be nil).
func (c *Client) call(ctx context.Context, method string, timeout time.Duration, contentType string, body []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return c.wrapErr(method, err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.hc.Do(req)
	if err != nil {
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
	return ups, err
}

// SendMessage sends a text message.
func (c *Client) SendMessage(ctx context.Context, p SendMessageParams) (Message, error) {
	body := struct {
		ChatID              int64                 `json:"chat_id"`
		MessageThreadID     int64                 `json:"message_thread_id,omitempty"`
		Text                string                `json:"text"`
		ParseMode           string                `json:"parse_mode,omitempty"`
		DisableNotification bool                  `json:"disable_notification,omitempty"`
		ReplyMarkup         *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
		ReplyParameters     *replyParameters      `json:"reply_parameters,omitempty"`
		LinkPreviewOptions  linkPreview           `json:"link_preview_options"`
	}{ChatID: p.ChatID, MessageThreadID: p.MessageThreadID, Text: p.Text, ParseMode: p.ParseMode,
		DisableNotification: p.DisableNotification, ReplyMarkup: p.ReplyMarkup,
		LinkPreviewOptions: linkPreview{true}}
	if p.ReplyToMessageID != 0 {
		body.ReplyParameters = &replyParameters{p.ReplyToMessageID}
	}
	var m Message
	err := c.callJSON(ctx, "sendMessage", c.timeout, body, &m)
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

// SetMyCommands sets the bot's command list.
func (c *Client) SetMyCommands(ctx context.Context, cmds []BotCommand) error {
	if cmds == nil {
		cmds = []BotCommand{}
	}
	body := struct {
		Commands []BotCommand `json:"commands"`
	}{cmds}
	return c.callJSON(ctx, "setMyCommands", c.timeout, body, nil)
}

// SendDocument uploads data as a document (multipart/form-data).
func (c *Client) SendDocument(ctx context.Context, chatID, threadID int64, filename string, data []byte, caption string, silent bool) (Message, error) {
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
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="document"; filename="%s"`,
		strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", "", "\n", "").Replace(filename)))
	h.Set("Content-Type", "application/octet-stream")
	part, err := mw.CreatePart(h)
	if err != nil {
		return Message{}, fmt.Errorf("telegram sendDocument: build request: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return Message{}, fmt.Errorf("telegram sendDocument: build request: %w", err)
	}
	if err := mw.Close(); err != nil {
		return Message{}, fmt.Errorf("telegram sendDocument: build request: %w", err)
	}
	var m Message
	err = c.call(ctx, "sendDocument", c.timeout, mw.FormDataContentType(), buf.Bytes(), &m)
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
	resp, err := c.hc.Do(req)
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
