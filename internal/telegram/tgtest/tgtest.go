// Package tgtest is a fake Telegram Bot API server for tests. It keeps chats,
// topics, and messages in memory, records every call, and returns the errors
// that the real API returns for the cases rex-tg handles.
package tgtest

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"rex-tg/internal/telegram"
)

// Call is one recorded request.
type Call struct {
	Method string
	Params map[string]any // JSON numbers are float64; multipart values are strings
	At     time.Time
}

// Message is a message as the fake server stores it.
type Message struct {
	ID        int64
	ChatID    int64
	ThreadID  int64
	FromID    int64
	FromBot   bool
	Text      string // text, or caption for documents
	ParseMode string
	Markup    *telegram.InlineKeyboardMarkup
	Keyboard  *telegram.ReplyKeyboardMarkup // a reply keyboard; only sendMessage takes one
	Reaction  string
	Silent    bool
	Edits     int
	Deleted   bool
	Filename  string // documents only
	File      []byte // documents only
	Photo     bool   // sent with sendPhoto
	ReplyTo   int64  // the message that a sendMessage replied to (reply_parameters)
	Rich      bool   // sent with sendRichMessage: a reply to it carries no text (seen live)
}

// Topic is a forum topic.
type Topic struct {
	ChatID   int64
	ThreadID int64
	Name     string
	Closed   bool
	Deleted  bool
}

type failure struct {
	n          int
	code       int
	desc       string
	retryAfter int
}

type file struct {
	id   string
	path string
	data []byte
}

// Server is the fake Bot API.
type Server struct {
	ts    *httptest.Server
	token string

	mu            sync.Mutex
	topicsEnabled bool
	calls         []Call
	fails         map[string][]*failure
	lost          map[string]int // calls that are handled, but answered with 502
	answerDelays  map[string]time.Duration
	delays        map[string]time.Duration
	msgs          map[int64][]*Message // by chat
	nextMsg       map[int64]int64
	topics        []*Topic
	updates       []telegram.Update
	nextUpdate    int64
	notify        chan struct{}
	commands      map[string][]telegram.BotCommand // by scope, see scopeKey
	files         map[string]*file                 // by file_id
	nextFile      int
	nextCB        int
	done          chan struct{}
}

