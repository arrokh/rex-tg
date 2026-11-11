package format

import (
	"bytes"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

var md = goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough))

// RenderHTML converts Markdown to the Telegram HTML subset. Raw HTML in the
// Markdown is escaped and shown as text.
func RenderHTML(markdown string) string { return render(markdown, true) }

// Plain converts Markdown to readable plain text.
func Plain(markdown string) string { return render(markdown, false) }

func render(markdown string, html bool) string {
	src := []byte(markdown)
	doc := md.Parser().Parse(text.NewReader(src))
	r := &renderer{src: src, html: html}
	return strings.TrimRight(strings.Join(r.blocks(doc), "\n\n"), " \t\r\n")
}

type renderer struct {
	src  []byte
	html bool
}

func (r *renderer) esc(s string) string {
	if r.html {
		return Escape(s)
	}
	return s
}

func (r *renderer) tag(name, inner string) string {
	if !r.html || inner == "" {
		return inner
	}
	return "<" + name + ">" + inner + "</" + name + ">"
}

// blocks renders the block children of n, skipping empty results.
func (r *renderer) blocks(n ast.Node) []string {
	var out []string
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if s := r.block(c); strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func (r *renderer) block(n ast.Node) string {
	switch n := n.(type) {
	case *ast.Heading:
		return r.tag("b", trimLines(r.inlines(n)))
	case *ast.Paragraph, *ast.TextBlock:
		return trimLines(r.inlines(n))
	case *ast.ThematicBreak:
		return "———"
	case *ast.FencedCodeBlock:
		return r.code(n.Lines(), string(n.Language(r.src)))
	case *ast.CodeBlock:
		return r.code(n.Lines(), "")
	case *ast.HTMLBlock:
		var sb strings.Builder
		writeLines(&sb, n.Lines(), r.src)
		if n.HasClosure() {
			sb.Write(n.ClosureLine.Value(r.src))
		}
		return r.esc(strings.TrimRight(sb.String(), "\r\n"))
	case *ast.Blockquote:
		inner := strings.Join(r.blocks(n), "\n\n")
		if inner == "" {
			return ""
		}
		if r.html {
			return "<blockquote>" + inner + "</blockquote>"
		}
		return prefixLines(inner, "> ")
	case *ast.List:
		return r.list(n)
	case *extast.Table:
		return r.table(n)
	}
	// Unknown container: render its children.
	if n.HasChildren() {
		return strings.Join(r.blocks(n), "\n\n")
	}
	return ""
}

func writeLines(sb *strings.Builder, lines *text.Segments, src []byte) {
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		sb.Write(seg.Value(src))
	}
}

var safeLang = func(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '-' || c == '+' || c == '#' || c == '.':
		default:
			return false
		}
	}
	return true
}

func (r *renderer) code(lines *text.Segments, lang string) string {
	var sb strings.Builder
	writeLines(&sb, lines, r.src)
	body := strings.TrimRight(sb.String(), "\r\n")
	if strings.TrimSpace(body) == "" {
		return ""
	}
	if !r.html {
		return body
	}
	if safeLang(lang) {
		return `<pre><code class="language-` + lang + `">` + Escape(body) + `</code></pre>`
	}
	return "<pre><code>" + Escape(body) + "</code></pre>"
}

func (r *renderer) list(l *ast.List) string {
	var items []string
	num := l.Start
	if num == 0 {
		num = 1
	}
	for it := l.FirstChild(); it != nil; it = it.NextSibling() {
		sep := "\n\n"
		if l.IsTight {
			sep = "\n"
		}
		body := strings.Join(r.blocks(it), sep)
		marker := "• "
		if l.IsOrdered() {
			marker = strconv.Itoa(num) + ". "
			num++
		}
		lines := strings.Split(body, "\n")
		for i := range lines {
			if i == 0 {
				lines[i] = marker + lines[i]
			} else if lines[i] != "" {
				lines[i] = "  " + lines[i]
			}
		}
		items = append(items, strings.TrimRight(strings.Join(lines, "\n"), " "))
	}
	return strings.Join(items, "\n")
}

func prefixLines(s, p string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(p+l, " ")
	}
	return strings.Join(lines, "\n")
}

func trimLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t\r")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func (r *renderer) table(t *extast.Table) string {
	var rows [][]string
	for row := t.FirstChild(); row != nil; row = row.NextSibling() {
		var cells []string
		for c := row.FirstChild(); c != nil; c = c.NextSibling() {
			plain := &renderer{src: r.src}
			cells = append(cells, strings.Join(strings.Fields(plain.inlines(c)), " "))
		}
		rows = append(rows, cells)
	}
	cols := len(t.Alignments)
	for _, row := range rows {
		cols = max(cols, len(row))
	}
	if cols == 0 {
		return ""
	}
	widths := make([]int, cols)
	for i := range widths {
		widths[i] = 3
	}
	for _, row := range rows {
		for i, c := range row {
			widths[i] = max(widths[i], utf8.RuneCountInString(c))
		}
	}
	align := func(i int) extast.Alignment {
		if i < len(t.Alignments) {
			return t.Alignments[i]
		}
		return extast.AlignNone
	}
	pad := func(s string, w int, a extast.Alignment) string {
		gap := w - utf8.RuneCountInString(s)
		switch a {
		case extast.AlignRight:
			return strings.Repeat(" ", gap) + s
		case extast.AlignCenter:
			return strings.Repeat(" ", gap/2) + s + strings.Repeat(" ", gap-gap/2)
		}
		return s + strings.Repeat(" ", gap)
	}
	var lines []string
	for ri, row := range rows {
		parts := make([]string, cols)
		for i := range parts {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			parts[i] = pad(cell, widths[i], align(i))
		}
		lines = append(lines, "| "+strings.Join(parts, " | ")+" |")
		if ri == 0 {
			seps := make([]string, cols)
			for i := range seps {
				seps[i] = strings.Repeat("-", widths[i])
			}
			lines = append(lines, "|-"+strings.Join(seps, "-|-")+"-|")
		}
	}
	body := strings.Join(lines, "\n")
	if !r.html {
		return body
	}
	return "<pre>" + Escape(body) + "</pre>"
}

func (r *renderer) inlines(n ast.Node) string {
	var sb strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		sb.WriteString(r.inline(c))
	}
	return sb.String()
}

func (r *renderer) text(b []byte) string {
	b = util.ResolveNumericReferences(b)
	b = util.ResolveEntityNames(b)
	b = util.UnescapePunctuations(b)
	b = bytes.ReplaceAll(b, []byte{0}, []byte("�"))
	return r.esc(string(b))
}

func safeURL(u string) bool {
	l := strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "mailto:")
}

func (r *renderer) link(url, label string) string {
	if !r.html {
		if label == "" || label == url {
			return url
		}
		return label + " (" + url + ")"
	}
	if !safeURL(url) {
		return label
	}
	return `<a href="` + attrEscaper.Replace(url) + `">` + label + `</a>`
}

func (r *renderer) inline(n ast.Node) string {
	switch n := n.(type) {
	case *ast.Text:
		s := r.text(n.Segment.Value(r.src))
		if n.HardLineBreak() || n.SoftLineBreak() {
			s += "\n"
		}
		return s
	case *ast.String:
		return r.esc(string(n.Value))
	case *ast.CodeSpan:
		var sb strings.Builder
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			if t, ok := c.(*ast.Text); ok {
				sb.Write(t.Segment.Value(r.src))
			}
		}
		code := strings.ReplaceAll(sb.String(), "\n", " ")
		return r.tag("code", r.esc(code))
	case *ast.Emphasis:
		if n.Level >= 2 {
			return r.tag("b", r.inlines(n))
		}
		return r.tag("i", r.inlines(n))
	case *extast.Strikethrough:
		return r.tag("s", r.inlines(n))
	case *ast.Link:
		return r.link(string(n.Destination), r.inlines(n))
	case *ast.AutoLink:
		u := string(n.URL(r.src))
		label := r.esc(string(n.Label(r.src)))
		if n.AutoLinkType == ast.AutoLinkEmail && !strings.HasPrefix(strings.ToLower(u), "mailto:") {
			u = "mailto:" + u
		}
		return r.link(u, label)
	case *ast.Image:
		alt := r.inlines(n)
		url := r.esc(string(n.Destination))
		if alt == "" {
			return url
		}
		return alt + " (" + url + ")"
	case *ast.RawHTML:
		var sb strings.Builder
		writeLines(&sb, n.Segments, r.src)
		return r.esc(sb.String())
	}
	return r.inlines(n)
}
