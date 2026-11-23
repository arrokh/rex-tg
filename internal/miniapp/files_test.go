package miniapp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func names(v View) string {
	var n []string
	for _, e := range v.Entries {
		if e.Dir {
			n = append(n, e.Name+"/")
		} else {
			n = append(n, e.Name)
		}
	}
	return strings.Join(n, " ")
}

// newRepo is a git repository with a tracked file, an ignored folder and
// file, a secret, a link out of it, and a nested folder.
func newRepo(t *testing.T) (root, outside string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root = t.TempDir()
	outside = t.TempDir()
	write(t, filepath.Join(outside, "secret.txt"), "outside")
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	write(t, filepath.Join(root, ".gitignore"), "node_modules/\nbuild.log\n")
	write(t, filepath.Join(root, "README.md"), "# Title\n\n**bold** <script>x</script>\n")
	write(t, filepath.Join(root, "main.go"), "package main\n// <tag> & token\n")
	write(t, filepath.Join(root, "src", "a.txt"), "a\n")
	write(t, filepath.Join(root, "node_modules", "x", "index.js"), "x")
	write(t, filepath.Join(root, "build.log"), "log")
	write(t, filepath.Join(root, ".env"), "KEY=1")
	write(t, filepath.Join(root, ".env.example"), "KEY=")
	write(t, filepath.Join(root, "server.pem"), "pem")
	write(t, filepath.Join(root, "bin.dat"), "a\x00b")
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "build.log"), filepath.Join(root, "log-link")); err != nil {
		t.Fatal(err)
	}
	return root, outside
}

