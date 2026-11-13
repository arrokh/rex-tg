package daemon

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Limits for the files that a final answer names (design section 9.4a).
const (
	maxImageFiles = 10       // files sent per answer
	maxImageBytes = 50 << 20 // the Bot API upload limit
)

var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".heic": true, ".heif": true, ".bmp": true, ".tif": true, ".tiff": true,
}

var (
	// [text](target) and ![alt](target); the target may be <...> with spaces.
	mdLinkRe = regexp.MustCompile(`!?\[[^\]]*\]\(\s*(?:<([^>]*)>|([^)\s]*))[^)]*\)`)
	// An inline code span.
	codeSpanRe = regexp.MustCompile("`([^`\n]+)`")
)

// imageCandidates lists the strings in an answer that may name an image
// file, in order of appearance (with repeats).
func imageCandidates(md string) []string {
	type hit struct {
		at int
		s  string
	}
	var hits []hit
	mask := []byte(md) // text that no link or code span has used yet
	blank := func(a, b int) {
		for i := a; i < b; i++ {
			if mask[i] != '\n' {
				mask[i] = ' '
			}
		}
	}
	for _, m := range mdLinkRe.FindAllStringSubmatchIndex(md, -1) {
		a, b := m[2], m[3]
		if a < 0 {
			a, b = m[4], m[5]
		}
		if a >= 0 && b > a {
			hits = append(hits, hit{m[0], md[a:b]})
		}
		blank(m[0], m[1])
	}
	for _, m := range codeSpanRe.FindAllStringSubmatchIndex(string(mask), -1) {
		hits = append(hits, hit{m[0], strings.TrimSpace(md[m[2]:m[3]])})
		blank(m[0], m[1])
	}
	rest := string(mask)
	for i := 0; i < len(rest); {
		if isSpace(rest[i]) {
			i++
			continue
		}
		j := i
		for j < len(rest) && !isSpace(rest[j]) {
			j++
		}
		if t := trimToken(rest[i:j]); t != "" {
			hits = append(hits, hit{i, t})
		}
		i = j
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].at < hits[j].at })
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.s
	}
	return out
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// trimToken removes quotes, brackets, and trailing punctuation around a bare
// word, and keeps it only when it has a file extension (resolveImage checks
// which).
func trimToken(t string) string {
	t = strings.TrimLeft(t, "\"'*_([{<")
	t = strings.TrimRight(t, ".,;:!?)]}>\"'*_")
	if t == "" || filepath.Ext(t) == "" {
		return ""
	}
	return t
}

// resolveImage turns a candidate into a clean absolute path, or "" when it
// is no file with one of the extensions exts (".png"). A relative path is
// resolved against cwd (an empty cwd skips it).
func resolveImage(c, home, cwd string, exts map[string]bool) string {
	c = strings.TrimSpace(c)
	switch {
	case strings.HasPrefix(c, "file://"):
		u, err := url.Parse(c)
		if err != nil || (u.Host != "" && u.Host != "localhost") {
			return ""
		}
		c = u.Path
	case strings.Contains(c, "://"):
		return ""
	case strings.Contains(c, "%"):
		if d, err := url.PathUnescape(c); err == nil {
			c = d
		}
	}
	c = strings.TrimRight(c, ".,;:!?)")
	if c == "" || !exts[strings.ToLower(filepath.Ext(c))] {
		return ""
	}
	switch {
	case c == "~" || strings.HasPrefix(c, "~/"):
		if home == "" {
			return ""
		}
		c = filepath.Join(home, c[1:])
	case !filepath.IsAbs(c):
		if cwd == "" || !filepath.IsAbs(cwd) {
			return ""
		}
		c = filepath.Join(cwd, c)
	}
	c = filepath.Clean(c)
	if !filepath.IsAbs(c) {
		return ""
	}
	return c
}

// findImages returns the image files that a final answer names and that
// exist: absolute paths, without repeats, in order, at most maxImageFiles.
func findImages(md, home, cwd string) []string { return findFiles(md, home, cwd, imageExts) }

