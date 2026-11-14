package telegram_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"rex-tg/internal/telegram"
	"rex-tg/internal/telegram/tgtest"
)

const chat = int64(42)

func setup(t *testing.T, opts ...telegram.Option) (*tgtest.Server, *telegram.Client) {
	t.Helper()
	s := tgtest.New(t)
	return s, s.Client(opts...)
}

func send(t *testing.T, c *telegram.Client, thread int64, text string) telegram.Message {
	t.Helper()
	m, err := c.SendMessage(context.Background(), telegram.SendMessageParams{ChatID: chat, MessageThreadID: thread, Text: text})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	return m
}

func TestGetMe(t *testing.T) {
	s, c := setup(t)
	u, err := c.GetMe(context.Background())
	if err != nil || !u.IsBot || !u.HasTopicsEnabled {
		t.Fatalf("GetMe = %+v, %v", u, err)
	}
	s.SetTopicsEnabled(false)
	u, _ = c.GetMe(context.Background())
	if u.HasTopicsEnabled {
		t.Fatal("topics should be disabled")
	}
}

func TestSendMessageFields(t *testing.T) {
	s, c := setup(t)
	kb := &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{{Text: "Go", CallbackData: "go"}, {Text: "Docs", URL: "https://example.com"}}}}
	m, err := c.SendMessage(context.Background(), telegram.SendMessageParams{
		ChatID: chat, MessageThreadID: 7, Text: "<b>hi</b>", ParseMode: "HTML",
		DisableNotification: true, ReplyMarkup: kb, ReplyToMessageID: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.MessageID == 0 || m.Chat.ID != chat || m.MessageThreadID != 7 {
		t.Fatalf("message = %+v", m)
	}
	calls := s.Calls("sendMessage")
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	p := calls[0].Params
	if p["chat_id"] != float64(chat) || p["message_thread_id"] != float64(7) || p["parse_mode"] != "HTML" || p["disable_notification"] != true {
		t.Fatalf("params = %v", p)
	}
	if lp, _ := p["link_preview_options"].(map[string]any); lp["is_disabled"] != true {
		t.Fatalf("link_preview_options = %v", p["link_preview_options"])
	}
	if rp, _ := p["reply_parameters"].(map[string]any); rp["message_id"] != float64(3) {
		t.Fatalf("reply_parameters = %v", p["reply_parameters"])
	}
	stored := s.Messages(chat)
	if len(stored) != 1 || stored[0].Markup == nil || stored[0].Markup.InlineKeyboard[0][1].URL != "https://example.com" || !stored[0].Silent {
		t.Fatalf("stored = %+v", stored)
	}
}

// A reply keyboard goes out as reply_markup when there is no inline
// keyboard; an inline keyboard wins.
func TestSendMessageReplyKeyboard(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	kb := &telegram.ReplyKeyboardMarkup{Keyboard: [][]telegram.KeyboardButton{{{Text: "/rt"}, {Text: "/screen"}}}, IsPersistent: true, ResizeKeyboard: true}
	if _, err := c.SendMessage(ctx, telegram.SendMessageParams{ChatID: chat, Text: "keys", Keyboard: kb}); err != nil {
		t.Fatal(err)
	}
	rm, _ := s.Calls("sendMessage")[0].Params["reply_markup"].(map[string]any)
	if rm["is_persistent"] != true || rm["resize_keyboard"] != true || rm["inline_keyboard"] != nil {
		t.Fatalf("reply_markup = %v", rm)
	}
	got := s.Messages(chat)[0]
	if got.Markup != nil || got.Keyboard == nil || got.Keyboard.Keyboard[0][1].Text != "/screen" {
		t.Fatalf("stored = %+v", got)
	}
	inline := &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{{Text: "Go", CallbackData: "go"}}}}
	if _, err := c.SendMessage(ctx, telegram.SendMessageParams{ChatID: chat, Text: "both", ReplyMarkup: inline, Keyboard: kb}); err != nil {
		t.Fatal(err)
	}
	if got := s.Messages(chat)[1]; got.Markup == nil || got.Keyboard != nil {
		t.Fatalf("both: stored = %+v", got)
	}
	if _, err := c.SendMessage(ctx, telegram.SendMessageParams{ChatID: chat, Text: "none"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Calls("sendMessage")[2].Params["reply_markup"]; ok {
		t.Fatal("reply_markup sent without a keyboard")
	}
}

func TestEditMessageText(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	m := send(t, c, 0, "one")
	if _, err := c.EditMessageText(ctx, telegram.EditMessageTextParams{ChatID: chat, MessageID: m.MessageID, Text: "two"}); err != nil {
		t.Fatal(err)
	}
	if got := s.Messages(chat)[0]; got.Text != "two" || got.Edits != 1 {
		t.Fatalf("stored = %+v", got)
	}
	p := s.Calls("editMessageText")[0].Params
	if lp, _ := p["link_preview_options"].(map[string]any); lp["is_disabled"] != true {
		t.Fatalf("link_preview_options = %v", p["link_preview_options"])
	}
	_, err := c.EditMessageText(ctx, telegram.EditMessageTextParams{ChatID: chat, MessageID: m.MessageID, Text: "two"})
	if telegram.Classify(err) != telegram.ClassNotModified {
		t.Fatalf("identical edit: %v", err)
	}
	_, err = c.EditMessageText(ctx, telegram.EditMessageTextParams{ChatID: chat, MessageID: 999, Text: "x"})
	if telegram.Classify(err) != telegram.ClassEditTargetGone {
		t.Fatalf("missing edit: %v", err)
	}
}

func TestEditMessageReplyMarkup(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	m := send(t, c, 0, "x")
	kb := &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{{Text: "A", CallbackData: "a"}}}}
	if err := c.EditMessageReplyMarkup(ctx, chat, m.MessageID, kb); err != nil {
		t.Fatal(err)
	}
	if s.Messages(chat)[0].Markup == nil {
		t.Fatal("markup not stored")
	}
	if err := c.EditMessageReplyMarkup(ctx, chat, m.MessageID, kb); telegram.Classify(err) != telegram.ClassNotModified {
		t.Fatalf("same markup: %v", err)
	}
	if err := c.EditMessageReplyMarkup(ctx, chat, m.MessageID, nil); err != nil {
		t.Fatal(err)
	}
	if s.Messages(chat)[0].Markup != nil {
		t.Fatal("markup not removed")
	}
}