// New starts a fake server that is closed when the test ends.
func New(t testing.TB) *Server {
	s := &Server{
		token:         "123456:FAKE-test-token-not-real",
		topicsEnabled: true,
		fails:         map[string][]*failure{},
		lost:          map[string]int{},
		answerDelays:  map[string]time.Duration{},
		delays:        map[string]time.Duration{},
		msgs:          map[int64][]*Message{},
		nextMsg:       map[int64]int64{},
		files:         map[string]*file{},
		commands:      map[string][]telegram.BotCommand{},
		nextUpdate:    1,
		notify:        make(chan struct{}),
		done:          make(chan struct{}),
	}
	s.ts = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Close stops the server. It is safe to call more than once.
func (s *Server) Close() {
	s.mu.Lock()
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	s.mu.Unlock()
	s.ts.Close()
}

// URL is the base URL for telegram.WithBaseURL.
func (s *Server) URL() string { return s.ts.URL }

// Token is the bot token the server accepts.
func (s *Server) Token() string { return s.token }

// Client returns a telegram.Client connected to the server.
func (s *Server) Client(opts ...telegram.Option) *telegram.Client {
	return telegram.New(s.token, append([]telegram.Option{telegram.WithBaseURL(s.URL())}, opts...)...)
}

// SetTopicsEnabled sets has_topics_enabled in getMe and whether
// createForumTopic works (default true).
func (s *Server) SetTopicsEnabled(v bool) {
	s.mu.Lock()
	s.topicsEnabled = v
	s.mu.Unlock()
}

// FailNext makes the next n calls of method fail with the given code,
// description, and retry_after seconds (0 = none). Codes of 500 and above
// answer with that HTTP status.
func (s *Server) FailNext(method string, n, code int, description string, retryAfter int) {
	s.mu.Lock()
	s.fails[method] = append(s.fails[method], &failure{n, code, description, retryAfter})
	s.mu.Unlock()
}

// LoseAnswerNext handles the next n calls of method as usual, then answers
// them with 502 Bad Gateway: the call took effect, but the client cannot
// know it (an unknown outcome).
func (s *Server) LoseAnswerNext(method string, n int) {
	s.mu.Lock()
	s.lost[method] += n
	s.mu.Unlock()
}

// DelayAnswer makes every call of method take effect at once but answer only
// after d (0 removes it). When the client goes away meanwhile, the call has
// still taken effect: the case of a sender that dies before Telegram answers.
func (s *Server) DelayAnswer(method string, d time.Duration) {
	s.mu.Lock()
	s.answerDelays[method] = d
	s.mu.Unlock()
}

// Delay makes every call of method wait d before it is handled (0 removes it).
// A call whose client has gone away is dropped without effect.
func (s *Server) Delay(method string, d time.Duration) {
	s.mu.Lock()
	s.delays[method] = d
	s.mu.Unlock()
}

// Calls returns the recorded calls of method ("" returns all).
func (s *Server) Calls(method string) []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Call
	for _, c := range s.calls {
		if method == "" || c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// Messages returns copies of all messages in the chat, in order, including
// deleted ones (see Message.Deleted).
func (s *Server) Messages(chatID int64) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Message
	for _, m := range s.msgs[chatID] {
		out = append(out, *m)
	}
	return out
}

// Topics returns copies of the chat's topics.
func (s *Server) Topics(chatID int64) []Topic {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Topic
	for _, t := range s.topics {
		if t.ChatID == chatID {
			out = append(out, *t)
		}
	}
	return out
}

// Commands returns the commands last set by setMyCommands for the default
// scope.
func (s *Server) Commands() []telegram.BotCommand {
	return s.CommandsFor(telegram.BotCommandScope{Type: "default"})
}

// CommandsFor returns the commands last set by setMyCommands for a scope.
func (s *Server) CommandsFor(scope telegram.BotCommandScope) []telegram.BotCommand {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]telegram.BotCommand(nil), s.commands[scopeKey(scope)]...)
}

// SetCommandsFor sets a scope's command list, as another program might have.
func (s *Server) SetCommandsFor(scope telegram.BotCommandScope, cmds []telegram.BotCommand) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands[scopeKey(scope)] = append([]telegram.BotCommand(nil), cmds...)
}

func scopeKey(sc telegram.BotCommandScope) string {
	switch sc.Type {
	case "", "default":
		return "default"
	case "chat":
		return fmt.Sprintf("chat:%d", sc.ChatID)
	}
	return sc.Type
}

func scopeOf(p map[string]any) telegram.BotCommandScope {
	m, _ := p["scope"].(map[string]any)
	return telegram.BotCommandScope{Type: str(m, "type"), ChatID: num(m, "chat_id")}
}

// AddFile registers a downloadable file and returns its GetFile result.
func (s *Server) AddFile(fileID, filePath string, data []byte) telegram.File {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[fileID] = &file{fileID, filePath, data}
	return telegram.File{FileID: fileID, FileUniqueID: "u" + fileID, FileSize: int64(len(data)), FilePath: filePath}
}

// PushMessage queues a text message from a user and returns its update.
func (s *Server) PushMessage(chatID, threadID, fromID int64, text string) telegram.Update {
	s.mu.Lock()
	m := s.addMsg(&Message{ChatID: chatID, ThreadID: threadID, FromID: fromID, Text: text})
	tm := s.wire(m)
	tm.Text = text
	s.mu.Unlock()
	return s.PushUpdate(telegram.Update{Message: &tm})
}