// A folder's listing: folders first, then files, without what git ignores,
// .git, secrets, or anything that is not there.
func TestBrowseListsWithoutHiddenFiles(t *testing.T) {
	root, _ := newRepo(t)
	v, err := Browse(context.Background(), root, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Dir || v.Path != "" {
		t.Fatalf("view = %+v", v)
	}
	// "out" is a folder (a link to one): it is listed, but opening it is refused.
	want := "out/ src/ .env.example .gitignore bin.dat log-link main.go README.md"
	if got := names(v); got != want {
		t.Errorf("entries = %q\nwant      %q", got, want)
	}
	sub, err := Browse(context.Background(), root, "src", nil)
	if err != nil || names(sub) != "a.txt" || sub.Path != "src" {
		t.Errorf("src = %+v, %v", sub, err)
	}
}

// No path leaves the root or reaches a hidden file: not "..", not a link
// out, not a link to an ignored file, not .git, not a secret.
func TestBrowseRefusesHiddenAndOutsidePaths(t *testing.T) {
	root, _ := newRepo(t)
	for _, p := range []string{
		"../" + filepath.Base(root), "../../etc/passwd", "/etc/passwd", "out", "out/secret.txt", "log-link",
		".git", ".git/config", ".env", "server.pem", "node_modules", "node_modules/x/index.js", "build.log", "nope.txt",
	} {
		if v, err := Browse(context.Background(), root, p, nil); err != ErrNoFile {
			t.Errorf("%q: %+v, %v; want ErrNoFile", p, v, err)
		}
	}
	// A path is cleaned inside the root: "/etc/passwd" is <root>/etc/passwd,
	// which is not there, ".." is the root, and "a/../src" is src.
	if v, err := Browse(context.Background(), root, "..", nil); err != nil || v.Path != "" || !v.Dir {
		t.Errorf("..: %+v, %v", v, err)
	}
	if v, err := Browse(context.Background(), root, "a/../src", nil); err != nil || v.Path != "src" {
		t.Errorf("a/../src: %+v, %v", v, err)
	}
}

// A file's content is escaped, or rendered when it is Markdown; it is
// redacted first; a binary file shows only its size.
func TestBrowseShowsFiles(t *testing.T) {
	root, _ := newRepo(t)
	redact := func(s string) string { return strings.ReplaceAll(s, "token", "[redacted]") }
	code, err := Browse(context.Background(), root, "main.go", redact)
	if err != nil || !strings.Contains(code.HTML, `<pre class="code">package main`) || !strings.Contains(code.HTML, "&lt;tag&gt; &amp; [redacted]") {
		t.Errorf("main.go = %+v, %v", code, err)
	}
	md, err := Browse(context.Background(), root, "README.md", nil)
	if err != nil || !strings.Contains(md.HTML, "<strong>bold</strong>") || strings.Contains(md.HTML, "<script>") {
		t.Errorf("README.md = %+v, %v", md, err)
	}
	bin, err := Browse(context.Background(), root, "bin.dat", nil)
	if err != nil || bin.HTML != "" || bin.Note != "Binary file, 3 B." {
		t.Errorf("bin.dat = %+v, %v", bin, err)
	}
	ex, err := Browse(context.Background(), root, ".env.example", nil)
	if err != nil || !strings.Contains(ex.HTML, "KEY=") {
		t.Errorf(".env.example = %+v, %v", ex, err)
	}
}

func TestBrowseRendersHTMLInSandbox(t *testing.T) {
	root, _ := newRepo(t)
	write(t, filepath.Join(root, "index.html"), `<h1>Hello</h1><script>parent.location='https://bad.invalid'</script><img src="https://bad.invalid/pixel">`)
	v, err := Browse(context.Background(), root, "index.html", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<iframe class="html-viewer" sandbox=""`, `srcdoc="`, `default-src &#39;none&#39;`, `&lt;script&gt;`, `https://bad.invalid/pixel`} {
		if !strings.Contains(v.HTML, want) {
			t.Errorf("HTML viewer lacks %q: %s", want, v.HTML)
		}
	}
	if strings.Contains(v.HTML, `<script>`) || strings.Contains(v.HTML, `src="https://bad.invalid`) {
		t.Errorf("HTML escaped into the outer page: %s", v.HTML)
	}
}

// A long text file shows its start, cut at a line.
func TestBrowseCutsLongFiles(t *testing.T) {
	root, _ := newRepo(t)
	line := strings.Repeat("é", 99) + "\n" // 199 bytes: the cut falls inside a character
	write(t, filepath.Join(root, "long.txt"), strings.Repeat(line, maxTextBytes/len(line)+50))
	v, err := Browse(context.Background(), root, "long.txt", nil)
	if err != nil || v.HTML == "" || !strings.HasPrefix(v.Note, "The first ") {
		t.Fatalf("long.txt = note %q, %v", v.Note, err)
	}
	if !strings.HasSuffix(v.HTML, "\n</pre>") {
		t.Errorf("the text was not cut at a line: …%q", v.HTML[len(v.HTML)-20:])
	}
}

// Outside a git repository, names that start with a dot are hidden.
func TestBrowseHidesDotFilesOutsideARepository(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".ssh", "config"), "x")
	write(t, filepath.Join(root, ".zshrc"), "x")
	write(t, filepath.Join(root, "notes.txt"), "x")
	v, err := Browse(context.Background(), root, "", nil)
	if err != nil || names(v) != "notes.txt" {
		t.Errorf("entries = %q, %v", names(v), err)
	}
	for _, p := range []string{".ssh", ".ssh/config", ".zshrc"} {
		if _, err := Browse(context.Background(), root, p, nil); err != ErrNoFile {
			t.Errorf("%q: %v", p, err)
		}
	}
}

// A link opens only what its target's path may show: not a key, not .git,
// not an ignored file. A link to a folder lists that folder.
func TestBrowseChecksWhereALinkLeads(t *testing.T) {
	root, _ := newRepo(t)
	write(t, filepath.Join(root, "keys", "id_rsa"), "PRIVATE")
	links := map[string]string{
		"notes.txt": filepath.Join(root, "keys", "id_rsa"),
		"cfg":       filepath.Join(root, ".git", "config"),
		"dotgit":    filepath.Join(root, ".git"),
		"mods":      filepath.Join(root, "node_modules"),
		"code":      filepath.Join(root, "src"),
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"notes.txt", "cfg", "dotgit", "dotgit/config", "mods", "mods/x/index.js", "keys/id_rsa"} {
		if v, err := Browse(context.Background(), root, p, nil); err != ErrNoFile {
			t.Errorf("%q: %+v, %v; want ErrNoFile", p, v, err)
		}
	}
	v, err := Browse(context.Background(), root, "code", nil)
	if err != nil || names(v) != "a.txt" {
		t.Errorf("a link to src lists %q, %v", names(v), err)
	}
	if f, err := Browse(context.Background(), root, "code/a.txt", nil); err != nil || !strings.Contains(f.HTML, "a\n") {
		t.Errorf("code/a.txt: %+v, %v", f, err)
	}
}

