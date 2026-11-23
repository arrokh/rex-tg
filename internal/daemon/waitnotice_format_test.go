package daemon

import (
	"strings"
	"testing"

	"rex-tg/internal/store"
)

func TestWaitNoticeListsEscapedTruncatedMessagePreviews(t *testing.T) {
	long := strings.Repeat("a", 26)
	got := waitNoticeFor(waitIdle, []store.Inbound{
		{Body: "first\n  <message>"},
		{Body: long},
	})
	want := "⏳ 2 messages wait until the agent is idle." +
		"\n- Message 1: first &lt;message&gt;" +
		"\n- Message 2: " + strings.Repeat("a", 25) + "…"
	if got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
}

func TestQueuedPreviewTruncatesByRune(t *testing.T) {
	got := queuedPreview(strings.Repeat("界", 26))
	want := strings.Repeat("界", 25) + "…"
	if got != want {
		t.Fatalf("preview has %d runes: %q, want %q", len([]rune(got)), got, want)
	}
}
