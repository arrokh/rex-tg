package daemon

import (
	"strings"
	"testing"
)

// Only the colors and styles of a capture go to the live screen: links,
// titles, cursor moves, and other control characters are dropped.
func TestSgrOnlyKeepsColorsAlone(t *testing.T) {
	for _, c := range []struct{ in, vt, plain string }{
		{"\x1b[0m\x1b[1m\x1b[38;5;2mok\x1b[0m done\r", "\x1b[0m\x1b[1m\x1b[38;5;2mok\x1b[0m done", "ok done"},
		{"\x1b[38:2::100:150:250mrgb\x1b[0m", "\x1b[38:2::100:150:250mrgb\x1b[0m", "rgb"},
		{"\x1b]8;;https://example.com\x07link\x1b]8;;\x07", "link", "link"},
		{"\x1b]0;title\x1b\\text", "text", "text"},
		{"a\x1b[2Jb\x1b[10;5Hc\x1b[?25l", "abc", "abc"},
		{"x\x1b(By\x07z\x7f", "xyz", "xyz"},
		{"cut \x1b[38;5", "cut ", "cut "},
		{"tab\there", "tab\there", "tab\there"},
	} {
		vt, plain := sgrOnly(c.in)
		if vt != c.vt || plain != c.plain {
			t.Errorf("sgrOnly(%q) = %q, %q; want %q, %q", c.in, vt, plain, c.vt, c.plain)
		}
	}
}

// The live screen keeps the bottom lines with their colors. A line that
// held a secret loses its colors and shows the redacted text; the secret is
// in neither form.
func TestScreenOfRedactsBeforeColors(t *testing.T) {
	secret := "sk-ant-" + "api03-abcdefghijklmnopqrstuvwxyz123456"
	raw := "old line\r\n\x1b[32mgreen\x1b[0m\r\nkey \x1b[1m" + secret + "\x1b[0m\r\n\x1b[31mred\x1b[0m\r\n\r\n"
	text, vt := screenOf(raw, 3)
	if strings.Contains(text, "abcdefghijklmnop") || strings.Contains(vt, "abcdefghijklmnop") {
		t.Fatalf("a secret got through:\n%q\n%q", text, vt)
	}
	lines := strings.Split(vt, "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "\x1b[32mgreen") || !strings.HasPrefix(lines[2], "\x1b[31mred") {
		t.Errorf("vt lines %q", lines)
	}
	if strings.Contains(lines[1], "\x1b[1m") || !strings.HasPrefix(lines[1], "key ") {
		t.Errorf("the redacted line kept its colors: %q", lines[1])
	}
	for _, l := range lines {
		if !strings.HasSuffix(l, "\x1b[0m") {
			t.Errorf("a line does not end in a reset: %q", l)
		}
	}
	if strings.Contains(text, "old line") || strings.Count(text, "\n") != 2 {
		t.Errorf("text %q, want the bottom 3 lines", text)
	}
}

// A secret that spans lines makes redaction join them: then the screen has
// no colors, only the redacted text.
func TestScreenOfWithoutColorsWhenLinesJoin(t *testing.T) {
	key := "-----BEGIN " + "OPENSSH PRIVATE KEY-----\nAAAAfakefakefake\n-----END " + "OPENSSH PRIVATE KEY-----"
	text, vt := screenOf("\x1b[33mbefore\x1b[0m\n"+key+"\nafter\n", 10)
	if vt != "" {
		t.Errorf("colors kept: %q", vt)
	}
	if strings.Contains(text, "AAAAfake") || !strings.Contains(text, "before") || !strings.Contains(text, "after") {
		t.Errorf("text %q", text)
	}
}
