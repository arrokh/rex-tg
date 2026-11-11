package daemon

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestFindImages(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	cwd := filepath.Join(root, "proj")
	for _, d := range []string{filepath.Join(home, "Desktop"), filepath.Join(cwd, "out"), filepath.Join(root, "dir.png")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	file := func(path string) string {
		t.Helper()
		if err := os.WriteFile(path, []byte("img"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	desk := file(filepath.Join(home, "Desktop", "screen-1.png"))
	shot := file(filepath.Join(home, "Desktop", "Screenshot 2026-11-11 at 23.33.23.png"))
	rel := file(filepath.Join(cwd, "out", "chart.JPG"))
	here := file(filepath.Join(cwd, "plain.webp"))
	abs := file(filepath.Join(root, "abs.gif"))
	spaced := file(filepath.Join(root, "my pic.png"))
	pct := file(filepath.Join(root, "a b.png"))
	notImage := file(filepath.Join(root, "notes.txt"))
	empty := filepath.Join(root, "empty.png")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	huge := filepath.Join(root, "huge.png")
	if err := os.WriteFile(huge, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(huge, maxImageBytes+1); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.png")
	if err := os.Symlink(abs, link); err != nil {
		t.Fatal(err)
	}

	var many []string
	var manyWant []string
	for i := range 12 {
		p := file(filepath.Join(root, "m"+strconv.Itoa(i)+".png"))
		many = append(many, p)
		if i < maxImageFiles {
			manyWant = append(manyWant, p)
		}
	}

	tests := []struct {
		name, md, cwd string
		want          []string
	}{
		{"bare home path with a full stop", "Saved to ~/Desktop/screen-1.png.", cwd, []string{desk}},
		{"markdown image", "![shot](" + abs + ")", cwd, []string{abs}},
		{"markdown link with a title", "[the chart](out/chart.JPG \"Chart\")", cwd, []string{rel}},
		{"angle brackets with spaces", "[s](<" + shot + ">)", cwd, []string{shot}},
		{"file url, percent-decoded", "![x](file://" + strings.ReplaceAll(pct, " ", "%20") + ")", cwd, []string{pct}},
		{"code span with spaces", "See `~/Desktop/Screenshot 2026-11-11 at 23.33.23.png` now", cwd, []string{shot}},
		{"bare relative names", "Made out/chart.JPG, ./plain.webp and ../abs.gif:", cwd, []string{rel, here, abs}},
		{"relative path without a folder", "It is plain.webp", "", nil},
		{"relative path with the folder", "It is plain.webp", cwd, []string{here}},
		{"trailing punctuation", "(" + abs + "), " + desk + "; " + here + "!", cwd, []string{abs, desk, here}},
		{"bold and quotes", "**" + abs + "** and \"" + desk + "\"", cwd, []string{abs, desk}},
		{"duplicates keep the first place", abs + " [again](" + abs + ") `" + abs + "` " + desk, cwd, []string{abs, desk}},
		{"unbroken dot segments", "x " + cwd + "/out/../out/./chart.JPG", cwd, []string{rel}},
		{"a space in a bare path is two words", "my pic.png", root, nil},
		{"space in a code span", "`my pic.png`", root, []string{spaced}},
		{"not an image", abs[:len(abs)-4] + ".txt " + notImage, cwd, nil},
		{"a folder named like an image", root + "/dir.png", cwd, nil},
		{"missing file", "/no/such/file.png ~/nothing.png", cwd, nil},
		{"empty file", empty, cwd, nil},
		{"over 50 MB", huge, cwd, nil},
		{"a symlink is followed", link, cwd, []string{filepath.Join(root, "link.png")}},
		{"web links are not files", "![w](https://example.com/a.png) https://example.com/b.png", cwd, nil},
		{"at most ten", strings.Join(many, " "), cwd, manyWant},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			if got := findImages(c.md, home, c.cwd); !slices.Equal(got, c.want) {
				t.Fatalf("findImages(%q) = %q, want %q", c.md, got, c.want)
			}
		})
	}
}
