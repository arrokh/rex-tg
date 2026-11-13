package format

import (
	"strings"
	"testing"
)

// checkMessages checks what every message of a split must be: valid HTML,
// within the limit, not empty, and without a "(1/2)" counter.
func checkMessages(t *testing.T, msgs []Part, limit int) {
	t.Helper()
	for i, p := range msgs {
		if msg := checkHTML(p.HTML); msg != "" {
			t.Errorf("message %d invalid HTML (%s): %q", i+1, msg, p.HTML)
		}
		if v := VisibleLen(p.HTML); v > limit {
			t.Errorf("message %d visible length %d > limit %d", i+1, v, limit)
		}
		if strings.TrimSpace(p.Plain) == "" {
			t.Errorf("message %d is empty", i+1)
		}
		if strings.Contains(p.HTML, "/"+itoa(len(msgs))+")") {
			t.Errorf("message %d has a counter: %q", i+1, p.HTML)
		}
	}
}

func para(word string, n int) string { return strings.TrimSpace(strings.Repeat(word+" ", n)) }

func TestMessagesShortIsOne(t *testing.T) {
	got := Messages("# Hi\n\n**there**", 100, 400)
	if len(got) != 1 || got[0].HTML != "<b>Hi</b>\n\n<b>there</b>" {
		t.Fatalf("got %#v", got)
	}
}

// Paragraphs are packed up to the soft length; no counters.
func TestMessagesSplitAtParagraphs(t *testing.T) {
	in := strings.Join([]string{para("aaa", 10), para("bbb", 10), para("ccc", 10), para("ddd", 10)}, "\n\n")
	got := Messages(in, 90, 400)
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2: %#v", len(got), got)
	}
	if !strings.HasPrefix(got[0].HTML, "aaa") || !strings.Contains(got[0].HTML, "bbb") || !strings.HasPrefix(got[1].HTML, "ccc") {
		t.Errorf("messages %q", []string{got[0].HTML, got[1].HTML})
	}
	checkMessages(t, got, 400)
}

// A heading starts a new message, unless what comes before it is short.
func TestMessagesBreakBeforeAHeading(t *testing.T) {
	in := para("intro", 12) + "\n\n## Next part\n\n" + para("body", 15)
	got := Messages(in, 120, 400)
	if len(got) != 2 || !strings.HasPrefix(got[1].HTML, "<b>Next part</b>") {
		t.Fatalf("got %q, want the heading to start the second message", htmls(got))
	}
	short := "Hi.\n\n## Part one\n\n" + para("body", 30)
	if got := Messages(short, 120, 400); !strings.HasPrefix(got[0].HTML, "Hi.") || !strings.Contains(got[0].HTML, "Part one") {
		t.Errorf("a short intro was left alone: %q", htmls(got))
	}
}

// A line that leads into a code block stays with it, and a code block that
// fits a message is never cut.
func TestMessagesKeepTheIntroWithItsCode(t *testing.T) {
	code := "```go\n" + strings.Repeat("fmt.Println(\"x\")\n", 6) + "```"
	in := para("before", 14) + "\n\nHere is the fix:\n\n" + code + "\n\n" + para("after", 14)
	got := Messages(in, 120, 400)
	for _, m := range got {
		if strings.Contains(m.HTML, "Here is the fix:") && !strings.Contains(m.HTML, "<pre>") {
			t.Fatalf("the intro is apart from its code: %q", htmls(got))
		}
		if strings.Count(m.HTML, "<pre") != strings.Count(m.HTML, "</pre>") {
			t.Fatalf("a code block was cut: %q", m.HTML)
		}
	}
	checkMessages(t, got, 400)
}

// A code block too long for one message is cut at its lines, and each piece
// is a code block again.
func TestMessagesCutAnOversizedCodeBlock(t *testing.T) {
	code := "```\n" + strings.Repeat("a line of code here\n", 40) + "```"
	got := Messages(code, 100, 300)
	if len(got) < 3 {
		t.Fatalf("got %d messages", len(got))
	}
	for _, m := range got {
		if !strings.HasPrefix(m.HTML, "<pre>") {
			t.Errorf("piece is not code: %q", m.HTML)
		}
	}
	checkMessages(t, got, 300)
}

// A short last message joins the one before.
func TestMessagesShortTailJoins(t *testing.T) {
	in := para("aaa", 20) + "\n\n" + para("bbb", 20) + "\n\nThanks."
	got := Messages(in, 90, 400)
	if last := got[len(got)-1].HTML; !strings.HasSuffix(last, "Thanks.") || last == "Thanks." {
		t.Errorf("tail alone: %q", htmls(got))
	}
	checkMessages(t, got, 400)
}

func htmls(ps []Part) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.HTML)
	}
	return out
}
