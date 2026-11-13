package miniapp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The changes view shows what changed in a git repository, from one of
// three bases:
//
//   - BaseHead, the last commit: what is not committed, staged or not, and
//     the new files that git does not ignore.
//   - BaseBranch, where the branch left the main branch: all the branch's
//     work, committed or not, and the new files.
//   - BaseTurn, the agent's last turn: the difference of the snapshots that
//     the daemon took at the turn's start and end (TurnRange).
//
// It only reads: it writes no index and no object. Like the file browser,
// it never shows the content of a file whose name looks secret, and it
// cleans what it shows with redact.

// Bases of a changes view.
const (
	BaseHead   = "head"   // the last commit
	BaseBranch = "branch" // where the branch left the main branch
	BaseTurn   = "turn"   // the agent's last turn
)

// TurnRange is what a turn changed: snapshots of a repository's files, as
// git trees, from the turn's start and end.
type TurnRange struct {
	Top  string    `json:"top"`  // the repository's top folder
	From string    `json:"from"` // the tree at the start
	To   string    `json:"to"`   // the tree at the end
	At   time.Time `json:"at"`   // when the turn ended
}

// Limits of the changes view.
const (
	maxChangeFiles   = 2000     // files in a list
	maxFileDiffBytes = 1 << 20  // of one file's diff
	maxCountBytes    = 8 << 20  // a new file's lines are counted up to this size
	maxCountTotal    = 64 << 20 // and all new files' lines up to this sum
	changesTimeout   = 15 * time.Second
)

// ErrNotRepo is a folder that is not in a git repository.
var ErrNotRepo = errors.New("miniapp: not a git repository")

// ErrNoBase is a base that a repository does not have, such as BaseBranch
// without a main branch.
var ErrNoBase = errors.New("miniapp: no such base")

// Change is a changed file in a list.
type Change struct {
	Path    string `json:"path"`   // relative to the repository's top, with "/"
	Status  string `json:"status"` // modified, added, deleted, untracked, type (a file became a link or back), conflict
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Binary  bool   `json:"binary,omitempty"`
	Hidden  bool   `json:"hidden,omitempty"` // its name looks secret: its changes are not shown
}

// ChangeList is the list of a changes view.
type ChangeList struct {
	Base    string   `json:"base"`              // BaseHead, BaseBranch or BaseTurn
	Branch  string   `json:"branch,omitempty"`  // the current branch, or the commit when none
	Main    string   `json:"main,omitempty"`    // what BaseBranch compares with, such as "origin/main"; "" for no BaseBranch
	TurnAt  string   `json:"turn_at,omitempty"` // when the last turn ended, "15:04"; "" for no BaseTurn
	Files   []Change `json:"files"`
	Total   int      `json:"total"` // all changed files, also those beyond the list's limit
	Added   int      `json:"added"`
	Removed int      `json:"removed"`
	Note    string   `json:"note,omitempty"`
	At      string   `json:"at,omitempty"` // when it was read, "15:04:05"
}

// FileChange is the change of one file.
type FileChange struct {
	Path string `json:"path"`
	HTML string `json:"html,omitempty"` // rendered and escaped
	Note string `json:"note,omitempty"`
}

// repo is what the changes view knows of a repository.
type repo struct {
	top    string // the top folder, as git names it
	real   string // the top folder without links
	head   string // the last commit; "" before the first one
	branch string
	main   string // the main branch, such as "origin/main"; "" for none
	fork   string // where HEAD left main; "" for none
}

// openRepo reads the repository of dir.
func openRepo(ctx context.Context, dir string) (repo, error) {
	var r repo
	out, err := gitLit(ctx, dir, changesTimeout, "", "rev-parse", "--show-toplevel")
	if err != nil {
		return r, ErrNotRepo
	}
	r.top = strings.TrimRight(string(out), "\n")
	if r.top == "" {
		return r, ErrNotRepo
	}
	if r.real, err = filepath.EvalSymlinks(r.top); err != nil {
		return r, ErrNotRepo
	}
	if out, err := gitLit(ctx, r.top, changesTimeout, "", "rev-parse", "--verify", "-q", "HEAD^{commit}"); err == nil {
		r.head = strings.TrimSpace(string(out))
	}
	if out, err := gitLit(ctx, r.top, changesTimeout, "", "branch", "--show-current"); err == nil {
		r.branch = strings.TrimSpace(string(out))
	}
	if r.branch == "" && len(r.head) >= 7 {
		r.branch = r.head[:7]
	}
	if r.head == "" {
		return r, nil
	}
	// The main branch: the remote's default, else a usual name.
	var cands []string
	if out, err := gitLit(ctx, r.top, changesTimeout, "", "symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			cands = append(cands, s)
		}
	}
	cands = append(cands, "origin/main", "origin/master", "main", "master")
	for _, c := range cands {
		ref := "refs/heads/" + c
		if strings.HasPrefix(c, "origin/") {
			ref = "refs/remotes/" + c
		}
		if _, err := gitLit(ctx, r.top, changesTimeout, "", "rev-parse", "--verify", "-q", ref+"^{commit}"); err == nil {
			r.main = c
			break
		}
	}
	if r.main != "" {
		if out, err := gitLit(ctx, r.top, changesTimeout, "", "merge-base", "HEAD", r.main); err == nil {
			r.fork = strings.TrimSpace(string(out))
		}
	}
	return r, nil
}

