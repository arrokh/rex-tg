package miniapp

import (
	"context"
	"time"
)

// The reply keyboard's buttons open their pages in one tap. Telegram gives a
// Mini App that a keyboard button opens no init data ("It is empty if the
// Mini App was launched from a keyboard button", core.telegram.org/bots/
// webapps), so such a button's address carries a key instead
// (app?k=<key>&go=<view>[&t=<thread>]). The key authorizes reads and validated
// control-panel actions. Sending a page-bound message additionally requires
// the current key and the owner's verified Tailscale login.

// Views that a keyboard button opens.
const (
	ViewPanel     = "panel"
	ViewScreen    = "screen"
	ViewFiles     = "files"
	ViewChanges   = "changes"
	ViewProject   = "project"
	ViewConfig    = "config"
	ViewAnnotate  = "annotate"
	ViewTemplates = "templates"
	ViewBookmarks = "bookmarks"
)

// KindAnnotate is a topic's Annotate page: the agent's answers, to add notes
// to and send back.
const KindAnnotate = "annotate"

// KindTemplates is the Mini App's locally saved text-template manager.
const KindTemplates = "templates"

// LaunchResult is the answer to a keyboard button: the page to open, the
// topics to pick from when the button names none, or why it cannot open.
type LaunchResult struct {
	Page  string      `json:"page,omitempty"`
	Pick  []TopicPick `json:"pick,omitempty"`
	Error string      `json:"error,omitempty"`
}

// TopicPick is a topic in the picker.
type TopicPick struct {
	Thread int64  `json:"thread"` // the store's thread id
	Name   string `json:"name"`
	Live   bool   `json:"live"`
}

// Launcher opens the page of a keyboard button (the daemon).
type Launcher interface {
	// Launch returns the page of view for thread (a store thread id; 0: the
	// button names no topic). It sends nothing to the chat.
	Launch(ctx context.Context, view string, thread int64) LaunchResult
}

// AnswerItem is one answer of the agent on the Annotate page.
type AnswerItem struct {
	ID       int64 // its outbox id
	At       time.Time
	Markdown string // the answer as Markdown, or as plain text when Plain
	Plain    bool
}

// Annotations is the daemon's side of the Annotate page.
type Annotations interface {
	// Answers returns the agent's answers of one session (conv) in a
	// topic, newest first: n of them older than the answer before (0: the
	// newest), and whether there are older ones. Other sessions' answers
	// never show.
	Answers(ctx context.Context, thread, conv, before int64, n int) (items []AnswerItem, more bool, err error)
	// SaveDraft stores notes for legacy keyboard pages that send them through
	// Telegram (sendData). A draft alone sends nothing; current pages use the
	// page-bound, Tailscale-authenticated send action.
	SaveDraft(ctx context.Context, thread, conv int64, text string) (id string, err error)
	// BotName is the bot's username without "@", for t.me links.
	BotName() string
}
