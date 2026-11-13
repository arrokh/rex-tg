package inbound

import (
	"strings"
	"testing"
)

func TestWithReply(t *testing.T) {
	tests := []struct{ name, text, quoted, want string }{
		{"no reply", "go on", "", "go on"},
		{"blank quote", "go on", " \n ", "go on"},
		{"one line", "do this one", "2. Rename the flag", "do this one\n\nIn reply to:\n> 2. Rename the flag"},
		{"lines and a blank line", "why?", "First.\r\n\r\n\r\nSecond.  ", "why?\n\nIn reply to:\n> First.\n>\n> Second."},
		{"a photo path", "/Users/a/up/1-photo.jpg", "x", "/Users/a/up/1-photo.jpg\n\nIn reply to:\n> x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := WithReply(tc.text, tc.quoted); got != tc.want {
				t.Fatalf("WithReply(%q, %q) =\n%q\nwant\n%q", tc.text, tc.quoted, got, tc.want)
			}
		})
	}
}

func TestWithReplyCutsALongQuote(t *testing.T) {
	got := WithReply("ok", strings.Repeat("a", MaxQuoteRunes+50))
	q := strings.TrimPrefix(got, "ok\n\nIn reply to:\n> ")
	if r := []rune(q); len(r) != MaxQuoteRunes || !strings.HasSuffix(q, "…") {
		t.Fatalf("quote has %d runes, ends %q", len(r), string(r[len(r)-3:]))
	}
}

func TestTakesQuote(t *testing.T) {
	for text, want := range map[string]bool{
		"do this one": true, "#2 please": true, "": true,
		"/compact": false, " /model opus": false, "!ls": false, " !git status": false,
	} {
		if got := TakesQuote(text); got != want {
			t.Errorf("TakesQuote(%q) = %v, want %v", text, got, want)
		}
	}
}

// The echo check still finds a reply by its start: the user's own words.
func TestWithReplyKeepsTheEchoPrefix(t *testing.T) {
	text := "please rename the flag as you said, and run the tests after"
	if prefix(echoText(WithReply(text, "an answer"))) != prefix(echoText(text)) {
		t.Fatal("the quote changed the prompt's start")
	}
}