// turnOf is the turn range when it is of this repository.
func (r repo) turnOf(t TurnRange) (TurnRange, bool) {
	return t, t.From != "" && t.To != "" && t.Top == r.top
}

// spec is what a base compares: from, and to, which is "" for the working
// files (with the new files that git does not track).
func (r repo) spec(ctx context.Context, base string, turn TurnRange) (from, to string, err error) {
	switch base {
	case BaseHead:
		if r.head != "" {
			return r.head, "", nil
		}
		// Before the first commit: everything is new.
		out, err := gitLit(ctx, r.top, changesTimeout, "", "hash-object", "-t", "tree", os.DevNull)
		if err != nil {
			return "", "", err
		}
		return strings.TrimSpace(string(out)), "", nil
	case BaseBranch:
		if r.fork != "" {
			return r.fork, "", nil
		}
	case BaseTurn:
		if t, ok := r.turnOf(turn); ok {
			return t.From, t.To, nil
		}
	}
	return "", "", ErrNoBase
}

// gitLit runs git for the changes view. Paths after "--" are taken as they
// are, not as patterns (check-ignore refuses this, so it is not gitCmd's).
func gitLit(ctx context.Context, dir string, limit time.Duration, stdin string, args ...string) ([]byte, error) {
	return gitRun(ctx, dir, limit, stdin, append([]string{"--literal-pathspecs"}, args...)...)
}

// diffArgs are the options of every diff: no colors, and no program of the
// repository's (an external diff or a text conversion).
var diffArgs = []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames"}

// AllChanges lists the changes of the repository of root since each base
// that it has: BaseHead first, then BaseBranch and BaseTurn when they are
// there. It reads the repository once.
func AllChanges(ctx context.Context, root string, turn TurnRange) ([]ChangeList, error) {
	r, err := openRepo(ctx, root)
	if err != nil {
		return nil, err
	}
	head, err := r.list(ctx, BaseHead, turn)
	if err != nil {
		return nil, err
	}
	out := []ChangeList{head}
	for _, b := range []string{BaseBranch, BaseTurn} {
		if l, err := r.list(ctx, b, turn); err == nil {
			out = append(out, l)
		} else if !errors.Is(err, ErrNoBase) {
			return nil, err
		}
	}
	return out, nil
}

// ListChanges lists the changes of the repository of root since base.
// turn is the topic's last turn (zero: none). With base "", it takes the
// first of BaseHead, BaseBranch and BaseTurn that shows changes, else
// BaseHead.
func ListChanges(ctx context.Context, root, base string, turn TurnRange) (ChangeList, error) {
	r, err := openRepo(ctx, root)
	if err != nil {
		return ChangeList{}, err
	}
	if base != "" {
		return r.list(ctx, base, turn)
	}
	first, err := r.list(ctx, BaseHead, turn)
	if err != nil || first.Total > 0 {
		return first, err
	}
	for _, b := range []string{BaseBranch, BaseTurn} {
		if l, err := r.list(ctx, b, turn); err == nil && l.Total > 0 {
			return l, nil
		}
	}
	return first, nil
}

