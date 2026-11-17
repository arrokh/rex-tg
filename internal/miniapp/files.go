package miniapp

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// The file browser shows the files of one folder, the agent's, read-only.
// It never leaves that folder: a path is cleaned, and a link that leads out
// of it is refused. It hides what is likely secret or noise: .git, files
// that git ignores (such as .env and node_modules), names of keys and
// credentials, and, outside a git repository, every name that starts with a
// dot.

// Limits of the file browser.
const (
	maxEntries          = 2000      // entries of one folder
	maxDirectoryEntries = 4096      // raw names retained from one folder listing
	maxTextBytes        = 512 << 10 // of a text file; a longer one shows its start
	maxImageBytes       = 4 << 20   // of an image shown as a picture
)

// Entry is a file or folder in a listing.
type Entry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
}

// View is a folder's listing or a file's content.
type View struct {
	Path    string  `json:"path"` // relative to the root, with "/"; "" is the root
	Dir     bool    `json:"dir"`
	Entries []Entry `json:"entries,omitempty"`
	HTML    string  `json:"html,omitempty"` // a file's content, rendered and escaped
	Note    string  `json:"note,omitempty"` // such as "Binary file, 2.1 MB."
}

// ErrNoFile is a path that is not there, hidden, or outside the root.
var ErrNoFile = errors.New("miniapp: no such file")

// secretNames are names that are never shown, as path.Match patterns.
var secretNames = []string{
	".git", ".env", ".env.*", "*.pem", "*.key", "*.p12", "*.pfx", "*.keystore",
	"id_rsa*", "id_dsa*", "id_ecdsa*", "id_ed25519*", "credentials.json", ".netrc",
}