// PushReply queues a user's text that replies to message replyTo, as
// Telegram sends it: reply_to_message holds that message with its text
// without markup, and quote the part the user quoted ("" for none). A
// replyTo equal to threadID is the topic's first message, which Telegram
// may name in reply_to_message of any message in a topic.
func (s *Server) PushReply(chatID, threadID, fromID, replyTo int64, quote, text string) telegram.Update {
	s.mu.Lock()
	var rm *telegram.Message
	if replyTo == threadID && threadID != 0 {
		rm = &telegram.Message{MessageID: threadID, MessageThreadID: threadID, Chat: telegram.Chat{ID: chatID, Type: "private"},
			From: &telegram.User{ID: 1, IsBot: true, FirstName: "TestBot", Username: "test_bot"}, Text: "",
			ForumTopicCreated: &telegram.ForumTopicCreated{Name: "topic"}}
	} else if old := s.find(chatID, replyTo); old != nil {
		w := s.wire(old)
		if old.Rich {
			w.Text = "" // Telegram gives a rich message's reply no text (seen live)
		}
		if old.ParseMode == "HTML" {
			if v, err := checkHTML(old.Text); err == nil {
				v = html.UnescapeString(v)
				if old.File != nil {
					w.Caption = v
				} else {
					w.Text = v
				}
			}
		}
		rm = &w
	}
	m := s.addMsg(&Message{ChatID: chatID, ThreadID: threadID, FromID: fromID, Text: text})
	tm := s.wire(m)
	tm.ReplyToMessage = rm
	if quote != "" {
		tm.Quote = &telegram.TextQuote{Text: quote, IsManual: true}
	}
	s.mu.Unlock()
	return s.PushUpdate(telegram.Update{Message: &tm})
}

// PushCallback queues a button press on messageID and returns its update.
func (s *Server) PushCallback(chatID, threadID, fromID, messageID int64, data string) telegram.Update {
	s.mu.Lock()
	s.nextCB++
	cb := &telegram.CallbackQuery{ID: "cb" + strconv.Itoa(s.nextCB), From: user(fromID), Data: data}
	if m := s.find(chatID, messageID); m != nil {
		tm := s.wire(m)
		cb.Message = &tm
	} else {
		cb.Message = &telegram.Message{MessageID: messageID, MessageThreadID: threadID, Chat: telegram.Chat{ID: chatID, Type: "private"}}
	}
	s.mu.Unlock()
	return s.PushUpdate(telegram.Update{CallbackQuery: cb})
}

// PushUpdate queues an update; a zero UpdateID is replaced by the next id.
// It returns the update as queued.
func (s *Server) PushUpdate(u telegram.Update) telegram.Update {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pushLocked(u)
}

// pushLocked is PushUpdate for a caller that holds s.mu.
func (s *Server) pushLocked(u telegram.Update) telegram.Update {
	if u.UpdateID == 0 {
		u.UpdateID = s.nextUpdate
	}
	if u.UpdateID >= s.nextUpdate {
		s.nextUpdate = u.UpdateID + 1
	}
	s.updates = append(s.updates, u)
	close(s.notify)
	s.notify = make(chan struct{})
	return u
}

// DeleteTopic deletes a topic and its messages. Later calls that use the
// thread fail with "message thread not found".
func (s *Server) DeleteTopic(chatID, threadID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, t := range s.topics {
		if t.ChatID == chatID && t.ThreadID == threadID {
			t.Deleted, found = true, true
		}
	}
	if !found {
		s.topics = append(s.topics, &Topic{ChatID: chatID, ThreadID: threadID, Deleted: true})
	}
	for _, m := range s.msgs[chatID] {
		if m.ThreadID == threadID {
			m.Deleted = true
		}
	}
}

// DeleteMessageOutside marks a message deleted as if the user removed it.
func (s *Server) DeleteMessageOutside(chatID, messageID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.find(chatID, messageID); m != nil {
		m.Deleted = true
	}
}

// ---- internals (callers hold s.mu) ----

func user(id int64) telegram.User { return telegram.User{ID: id, FirstName: "Tester"} }

func (s *Server) addMsg(m *Message) *Message {
	s.nextMsg[m.ChatID]++
	m.ID = s.nextMsg[m.ChatID]
	s.msgs[m.ChatID] = append(s.msgs[m.ChatID], m)
	return m
}

func (s *Server) find(chatID, id int64) *Message {
	for _, m := range s.msgs[chatID] {
		if m.ID == id {
			return m
		}
	}
	return nil
}