func TestDeleteMessage(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	m := send(t, c, 0, "x")
	if err := c.DeleteMessage(ctx, chat, m.MessageID); err != nil {
		t.Fatal(err)
	}
	if !s.Messages(chat)[0].Deleted {
		t.Fatal("not deleted")
	}
	if err := c.DeleteMessage(ctx, chat, m.MessageID); telegram.Classify(err) != telegram.ClassDeleteTargetGone {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := c.EditMessageText(ctx, telegram.EditMessageTextParams{ChatID: chat, MessageID: m.MessageID, Text: "y"}); telegram.Classify(err) != telegram.ClassEditTargetGone {
		t.Fatalf("edit deleted: %v", err)
	}
}

func TestSetMessageReaction(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	m := send(t, c, 0, "x")
	if err := c.SetMessageReaction(ctx, chat, m.MessageID, "👀"); err != nil {
		t.Fatal(err)
	}
	if got := s.Messages(chat)[0].Reaction; got != "👀" {
		t.Fatalf("reaction = %q", got)
	}
	if err := c.SetMessageReaction(ctx, chat, m.MessageID, ""); err != nil {
		t.Fatal(err)
	}
	if got := s.Messages(chat)[0].Reaction; got != "" {
		t.Fatalf("reaction = %q", got)
	}
	r, ok := s.Calls("setMessageReaction")[1].Params["reaction"].([]any)
	if !ok || len(r) != 0 {
		t.Fatalf("clearing must send [], got %#v", s.Calls("setMessageReaction")[1].Params["reaction"])
	}
}

func TestChatActionAndCallbackAndCommands(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	if err := c.SendChatAction(ctx, chat, 5, "typing"); err != nil {
		t.Fatal(err)
	}
	if p := s.Calls("sendChatAction")[0].Params; p["action"] != "typing" || p["message_thread_id"] != float64(5) {
		t.Fatalf("params = %v", p)
	}
	if err := c.AnswerCallbackQuery(ctx, "cb1", "Done", true); err != nil {
		t.Fatal(err)
	}
	if p := s.Calls("answerCallbackQuery")[0].Params; p["callback_query_id"] != "cb1" || p["text"] != "Done" || p["show_alert"] != true {
		t.Fatalf("params = %v", p)
	}
	cmds := []telegram.BotCommand{{Command: "status", Description: "Show status"}}
	if err := c.SetMyCommands(ctx, cmds, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.Commands(); len(got) != 1 || got[0] != cmds[0] {
		t.Fatalf("commands = %v", got)
	}
	// A list for one chat is separate from the default list.
	mine := &telegram.BotCommandScope{Type: "chat", ChatID: chat}
	both := append(cmds, telegram.BotCommand{Command: "help", Description: "Help"})
	if err := c.SetMyCommands(ctx, both, mine); err != nil {
		t.Fatal(err)
	}
	if p := s.Calls("setMyCommands")[1].Params; fmt.Sprint(p["scope"]) != fmt.Sprintf("map[chat_id:%d type:chat]", chat) {
		t.Fatalf("scope sent = %v", p["scope"])
	}
	got, err := c.GetMyCommands(ctx, mine)
	if err != nil || len(got) != 2 {
		t.Fatalf("chat commands = %v, %v", got, err)
	}
	if got, err := c.GetMyCommands(ctx, nil); err != nil || len(got) != 1 {
		t.Fatalf("default commands = %v, %v", got, err)
	}
}

func TestForumTopics(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	a, err := c.CreateForumTopic(ctx, chat, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := c.CreateForumTopic(ctx, chat, "beta")
	if b.MessageThreadID <= a.MessageThreadID || a.Name != "alpha" {
		t.Fatalf("topics = %+v %+v", a, b)
	}
	if err := c.EditForumTopic(ctx, chat, a.MessageThreadID, "renamed"); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseForumTopic(ctx, chat, a.MessageThreadID); err != nil {
		t.Fatal(err)
	}
	topics := s.Topics(chat)
	if topics[0].Name != "renamed" || !topics[0].Closed || topics[1].Closed {
		t.Fatalf("topics = %+v", topics)
	}
	s.DeleteTopic(chat, b.MessageThreadID)
	for name, err := range map[string]error{
		"send": func() error {
			_, e := c.SendMessage(ctx, telegram.SendMessageParams{ChatID: chat, MessageThreadID: b.MessageThreadID, Text: "x"})
			return e
		}(),
		"action": c.SendChatAction(ctx, chat, b.MessageThreadID, "typing"),
		"edit":   c.EditForumTopic(ctx, chat, b.MessageThreadID, "z"),
		"close":  c.CloseForumTopic(ctx, chat, b.MessageThreadID),
	} {
		if telegram.Classify(err) != telegram.ClassThreadNotFound {
			t.Errorf("%s after DeleteTopic: %v", name, err)
		}
	}
	s.SetTopicsEnabled(false)
	if _, err := c.CreateForumTopic(ctx, chat, "no"); telegram.Classify(err) != telegram.ClassBadRequest {
		t.Fatalf("create without topics: %v", err)
	}
}

func TestParseEntities(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	ok := []string{"<b>x</b>", "<b><i>x</i></b> &amp; <code>y</code>", `<a href="https://e.com">l</a>`, `<pre><code class="language-go">x</code></pre>`, "plain & text"}
	for _, h := range ok {
		if _, err := c.SendMessage(ctx, telegram.SendMessageParams{ChatID: chat, Text: h, ParseMode: "HTML"}); err != nil {
			t.Errorf("%q: %v", h, err)
		}
	}
	bad := []string{"<b>x", "x</b>", "<b><i>x</b></i>", "<blink>x</blink>", "a < b"}
	for _, h := range bad {
		_, err := c.SendMessage(ctx, telegram.SendMessageParams{ChatID: chat, Text: h, ParseMode: "HTML"})
		if telegram.Classify(err) != telegram.ClassParseEntities {
			t.Errorf("%q: class %v err %v", h, telegram.Classify(err), err)
		}
	}
	if len(s.Messages(chat)) != len(ok) {
		t.Fatalf("rejected messages were stored")
	}
	// Plain text is never parsed.
	if _, err := c.SendMessage(ctx, telegram.SendMessageParams{ChatID: chat, Text: "<b>x"}); err != nil {
		t.Fatal(err)
	}
	m := send(t, c, 0, "base")
	_, err := c.EditMessageText(ctx, telegram.EditMessageTextParams{ChatID: chat, MessageID: m.MessageID, Text: "<b>x", ParseMode: "HTML"})
	if telegram.Classify(err) != telegram.ClassParseEntities {
		t.Fatalf("edit: %v", err)
	}
}

func TestClassify(t *testing.T) {
	api := func(code int, d string) error { return &telegram.APIError{Method: "m", Code: code, Description: d} }
	tests := []struct {
		name string
		err  error
		want telegram.Class
	}{
		{"nil", nil, telegram.ClassNone},
		{"429", api(429, "Too Many Requests: retry after 3"), telegram.ClassRateLimited},
		{"parse", api(400, "Bad Request: can't parse entities: Unsupported start tag"), telegram.ClassParseEntities},
		{"parse case", api(400, "BAD REQUEST: CAN'T PARSE ENTITIES"), telegram.ClassParseEntities},
		{"not modified", api(400, "Bad Request: message is not modified: specified"), telegram.ClassNotModified},
		{"edit gone", api(400, "Bad Request: message to edit not found"), telegram.ClassEditTargetGone},
		{"delete gone", api(400, "Bad Request: message to delete not found"), telegram.ClassDeleteTargetGone},
		{"cannot delete", api(400, "Bad Request: message can't be deleted"), telegram.ClassDeleteTargetGone},
		{"thread", api(400, "Bad Request: message thread not found"), telegram.ClassThreadNotFound},
		{"topic id", api(400, "Bad Request: TOPIC_ID_INVALID"), telegram.ClassThreadNotFound},
		{"topic closed", api(400, "Bad Request: TOPIC_CLOSED"), telegram.ClassThreadNotFound},
		{"topic same name", api(400, "Bad Request: TOPIC_NOT_MODIFIED"), telegram.ClassNotModified},
		{"topic deleted", api(400, "Bad Request: TOPIC_DELETED"), telegram.ClassThreadNotFound},
		{"thread not found", api(400, "Bad Request: thread not found"), telegram.ClassThreadNotFound},
		{"blocked", api(403, "Forbidden: bot was blocked by the user"), telegram.ClassBlocked},
		{"conflict", api(409, "Conflict: terminated by other getUpdates request"), telegram.ClassConflict},
		{"bad request", api(400, "Bad Request: chat not found"), telegram.ClassBadRequest},
		{"401", api(401, "Unauthorized"), telegram.ClassBadRequest},
		{"unknown", &telegram.UnknownOutcomeError{Method: "m", Err: errors.New("x")}, telegram.ClassUnknownOutcome},
		{"wrapped", fmt.Errorf("ctx: %w", api(429, "x")), telegram.ClassRateLimited},
		{"foreign", errors.New("boom"), telegram.ClassUnknownOutcome},
	}
	for _, tc := range tests {
		if got := telegram.Classify(tc.err); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestClassifyOverTheWire(t *testing.T) {
	cases := []struct {
		code int
		desc string
		want telegram.Class
	}{
		{429, "Too Many Requests: retry after 2", telegram.ClassRateLimited},
		{403, "Forbidden: bot was blocked by the user", telegram.ClassBlocked},
		{409, "Conflict: terminated by other getUpdates request", telegram.ClassConflict},
		{400, "Bad Request: chat not found", telegram.ClassBadRequest},
		{500, "Internal Server Error", telegram.ClassUnknownOutcome},
		{502, "Bad Gateway", telegram.ClassUnknownOutcome},
	}
	for _, tc := range cases {
		s, c := setup(t)
		s.FailNext("deleteMessage", 1, tc.code, tc.desc, 0)
		err := c.DeleteMessage(context.Background(), chat, 1)
		if got := telegram.Classify(err); got != tc.want {
			t.Errorf("%d: got %v want %v (%v)", tc.code, got, tc.want, err)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	s, c := setup(t)
	s.FailNext("sendMessage", 2, 429, "Too Many Requests: retry after 7", 7)
	for i := 0; i < 2; i++ {
		_, err := c.SendMessage(context.Background(), telegram.SendMessageParams{ChatID: chat, Text: "x"})
		var api *telegram.APIError
		if !errors.As(err, &api) {
			t.Fatalf("err = %v", err)
		}
		if api.Code != 429 || api.RetryAfter != 7*time.Second || api.Method != "sendMessage" || api.Held {
			t.Fatalf("api = %+v", api)
		}
	}
	if _, err := c.SendMessage(context.Background(), telegram.SendMessageParams{ChatID: chat, Text: "x"}); err != nil {
		t.Fatalf("third call should work: %v", err)
	}
	if got := len(s.Messages(chat)); got != 1 {
		t.Fatalf("messages = %d", got)
	}
}

// A 429 with a long wait holds its method: the client refuses it without
// asking Telegram until the wait ends, and the other methods still go.
func TestLongWaitHoldsTheMethod(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s, c := setup(t, telegram.WithClock(func() time.Time { return now }))
	ctx := context.Background()
	s.FailNext("sendRichMessage", 1, 429, "Too Many Requests: retry after 3600", 3600)
	for i := 0; i < 2; i++ {
		_, err := c.SendRichMessage(ctx, chat, 0, "hi", false, nil)
		var api *telegram.APIError
		if !errors.As(err, &api) || api.Code != 429 || !api.Held || api.RetryAfter <= telegram.HoldAfter {
			t.Fatalf("call %d: %v", i, err)
		}
		if telegram.Classify(err) != telegram.ClassRateLimited {
			t.Fatalf("class = %v", telegram.Classify(err))
		}
	}
	if n := len(s.Calls("sendRichMessage")); n != 1 {
		t.Fatalf("calls during the hold = %d, want 1", n)
	}
	if hs := c.Holds(); len(hs) != 1 || hs[0].Method != "sendRichMessage" || !hs[0].Until.Equal(now.Add(time.Hour)) {
		t.Fatalf("holds = %+v", hs)
	}
	if _, err := c.SendMessage(ctx, telegram.SendMessageParams{ChatID: chat, Text: "other"}); err != nil {
		t.Fatalf("another method: %v", err)
	}
	now = now.Add(time.Hour)
	if _, err := c.SendRichMessage(ctx, chat, 0, "hi", false, nil); err != nil {
		t.Fatalf("after the wait: %v", err)
	}
	if len(c.Holds()) != 0 {
		t.Fatalf("holds after the wait = %+v", c.Holds())
	}
}

func TestTimeoutIsUnknownOutcome(t *testing.T) {
	s, c := setup(t, telegram.WithTimeout(100*time.Millisecond))
	s.Delay("sendMessage", 2*time.Second)
	start := time.Now()
	_, err := c.SendMessage(context.Background(), telegram.SendMessageParams{ChatID: chat, Text: "x"})
	var u *telegram.UnknownOutcomeError
	if !errors.As(err, &u) || u.Method != "sendMessage" {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("should wrap DeadlineExceeded: %v", err)
	}
	if telegram.Classify(err) != telegram.ClassUnknownOutcome {
		t.Fatalf("class = %v", telegram.Classify(err))
	}
	if time.Since(start) > time.Second {
		t.Fatal("did not time out promptly")
	}
	if len(s.Messages(chat)) != 0 {
		t.Fatal("dropped request should have no effect")
	}
}

func TestCallerDeadlineAndCancel(t *testing.T) {
	s, c := setup(t)
	s.Delay("deleteMessage", 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := c.DeleteMessage(ctx, chat, 1); telegram.Classify(err) != telegram.ClassUnknownOutcome {
		t.Fatalf("deadline: %v", err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel2)
	err := c.DeleteMessage(ctx2, chat, 1)
	if !errors.Is(err, context.Canceled) || telegram.Classify(err) != telegram.ClassUnknownOutcome {
		t.Fatalf("cancel: %v", err)
	}
	ctx3, cancel3 := context.WithCancel(context.Background())
	cancel3()
	if _, err := c.GetMe(ctx3); !errors.Is(err, context.Canceled) {
		t.Fatalf("already canceled: %v", err)
	}
}

func TestTokenNeverInErrors(t *testing.T) {
	s := tgtest.New(t)
	tok := s.Token()
	check := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: expected an error", name)
			return
		}
		var chain []string
		for e := error(err); e != nil; e = errors.Unwrap(e) {
			chain = append(chain, e.Error())
		}
		for _, str := range append(chain, fmt.Sprintf("%v|%+v|%#v", err, err, err)) {
			if strings.Contains(str, tok) || strings.Contains(str, "FAKE-test-token") {
				t.Errorf("%s: token leaked: %s", name, str)
			}
		}
	}
	ctx := context.Background()

	// Timeout.
	slow := s.Client(telegram.WithTimeout(50 * time.Millisecond))
	s.Delay("getMe", time.Second)
	_, err := slow.GetMe(ctx)
	check("timeout", err)
	s.Delay("getMe", 0)

	// 5xx.
	s.FailNext("getMe", 1, 502, "Bad Gateway", 0)
	_, err = s.Client().GetMe(ctx)
	check("5xx", err)

	// Telegram error that echoes the token in its description.
	s.FailNext("getMe", 1, 400, "Bad Request: bad path /bot"+tok+"/getMe", 0)
	_, err = s.Client().GetMe(ctx)
	check("echoed", err)

	// Bad URL and a dead server.
	_, err = telegram.New(tok, telegram.WithBaseURL("http://bad host\x7f")).GetMe(ctx)
	check("bad url", err)
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	_, err = telegram.New(tok, telegram.WithBaseURL(url)).GetMe(ctx)
	check("refused", err)
	err = telegram.New(tok, telegram.WithBaseURL(url)).DownloadFile(ctx, "a/b", &bytes.Buffer{}, 10)
	check("download refused", err)
}

func TestMalformedBodies(t *testing.T) {
	bodies := map[string]string{
		"not json":     `<html>`,
		"empty":        ``,
		"bad result":   `{"ok":true,"result":"nope"}`,
		"truncated":    `{"ok":true,"res`,
		"ok no result": `{"ok":true}`,
	}
	for name, body := range bodies {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		c := telegram.New("1:T", telegram.WithBaseURL(srv.URL))
		_, err := c.GetMe(context.Background())
		var u *telegram.UnknownOutcomeError
		if !errors.As(err, &u) {
			t.Errorf("%s: err = %v", name, err)
		}
		srv.Close()
	}
}

func TestServerErrorsAreUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	err := telegram.New("1:T", telegram.WithBaseURL(srv.URL)).DeleteMessage(context.Background(), 1, 1)
	if telegram.Classify(err) != telegram.ClassUnknownOutcome {
		t.Fatalf("err = %v", err)
	}
}

func TestGetUpdates(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	s.PushMessage(chat, 0, 9, "one")
	u2 := s.PushMessage(chat, 3, 9, "two")
	ups, err := c.GetUpdates(ctx, 0, 1, []string{"message", "callback_query"})
	if err != nil || len(ups) != 2 {
		t.Fatalf("ups = %v, %v", ups, err)
	}
	if ups[1].Message.Text != "two" || ups[1].Message.MessageThreadID != 3 || !ups[1].Message.IsTopicMessage || ups[1].Message.From.ID != 9 {
		t.Fatalf("update = %+v", ups[1].Message)
	}
	if got, _ := s.Calls("getUpdates")[0].Params["allowed_updates"].([]any); len(got) != 2 {
		t.Fatalf("allowed_updates = %v", got)
	}
	// Offset confirms earlier updates.
	ups, err = c.GetUpdates(ctx, u2.UpdateID, 1, nil)
	if err != nil || len(ups) != 1 || ups[0].UpdateID != u2.UpdateID {
		t.Fatalf("ups = %+v, %v", ups, err)
	}
	ups, err = c.GetUpdates(ctx, u2.UpdateID+1, 1, nil)
	if err != nil || len(ups) != 0 {
		t.Fatalf("ups = %+v, %v", ups, err)
	}
}

func TestGetUpdatesLongPoll(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()

	// Returns when an update is pushed.
	time.AfterFunc(150*time.Millisecond, func() { s.PushMessage(chat, 0, 1, "late") })
	start := time.Now()
	ups, err := c.GetUpdates(ctx, 1, 20, nil)
	if err != nil || len(ups) != 1 || ups[0].Message.Text != "late" {
		t.Fatalf("ups = %+v, %v", ups, err)
	}
	if d := time.Since(start); d < 100*time.Millisecond || d > 5*time.Second {
		t.Fatalf("returned after %v", d)
	}

	// Returns an empty list at the timeout.
	start = time.Now()
	ups, err = c.GetUpdates(ctx, ups[0].UpdateID+1, 1, nil)
	if err != nil || len(ups) != 0 {
		t.Fatalf("ups = %+v, %v", ups, err)
	}
	if d := time.Since(start); d < 900*time.Millisecond || d > 5*time.Second {
		t.Fatalf("timeout after %v", d)
	}

	// Cancellation ends a pending poll.
	cctx, cancel := context.WithCancel(ctx)
	time.AfterFunc(100*time.Millisecond, cancel)
	start = time.Now()
	if _, err := c.GetUpdates(cctx, 100, 30, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("cancel was slow")
	}
}

func TestPushCallback(t *testing.T) {
	s, c := setup(t)
	m := send(t, c, 4, "pick")
	s.PushCallback(chat, 4, 9, m.MessageID, "choice:1")
	ups, err := c.GetUpdates(context.Background(), 0, 1, nil)
	if err != nil || len(ups) != 1 || ups[0].CallbackQuery == nil {
		t.Fatalf("ups = %+v, %v", ups, err)
	}
	cb := ups[0].CallbackQuery
	if cb.Data != "choice:1" || cb.From.ID != 9 || cb.ID == "" || cb.Message == nil || cb.Message.MessageID != m.MessageID {
		t.Fatalf("callback = %+v", cb)
	}
}

func TestSendDocumentRoundTrip(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	data := []byte("line1\nline2\x00\xff binary")
	m, err := c.SendDocument(ctx, chat, 5, `we"ird.txt`, data, "the caption", true)
	if err != nil {
		t.Fatal(err)
	}
	if m.Document == nil || m.Document.FileName != `we"ird.txt` || m.Document.FileSize != int64(len(data)) || m.Caption != "the caption" {
		t.Fatalf("message = %+v doc=%+v", m, m.Document)
	}
	st := s.Messages(chat)[0]
	if !bytes.Equal(st.File, data) || st.ThreadID != 5 || !st.Silent || st.Text != "the caption" {
		t.Fatalf("stored = %+v", st)
	}
	f, err := c.GetFile(ctx, m.Document.FileID)
	if err != nil || f.FilePath == "" || f.FileSize != int64(len(data)) {
		t.Fatalf("file = %+v, %v", f, err)
	}
	var buf bytes.Buffer
	if err := c.DownloadFile(ctx, f.FilePath, &buf, 1<<20); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), data) {
		t.Fatalf("download = %q", buf.Bytes())
	}
	if _, err := c.GetFile(ctx, "nope"); telegram.Classify(err) != telegram.ClassBadRequest {
		t.Fatalf("GetFile unknown: %v", err)
	}
}

func TestDownloadLimit(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	data := bytes.Repeat([]byte("x"), 1000)
	f := s.AddFile("f1", "photos/a.bin", data)

	var buf bytes.Buffer
	if err := c.DownloadFile(ctx, f.FilePath, &buf, 1000); err != nil {
		t.Fatalf("exact size: %v", err)
	}
	buf.Reset()
	err := c.DownloadFile(ctx, f.FilePath, &buf, 999)
	if !errors.Is(err, telegram.ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if buf.Len() > 999 {
		t.Fatalf("wrote %d bytes past the limit", buf.Len())
	}
	buf.Reset()
	if err := c.DownloadFile(ctx, f.FilePath, &buf, 0); err != nil || buf.Len() != 1000 {
		t.Fatalf("no limit: %v len=%d", err, buf.Len())
	}
	err = c.DownloadFile(ctx, "missing/file", &buf, 10)
	var api *telegram.APIError
	if !errors.As(err, &api) || api.Code != 404 {
		t.Fatalf("missing: %v", err)
	}
}

func TestDownloadLimitWithoutContentLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush() // forces chunked encoding, so no Content-Length
		w.Write(bytes.Repeat([]byte("y"), 5000))
	}))
	defer srv.Close()
	c := telegram.New("1:T", telegram.WithBaseURL(srv.URL))
	var buf bytes.Buffer
	err := c.DownloadFile(context.Background(), "x", &buf, 100)
	if !errors.Is(err, telegram.ErrTooLarge) || buf.Len() > 100 {
		t.Fatalf("err = %v len=%d", err, buf.Len())
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestDownloadWriterError(t *testing.T) {
	s, c := setup(t)
	f := s.AddFile("f", "a/b", []byte("data"))
	err := c.DownloadFile(context.Background(), f.FilePath, failWriter{}, 100)
	if err == nil || !strings.Contains(err.Error(), "disk full") || telegram.Classify(err) == telegram.ClassNone {
		t.Fatalf("err = %v", err)
	}
	var u *telegram.UnknownOutcomeError
	if errors.As(err, &u) {
		t.Fatal("writer error is not an unknown outcome")
	}
}

func TestDownloadCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	c := telegram.New("1:T", telegram.WithBaseURL(srv.URL))
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	err := c.DownloadFile(ctx, "x", &bytes.Buffer{}, 10)
	if telegram.Classify(err) != telegram.ClassUnknownOutcome {
		t.Fatalf("err = %v", err)
	}
}

func TestWrongTokenIsRejected(t *testing.T) {
	s := tgtest.New(t)
	c := telegram.New("999:WRONG", telegram.WithBaseURL(s.URL()))
	_, err := c.GetMe(context.Background())
	var api *telegram.APIError
	if !errors.As(err, &api) || api.Code != 401 {
		t.Fatalf("err = %v", err)
	}
}

func TestCallsAreRecordedWithTime(t *testing.T) {
	s, c := setup(t)
	before := time.Now()
	send(t, c, 0, "a")
	_, _ = c.GetMe(context.Background())
	all := s.Calls("")
	if len(all) != 2 || all[0].Method != "sendMessage" || all[1].Method != "getMe" || all[0].At.Before(before) {
		t.Fatalf("calls = %+v", all)
	}
}

func TestClassString(t *testing.T) {
	if telegram.ClassRateLimited.String() != "rate_limited" || telegram.Class(99).String() == "" {
		t.Fatal("String")
	}
}

func TestSendRichMessage(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	kb := &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{{Text: "Go", CallbackData: "go"}}}}
	m, err := c.SendRichMessage(ctx, chat, 7, "# Title\n\n**bold**", true, kb)
	if err != nil {
		t.Fatal(err)
	}
	if m.MessageID == 0 || m.MessageThreadID != 7 {
		t.Fatalf("message = %+v", m)
	}
	calls := s.Calls("sendRichMessage")
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	p := calls[0].Params
	rm, _ := p["rich_message"].(map[string]any)
	if p["chat_id"] != float64(chat) || p["message_thread_id"] != float64(7) || p["disable_notification"] != true ||
		rm["markdown"] != "# Title\n\n**bold**" || len(rm) != 1 || p["reply_markup"] == nil {
		t.Fatalf("params = %v", p)
	}
	stored := s.Messages(chat)
	if len(stored) != 1 || stored[0].Text != "# Title\n\n**bold**" || !stored[0].Silent || stored[0].Markup == nil {
		t.Fatalf("stored = %+v", stored)
	}

	// Optional fields are omitted.
	if _, err := c.SendRichMessage(ctx, chat, 0, "x", false, nil); err != nil {
		t.Fatal(err)
	}
	p = s.Calls("sendRichMessage")[1].Params
	for _, k := range []string{"message_thread_id", "disable_notification", "reply_markup"} {
		if _, ok := p[k]; ok {
			t.Errorf("%s should be omitted: %v", k, p)
		}
	}
}

