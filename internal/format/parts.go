package format

import (
	"strconv"
	"strings"
)

// Part is one Telegram message: its HTML and the plain-text fallback.
type Part struct{ HTML, Plain string }

// Parts renders markdown and splits it into parts whose visible length
// (including the " (i/n)" suffix when there is more than one part) is at most
// limit. It returns nil for empty input.
//
// The Markdown source is split at blank lines first, then at line ends, then
// at rune boundaries. A fenced code block is split only when it alone is too
// long; each piece is then fenced again with the same info string.
func Parts(markdown string, limit int) []Part {
	if strings.TrimSpace(markdown) == "" {
		return nil
	}
	if h := RenderHTML(markdown); VisibleLen(h) <= limit {
		return []Part{{HTML: h, Plain: Plain(markdown)}}
	}
	var chunks []string
	for digits := 1; ; digits++ {
		w := strings.Repeat("9", digits)
		reserve := len(" (" + w + "/" + w + ")")
		budget := max(limit-reserve, 1)
		chunks = split(markdown, budget)
		if len(strconv.Itoa(len(chunks))) <= digits || digits >= 6 {
			break
		}
	}
	n := len(chunks)
	parts := make([]Part, 0, n)
	for i, c := range chunks {
		suffix := " (" + strconv.Itoa(i+1) + "/" + strconv.Itoa(n) + ")"
		parts = append(parts, Part{
			HTML:  RenderHTML(c) + suffix,
			Plain: Plain(c) + suffix,
		})
	}
	return parts
}

func fits(md string, budget int) bool { return VisibleLen(RenderHTML(md)) <= budget }

// unit is a piece of source that is split as a whole when possible.
type unit struct {
	text  string
	fence bool
}

func split(markdown string, budget int) []string {
	var out []string
	cur := ""
	flush := func() {
		if cur != "" {
			out = append(out, cur)
			cur = ""
		}
	}
	for _, u := range units(markdown) {
		if fits(u.text, budget) {
			if cur != "" && fits(cur+"\n\n"+u.text, budget) {
				cur += "\n\n" + u.text
				continue
			}
			flush()
			cur = u.text
			continue
		}
		flush()
		if u.fence {
			out = append(out, splitFence(u.text, budget)...)
		} else {
			out = append(out, splitText(u.text, budget)...)
		}
	}
	flush()
	return out
}

func fenceOpen(line string) (marker string, ok bool) {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 || len(t) < 3 {
		return "", false
	}
	c := t[0]
	if c != '`' && c != '~' {
		return "", false
	}
	n := 0
	for n < len(t) && t[n] == c {
		n++
	}
	if n < 3 || (c == '`' && strings.Contains(t[n:], "`")) {
		return "", false
	}
	return t[:n], true
}

func fenceClose(line, marker string) bool {
	t := strings.TrimSpace(line)
	return len(t) >= len(marker) && strings.Trim(t, marker[:1]) == "" && t[0] == marker[0]
}

// units splits source at blank lines outside fences, and puts every fenced
// block in its own unit.
func units(markdown string) []unit {
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	var out []unit
	var cur []string
	flush := func(fence bool) {
		if t := strings.Join(cur, "\n"); strings.TrimSpace(t) != "" {
			out = append(out, unit{text: t, fence: fence})
		}
		cur = nil
	}
	marker := ""
	for _, l := range lines {
		switch {
		case marker != "":
			cur = append(cur, l)
			if fenceClose(l, marker) {
				marker = ""
				flush(true)
			}
		case func() bool { m, ok := fenceOpen(l); marker = m; return ok }():
			flush(false)
			cur = append(cur, l)
		case strings.TrimSpace(l) == "":
			flush(false)
		default:
			cur = append(cur, l)
		}
	}
	flush(marker != "")
	return out
}

// splitText splits a block of text at line ends, then at rune boundaries.
func splitText(t string, budget int) []string {
	return packLines(strings.Split(t, "\n"), budget, func(s string) string { return s })
}

// splitFence splits an oversized fenced block, fencing each piece again.
func splitFence(t string, budget int) []string {
	lines := strings.Split(t, "\n")
	open := lines[0]
	marker, _ := fenceOpen(open)
	body := lines[1:]
	if len(body) > 0 && fenceClose(body[len(body)-1], marker) {
		body = body[:len(body)-1]
	}
	return packLines(body, budget, func(s string) string { return open + "\n" + s + "\n" + marker })
}

// packLines groups lines greedily so that wrap(group) fits the budget. A line
// that is too long on its own is cut at rune boundaries.
func packLines(lines []string, budget int, wrap func(string) string) []string {
	var out []string
	cur := ""
	have := false
	flush := func() {
		if have {
			out = append(out, wrap(cur))
		}
		cur, have = "", false
	}
	for _, l := range lines {
		if have && fits(wrap(cur+"\n"+l), budget) {
			cur += "\n" + l
			continue
		}
		flush()
		if fits(wrap(l), budget) {
			cur, have = l, true
			continue
		}
		for _, piece := range hardSplit(l, func(s string) bool { return fits(wrap(s), budget) }) {
			out = append(out, wrap(piece))
		}
	}
	flush()
	return out
}

// hardSplit cuts s into the longest rune prefixes accepted by ok.
func hardSplit(s string, ok func(string) bool) []string {
	var out []string
	r := []rune(s)
	for len(r) > 0 {
		lo, hi := 1, len(r)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if ok(string(r[:mid])) {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		out = append(out, string(r[:lo]))
		r = r[lo:]
	}
	return out
}
