package daemon

import (
	"testing"

	"rex-tg/internal/store"
)

// Only a topic's first message and the /help answer of a topic carry the
// reply keyboard: they are never edited, and an edit takes inline keyboards
// only.
func TestPartKeyboard(t *testing.T) {
	cases := []struct {
		p    store.Part
		want bool
	}{
		{store.Part{Kind: "notice", Key: "intro:4"}, true},
		{store.Part{Kind: "notice", Key: "reply:812:help"}, true},
		{store.Part{Kind: "notice", Key: "intro:4", Part: 1}, false}, // only the first part
		{store.Part{Kind: "final", Key: "intro:4"}, false},           // an answer
		{store.Part{Kind: "notice", Key: "reply:812:info"}, false},   // another command
		{store.Part{Kind: "notice", Key: "typing-unknown:9"}, false}, // a notice of the daemon
		{store.Part{Kind: "notice", Key: "alert:help"}, false},       // not a command answer
	}
	for _, c := range cases {
		if got := partKeyboard(c.p) != nil; got != c.want {
			t.Errorf("%+v: keyboard %v, want %v", c.p, got, c.want)
		}
	}
}