func TestSendRichMessageErrors(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	if _, err := c.SendRichMessage(ctx, chat, 0, "", false, nil); telegram.Classify(err) != telegram.ClassBadRequest {
		t.Fatalf("empty: %v", err)
	}
	s.FailNext("sendRichMessage", 1, 400, "Bad Request: can't parse entities: bad", 0)
	if _, err := c.SendRichMessage(ctx, chat, 0, "x", false, nil); telegram.Classify(err) != telegram.ClassParseEntities {
		t.Fatalf("parse: %v", err)
	}
	s.FailNext("sendRichMessage", 1, 429, "Too Many Requests: retry after 3", 3)
	if _, err := c.SendRichMessage(ctx, chat, 0, "x", false, nil); telegram.Classify(err) != telegram.ClassRateLimited {
		t.Fatalf("rate: %v", err)
	}
	s.FailNext("sendRichMessage", 1, 502, "Bad Gateway", 0)
	if _, err := c.SendRichMessage(ctx, chat, 0, "x", false, nil); telegram.Classify(err) != telegram.ClassUnknownOutcome {
		t.Fatalf("5xx: %v", err)
	}
	s.DeleteTopic(chat, 9)
	if _, err := c.SendRichMessage(ctx, chat, 9, "x", false, nil); telegram.Classify(err) != telegram.ClassThreadNotFound {
		t.Fatalf("thread: %v", err)
	}
	if n := len(s.Messages(chat)); n != 0 {
		t.Fatalf("messages stored: %d", n)
	}
}

