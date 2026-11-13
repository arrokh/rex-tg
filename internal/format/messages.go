package format

import "strings"

// Messages renders markdown as chat messages, split where a person would
// split a long reply: between paragraphs, before a heading, never inside a
// code block, and never right after a heading or a line that introduces
// what follows (one that ends with ":"). Each message aims at soft visible
// characters and has at most limit. Unlike Parts, there is no " (1/2)"
// counter. It returns nil for empty input.
func Messages(markdown string, soft, limit int) []Part {
	if strings.TrimSpace(markdown) == "" {
		return nil
	}
	if soft <= 0 || soft > limit {
		soft = limit
	}
	if h := RenderHTML(markdown); VisibleLen(h) <= soft {
		return []Part{{HTML: h, Plain: Plain(markdown)}}
	}
	var groups []string
	cur, lastHeading := "", false
	flush := func() {
		if cur != "" {
			groups = append(groups, cur)
			cur = ""
		}
	}
	for _, u := range units(markdown) {
		if !fits(u.text, limit) {
			// One block too long for a message: cut it at its lines.
			flush()
			if u.fence {
				groups = append(groups, splitFence(u.text, limit)...)
			} else {
				groups = append(groups, splitText(u.text, limit)...)
			}
			continue
		}
		heading := isHeading(u.text)
		if cur == "" {
			cur, lastHeading = u.text, heading
			continue
		}
		joined := cur + "\n\n" + u.text
		next := !fits(joined, soft) || (heading && !fits(cur, soft/4))
		if (introduces(cur) || lastHeading) && fits(joined, limit) {
			next = false // "Here is the fix:" stays with the fix, a heading with its text
		}
		if next {
			flush()
		} else {
			u.text = joined
		}
		cur, lastHeading = u.text, heading
	}
	flush()
	// A short last message joins the one before, when it fits.
	if n := len(groups); n >= 2 && fits(groups[n-1], soft/4) && fits(groups[n-2]+"\n\n"+groups[n-1], limit) {
		groups = append(groups[:n-2], groups[n-2]+"\n\n"+groups[n-1])
	}
	parts := make([]Part, 0, len(groups))
	for _, g := range groups {
		parts = append(parts, Part{HTML: RenderHTML(g), Plain: Plain(g)})
	}
	return parts
}

// isHeading tells whether a block starts with a Markdown heading.
func isHeading(block string) bool {
	t := strings.TrimLeft(block, " ")
	n := 0
	for n < len(t) && n < 7 && t[n] == '#' {
		n++
	}
	return n >= 1 && n <= 6 && len(t) > n && t[n] == ' '
}

// introduces tells whether a block ends with a line that leads into the next
// block, such as "Here is the fix:".
func introduces(block string) bool {
	return strings.HasSuffix(strings.TrimRight(block, " \t\n"), ":")
}
