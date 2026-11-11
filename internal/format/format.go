// Package format turns Markdown into Telegram HTML, splits long text into
// message parts, counts length the way Telegram does, and removes secrets.
package format

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

var attrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

var htmlUnescaper = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&amp;", "&")

// Escape escapes &, < and > so s is safe as Telegram HTML text.
func Escape(s string) string { return htmlEscaper.Replace(s) }

// UTF16Len returns the length of s in UTF-16 code units.
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
		if r == utf8.RuneError {
			// RuneLen reports -1 for invalid runes; Telegram sees U+FFFD.
			n += 2
		}
	}
	return n
}

// VisibleLen returns the UTF-16 length of html after Telegram removes the
// tags and decodes the entities.
func VisibleLen(html string) int {
	var sb strings.Builder
	inTag := false
	for i := 0; i < len(html); i++ {
		c := html[i]
		switch {
		case inTag:
			if c == '>' {
				inTag = false
			}
		case c == '<':
			inTag = true
		default:
			sb.WriteByte(c)
		}
	}
	return UTF16Len(htmlUnescaper.Replace(sb.String()))
}

// Truncate makes s one line and cuts it to at most maxRunes runes. When it
// cuts, the result ends with "…" (counted in maxRunes).
func Truncate(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	cut := strings.TrimRight(string(r[:maxRunes-1]), " ")
	return cut + "…"
}