// Names of secrets match in any case: macOS finds .env by .ENV.
func TestBrowseSecretNamesIgnoreCase(t *testing.T) {
	root, _ := newRepo(t)
	write(t, filepath.Join(root, "ID_RSA"), "PRIVATE")
	for _, p := range []string{"ID_RSA", "id_rsa", ".ENV", "Server.PEM", "SERVER.pem"} {
		if v, err := Browse(context.Background(), root, p, nil); err != ErrNoFile {
			t.Errorf("%q: %+v, %v; want ErrNoFile", p, v, err)
		}
	}
	if v, _ := Browse(context.Background(), root, "", nil); strings.Contains(names(v), "ID_RSA") {
		t.Errorf("listed: %q", names(v))
	}
}

// A FIFO (or any file that is not a regular file) is not opened: reading it
// could block.
func TestBrowseRefusesAFIFO(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
		t.Skip("no FIFO:", err)
	}
	if v, err := Browse(context.Background(), root, "pipe", nil); err != ErrNoFile {
		t.Errorf("pipe: %+v, %v", v, err)
	}
}

// A picture is shown as a data: URI; one over the limit is named only.
// Markdown drops raw HTML and javascript: links.
func TestBrowseImagesAndUnsafeMarkdown(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "dot.png"), "\x89PNG\r\n\x1a\nfake")
	write(t, filepath.Join(root, "big.jpg"), strings.Repeat("x", maxImageBytes+1))
	write(t, filepath.Join(root, "doc.md"), "[click](javascript:alert(1)) <img src=x onerror=alert(1)>\n")
	v, err := Browse(context.Background(), root, "dot.png", nil)
	if err != nil || !strings.HasPrefix(v.HTML, `<img class="image" alt="" src="data:image/png;base64,`) {
		t.Errorf("dot.png: %+v, %v", v, err)
	}
	if v, err := Browse(context.Background(), root, "big.jpg", nil); err != nil || v.HTML != "" || !strings.Contains(v.Note, "too big") {
		t.Errorf("big.jpg: %q %q, %v", v.HTML, v.Note, err)
	}
	md, err := Browse(context.Background(), root, "doc.md", nil)
	if err != nil || strings.Contains(md.HTML, "javascript:") || strings.Contains(md.HTML, "onerror") {
		t.Errorf("doc.md: %q, %v", md.HTML, err)
	}
}

// A big folder lists its first maxEntries entries, with a note.
func TestBrowseCapsBigFolders(t *testing.T) {
	root := t.TempDir()
	for i := 0; i <= maxEntries; i++ {
		write(t, filepath.Join(root, fmt.Sprintf("f%04d.txt", i)), "")
	}
	v, err := Browse(context.Background(), root, "", nil)
	if err != nil || len(v.Entries) != maxEntries || v.Note != fmt.Sprintf("The first %d of %d entries.", maxEntries, maxEntries+1) {
		t.Errorf("%d entries, note %q, %v", len(v.Entries), v.Note, err)
	}
}

// An oversized directory is read only to the fixed scan limit and says that
// the returned subset may be incomplete.
func TestBrowseBoundsVeryLargeFolders(t *testing.T) {
	root := t.TempDir()
	for i := 0; i <= maxDirectoryEntries; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("entry-%05d.txt", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	v, err := Browse(context.Background(), root, "", nil)
	if err != nil || len(v.Entries) != maxEntries || !strings.Contains(v.Note, "too large to list completely") || !strings.Contains(v.Note, "omitted") {
		t.Errorf("%d entries, note %q, %v", len(v.Entries), v.Note, err)
	}
}