// A request that cannot connect never left this machine: it is "not sent",
// and the token is not in the error.
func TestNoConnectionIsNotSent(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens there now
	const token = "123456:SECRET-token-for-this-test"
	c := telegram.New(token, telegram.WithBaseURL("http://"+addr))
	_, err = c.SendMessage(context.Background(), telegram.SendMessageParams{ChatID: chat, Text: "hi"})
	if telegram.Classify(err) != telegram.ClassNotSent {
		t.Fatalf("class = %v (%v), want not sent", telegram.Classify(err), err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("the token is in the error: %v", err)
	}
}

// A connection that drops after the request was written may have posted the
// message: the outcome stays unknown.
func TestDropAfterWriteIsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer srv.Close()
	c := telegram.New("123456:abc", telegram.WithBaseURL(srv.URL))
	_, err := c.SendMessage(context.Background(), telegram.SendMessageParams{ChatID: chat, Text: "hi"})
	if telegram.Classify(err) != telegram.ClassUnknownOutcome {
		t.Fatalf("class = %v (%v), want unknown outcome", telegram.Classify(err), err)
	}
}

// A TLS handshake that never finishes cannot have posted the message.
func TestTLSHandshakeTimeoutIsNotSent(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c) // accept and never answer
			mu.Unlock()
		}
	}()
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	}()
	const token = "123456:SECRET-token-for-this-test"
	c := telegram.New(token, telegram.WithBaseURL("https://"+ln.Addr().String()), telegram.WithTimeout(300*time.Millisecond))
	start := time.Now()
	_, err = c.SendMessage(context.Background(), telegram.SendMessageParams{ChatID: chat, Text: "hi"})
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %v", d)
	}
	if telegram.Classify(err) != telegram.ClassNotSent {
		t.Fatalf("class = %v (%v), want not sent", telegram.Classify(err), err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("the token is in the error: %v", err)
	}
}