// findFiles is findImages for the files with the extensions exts.
func findFiles(md, home, cwd string, exts map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range imageCandidates(md) {
		p := resolveImage(c, home, cwd, exts)
		if p == "" {
			continue
		}
		real, ok := placeOK(p, home, cwd)
		if !ok || seen[real] { // one file named twice, also by a link, goes once
			continue
		}
		seen[real] = true
		fi, err := os.Stat(real)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 || fi.Size() > maxImageBytes {
			continue
		}
		out = append(out, p)
		if len(out) == maxImageFiles {
			break
		}
	}
	return out
}

// sendExts returns the extensions of the files that a final answer sends:
// the images when send_images is on, and the types in send_files.
func sendExts(sendImages bool, files []string) map[string]bool {
	exts := map[string]bool{}
	if sendImages {
		for x := range imageExts {
			exts[x] = true
		}
	}
	for _, x := range files {
		if x = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(x), ".")); x != "" {
			exts["."+x] = true
		}
	}
	return exts
}

// placeOK resolves the symlinks of p and returns where the file really is,
// when it may go to Telegram from there (allowedPlace). A sender reads the
// returned path, not p: a link swapped after the check then leads nowhere new.
func placeOK(p, home, cwd string) (string, bool) {
	real, err := filepath.EvalSymlinks(p)
	if err != nil || !allowedPlace(real, home, cwd) {
		return "", false
	}
	return real, true
}

// allowedPlace tells whether a file may go to Telegram from where it really
// is (after symlinks): inside the agent's folder, the home folder, or a
// temporary folder, and not in a hidden folder or file (~/.ssh, ~/.aws, a
// project's .env) or in ~/Library (keychains, app data). An answer that
// names a file elsewhere, such as /etc or another disk, sends nothing: an
// agent can be led to name any path.
func allowedPlace(real, home, cwd string) bool {
	// Each place as written and as it really is (/var is /private/var).
	both := func(p string) []string {
		out := []string{filepath.Clean(p)}
		if rp, err := filepath.EvalSymlinks(p); err == nil && rp != out[0] {
			out = append(out, rp)
		}
		return out
	}
	// The Mac's disks ignore case by default: ~/library is ~/Library.
	inside := func(root, p string) (string, bool) {
		rel, err := filepath.Rel(strings.ToLower(root), strings.ToLower(p))
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			return "", false
		}
		return rel, true // in lower case: the checks below look only for a leading "."
	}
	var libs []string
	if home != "" && filepath.IsAbs(home) {
		libs = both(filepath.Join(home, "Library"))
	}
	// The agent's folder is its own place, but not when it holds ~/Library:
	// an agent started in ~ or / gets only the rules of the other places.
	cwdRoots := map[string]bool{}
	if cwd != "" && filepath.IsAbs(cwd) && filepath.Clean(cwd) != "/" {
		own := true
		for _, r := range both(cwd) {
			for _, lib := range libs {
				if _, in := inside(r, lib); in {
					own = false
				}
			}
		}
		if own {
			for _, r := range both(cwd) {
				cwdRoots[r] = true
			}
		}
	}
	var roots []string
	for r := range cwdRoots {
		roots = append(roots, r)
	}
	for _, r := range []string{home, os.TempDir(), "/tmp"} {
		if r != "" && filepath.IsAbs(r) {
			roots = append(roots, both(r)...)
		}
	}
	for _, root := range roots {
		rel, ok := inside(root, real)
		if !ok {
			continue
		}
		if cwdRoots[root] {
			// The agent's own folder: its files may go, also when the
			// folder is in ~/Library (iCloud Drive, Dropbox), but still
			// not from a hidden folder in it.
			for _, part := range strings.Split(rel, string(filepath.Separator)) {
				if strings.HasPrefix(part, ".") && part != "." {
					return false
				}
			}
			return true
		}
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			if strings.HasPrefix(part, ".") && part != "." {
				return false // a hidden folder or file
			}
		}
		for _, lib := range libs {
			if _, ok := inside(lib, real); ok {
				return false
			}
		}
		return true
	}
	return false
}
