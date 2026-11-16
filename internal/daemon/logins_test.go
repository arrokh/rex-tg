package daemon

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/config"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

// testButtonsKey is a made-up key for the buttons.
var testButtonsKey = strings.Repeat("ab", 32)

func storeDaemon(t *testing.T) *daemon {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	d := &daemon{st: st, ctx: context.Background(), cfg: config.Default("/h"), log: slog.New(slog.NewTextHandler(io.Discard, nil)), kb: &keyboardSource{}}
	d.kb.snap.Store(&kbSnap{key: testButtonsKey, gen: "1"})
	d.logins = &miniLogins{d: d}
	return d
}

// The key works from the owner's Tailscale logins only: learned from a
// signed visit or the key's first use, kept across a restart, and forgotten
// with /rekey; or named in tailnet_users.
func TestMiniLogins(t *testing.T) {
	d := storeDaemon(t)
	l := d.logins
	k := testButtonsKey
	if l.KeyLoginOK("old"+k[3:], "thief@example.com") {
		t.Fatal("an old key taught a login")
	}
	if !l.KeyLoginOK(k, "phone@example.com") {
		t.Fatal("the first use of the key was refused")
	}
	if l.KeyLoginOK(k, "coworker@example.com") || l.KeyLoginOK(k, "") || l.KeyLoginOK(k, "  ") {
		t.Fatal("another login was let in")
	}
	if !l.KeyLoginOK(k, " Phone@Example.com ") {
		t.Error("the same login in other case was refused")
	}
	l.Owner("laptop@example.com")
	again := &miniLogins{d: d} // a restart
	if !again.KeyLoginOK(k, "phone@example.com") || !again.KeyLoginOK(k, "laptop@example.com") || again.KeyLoginOK(k, "coworker@example.com") {
		t.Errorf("after a restart: %v", again.known)
	}
	again.forget()
	if !again.KeyLoginOK(k, "new@example.com") || again.KeyLoginOK(k, "phone@example.com") {
		t.Errorf("after /rekey: %v", again.known)
	}

	d.cfg.MiniApp.TailnetUsers = []string{" Me@Example.com"}
	fixed := &miniLogins{d: d}
	fixed.Owner("other@example.com")
	if !fixed.KeyLoginOK(k, "me@example.com") || fixed.KeyLoginOK(k, "other@example.com") || fixed.KeyLoginOK(k, "new@example.com") || fixed.KeyLoginOK("x"+k[1:], "me@example.com") {
		t.Error("tailnet_users is not the whole list")
	}
}

// Notes go only to the session that the page showed, and not from another
// agent's topic; the main chat (where the buttons ask which topic) may send.
func TestAnnotWrongPlace(t *testing.T) {
	d := storeDaemon(t)
	ctx := context.Background()
	const chat = int64(42)
	link := func(native string, topic int64) store.Thread {
		c, err := d.st.UpsertConversation(ctx, store.Conversation{Runtime: agents.Pi, NativeID: native, Transcript: "/t/" + native, CWD: "/w"})
		if err != nil {
			t.Fatal(err)
		}
		th, err := d.st.LinkTopic(ctx, chat, topic, c.ID, native, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return th
	}
	a, b := link("s1", 101), link("s2", 102)
	from := func(topic int64) *telegram.Message {
		return &telegram.Message{Chat: telegram.Chat{ID: chat}, MessageThreadID: topic}
	}
	ok := annotDraft{Thread: a.ID, Conv: a.CurrentConversationID}
	for _, c := range []struct {
		name string
		m    *telegram.Message
		dr   annotDraft
		want string // "" or a part of the refusal
	}{
		{"its own topic", from(101), ok, ""},
		{"the main chat", from(0), ok, ""},
		{"a topic of no agent", from(555), ok, ""},
		{"another agent's topic", from(102), ok, "came from this topic"},
		{"another session", from(101), annotDraft{Thread: a.ID, Conv: a.CurrentConversationID + 100}, "another session"},
		{"no session", from(101), annotDraft{Thread: a.ID}, "another session"},
	} {
		got := d.annotWrongPlace(c.m, a, c.dr)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	_ = b
}