// OnMessage gets the number of every message that the bot sends and of
// every message in an update; a send without an answer reports none.
func TestOnMessageReportsEveryMessageNumber(t *testing.T) {
	s, c := setup(t)
	var mu sync.Mutex
	var got []int64
	c.OnMessage(func(chatID, messageID int64) {
		mu.Lock()
		defer mu.Unlock()
		if chatID == 7 {
			got = append(got, messageID)
		}
	})
	ctx := context.Background()
	m, err := c.SendMessage(ctx, telegram.SendMessageParams{ChatID: 7, Text: "a"})
	if err != nil {
		t.Fatal(err)
	}
	s.FailNext("sendMessage", 1, 502, "Bad Gateway", 0)
	if _, err := c.SendMessage(ctx, telegram.SendMessageParams{ChatID: 7, Text: "b"}); err == nil {
		t.Fatal("want an error")
	}
	d, err := c.SendDocument(ctx, 7, 0, "f.md", []byte("x"), "", false)
	if err != nil {
		t.Fatal(err)
	}
	u := s.PushMessage(7, 0, 9, "from the user")
	if _, err := c.GetUpdates(ctx, 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []int64{m.MessageID, d.MessageID, u.Message.MessageID}; !slices.Equal(got, want) {
		t.Fatalf("numbers %v, want %v", got, want)
	}
	c.OnMessage(nil) // removed: no more calls
	if _, err := c.SendMessage(ctx, telegram.SendMessageParams{ChatID: 7, Text: "c"}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("numbers after removal: %v", got)
	}
}

func TestSendPhotoRoundTrip(t *testing.T) {
	s, c := setup(t)
	ctx := context.Background()
	data := []byte("fake picture bytes")
	m, err := c.SendPhoto(ctx, chat, 5, "a.png", data, "a.png", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Photo) == 0 || m.Caption != "a.png" || m.Document != nil {
		t.Fatalf("message = %+v", m)
	}
	st := s.Messages(chat)[0]
	if !st.Photo || !bytes.Equal(st.File, data) || st.ThreadID != 5 || !st.Silent || st.Text != "a.png" {
		t.Fatalf("stored = %+v", st)
	}
	s.FailNext("sendPhoto", 1, 400, "Bad Request: PHOTO_INVALID_DIMENSIONS", 0)
	if _, err := c.SendPhoto(ctx, chat, 5, "a.png", data, "", false); telegram.Classify(err) != telegram.ClassBadRequest {
		t.Fatalf("refused photo: %v", err)
	}
}
