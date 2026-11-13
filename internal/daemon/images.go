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
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		fi, err := os.Stat(p)
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
