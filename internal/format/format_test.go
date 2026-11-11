package format

import (
	"regexp"
	"strings"
	"testing"
)

func TestEscape(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"plain", "plain"},
		{"a < b && c > d", "a &lt; b &amp;&amp; c &gt; d"},
		{`"quoted"`, `"quoted"`},
		{"&lt;", "&amp;lt;"},
	}
	for _, tc := range tests {
		if got := Escape(tc.in); got != tc.want {
			t.Errorf("Escape(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRenderHTML(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"paragraph", "Hello world", "Hello world"},
		{"two paragraphs", "one\n\ntwo", "one\n\ntwo"},
		{"soft break", "one\ntwo", "one\ntwo"},
		{"heading", "# Title\n\ntext", "<b>Title</b>\n\ntext"},
		{"heading level 3", "### Sub", "<b>Sub</b>"},
		{"bold", "a **b** c", "a <b>b</b> c"},
		{"italic", "a *b* c", "a <i>b</i> c"},
		{"bold italic", "***x***", "<i><b>x</b></i>"},
		{"strike", "a ~~b~~ c", "a <s>b</s> c"},
		{"inline code", "use `a<b` here", "use <code>a&lt;b</code> here"},
		{"escaping", "1 < 2 & 3 > 2", "1 &lt; 2 &amp; 3 &gt; 2"},
		{"backslash escape", `\*not italic\*`, "*not italic*"},
		{"entity", "a &amp; b", "a &amp; b"},
		{"bullets", "- a\n- b", "• a\n• b"},
		{"ordered", "1. a\n2. b", "1. a\n2. b"},
		{"ordered start", "3. a\n4. b", "3. a\n4. b"},
		{"nested list", "- a\n  - b\n    - c\n- d", "• a\n  • b\n    • c\n• d"},
		{"nested ordered", "1. a\n   - b\n2. c", "1. a\n  • b\n2. c"},
		{"fenced go", "```go\nfmt.Println(\"<x>\")\n```", "<pre><code class=\"language-go\">fmt.Println(\"&lt;x&gt;\")</code></pre>"},
		{"fenced no lang", "```\nplain\n```", "<pre><code>plain</code></pre>"},
		{"tilde fence", "~~~sh\nls\n~~~", "<pre><code class=\"language-sh\">ls</code></pre>"},
		{"indented code", "    a := 1\n    b := 2", "<pre><code>a := 1\nb := 2</code></pre>"},
		{"unsafe lang", "```a\"b\nx\n```", "<pre><code>x</code></pre>"},
		{"link", "[go](https://go.dev/a?b=1&c=2)", `<a href="https://go.dev/a?b=1&amp;c=2">go</a>`},
		{"mailto", "[me](mailto:a@b.co)", `<a href="mailto:a@b.co">me</a>`},
		{"link quote", `[x](<https://a.b/"c>)`, `<a href="https://a.b/&quot;c">x</a>`},
		{"javascript link", "[x](javascript:alert(1))", "x"},
		{"file link", "[x](file:///etc/passwd)", "x"},
		{"relative link", "[x](docs/a.md)", "x"},
		{"autolink", "<https://go.dev>", `<a href="https://go.dev">https://go.dev</a>`},
		{"autolink email", "<a@b.co>", `<a href="mailto:a@b.co">a@b.co</a>`},
		{"autolink unsafe", "<ftp://x.y/z>", "ftp://x.y/z"},
		{"image", "![logo](https://x.y/l.png)", "logo (https://x.y/l.png)"},
		{"blockquote", "> quoted\n> text\n\nafter", "<blockquote>quoted\ntext</blockquote>\n\nafter"},
		{"blockquote paragraphs", "> a\n>\n> b", "<blockquote>a\n\nb</blockquote>"},
		{"thematic break", "a\n\n---\n\nb", "a\n\n———\n\nb"},
		{"raw html inline", "x <b>y</b> z", "x &lt;b&gt;y&lt;/b&gt; z"},
		{"raw html block", "<div>\nhi\n</div>", "&lt;div&gt;\nhi\n&lt;/div&gt;"},
		{"raw html script", "<script>alert(1)</script>", "&lt;script&gt;alert(1)&lt;/script&gt;"},
		{"hard break", "a  \nb", "a\nb"},
		{"trailing space", "text   \n\n\n", "text"},
		{"table", "| Name | Qty |\n|:-----|----:|\n| apple | 3 |\n| fig | 12 |",
			"<pre>| Name  | Qty |\n|-------|-----|\n| apple |   3 |\n| fig   |  12 |</pre>"},
		{"table center and escape", "| a<b |\n|:-:|\n| x |",
			"<pre>| a&lt;b |\n|-----|\n|  x  |</pre>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RenderHTML(tc.in)
			if got != tc.want {
				t.Errorf("RenderHTML(%q)\n got: %q\nwant: %q", tc.in, got, tc.want)
			}
			if strings.HasSuffix(got, " ") || strings.HasSuffix(got, "\n") {
				t.Errorf("trailing whitespace in %q", got)
			}
		})
	}
}

