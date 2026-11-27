package daemon

import (
	"strings"
	"testing"
	"time"

	"rex-tg/internal/store"
)

// Only a topic's first message, its keyboard note, and the /help answer of a
// topic carry the reply keyboard: they are never edited, and an edit takes
// inline keyboards only.
func TestPartKeyboard(t *testing.T) {
	cases := []struct {
		p    store.Part
		want bool
	}{
		{store.Part{Kind: "notice", Key: "intro:4"}, true},
		{store.Part{Kind: "notice", Key: "reply:812:help"}, true},
		{store.Part{Kind: "notice", Key: "keyboard:3:1:-"}, true},
		{store.Part{Kind: "notice", Key: "attach:7:812"}, true},
		{store.Part{Kind: "notice", Key: "intro:4", Part: 1}, false}, // only the first part
		{store.Part{Kind: "final", Key: "intro:4"}, false},           // an answer
		{store.Part{Kind: "notice", Key: "reply:812:info"}, false},   // another command
		{store.Part{Kind: "notice", Key: "typing-unknown:9"}, false}, // a notice of the daemon
		{store.Part{Kind: "notice", Key: "alert:help"}, false},       // not a command answer
	}
	k := &keyboardSource{}
	for _, c := range cases {
		if got := k.forPart(c.p) != nil; got != c.want {
			t.Errorf("%+v: keyboard %v, want %v", c.p, got, c.want)
		}
	}
}

func TestWaitMarkupEligiblePrefix(t *testing.T) {
	for _, tc := range []struct {
		total, sendable int
		want            string
	}{
		{3, 3, "⚡ Send all now"},
		{3, 2, "⚡ Send 2 now"},
		{3, 1, "⚡ Send now"},
		{3, 0, ""},
	} {
		markup := waitMarkup(8, tc.total, tc.sendable)
		var send string
		for _, row := range markup.InlineKeyboard {
			for _, button := range row {
				if strings.Contains(button.Text, "Send") {
					send = button.Text
				}
			}
		}
		if send != tc.want {
			t.Errorf("total %d, sendable %d: button %q, want %q", tc.total, tc.sendable, send, tc.want)
		}
	}
}

// With the Mini App, each button opens its page; the topic's store id is in
// the address, and the main chat has none.
func TestKeyboardWebAppURLs(t *testing.T) {
	key := strings.Repeat("ab", 32)
	k := &keyboardSource{}
	k.snap.Store(&kbSnap{key: key, gen: "1", url: "https://mac.example.ts.net/"})
	kb := k.forPart(store.Part{Kind: "notice", Key: "intro:4", ThreadID: 7})
	if len(kb.Keyboard) != 2 || len(kb.Keyboard[0]) != 3 || len(kb.Keyboard[1]) != 3 {
		t.Fatalf("rows: %+v", kb.Keyboard)
	}
	want := map[string]string{
		"🎛 Panel": "panel", "🖥 Screen": "screen", "📁 Project": "project", "✍️ Annotate": "annotate", "🔖 Bookmarks": "bookmarks", "🧩 Templates": "templates",
	}
	for _, row := range kb.Keyboard {
		for _, b := range row {
			view, hasView := want[b.Text]
			if !hasView {
				t.Errorf("unexpected keyboard button: %+v", b)
				continue
			}
			if b.WebApp == nil {
				t.Fatalf("%q has no web_app", b.Text)
			}
			if exp := "https://mac.example.ts.net/app?k=" + key + "&go=" + view + "&t=7"; b.WebApp.URL != exp {
				t.Errorf("%q: %s, want %s", b.Text, b.WebApp.URL, exp)
			}
		}
	}
	for _, row := range keyboardFor(k.snap.Load(), 0).Keyboard {
		for _, b := range row {
			if b.WebApp != nil && strings.Contains(b.WebApp.URL, "&t=") {
				t.Errorf("main chat button with a topic: %s", b.WebApp.URL)
			}
		}
	}
	// Without a Mini App the buttons are text buttons.
	text := keyboardFor(&kbSnap{key: key, gen: "1"}, 7)
	for _, row := range text.Keyboard {
		for _, b := range row {
			if b.WebApp != nil {
				t.Errorf("%q: web_app without a Mini App", b.Text)
			}
		}
	}
}

// The keyboard value changes with the key generation and the address, so the
// chats get the buttons again.
func TestKeyboardValue(t *testing.T) {
	a := keyboardValue("1", "")
	for _, other := range []string{keyboardValue("2", ""), keyboardValue("1", "https://a.example"), keyboardValue("1", "https://b.example")} {
		if other == a {
			t.Errorf("value %q did not change", a)
		}
	}
	u := "https://a.example"
	if v1, v2 := keyboardValue("1", u), keyboardValue("1", u); v1 != v2 {
		t.Errorf("the value is not stable: %q, %q", v1, v2)
	}
	if !strings.HasPrefix(a, keyboardVersion+":1:") {
		t.Errorf("value = %q", a)
	}
}