func (r repo) list(ctx context.Context, base string, turn TurnRange) (ChangeList, error) {
	l := ChangeList{Base: base, Branch: r.branch, Files: []Change{}}
	if r.fork != "" {
		l.Main = r.main
	}
	if t, ok := r.turnOf(turn); ok {
		l.TurnAt = t.At.Local().Format("15:04")
	}
	from, to, err := r.spec(ctx, base, turn)
	if err != nil {
		return l, err
	}
	revs := []string{from}
	if to != "" {
		revs = append(revs, to)
	}
	num, err := gitLit(ctx, r.top, changesTimeout, "", append(append(append(diffArgs, "--numstat", "-z"), revs...), "--")...)
	if err != nil {
		return l, err
	}
	names, err := gitLit(ctx, r.top, changesTimeout, "", append(append(append(diffArgs, "--name-status", "-z"), revs...), "--")...)
	if err != nil {
		return l, err
	}
	var news []byte
	if to == "" {
		if news, err = gitLit(ctx, r.top, changesTimeout, "", "ls-files", "--others", "--exclude-standard", "-z"); err != nil {
			return l, err
		}
	}
	l.Files = append(l.Files, parseNameStatus(names)...)
	byPath := make(map[string]int, len(l.Files))
	for i, c := range l.Files {
		byPath[c.Path] = i
	}
	// The counts: "added\tremoved\tpath\0", "-" for a binary file.
	for _, rec := range strings.Split(string(num), "\x00") {
		f := strings.SplitN(rec, "\t", 3)
		if len(f) != 3 {
			continue
		}
		i, ok := byPath[f[2]]
		if !ok {
			continue
		}
		c := &l.Files[i]
		if f[0] == "-" {
			c.Binary = true
			continue
		}
		c.Added, _ = strconv.Atoi(f[0])
		c.Removed, _ = strconv.Atoi(f[1])
	}
	budget, counted := int64(maxCountTotal), 0
	for _, p := range strings.Split(string(news), "\x00") {
		if p == "" || strings.HasSuffix(p, "/") { // "/": a repository inside
			continue
		}
		if ctx.Err() != nil {
			return l, ctx.Err()
		}
		c := Change{Path: p, Status: "untracked"}
		if !hiddenPath(p, true) && budget > 0 && counted < maxChangeFiles {
			n, bin, read := countLines(r.real, p)
			c.Added, c.Binary = n, bin
			budget -= max(read, 4<<10) // an open costs too
			counted++
		}
		l.Files = append(l.Files, c)
	}
	sort.SliceStable(l.Files, func(i, j int) bool { return l.Files[i].Path < l.Files[j].Path })
	for i := range l.Files {
		c := &l.Files[i]
		c.Hidden = hiddenPath(c.Path, true)
		l.Added += c.Added
		l.Removed += c.Removed
	}
	l.Total = len(l.Files)
	if l.Total > maxChangeFiles {
		l.Note = fmt.Sprintf("The first %d of %d changed files.", maxChangeFiles, l.Total)
		l.Files = l.Files[:maxChangeFiles]
	}
	return l, nil
}

// parseNameStatus reads "git diff --name-status -z": a status, then a path.
func parseNameStatus(out []byte) []Change {
	var cs []Change
	f := strings.Split(string(out), "\x00")
	for i := 0; i+1 < len(f); i += 2 {
		if f[i] == "" {
			break
		}
		status := "modified"
		switch f[i][0] {
		case 'A':
			status = "added"
		case 'D':
			status = "deleted"
		case 'T':
			status = "type"
		case 'U':
			status = "conflict"
		}
		cs = append(cs, Change{Path: f[i+1], Status: status})
	}
	return cs
}

// newFile opens the new file rel in the folder real (without links). A
// file in a folder that a link leads to, or that is not regular, is not
// opened: it returns ErrNoFile. A link itself returns its target, like git
// shows one.
func newFile(real, rel string) (f *os.File, link string, err error) {
	full := filepath.Join(real, filepath.FromSlash(rel))
	if !within(real, full) || full == real {
		return nil, "", ErrNoFile
	}
	dir := filepath.Dir(full)
	if d, err := filepath.EvalSymlinks(dir); err != nil || d != dir {
		return nil, "", ErrNoFile
	}
	if fi, err := os.Lstat(full); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		t, err := os.Readlink(full)
		if err != nil {
			return nil, "", ErrNoFile
		}
		return nil, t, nil
	}
	f, err = os.OpenFile(full, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", ErrNoFile
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, "", ErrNoFile
	}
	return f, "", nil
}