func TestPlain(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"heading and bold", "# Title\n\n**bold** and *it* and `code`", "Title\n\nbold and it and code"},
		{"list", "- a\n- b", "• a\n• b"},
		{"link", "[go](https://go.dev)", "go (https://go.dev)"},
		{"code block", "```go\nx := 1 < 2\n```", "x := 1 < 2"},
		{"blockquote", "> hi", "> hi"},
		{"raw html", "a <b>c</b>", "a <b>c</b>"},
		{"image", "![alt](https://x.y/i.png)", "alt (https://x.y/i.png)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Plain(tc.in); got != tc.want {
				t.Errorf("Plain(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestUTF16Len(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"héllo", 5},
		{"日本語", 3},
		{"😀", 2},
		{"a😀b", 4},
		{"👨‍👩‍👧", 8},
		{"𠀋", 2},
	}
	for _, tc := range tests {
		if got := UTF16Len(tc.in); got != tc.want {
			t.Errorf("UTF16Len(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestVisibleLen(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"<b>abc</b>", 3},
		{"a &amp; b", 5},
		{"&lt;&gt;&quot;", 3},
		{`<a href="https://x.y/?a=1&amp;b=2">go</a>`, 2},
		{`<pre><code class="language-go">x &lt; 1</code></pre>`, 5},
		{"<b>😀</b>", 2},
		{"日本<i>語</i>", 3},
	}
	for _, tc := range tests {
		if got := VisibleLen(tc.in); got != tc.want {
			t.Errorf("VisibleLen(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestRedact(t *testing.T) {
	pem := "-----BEGIN RSA PRIV" + "ATE KEY-----\nFAKEKEYBODYLINEONEFAKEKEYBODYLINEONEFAKEKEYBODYLINEONEFAKEKEYBOD\nFAKEKEYBODYLINETWOFAKEKEYBODYLINETWOFAKE==\n-----END RSA PRIV" + "ATE KEY-----"
	positives := []struct{ name, in, want string }{
		{"sk", "key sk-" + "abcdefghijklmnopqrstuvwx end", "key [redacted] end"},
		{"sk-proj", "sk-proj-" + "AbC_123-defGHI456jklMNO789", "[redacted]"},
		{"sk-ant", "sk-ant-" + "api03-AbCdEfGhIjKlMnOpQrStUv", "[redacted]"},
		{"ghp", "token=ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789", "token=[redacted]"},
		{"gho", "gho_" + "abcdefghijklmnopqrstuvwxyz0123456789", "[redacted]"},
		{"ghu", "ghu_" + "abcdefghijklmnopqrstuvwxyz0123456789", "[redacted]"},
		{"ghs", "ghs_" + "abcdefghijklmnopqrstuvwxyz0123456789", "[redacted]"},
		{"ghr", "ghr_" + "abcdefghijklmnopqrstuvwxyz0123456789", "[redacted]"},
		{"github_pat", "github_pat_" + "11ABCDEFG0abcdefghijkl_mnopqrstuvwxyz0123456789", "[redacted]"},
		{"aws", "AWS AKIA" + "IOSFODNN7EXAMPLE here", "AWS [redacted] here"},
		{"slack", "xoxb-" + "123456789012-abcdefghijkl", "[redacted]"},
		{"slack p", "xoxp-" + "123456789012-abcdefghijkl", "[redacted]"},
		{"pem", "before\n" + pem + "\nafter", "before\n[redacted]\nafter"},
		{"pem no end", "x\n-----BEGIN PRIV" + "ATE KEY-----\nFAKEKEYBODYOPEN", "x\n[redacted]"},
		{"jwt", "jwt eyJhbGciOiJIUzI1NiJ9." + "eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk ok", "jwt [redacted] ok"},
		{"telegram", "bot 123456789:" + "AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw0 x", "bot [redacted] x"},
		{"bearer", "Authorization: Bearer abcdef0123456789ABCDEF.xyz", "Authorization: Bearer [redacted]"},
		{"bearer lower", "bearer abcdef0123456789ABCDEF", "bearer [redacted]"},
		{"bearer jwt", "Bearer eyJhbGciOiJIUzI1NiJ9." + "eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K", "Bearer [redacted]"},
		{"two", "sk-" + "abcdefghijklmnopqrstuvwx and AKIA" + "IOSFODNN7EXAMPLE", "[redacted] and [redacted]"},
	}
	for _, tc := range positives {
		t.Run(tc.name, func(t *testing.T) {
			if got := Redact(tc.in); got != tc.want {
				t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	negatives := []string{
		"",
		"just ordinary text, nothing to see.",
		"commit 3f786850e387550fdab836ed7e6dc881de23001b fix bug",
		"sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		"id 123e4567-e89b-12d3-a456-426614174000",
		"the risk-assessment-for-the-quarter-plan is ready",
		"a task-oriented-workflow-for-everyone-involved",
		"sk-short",
		"AKIA123",
		"Bearer of bad news",
		"Bearer authentication",
		"time 12:34:56 and 20240101:1200",
		"ghp_short",
		"eyJ is a prefix",
		"-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----",
	}
	for _, in := range negatives {
		if got := Redact(in); got != in {
			t.Errorf("Redact(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"cut", "hello world", 8, "hello w…"},
		{"newlines", "a\nb\t\tc  d", 20, "a b c d"},
		{"multiline cut", "line one\nline two\nline three", 12, "line one li…"},
		{"unicode", "日本語のテキスト", 5, "日本語の…"},
		{"emoji", "😀😀😀😀", 3, "😀😀…"},
		{"one", "abc", 1, "…"},
		{"zero", "abc", 0, ""},
		{"empty", "", 5, ""},
		{"trailing space before cut", "ab cd ef", 6, "ab cd…"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Truncate(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
			if n := len([]rune(got)); n > tc.max {
				t.Errorf("result has %d runes, max %d", n, tc.max)
			}
		})
	}
}

var tagRe = regexp.MustCompile(`<(/?)([a-z]+)([^>]*)>`)

var allowedTags = map[string]bool{
	"b": true, "strong": true, "i": true, "em": true, "u": true, "ins": true,
	"s": true, "strike": true, "del": true, "span": true, "a": true,
	"code": true, "pre": true, "blockquote": true,
}

// checkHTML reports whether h has only allowed tags, properly nested, with no
// raw < or > in text.
func checkHTML(h string) string {
	var stack []string
	rest := h
	for {
		i := strings.IndexAny(rest, "<>")
		if i < 0 {
			break
		}
		if rest[i] == '>' {
			return "stray >"
		}
		loc := tagRe.FindStringSubmatchIndex(rest[i:])
		if loc == nil || loc[0] != 0 {
			return "stray <"
		}
		m := tagRe.FindStringSubmatch(rest[i:])
		name := m[2]
		if !allowedTags[name] {
			return "tag not allowed: " + name
		}
		if m[1] == "/" {
			if len(stack) == 0 || stack[len(stack)-1] != name {
				return "bad close: " + name
			}
			stack = stack[:len(stack)-1]
		} else {
			stack = append(stack, name)
		}
		rest = rest[i+loc[1]:]
	}
	if len(stack) != 0 {
		return "unclosed: " + strings.Join(stack, ",")
	}
	return ""
}

func TestCheckHTMLChecker(t *testing.T) {
	if msg := checkHTML("<b>a</b>"); msg != "" {
		t.Errorf("valid rejected: %s", msg)
	}
	for _, bad := range []string{"<b>a", "<b><i>a</b></i>", "a < b", "<div>x</div>"} {
		if checkHTML(bad) == "" {
			t.Errorf("invalid accepted: %q", bad)
		}
	}
}

func checkParts(t *testing.T, parts []Part, limit int) {
	t.Helper()
	n := len(parts)
	for i, p := range parts {
		if msg := checkHTML(p.HTML); msg != "" {
			t.Errorf("part %d invalid HTML (%s): %q", i+1, msg, p.HTML)
		}
		if v := VisibleLen(p.HTML); v > limit {
			t.Errorf("part %d visible length %d > limit %d", i+1, v, limit)
		}
		if n > 1 {
			suffix := " (" + itoa(i+1) + "/" + itoa(n) + ")"
			if !strings.HasSuffix(p.HTML, suffix) || !strings.HasSuffix(p.Plain, suffix) {
				t.Errorf("part %d lacks suffix %q: html=%q plain=%q", i+1, suffix, p.HTML, p.Plain)
			}
		} else if strings.Contains(p.HTML, "(1/1)") {
			t.Errorf("single part has suffix: %q", p.HTML)
		}
		if strings.TrimSpace(p.Plain) == "" {
			t.Errorf("part %d has empty plain text", i+1)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func TestPartsEmpty(t *testing.T) {
	for _, in := range []string{"", "  \n\n"} {
		if got := Parts(in, 100); len(got) != 0 {
			t.Errorf("Parts(%q) = %v, want none", in, got)
		}
	}
}

func TestPartsFitsInOne(t *testing.T) {
	got := Parts("# Hi\n\n**there**", 100)
	want := []Part{{HTML: "<b>Hi</b>\n\n<b>there</b>", Plain: "Hi\n\nthere"}}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("got %#v, want %#v", got, want)
	}
	// Exactly at the limit still fits in one part, without a suffix.
	got = Parts("abcde", 5)
	if len(got) != 1 || got[0].HTML != "abcde" {
		t.Errorf("exact fit: got %#v", got)
	}
}

func TestPartsSplitsAtBlankLines(t *testing.T) {
	in := "aaaa aaaa\n\nbbbb bbbb\n\ncccc cccc"
	got := Parts(in, 18)
	want := []Part{
		{"aaaa aaaa (1/3)", "aaaa aaaa (1/3)"},
		{"bbbb bbbb (2/3)", "bbbb bbbb (2/3)"},
		{"cccc cccc (3/3)", "cccc cccc (3/3)"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d parts: %#v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("part %d = %#v, want %#v", i+1, got[i], want[i])
		}
	}
	checkParts(t, got, 18)
}

func TestPartsPacksParagraphs(t *testing.T) {
	in := "aaa\n\nbbb\n\nccc\n\nddd"
	got := Parts(in, 16)
	want := []string{"aaa\n\nbbb (1/2)", "ccc\n\nddd (2/2)"}
	if len(got) != 2 || got[0].HTML != want[0] || got[1].HTML != want[1] {
		t.Errorf("got %#v, want %q", got, want)
	}
	checkParts(t, got, 16)
}

func TestPartsSplitsAtLines(t *testing.T) {
	in := "line one\nline two\nline three\nline four"
	got := Parts(in, 24)
	checkParts(t, got, 24)
	if len(got) < 2 {
		t.Fatalf("expected a split, got %#v", got)
	}
	for i, p := range got {
		body := strings.TrimSuffix(p.HTML, " ("+itoa(i+1)+"/"+itoa(len(got))+")")
		for _, l := range strings.Split(body, "\n") {
			if !strings.HasPrefix(l, "line ") {
				t.Errorf("line was cut in the middle: %q", l)
			}
		}
	}
}

func TestPartsHardSplit(t *testing.T) {
	in := strings.Repeat("x", 50)
	got := Parts(in, 20)
	checkParts(t, got, 20)
	joined := ""
	for i, p := range got {
		joined += strings.TrimSuffix(p.HTML, " ("+itoa(i+1)+"/"+itoa(len(got))+")")
	}
	if joined != in {
		t.Errorf("text lost: %q", joined)
	}
	if len(got) < 3 {
		t.Errorf("expected at least 3 parts, got %d", len(got))
	}
}

func TestPartsKeepsFittingCodeBlockWhole(t *testing.T) {
	code := "```go\nfunc a() {\n\n\treturn\n}\n```"
	in := strings.Repeat("word ", 8) + "\n\n" + code + "\n\n" + strings.Repeat("tail ", 8)
	got := Parts(in, 60)
	checkParts(t, got, 60)
	if len(got) < 2 {
		t.Fatalf("expected a split, got %#v", got)
	}
	whole := "<pre><code class=\"language-go\">func a() {\n\n\treturn\n}</code></pre>"
	found := 0
	for _, p := range got {
		if strings.Contains(p.HTML, whole) {
			found++
		} else if strings.Contains(p.HTML, "<pre>") {
			t.Errorf("code block split: %q", p.HTML)
		}
	}
	if found != 1 {
		t.Errorf("code block found whole %d times in %#v", found, got)
	}
}

func TestPartsSplitsOversizedCodeBlock(t *testing.T) {
	var lines []string
	for i := 0; i < 12; i++ {
		lines = append(lines, "line"+itoa(i)+" := "+itoa(i))
	}
	in := "```go\n" + strings.Join(lines, "\n") + "\n```"
	got := Parts(in, 80)
	checkParts(t, got, 80)
	if len(got) < 2 {
		t.Fatalf("expected a split, got %#v", got)
	}
	var all []string
	for i, p := range got {
		if !strings.HasPrefix(p.HTML, `<pre><code class="language-go">`) {
			t.Errorf("part %d not fenced with language: %q", i+1, p.HTML)
		}
		body := strings.TrimSuffix(p.HTML, " ("+itoa(i+1)+"/"+itoa(len(got))+")")
		body = strings.TrimPrefix(body, `<pre><code class="language-go">`)
		body = strings.TrimSuffix(body, "</code></pre>")
		all = append(all, strings.Split(body, "\n")...)
	}
	if strings.Join(all, "\n") != strings.Join(lines, "\n") {
		t.Errorf("code lines changed:\n%q", all)
	}
}

func TestPartsOversizedCodeLongLine(t *testing.T) {
	in := "```\n" + strings.Repeat("y", 100) + "\n```"
	got := Parts(in, 40)
	checkParts(t, got, 40)
	if len(got) < 3 {
		t.Errorf("expected several parts, got %d", len(got))
	}
}

func TestPartsEscapedAndFormattedText(t *testing.T) {
	in := strings.Repeat("**bold** a<b & c `code` [l](https://x.y/?a=1&b=2)\n\n", 20)
	got := Parts(in, 120)
	checkParts(t, got, 120)
	if len(got) < 2 {
		t.Fatalf("expected a split, got %d parts", len(got))
	}
}

func TestPartsEmojiNearLimit(t *testing.T) {
	// 40 emoji are 80 UTF-16 units; the limit is tight on purpose.
	in := strings.Repeat("😀", 40) + "\n\n" + strings.Repeat("🎉", 40) + "\n\n" + strings.Repeat("日", 30)
	for _, limit := range []int{40, 60, 79, 80, 81, 90, 100, 170} {
		got := Parts(in, limit)
		checkParts(t, got, limit)
		if len(got) == 0 {
			t.Errorf("limit %d: no parts", limit)
		}
		var sb strings.Builder
		for i, p := range got {
			body := p.HTML
			if len(got) > 1 {
				body = strings.TrimSuffix(body, " ("+itoa(i+1)+"/"+itoa(len(got))+")")
			}
			sb.WriteString(strings.ReplaceAll(body, "\n", ""))
		}
		if sb.String() != strings.ReplaceAll(in, "\n", "") {
			t.Errorf("limit %d: emoji lost or corrupted", limit)
		}
	}
}

func TestPartsManyPartsSuffixWidth(t *testing.T) {
	// More than 9 parts, so the suffix grows to two digits.
	in := strings.Repeat("0123456789\n\n", 30)
	got := Parts(in, 16)
	if len(got) < 10 {
		t.Fatalf("expected 10+ parts, got %d", len(got))
	}
	checkParts(t, got, 16)
}

func TestPartsRawHTMLStaysEscaped(t *testing.T) {
	in := strings.Repeat("<div>x</div> text\n\n", 10)
	got := Parts(in, 60)
	checkParts(t, got, 60)
}