func TestAnswerSource(t *testing.T) {
	none := func(string) (string, bool) { return "", false }
	row := func(kind string, parts ...store.Part) store.OutboxRow {
		return store.OutboxRow{Kind: kind, Key: "k", Parts: parts}
	}
	cases := []struct {
		name      string
		r         store.OutboxRow
		page      func(string) (string, bool)
		md        string
		plain, ok bool
	}{
		{"page wins", row("answer", store.Part{Format: store.FormatHTML, Content: "short"}),
			func(string) (string, bool) { return "# whole", true }, "# whole", false, true},
		{"rich is markdown", row("answer", store.Part{Format: store.FormatRich, Content: "**x**"}), none, "**x**", false, true},
		{"md document", row("answer", store.Part{Format: store.FormatDocument, Filename: "a.MD", Content: "# d"}), none, "# d", false, true},
		{"txt document is skipped", row("answer", store.Part{Format: store.FormatDocument, Filename: "a.txt", Content: "d"}), none, "", false, false},
		{"html is plain", row("backfill", store.Part{Format: store.FormatHTML, Content: "<b>a</b> &amp; b"}, store.Part{Format: store.FormatHTML, Content: "c"}), none, "a & b\n\nc", true, true},
		{"failed part", row("answer", store.Part{Format: store.FormatHTML, Content: "x", State: "failed"}), none, "", false, false},
		{"image", row("answer", store.Part{Format: store.FormatImage, Content: "/tmp/a.png"}), none, "", false, false},
		{"backfill ignores a page", row("backfill", store.Part{Format: store.FormatPlain, Content: "p"}),
			func(string) (string, bool) { return "whole", true }, "p", true, true},
	}
	for _, c := range cases {
		md, plain, ok := answerSource(c.r, c.page)
		if md != c.md || plain != c.plain || ok != c.ok {
			t.Errorf("%s: got %q %v %v, want %q %v %v", c.name, md, plain, ok, c.md, c.plain, c.ok)
		}
	}
}

func TestParseAnnotateData(t *testing.T) {
	const id24 = "0123456789abcdef01234567"
	good := `{"v":1,"a":"annotate","d":"` + id24 + `"}`
	if id, ok := parseAnnotateData(good); !ok || id != id24 {
		t.Errorf("good: %q %v", id, ok)
	}
	for _, bad := range []string{"", "x", `{"v":2,"a":"annotate","d":"` + id24 + `"}`, `{"v":1,"a":"other","d":"` + id24 + `"}`,
		`{"v":1,"a":"annotate"}`, `{"v":1,"a":"annotate","d":"0123456789abcdef0123456"}`, `{"v":1,"a":"annotate","d":"0123456789abcdef0123456z"}`,
		`{"v":1,"a":"annotate","d":"` + strings.Repeat("a", 65) + `"}`,
		`{"v":1,"a":"annotate","d":"` + id24 + `"}` + strings.Repeat(" ", 5000)} {
		if _, ok := parseAnnotateData(bad); ok {
			t.Errorf("accepted %.40q", bad)
		}
	}
}

// A draft's queued row comes from its id: the same draft always gets the
// same row, above the /at rows, and two drafts get two rows.
func TestAnnotRowID(t *testing.T) {
	a, b := annotRowID("0123456789abcdef01234567"), annotRowID("0123456789abcdef01234567")
	c := annotRowID("ffffffffffff000000000000")
	if a != b || a == c || a < annotBase || c >= annotBase+(1<<48) || annotBase <= schedBase {
		t.Errorf("rows %d %d %d", a, b, c)
	}
}

func TestDraftFresh(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	for _, c := range []struct {
		age  time.Duration
		want bool
	}{{0, true}, {59 * time.Minute, true}, {time.Hour, true}, {time.Hour + time.Second, false}, {-time.Minute, false}} {
		if got := draftFresh(annotDraft{At: now.Add(-c.age).Unix()}, now); got != c.want {
			t.Errorf("age %v: %v, want %v", c.age, got, c.want)
		}
	}
}

func TestAnnotSentNote(t *testing.T) {
	short := annotSentNote("a < b & c")
	if !strings.HasPrefix(short, "✍️ Notes sent to the agent\n<blockquote expandable>") || !strings.Contains(short, "a &lt; b &amp; c") {
		t.Errorf("note = %q", short)
	}
	long := annotSentNote(strings.Repeat("<&>", 6000))
	if len(long) > 4096 || !strings.HasSuffix(long, "…</blockquote>") {
		t.Errorf("long note: %d bytes, ends %q", len(long), long[len(long)-20:])
	}
}
