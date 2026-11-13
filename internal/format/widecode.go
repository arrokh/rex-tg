package format

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// WideCodeLine is the longest code line that stays in a message. A phone
// shows about 40 monospace characters on a line, so a wider block scrolls
// sideways or wraps until it cannot be read; a block with a line over this
// goes as a file instead (SplitWideCode).
const WideCodeLine = 80

// Lines of a wide block that stay in the message, and their width.
const (
	previewLines = 3
	previewWidth = 60
)

// CodeFile is a code block taken out of a message, to be sent as a file.
type CodeFile struct {
	Name    string // such as "code-1.go"
	Content string
}

// SplitWideCode takes the fenced code blocks that have a line wider than
// WideCodeLine out of markdown. Each one leaves a short preview and a line
// that names its file; the files come back in order. Markdown without such a
// block comes back as it is.
func SplitWideCode(markdown string) (string, []CodeFile) {
	lines := strings.SplitAfter(markdown, "\n")
	var out strings.Builder
	var files []CodeFile
	for i := 0; i < len(lines); i++ {
		indent, fence, lang, ok := openFence(lines[i])
		if !ok {
			out.WriteString(lines[i])
			continue
		}
		end := -1
		for j := i + 1; j < len(lines); j++ {
			if closesFence(lines[j], fence) {
				end = j
				break
			}
		}
		if end < 0 { // not closed: leave the rest as it is
			for _, l := range lines[i:] {
				out.WriteString(l)
			}
			break
		}
		body := make([]string, 0, end-i-1)
		for _, l := range lines[i+1 : end] {
			body = append(body, strings.TrimRight(strings.TrimPrefix(l, indent), "\r\n"))
		}
		if !wide(body) {
			for _, l := range lines[i : end+1] {
				out.WriteString(l)
			}
			i = end
			continue
		}
		f := CodeFile{Name: fmt.Sprintf("code-%d%s", len(files)+1, codeExt(lang)), Content: strings.Join(body, "\n") + "\n"}
		files = append(files, f)
		fmt.Fprintf(&out, "%s%s%s\n", indent, fence, lang)
		for k, l := range body {
			if k == previewLines {
				fmt.Fprintf(&out, "%s…\n", indent)
				break
			}
			fmt.Fprintf(&out, "%s%s\n", indent, cutLine(l, previewWidth))
		}
		fmt.Fprintf(&out, "%s%s\n", indent, fence)
		fmt.Fprintf(&out, "%s_Full block (%d lines) in `%s`, below._", indent, len(body), f.Name)
		if !strings.HasSuffix(lines[end], "\n") {
			i = end
			continue
		}
		out.WriteString("\n")
		i = end
	}
	if len(files) == 0 {
		return markdown, nil
	}
	return out.String(), files
}

// openFence reads a line that opens a fenced code block: its indent, the
// fence (``` or ~~~, three or more), and the language word after it.
func openFence(l string) (indent, fence, lang string, ok bool) {
	t := strings.TrimRight(l, "\r\n")
	rest := strings.TrimLeft(t, " \t")
	indent = t[:len(t)-len(rest)]
	for _, c := range []byte{'`', '~'} {
		n := 0
		for n < len(rest) && rest[n] == c {
			n++
		}
		if n >= 3 {
			info := strings.TrimSpace(rest[n:])
			if c == '`' && strings.Contains(info, "`") {
				return "", "", "", false // inline code, not a fence
			}
			if f := strings.Fields(info); len(f) > 0 {
				lang = f[0]
			}
			return indent, rest[:n], lang, true
		}
	}
	return "", "", "", false
}

// closesFence tells whether l closes a block opened with fence: the same
// mark, at least as long, and nothing after it.
func closesFence(l, fence string) bool {
	t := strings.TrimSpace(l)
	if len(t) < len(fence) || t[0] != fence[0] {
		return false
	}
	return strings.Trim(t, string(fence[0])) == ""
}

// wide tells whether a line of the block is wider than WideCodeLine; a tab
// counts as four characters.
func wide(body []string) bool {
	for _, l := range body {
		if utf8.RuneCountInString(strings.ReplaceAll(l, "\t", "    ")) > WideCodeLine {
			return true
		}
	}
	return false
}

func cutLine(l string, n int) string {
	r := []rune(l)
	if len(r) <= n {
		return l
	}
	return string(r[:n-1]) + "…"
}

// codeExts maps a fence's language to a file extension.
var codeExts = map[string]string{
	"go": ".go", "golang": ".go", "py": ".py", "python": ".py", "js": ".js", "javascript": ".js",
	"ts": ".ts", "typescript": ".ts", "tsx": ".tsx", "jsx": ".jsx", "json": ".json", "jsonl": ".jsonl",
	"yaml": ".yaml", "yml": ".yaml", "toml": ".toml", "sh": ".sh", "bash": ".sh", "zsh": ".sh",
	"shell": ".sh", "console": ".txt", "sql": ".sql", "html": ".html", "css": ".css", "md": ".md",
	"markdown": ".md", "diff": ".diff", "patch": ".diff", "rs": ".rs", "rust": ".rs", "java": ".java",
	"kt": ".kt", "kotlin": ".kt", "swift": ".swift", "rb": ".rb", "ruby": ".rb", "c": ".c", "cpp": ".cpp",
	"xml": ".xml", "csv": ".csv",
}

func codeExt(lang string) string {
	if x, ok := codeExts[strings.ToLower(lang)]; ok {
		return x
	}
	return ".txt"
}