// secretName tells whether a name is never shown. An example or sample
// file (".env.example") is shown: it holds no secrets. Case does not count:
// macOS finds ".env" by ".ENV" too.
func secretName(name string) bool {
	name = strings.ToLower(name)
	if strings.HasSuffix(name, ".example") || strings.HasSuffix(name, ".sample") {
		return false
	}
	for _, p := range secretNames {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// hiddenName tells whether a name is not shown: a secret's name, or,
// outside a git repository, a name that starts with a dot.
func hiddenName(name string, repo bool) bool {
	return secretName(name) || (!repo && strings.HasPrefix(name, "."))
}

// hiddenPath tells whether any part of a slash path is hidden.
func hiddenPath(rel string, repo bool) bool {
	if rel == "" {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if hiddenName(part, repo) {
			return true
		}
	}
	return false
}

// relSlash is p relative to base, with "/"; "" is base itself.
func relSlash(base, p string) string {
	r, err := filepath.Rel(base, p)
	if err != nil || r == "." {
		return ""
	}
	return filepath.ToSlash(r)
}

// Browse shows rel inside root: a folder's entries or a file's content.
// redact cleans a text file's content before it is shown (nil: as it is).
func Browse(ctx context.Context, root, rel string, redact func(string) string) (View, error) {
	rel = strings.Trim(path.Clean("/"+filepath.ToSlash(rel)), "/")
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return View{}, ErrNoFile
	}
	repo := inRepo(ctx, base)
	if hiddenPath(rel, repo) {
		return View{}, ErrNoFile
	}
	abs := filepath.Join(base, filepath.FromSlash(rel))
	real, err := filepath.EvalSymlinks(abs)
	if err != nil || !within(base, real) {
		return View{}, ErrNoFile
	}
	// Where a link leads is checked like the path asked for: a link must not
	// open a hidden file.
	realRel := relSlash(base, real)
	if hiddenPath(realRel, repo) {
		return View{}, ErrNoFile
	}
	if repo && rel != "" {
		// git does not look past a link in a path, so it is asked about the
		// target, and about the link itself in its real folder.
		check := []string{realRel}
		if parent, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil && within(base, parent) {
			check = append(check, path.Join(relSlash(base, parent), path.Base(rel)))
		}
		ign := ignored(ctx, base, check)
		for _, c := range check {
			if c != "" && ign[c] {
				return View{}, ErrNoFile
			}
		}
	}
	fi, err := os.Stat(real)
	if err != nil {
		return View{}, ErrNoFile
	}
	if fi.IsDir() {
		return listDir(ctx, base, rel, realRel, real, repo)
	}
	return showFile(real, rel, redact)
}

// within tells whether p is root or inside it.
func within(root, p string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}

// readDirPrefix retains at most maxDirectoryEntries names and reads one extra
// to detect truncation. File.ReadDir returns directory order, so callers sort
// the bounded prefix themselves.
func readDirPrefix(dir string) ([]os.DirEntry, bool, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	entries, err := f.ReadDir(maxDirectoryEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	if len(entries) > maxDirectoryEntries {
		return entries[:maxDirectoryEntries], true, nil
	}
	return entries, false, nil
}

// listDir lists the folder real, which is rel as asked and realRel inside
// base. A large folder is only read up to maxDirectoryEntries raw names; a
// partial listing says so rather than implying it is complete.
func listDir(ctx context.Context, base, rel, realRel, real string, repo bool) (View, error) {
	des, partial, err := readDirPrefix(real)
	if err != nil {
		return View{}, ErrNoFile
	}
	v := View{Path: rel, Dir: true}
	var shown []os.DirEntry
	for _, de := range des {
		if !hiddenName(de.Name(), repo) {
			shown = append(shown, de)
		}
	}
	// Folders first: a link to a folder counts as a file here, and moves
	// with the folders once it is looked at.
	sort.Slice(shown, func(i, j int) bool {
		a, b := shown[i], shown[j]
		if a.IsDir() != b.IsDir() {
			return a.IsDir()
		}
		return strings.ToLower(a.Name()) < strings.ToLower(b.Name())
	})
	if partial {
		v.Note = "This folder is too large to list completely; some entries may be omitted."
	} else if len(shown) > maxEntries {
		v.Note = fmt.Sprintf("The first %d of %d entries.", maxEntries, len(shown))
	}
	if len(shown) > maxEntries {
		shown = shown[:maxEntries]
	}
	var ign map[string]bool
	if repo && len(shown) > 0 {
		paths := make([]string, len(shown))
		for i, de := range shown {
			paths[i] = path.Join(realRel, de.Name())
		}
		ign = ignored(ctx, base, paths)
	}
	for _, de := range shown {
		n := de.Name()
		if ign[path.Join(realRel, n)] {
			continue
		}
		e := Entry{Name: n}
		if fi, err := os.Stat(filepath.Join(real, n)); err == nil { // follows a link
			e.Dir = fi.IsDir()
			if !e.Dir {
				e.Size = fi.Size()
			}
		}
		v.Entries = append(v.Entries, e)
	}
	sort.SliceStable(v.Entries, func(i, j int) bool {
		a, b := v.Entries[i], v.Entries[j]
		if a.Dir != b.Dir {
			return a.Dir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return v, nil
}

var imageTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
}

// showFile shows the file real. It opens the file without following a
// link and without waiting (a FIFO put there since the checks does not
// block), and shows it only when what it opened is a regular file.
func showFile(real, rel string, redact func(string) string) (View, error) {
	v := View{Path: rel}
	f, err := os.OpenFile(real, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return View{}, ErrNoFile
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return View{}, ErrNoFile
	}
	size := fi.Size()
	ext := strings.ToLower(path.Ext(rel))
	if mime, ok := imageTypes[ext]; ok {
		if size > maxImageBytes {
			v.Note = fmt.Sprintf("Image, %s: too big to show.", Size(size))
			return v, nil
		}
		data, err := io.ReadAll(io.LimitReader(f, maxImageBytes))
		if err != nil {
			return View{}, ErrNoFile
		}
		v.HTML = `<img class="image" alt="" src="data:` + mime + ";base64," + base64.StdEncoding.EncodeToString(data) + `">`
		return v, nil
	}
	data, err := io.ReadAll(io.LimitReader(f, maxTextBytes))
	if err != nil {
		return View{}, ErrNoFile
	}
	if !isText(data, size > maxTextBytes) {
		v.Note = fmt.Sprintf("Binary file, %s.", Size(size))
		return v, nil
	}
	text := string(data)
	if size > maxTextBytes {
		// Cut at the last whole line, so no character is cut in two.
		if i := strings.LastIndexByte(text, '\n'); i > 0 {
			text = text[:i+1]
		}
		v.Note = fmt.Sprintf("The first %s of %s.", Size(int64(len(text))), Size(size))
	}
	if redact != nil {
		text = redact(text)
	}
	switch ext {
	case ".md", ".markdown":
		v.HTML = `<div class="markdown">` + RenderAnswer(text) + `</div>`
	default:
		v.HTML = `<pre class="code">` + html.EscapeString(text) + `</pre>`
	}
	return v, nil
}

// isText tells whether data looks like text: no NUL byte, and valid UTF-8
// (the end may be cut when the file was cut).
func isText(data []byte, cut bool) bool {
	if bytes.IndexByte(data, 0) >= 0 {
		return false
	}
	if cut {
		for i := 0; i < utf8.UTFMax && len(data) > 0 && !utf8.Valid(data); i++ {
			data = data[:len(data)-1]
		}
	}
	return utf8.Valid(data)
}

// Size is a byte count for people: "512 B", "2.1 KB", "3.4 MB".
func Size(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d B", n)
	case n < 1000*1000:
		return fmt.Sprintf("%.1f KB", float64(n)/1000)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/1000/1000)
}

// gitSlots caps the git calls of the file browser that run at once.
var gitSlots = make(chan struct{}, 2)

// gitCmd runs git in dir with a time limit, without the user's fsmonitor
// (it can start a daemon).
func gitCmd(ctx context.Context, dir string, stdin string, args ...string) ([]byte, error) {
	return gitRun(ctx, dir, 5*time.Second, stdin, args...)
}

// maxGitOutput caps what one git call may print: more is an error.
const maxGitOutput = 32 << 20

// errGitOutput is a git call that printed more than its cap.
var errGitOutput = errors.New("miniapp: git printed too much")

// gitRun runs git in dir like gitCmd, with a time limit of its own. Output
// beyond maxGitOutput is an error.
func gitRun(ctx context.Context, dir string, limit time.Duration, stdin string, args ...string) ([]byte, error) {
	out, over, err := gitOut(ctx, dir, limit, maxGitOutput, stdin, args...)
	if over {
		return nil, errGitOutput
	}
	return out, err
}

// capped keeps the first max bytes written to it. A write beyond them
// fails, so git stops at once (a broken pipe).
type capped struct {
	b    bytes.Buffer
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.b.Len(); len(p) > room {
		c.b.Write(p[:max(room, 0)])
		c.over = true
		return 0, errGitOutput
	}
	return c.b.Write(p)
}

// gitOut runs git in dir and keeps the first max bytes of its output; over
// tells that there was more (then err is nil).
func gitOut(ctx context.Context, dir string, limit time.Duration, max int, stdin string, args ...string) (out []byte, over bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	select { // a few at a time, however many requests come
	case gitSlots <- struct{}{}:
		defer func() { <-gitSlots }()
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "-c", "core.fsmonitor=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	c := &capped{max: max}
	cmd.Stdout = c
	err = cmd.Run()
	if c.over {
		return c.b.Bytes(), true, nil
	}
	return c.b.Bytes(), false, err
}

// inRepo tells whether dir is in a git repository's work tree.
func inRepo(ctx context.Context, dir string) bool {
	out, err := gitCmd(ctx, dir, "", "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// ignored returns which of the paths (relative to dir, with "/") git
// ignores. A tracked file is not ignored. When git fails, every path counts
// as ignored: nothing is shown that should be hidden.
func ignored(ctx context.Context, dir string, paths []string) map[string]bool {
	out := make(map[string]bool, len(paths))
	var in strings.Builder
	for _, p := range paths {
		in.WriteString(p)
		in.WriteByte(0)
	}
	res, err := gitCmd(ctx, dir, in.String(), "check-ignore", "-z", "--stdin")
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) { // 1: none is ignored
		for _, p := range paths {
			out[p] = true
		}
		return out
	}
	for _, p := range strings.Split(string(res), "\x00") {
		if p != "" {
			out[p] = true
		}
	}
	return out
}
