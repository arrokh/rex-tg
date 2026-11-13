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

// Other file types count when display.send_files lists them; send_images
// off leaves only those.
func TestFindFiles(t *testing.T) {
	root := t.TempDir()
	write := func(name string) string {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	pdf, csv, html, png, env := write("report.PDF"), write("data.csv"), write("page.html"), write("a.png"), write("notes.env")
	md := "Made `" + pdf + "`, [data](" + csv + "), " + html + ", " + png + " and " + env + "."
	tests := []struct {
		name   string
		images bool
		files  []string
		want   []string
	}{
		{"defaults", true, []string{"pdf", "csv", "tsv", "html", "htm", "xlsx", "docx", "pptx"}, []string{pdf, csv, html, png}},
		{"images off", false, []string{".PDF"}, []string{pdf}},
		{"nothing", false, nil, nil},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			if got := findFiles(md, root, root, sendExts(c.images, c.files)); !slices.Equal(got, c.want) {
				t.Fatalf("findFiles = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSizeText(t *testing.T) {
	for n, want := range map[int64]string{812: "812 B", 14 << 10: "14 KB", 3355443: "3.2 MB"} {
		if got := sizeText(n); got != want {
			t.Errorf("sizeText(%d) = %q, want %q", n, got, want)
		}
	}
}

// Files go to Telegram only from the agent's folder, the home folder, or a
// temporary folder, and never from a hidden place or ~/Library.
func TestAllowedPlace(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	cwd := filepath.Join(root, "work", "app") // a project outside home
	for path, want := range map[string]bool{
		filepath.Join(cwd, "out", "report.pdf"):                  true,
		filepath.Join(home, "Desktop", "shot.png"):               true,
		filepath.Join(home, ".ssh", "id_rsa.png"):                false,
		filepath.Join(home, "work", ".secret", "data.csv"):       false,
		filepath.Join(home, "Library", "Keychains", "login.csv"): false,
		filepath.Join(cwd, ".env.csv"):                           false,
		"/etc/hosts.csv":                                         false,
	} {
		if got := allowedPlace(path, home, cwd); got != want {
			t.Errorf("allowedPlace(%q) = %v, want %v", path, got, want)
		}
	}
	// The temp folder itself counts, for files made there.
	if !allowedPlace(filepath.Join(os.TempDir(), "chart.png"), home, cwd) {
		t.Error("a file in the temp folder is refused")
	}
	// A symlink in the project to a hidden place is judged where it points.
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(home, ".aws", "keys.csv")
	if err := os.WriteFile(secret, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(cwd, "keys.csv")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if got := findFiles("See `"+link+"`.", home, cwd, sendExts(false, []string{"csv"})); len(got) != 0 {
		t.Errorf("a link to a hidden place was offered: %v", got)
	}
}

// The Mac's disk ignores case, and an agent's own folder may be in
// ~/Library (iCloud Drive): its files may go, not hidden ones.
func TestAllowedPlaceCaseAndOwnFolder(t *testing.T) {
	home := "/Users/someone"
	if allowedPlace("/Users/someone/library/Keychains/login.png", home, "/work/app") {
		t.Error("~/library in lower case slipped through")
	}
	icloud := "/Users/someone/Library/Mobile Documents/com~apple~CloudDocs/app"
	if !allowedPlace(icloud+"/out/chart.png", home, icloud) {
		t.Error("a file of an agent working in iCloud Drive is refused")
	}
	if allowedPlace(icloud+"/.git/x.png", home, icloud) {
		t.Error("a hidden folder in the agent's own folder is allowed")
	}
	// An agent started in ~ or / does not open ~/Library or /etc.
	for _, cwd := range []string{home, "/Users", "/", home + "/Library"} {
		for _, p := range []string{home + "/Library/Keychains/login.csv", "/etc/hosts.csv", home + "/.ssh/k.csv"} {
			if allowedPlace(p, home, cwd) {
				t.Errorf("cwd %q: %q is allowed", cwd, p)
			}
		}
		if !allowedPlace(home+"/Desktop/r.pdf", home, cwd) {
			t.Errorf("cwd %q: a file on the Desktop is refused", cwd)
		}
	}
	// Other apps' caches next to the temp folder are not a temp folder.
	if allowedPlace("/var/folders/zz/other/C/com.example.app/data.csv", home, "/work/app") {
		t.Error("another app's cache is allowed")
	}
}