func (s *Server) topic(chatID, threadID int64) *Topic {
	for _, t := range s.topics {
		if t.ChatID == chatID && t.ThreadID == threadID {
			return t
		}
	}
	return nil
}

func (s *Server) threadGone(chatID, threadID int64) bool {
	t := s.topic(chatID, threadID)
	return threadID != 0 && t != nil && t.Deleted
}

func (s *Server) wire(m *Message) telegram.Message {
	out := telegram.Message{
		MessageID: m.ID, MessageThreadID: m.ThreadID, IsTopicMessage: m.ThreadID != 0,
		Chat: telegram.Chat{ID: m.ChatID, Type: "private"}, Date: time.Now().Unix(),
	}
	if m.FromBot {
		out.From = &telegram.User{ID: 1, IsBot: true, FirstName: "TestBot", Username: "test_bot"}
	} else {
		u := user(m.FromID)
		out.From = &u
	}
	if m.File != nil {
		out.Caption = m.Text
	} else {
		out.Text = m.Text
	}
	return out
}

type apiErr struct {
	code       int
	desc       string
	retryAfter int
}

func bad(desc string) *apiErr { return &apiErr{code: 400, desc: "Bad Request: " + desc} }

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	// File download: /file/bot<token>/<path>
	if prefix := "/file/bot" + s.token + "/"; strings.HasPrefix(r.URL.Path, prefix) {
		s.download(w, strings.TrimPrefix(r.URL.Path, prefix))
		return
	}
	prefix := "/bot" + s.token + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeJSON(w, 401, map[string]any{"ok": false, "error_code": 401, "description": "Unauthorized"})
		return
	}
	method := strings.TrimPrefix(r.URL.Path, prefix)
	params, docName, docData, err := parseRequest(r)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: " + err.Error()})
		return
	}

	s.mu.Lock()
	recorded := params
	if docData != nil {
		recorded = copyMap(params)
		key := "document"
		if method == "sendPhoto" {
			key = "photo"
		}
		recorded[key] = map[string]any{"filename": docName, "size": float64(len(docData))}
	}
	s.calls = append(s.calls, Call{Method: method, Params: recorded, At: time.Now()})
	delay := s.delays[method]
	s.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		case <-s.done:
			return
		}
	}

	s.mu.Lock()
	if q := s.fails[method]; len(q) > 0 {
		f := q[0]
		f.n--
		if f.n <= 0 {
			s.fails[method] = q[1:]
		}
		s.mu.Unlock()
		writeErr(w, &apiErr{f.code, f.desc, f.retryAfter})
		return
	}
	s.mu.Unlock()

	var result any
	var e *apiErr
	if method == "getUpdates" {
		result, e = s.getUpdates(r, params)
	} else {
		s.mu.Lock()
		result, e = s.handle(method, params, docName, docData)
		s.mu.Unlock()
	}
	if e != nil {
		writeErr(w, e)
		return
	}
	s.mu.Lock()
	lose := s.lost[method] > 0
	if lose {
		s.lost[method]--
	}
	answerDelay := s.answerDelays[method]
	s.mu.Unlock()
	if answerDelay > 0 {
		select {
		case <-time.After(answerDelay):
		case <-r.Context().Done():
			return // taken effect; the client is gone
		case <-s.done:
			return
		}
	}
	if lose {
		writeErr(w, &apiErr{502, "Bad Gateway", 0})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "result": result})
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func writeErr(w http.ResponseWriter, e *apiErr) {
	body := map[string]any{"ok": false, "error_code": e.code, "description": e.desc}
	if e.retryAfter > 0 {
		body["parameters"] = map[string]any{"retry_after": e.retryAfter}
	}
	status := e.code
	if status < 100 || status > 599 {
		status = 400
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func parseRequest(r *http.Request) (params map[string]any, docName string, docData []byte, err error) {
	params = map[string]any{}
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err = r.ParseMultipartForm(32 << 20); err != nil {
			return nil, "", nil, fmt.Errorf("bad multipart: %v", err)
		}
		for k, v := range r.MultipartForm.Value {
			if len(v) > 0 {
				params[k] = v[0]
			}
		}
		for k, fh := range r.MultipartForm.File {
			if len(fh) == 0 {
				continue
			}
			f, oerr := fh[0].Open()
			if oerr != nil {
				return nil, "", nil, oerr
			}
			b, rerr := io.ReadAll(f)
			f.Close()
			if rerr != nil {
				return nil, "", nil, rerr
			}
			if k == "document" || k == "photo" {
				docName, docData = fh[0].Filename, append([]byte{}, b...)
			}
		}
		return params, docName, docData, nil
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, "", nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return params, "", nil, nil
	}
	if err = json.Unmarshal(b, &params); err != nil {
		return nil, "", nil, fmt.Errorf("can't parse JSON: %v", err)
	}
	return params, "", nil, nil
}

func num(p map[string]any, k string) int64 {
	switch v := p[k].(type) {
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}

func str(p map[string]any, k string) string { s, _ := p[k].(string); return s }

func flag(p map[string]any, k string) bool {
	switch v := p[k].(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

// markupOf reads an inline keyboard. Edits take nothing else; the fake also
// refuses a reply keyboard on the other sends, which rex-tg never makes.
func markupOf(p map[string]any) (*telegram.InlineKeyboardMarkup, *apiErr) {
	mk, kb, e := sendMarkupOf(p)
	if e == nil && kb != nil {
		return nil, bad("inline keyboard expected")
	}
	return mk, e
}

// sendMarkupOf reads the reply_markup of sendMessage: an inline keyboard or
// a reply keyboard.
func sendMarkupOf(p map[string]any) (*telegram.InlineKeyboardMarkup, *telegram.ReplyKeyboardMarkup, *apiErr) {
	raw, ok := p["reply_markup"]
	if !ok || raw == nil {
		return nil, nil, nil
	}
	b, _ := json.Marshal(raw)
	if s, ok := raw.(string); ok { // a form field of an upload holds the JSON as text
		b = []byte(s)
	}
	var both struct {
		telegram.InlineKeyboardMarkup
		telegram.ReplyKeyboardMarkup
	}
	if err := json.Unmarshal(b, &both); err != nil {
		return nil, nil, bad("can't parse reply keyboard markup JSON object")
	}
	switch {
	case both.InlineKeyboard != nil:
		return &both.InlineKeyboardMarkup, nil, nil
	case len(both.Keyboard) > 0:
		return nil, &both.ReplyKeyboardMarkup, nil
	}
	return nil, nil, bad("can't parse reply keyboard markup JSON object")
}

// handle runs a non-getUpdates method. s.mu is held.
func (s *Server) handle(method string, p map[string]any, docName string, docData []byte) (any, *apiErr) {
	chat := num(p, "chat_id")
	switch method {
	case "getMe":
		return telegram.User{ID: 1, IsBot: true, FirstName: "TestBot", Username: "test_bot",
			HasTopicsEnabled: s.topicsEnabled, AllowsUsersToCreateTopics: s.topicsEnabled}, nil
	case "sendMessage":
		if chat == 0 {
			return nil, bad("chat_id is empty")
		}
		text := str(p, "text")
		if text == "" {
			return nil, bad("message text is empty")
		}
		thread := num(p, "message_thread_id")
		if s.threadGone(chat, thread) {
			return nil, bad("message thread not found")
		}
		if e := checkText(text, str(p, "parse_mode")); e != nil {
			return nil, e
		}
		mk, kb, e := sendMarkupOf(p)
		if e != nil {
			return nil, e
		}
		var replyTo int64
		if rp, ok := p["reply_parameters"].(map[string]any); ok {
			replyTo = num(rp, "message_id")
		}
		m := s.addMsg(&Message{ChatID: chat, ThreadID: thread, FromBot: true, Text: text,
			ParseMode: str(p, "parse_mode"), Markup: mk, Keyboard: kb, Silent: flag(p, "disable_notification"), ReplyTo: replyTo})
		out := s.wire(m)
		out.Text = text
		return out, nil
	case "sendRichMessage":
		if chat == 0 {
			return nil, bad("chat_id is empty")
		}
		rm, _ := p["rich_message"].(map[string]any)
		set := 0
		for _, k := range []string{"markdown", "html", "blocks"} {
			if v, ok := rm[k]; ok && v != nil {
				set++
			}
		}
		if set != 1 {
			return nil, bad("rich_message must have exactly one of markdown, html, or blocks")
		}
		text := str(rm, "markdown")
		if text == "" {
			return nil, bad("rich message is empty")
		}
		if len(utf16.Encode([]rune(text))) > 32768 {
			return nil, bad("rich message is too long")
		}
		thread := num(p, "message_thread_id")
		if s.threadGone(chat, thread) {
			return nil, bad("message thread not found")
		}
		mk, e := markupOf(p)
		if e != nil {
			return nil, e
		}
		m := s.addMsg(&Message{ChatID: chat, ThreadID: thread, FromBot: true, Text: text,
			Markup: mk, Silent: flag(p, "disable_notification"), Rich: true})
		out := s.wire(m)
		out.Text = text
		return out, nil
	case "editMessageText":
		m, e := s.editable(chat, num(p, "message_id"))
		if e != nil {
			return nil, e
		}
		text := str(p, "text")
		if text == "" {
			return nil, bad("message text is empty")
		}
		if e := checkText(text, str(p, "parse_mode")); e != nil {
			return nil, e
		}
		mk, e := markupOf(p)
		if e != nil {
			return nil, e
		}
		if text == m.Text && reflect.DeepEqual(mk, m.Markup) {
			return nil, notModified()
		}
		m.Text, m.ParseMode, m.Markup = text, str(p, "parse_mode"), mk
		m.Edits++
		out := s.wire(m)
		out.Text = text
		return out, nil
	case "editMessageReplyMarkup":
		m, e := s.editable(chat, num(p, "message_id"))
		if e != nil {
			return nil, e
		}
		mk, e := markupOf(p)
		if e != nil {
			return nil, e
		}
		if reflect.DeepEqual(mk, m.Markup) {
			return nil, notModified()
		}
		m.Markup = mk
		m.Edits++
		return s.wire(m), nil
	case "deleteMessage":
		m := s.find(chat, num(p, "message_id"))
		if m == nil || m.Deleted {
			return nil, bad("message to delete not found")
		}
		m.Deleted = true
		return true, nil
	case "setMessageReaction":
		m := s.find(chat, num(p, "message_id"))
		if m == nil || m.Deleted {
			return nil, bad("message to react not found")
		}
		m.Reaction = ""
		if list, _ := p["reaction"].([]any); len(list) > 0 {
			if first, _ := list[0].(map[string]any); first != nil {
				m.Reaction = str(first, "emoji")
			}
		}
		return true, nil
	case "sendMessageDraft":
		if num(p, "draft_id") == 0 {
			return nil, bad("draft_id must be non-zero")
		}
		if err := checkText(str(p, "text"), str(p, "parse_mode")); err != nil {
			return nil, err
		}
		if s.threadGone(chat, num(p, "message_thread_id")) {
			return nil, bad("message thread not found")
		}
		return true, nil
	case "sendChatAction":
		if str(p, "action") == "" {
			return nil, bad("action must be specified")
		}
		if s.threadGone(chat, num(p, "message_thread_id")) {
			return nil, bad("message thread not found")
		}
		return true, nil
	case "createForumTopic":
		if !s.topicsEnabled {
			return nil, bad("the chat is not a forum")
		}
		name := str(p, "name")
		if name == "" {
			return nil, bad("topic name must be non-empty")
		}
		// A topic id is the id of its service message, so it shares the counter.
		s.nextMsg[chat]++
		id := s.nextMsg[chat]
		s.topics = append(s.topics, &Topic{ChatID: chat, ThreadID: id, Name: name})
		// Telegram sends the bot its own "topic created" message as an update
		// (seen live). It is not kept with the chat's messages.
		s.pushLocked(telegram.Update{Message: &telegram.Message{
			MessageID: id, MessageThreadID: id, IsTopicMessage: true, Chat: telegram.Chat{ID: chat, Type: "private"},
			From: &telegram.User{ID: 1, IsBot: true, FirstName: "TestBot", Username: "test_bot"}, Date: time.Now().Unix(),
		}})
		return telegram.ForumTopic{MessageThreadID: id, Name: name}, nil
	case "editForumTopic", "closeForumTopic":
		t := s.topic(chat, num(p, "message_thread_id"))
		if t == nil || t.Deleted {
			return nil, bad("message thread not found")
		}
		if method == "closeForumTopic" {
			if t.Closed {
				return nil, bad("TOPIC_NOT_MODIFIED")
			}
			t.Closed = true
		} else if n := str(p, "name"); n != "" {
			if n == t.Name {
				return nil, bad("TOPIC_NOT_MODIFIED")
			}
			t.Name = n
		}
		return true, nil
	case "answerCallbackQuery":
		if str(p, "callback_query_id") == "" {
			return nil, bad("query ID must be specified")
		}
		return true, nil
	case "setMyCommands":
		raw, _ := json.Marshal(p["commands"])
		var cmds []telegram.BotCommand
		_ = json.Unmarshal(raw, &cmds)
		s.commands[scopeKey(scopeOf(p))] = cmds
		return true, nil
	case "getMyCommands":
		out := s.commands[scopeKey(scopeOf(p))]
		if out == nil {
			out = []telegram.BotCommand{}
		}
		return out, nil
	case "sendDocument":
		if chat == 0 {
			return nil, bad("chat_id is empty")
		}
		if docData == nil {
			return nil, bad("there is no document in the request")
		}
		thread := num(p, "message_thread_id")
		if s.threadGone(chat, thread) {
			return nil, bad("message thread not found")
		}
		s.nextFile++
		fid := "doc" + strconv.Itoa(s.nextFile)
		fpath := "documents/file_" + strconv.Itoa(s.nextFile) + "_" + docName
		s.files[fid] = &file{fid, fpath, docData}
		m := s.addMsg(&Message{ChatID: chat, ThreadID: thread, FromBot: true, Text: str(p, "caption"),
			Silent: flag(p, "disable_notification"), Filename: docName, File: docData})
		out := s.wire(m)
		out.Document = &telegram.Document{FileID: fid, FileName: docName, MimeType: "application/octet-stream", FileSize: int64(len(docData))}
		return out, nil
	case "sendPhoto":
		if chat == 0 {
			return nil, bad("chat_id is empty")
		}
		if docData == nil {
			return nil, bad("there is no photo in the request")
		}
		thread := num(p, "message_thread_id")
		if s.threadGone(chat, thread) {
			return nil, bad("message thread not found")
		}
		mk, merr := markupOf(p)
		if merr != nil {
			return nil, merr
		}
		s.nextFile++
		fid := "photo" + strconv.Itoa(s.nextFile)
		fpath := "photos/file_" + strconv.Itoa(s.nextFile) + "_" + docName
		s.files[fid] = &file{fid, fpath, docData}
		m := s.addMsg(&Message{ChatID: chat, ThreadID: thread, FromBot: true, Text: str(p, "caption"),
			Silent: flag(p, "disable_notification"), Filename: docName, File: docData, Photo: true, Markup: mk})
		out := s.wire(m)
		out.Photo = []telegram.PhotoSize{{FileID: fid, FileUniqueID: "u" + fid, FileSize: int64(len(docData))}}
		return out, nil
	case "getFile":
		f := s.files[str(p, "file_id")]
		if f == nil {
			return nil, bad("invalid file_id")
		}
		return telegram.File{FileID: f.id, FileUniqueID: "u" + f.id, FileSize: int64(len(f.data)), FilePath: f.path}, nil
	}
	return nil, &apiErr{code: 404, desc: "Not Found"}
}

func notModified() *apiErr {
	return bad("message is not modified: specified new message content and reply markup are exactly the same as a current content and reply markup of the message")
}

func (s *Server) editable(chat, id int64) (*Message, *apiErr) {
	m := s.find(chat, id)
	if m == nil || m.Deleted {
		return nil, bad("message to edit not found")
	}
	if !m.FromBot {
		return nil, bad("message can't be edited")
	}
	return m, nil
}

func (s *Server) getUpdates(r *http.Request, p map[string]any) (any, *apiErr) {
	offset := num(p, "offset")
	deadline := time.After(time.Duration(num(p, "timeout")) * time.Second)
	for {
		s.mu.Lock()
		if offset != 0 {
			kept := s.updates[:0]
			for _, u := range s.updates {
				if u.UpdateID >= offset {
					kept = append(kept, u)
				}
			}
			s.updates = kept
		}
		out := []telegram.Update{}
		out = append(out, s.updates...)
		ch := s.notify
		s.mu.Unlock()
		if len(out) > 0 {
			return out, nil
		}
		select {
		case <-ch:
		case <-deadline:
			return []telegram.Update{}, nil
		case <-r.Context().Done():
			return nil, &apiErr{code: 499, desc: "client closed request"}
		case <-s.done:
			return []telegram.Update{}, nil
		}
	}
}

func (s *Server) download(w http.ResponseWriter, path string) {
	s.mu.Lock()
	var data []byte
	found := false
	for _, f := range s.files {
		if f.path == path {
			data, found = f.data, true
		}
	}
	s.calls = append(s.calls, Call{Method: "downloadFile", Params: map[string]any{"file_path": path}, At: time.Now()})
	s.mu.Unlock()
	if !found {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// ---- HTML check ----

var allowedTags = map[string]bool{
	"b": true, "strong": true, "i": true, "em": true, "u": true, "ins": true,
	"s": true, "strike": true, "del": true, "a": true, "code": true, "pre": true,
	"blockquote": true, "tg-spoiler": true, "span": true, "tg-emoji": true,
}

// checkText validates HTML tag balance and the 4096 UTF-16 limit on visible text.
func checkText(text, parseMode string) *apiErr {
	visible := text
	if parseMode == "HTML" {
		v, err := checkHTML(text)
		if err != nil {
			return bad("can't parse entities: " + err.Error())
		}
		visible = v
	}
	if len(utf16.Encode([]rune(visible))) > 4096 {
		return bad("message is too long")
	}
	return nil
}

// checkHTML returns the text without tags, or an error shaped like Telegram's.
func checkHTML(s string) (string, error) {
	var stack []string
	var vis strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '<' {
			vis.WriteByte(s[i])
			i++
			continue
		}
		end := strings.IndexByte(s[i:], '>')
		if end < 0 {
			return "", fmt.Errorf("unclosed start tag at byte offset %d", i)
		}
		body := s[i+1 : i+end]
		closing := strings.HasPrefix(body, "/")
		name := strings.TrimPrefix(body, "/")
		if k := strings.IndexAny(name, " \t\r\n"); k >= 0 {
			if closing {
				return "", fmt.Errorf("unsupported end tag at byte offset %d", i)
			}
			name = name[:k]
		}
		name = strings.ToLower(name)
		if name == "" || !allowedTags[name] {
			kind := "start"
			if closing {
				kind = "end"
			}
			return "", fmt.Errorf("unsupported %s tag %q at byte offset %d", kind, name, i)
		}
		if closing {
			if len(stack) == 0 || stack[len(stack)-1] != name {
				return "", fmt.Errorf("unmatched end tag at byte offset %d, expected \"</%s>\", found \"</%s>\"", i, top(stack), name)
			}
			stack = stack[:len(stack)-1]
		} else {
			stack = append(stack, name)
		}
		i += end + 1
	}
	if len(stack) > 0 {
		return "", fmt.Errorf("can't find end tag corresponding to start tag %q", stack[len(stack)-1])
	}
	return vis.String(), nil
}

func top(stack []string) string {
	if len(stack) == 0 {
		return ""
	}
	return stack[len(stack)-1]
}