// countLines counts the lines of a new file as git does, and returns how
// many bytes it read. A file with a NUL byte near its start is binary. A
// file longer than maxCountBytes has no count (0).
func countLines(real, rel string) (lines int, binary bool, read int64) {
	f, link, err := newFile(real, rel)
	if err != nil {
		return 0, false, 0
	}
	if f == nil {
		return 1, false, int64(len(link))
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	var last byte
	for {
		if read >= maxCountBytes {
			return 0, false, read // too long to count
		}
		n, err := f.Read(buf)
		if n > 0 {
			if read < 8000 && bytes.IndexByte(buf[:min(n, int(8000-read))], 0) >= 0 {
				return 0, true, read + int64(n)
			}
			lines += bytes.Count(buf[:n], []byte{'\n'})
			last = buf[n-1]
			read += int64(n)
		}
		if err != nil {
			break
		}
	}
	if read > 0 && last != '\n' {
		lines++
	}
	return lines, false, read
}

// cleanRel cleans a path asked for: relative, with "/", inside the top.
func cleanRel(rel string) string {
	return strings.Trim(path.Clean("/"+filepath.ToSlash(rel)), "/")
}

// ChangeOf shows the change of one file since base (turn: as ListChanges).
// untracked asks for the new file that git does not track (a path may be
// both: a file deleted from the index and still there). redact cleans the
// text before it is shown.
func ChangeOf(ctx context.Context, root, rel, base string, untracked bool, turn TurnRange, redact func(string) string) (FileChange, error) {
	rel = cleanRel(rel)
	if rel == "" {
		return FileChange{}, ErrNoFile
	}
	r, err := openRepo(ctx, root)
	if err != nil {
		return FileChange{}, err
	}
	fc := FileChange{Path: rel}
	if hiddenPath(rel, true) {
		fc.Note = "This file may hold secrets, so its changes are not shown."
		return fc, nil
	}
	if redact == nil {
		redact = func(s string) string { return s }
	}
	from, to, err := r.spec(ctx, base, turn)
	if err != nil {
		return FileChange{}, err
	}
	if untracked && to == "" {
		return r.newFileChange(ctx, rel, redact)
	}
	revs := []string{from}
	if to != "" {
		revs = append(revs, to)
	}
	// One changed file, not a folder: git takes a folder's path for all
	// the files in it, which would show the secrets inside.
	names, err := gitLit(ctx, r.top, changesTimeout, "", append(append(append(diffArgs, "--name-only", "-z"), revs...), "--", rel)...)
	switch {
	case err != nil:
		return FileChange{}, ErrNoFile
	case len(names) == 0:
		fc.Note = "This file has no changes now."
		return fc, nil
	case string(names) != rel+"\x00":
		return FileChange{}, ErrNoFile
	}
	out, over, err := gitOut(ctx, r.top, changesTimeout, maxFileDiffBytes, "",
		append([]string{"--literal-pathspecs"}, append(append(append(diffArgs, "--src-prefix=a/", "--dst-prefix=b/"), revs...), "--", rel)...)...)
	if err != nil {
		return FileChange{}, ErrNoFile
	}
	diff := string(out)
	if over {
		if i := strings.LastIndexByte(diff, '\n'); i > 0 {
			diff = diff[:i+1]
		}
		fc.Note = fmt.Sprintf("The diff is cut at %s.", Size(maxFileDiffBytes))
	}
	body, binary := renderFileDiff(redact(diff))
	if body == "" && !binary {
		// No line changed: only the mode, or an empty file came or went.
		switch {
		case strings.Contains(diff, "\nold mode "):
			fc.Note = "Only the file's mode changed" + modeChange(diff) + "."
		case strings.Contains(diff, "\nnew file mode "):
			fc.Note = "A new, empty file."
		case strings.Contains(diff, "\ndeleted file mode "):
			fc.Note = "An empty file was deleted."
		default:
			fc.Note = "No line changed."
		}
	}
	if binary {
		fc.Note = "Binary file."
		if img := r.image(rel); img != "" && to == "" { // the working file is what this base shows
			fc.Note = "Binary file. Now it looks like this:"
			body = img
		}
	}
	fc.HTML = body
	return fc, nil
}

// newFileChange shows a new file: all its lines are added.
func (r repo) newFileChange(ctx context.Context, rel string, redact func(string) string) (FileChange, error) {
	fc := FileChange{Path: rel}
	// Only a new file that git does not ignore: else this would show
	// what the file browser hides, such as .env.
	out, err := gitLit(ctx, r.top, changesTimeout, "", "ls-files", "--others", "--exclude-standard", "-z", "--", rel)
	if err != nil || string(out) != rel+"\x00" {
		return FileChange{}, ErrNoFile
	}
	if _, ok := imageTypes[strings.ToLower(path.Ext(rel))]; ok {
		if img := r.image(rel); img != "" {
			fc.HTML = img
			return fc, nil
		}
	}
	f, link, err := newFile(r.real, rel)
	if err != nil {
		return FileChange{}, ErrNoFile
	}
	var text string
	if f == nil {
		text = redact(link)
		fc.Note = "A link."
	} else {
		defer f.Close()
		fi, _ := f.Stat()
		data, err := io.ReadAll(io.LimitReader(f, maxTextBytes))
		if err != nil {
			return FileChange{}, ErrNoFile
		}
		cut := fi != nil && fi.Size() > maxTextBytes
		if !isText(data, cut) {
			fc.Note = "New binary file."
			if fi != nil {
				fc.Note = fmt.Sprintf("New binary file, %s.", Size(fi.Size()))
			}
			return fc, nil
		}
		text = string(data)
		if cut {
			if i := strings.LastIndexByte(text, '\n'); i > 0 {
				text = text[:i+1]
			}
			fc.Note = fmt.Sprintf("The first %s of %s.", Size(int64(len(text))), Size(fi.Size()))
		}
		text = redact(text)
	}
	if text == "" {
		if fc.Note == "" {
			fc.Note = "A new, empty file."
		}
		return fc, nil
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	var d strings.Builder
	fmt.Fprintf(&d, "@@ -0,0 +1,%d @@\n", len(lines))
	for _, l := range lines {
		d.WriteString("+" + l + "\n")
	}
	fc.HTML, _ = renderFileDiff(d.String())
	return fc, nil
}

// image shows a file of the working tree as a picture, or returns "" when
// it is not an image or not there.
func (r repo) image(rel string) string {
	if _, ok := imageTypes[strings.ToLower(path.Ext(rel))]; !ok {
		return ""
	}
	full := filepath.Join(r.real, filepath.FromSlash(rel))
	if dir := filepath.Dir(full); !within(r.real, full) {
		return ""
	} else if d, err := filepath.EvalSymlinks(dir); err != nil || d != dir {
		return ""
	}
	v, err := showFile(full, rel, nil)
	if err != nil {
		return ""
	}
	return v.HTML
}

// modeChange is " (100644 → 100755)" from a diff's mode lines, or "".
func modeChange(diff string) string {
	var old, cur string
	for _, l := range strings.Split(diff, "\n") {
		if v, ok := strings.CutPrefix(l, "old mode "); ok {
			old = v
		} else if v, ok := strings.CutPrefix(l, "new mode "); ok {
			cur = v
		}
	}
	if old == "" || cur == "" {
		return ""
	}
	return " (" + old + " → " + cur + ")"
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// renderFileDiff renders the unified diff of one file with line numbers:
// each line's number in the old and the new file, the hunks' headers, and
// added and removed lines marked. It tells whether git found the file
// binary.
func renderFileDiff(diff string) (string, bool) {
	var b strings.Builder
	binary, inHunk := false, false
	oldN, newN := 0, 0
	num := func(n int) string {
		if n <= 0 {
			return "<i></i>"
		}
		return "<i>" + strconv.Itoa(n) + "</i>"
	}
	row := func(class string, o, n int, sign, text string) {
		b.WriteString(`<div class="dl ` + class + `"><span class="g">` + num(o) + num(n) + `</span><span class="s">` +
			html.EscapeString(sign) + `</span><span class="t">` + html.EscapeString(text) + "</span></div>")
	}
	for _, l := range strings.Split(strings.TrimRight(diff, "\n"), "\n") {
		if m := hunkHeader.FindStringSubmatch(l); m != nil {
			oldN, _ = strconv.Atoi(m[1])
			newN, _ = strconv.Atoi(m[2])
			if oldN == 0 { // "-0,0": no old lines
				oldN = 1
			}
			if newN == 0 {
				newN = 1
			}
			inHunk = true
			b.WriteString(`<div class="dl hunk"><span class="t">` + html.EscapeString(l) + "</span></div>")
			continue
		}
		if strings.HasPrefix(l, "diff --git ") && inHunk {
			break // one file only
		}
		if !inHunk {
			if strings.HasPrefix(l, "Binary files ") || strings.HasPrefix(l, "GIT binary patch") {
				binary = true
			}
			continue
		}
		switch {
		case strings.HasPrefix(l, "+"):
			row("add", 0, newN, "+", l[1:])
			newN++
		case strings.HasPrefix(l, "-"):
			row("del", oldN, 0, "-", l[1:])
			oldN++
		case strings.HasPrefix(l, `\`):
			b.WriteString(`<div class="dl meta"><span class="t">` + html.EscapeString(l) + "</span></div>")
		default:
			row("ctx", oldN, newN, " ", strings.TrimPrefix(l, " "))
			oldN++
			newN++
		}
	}
	if b.Len() == 0 {
		return "", binary
	}
	return `<div class="fdiff">` + b.String() + "</div>", binary
}
