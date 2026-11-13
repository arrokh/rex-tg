package miniapp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitT runs git in dir for a test, as a fixed author.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@example.com", "-c", "user.name=t",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// changesRepo is a repository with one commit on main.
func changesRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	gitT(t, root, "init", "-q")
	write(t, filepath.Join(root, ".gitignore"), "build.log\n")
	write(t, filepath.Join(root, "main.go"), "package main\n\nfunc main() {}\n")
	write(t, filepath.Join(root, "old.txt"), "old\n")
	write(t, filepath.Join(root, "internal", "deep", "x.go"), "package deep\n")
	write(t, filepath.Join(root, "id_rsa"), "key one\n")
	gitT(t, root, "add", "-A")
	gitT(t, root, "commit", "-q", "-m", "one")
	return root
}

func byPath(l ChangeList) map[string]Change {
	m := map[string]Change{}
	for _, c := range l.Files {
		m[c.Path] = c
	}
	return m
}

func TestListChangesNotCommitted(t *testing.T) {
	root := changesRepo(t)
	write(t, filepath.Join(root, "main.go"), "package main\n\nfunc main() {\n\tprintln(1)\n}\n")
	os.Remove(filepath.Join(root, "old.txt"))
	write(t, filepath.Join(root, "notes", "new.md"), "a\nb\nc") // no newline at the end
	write(t, filepath.Join(root, "staged.txt"), "s\n")
	gitT(t, root, "add", "staged.txt")
	write(t, filepath.Join(root, "build.log"), "ignored\n")
	write(t, filepath.Join(root, "id_rsa"), "key two\n")
	write(t, filepath.Join(root, "blob.bin"), "a\x00b")

	l, err := ListChanges(context.Background(), root, "", TurnRange{})
	if err != nil {
		t.Fatal(err)
	}
	if l.Base != BaseHead || l.Branch != "main" || l.Main != "main" {
		t.Errorf("base %q branch %q main %q", l.Base, l.Branch, l.Main)
	}
	got := byPath(l)
	want := map[string]string{
		"main.go": "modified", "old.txt": "deleted", "notes/new.md": "untracked",
		"staged.txt": "added", "id_rsa": "modified", "blob.bin": "untracked",
	}
	if len(got) != len(want) {
		t.Errorf("files %v", l.Files)
	}
	for p, s := range want {
		if got[p].Status != s {
			t.Errorf("%s: %+v, want %s", p, got[p], s)
		}
	}
	if c := got["main.go"]; c.Added != 3 || c.Removed != 1 {
		t.Errorf("main.go counts %+v", c)
	}
	if c := got["notes/new.md"]; c.Added != 3 {
		t.Errorf("a new file's lines, as git counts them: %+v", c)
	}
	if !got["id_rsa"].Hidden || got["main.go"].Hidden {
		t.Error("only the key is hidden")
	}
	if !got["blob.bin"].Binary {
		t.Error("blob.bin is binary")
	}
	if _, ok := got["build.log"]; ok {
		t.Error("an ignored file is listed")
	}
	if l.Total != 6 {
		t.Errorf("total %d", l.Total)
	}
}

func TestListChangesChoosesTheBranch(t *testing.T) {
	root := changesRepo(t)
	gitT(t, root, "checkout", "-q", "-b", "feature")
	write(t, filepath.Join(root, "feature.go"), "package main\n")
	gitT(t, root, "add", "-A")
	gitT(t, root, "commit", "-q", "-m", "two")

	l, err := ListChanges(context.Background(), root, "", TurnRange{})
	if err != nil {
		t.Fatal(err)
	}
	if l.Base != BaseBranch || l.Main != "main" || l.Branch != "feature" {
		t.Fatalf("nothing is uncommitted, so the branch: %+v", l)
	}
	if c := byPath(l)["feature.go"]; c.Status != "added" || c.Added != 1 {
		t.Errorf("feature.go %+v", c)
	}
	head, err := ListChanges(context.Background(), root, BaseHead, TurnRange{})
	if err != nil || head.Total != 0 {
		t.Errorf("not committed: %+v %v", head, err)
	}
	fc, err := ChangeOf(context.Background(), root, "feature.go", BaseBranch, false, TurnRange{}, nil)
	if err != nil || !strings.Contains(fc.HTML, "package main") {
		t.Errorf("the branch's change of feature.go: %+v %v", fc, err)
	}
}

func TestListChangesOfATurn(t *testing.T) {
	root := changesRepo(t)
	top := gitT(t, root, "rev-parse", "--show-toplevel")
	from := gitT(t, root, "rev-parse", "HEAD^{tree}")
	write(t, filepath.Join(root, "turn.txt"), "made in the turn\n")
	gitT(t, root, "add", "-A")
	gitT(t, root, "commit", "-q", "-m", "turn") // a turn may commit: its changes stay its own
	to := gitT(t, root, "rev-parse", "HEAD^{tree}")
	write(t, filepath.Join(root, "later.txt"), "after the turn\n")

	turn := TurnRange{Top: top, From: from, To: to, At: time.Now()}
	l, err := ListChanges(context.Background(), root, BaseTurn, turn)
	if err != nil {
		t.Fatal(err)
	}
	got := byPath(l)
	if len(got) != 1 || got["turn.txt"].Status != "added" || l.TurnAt == "" {
		t.Errorf("the turn's changes only: %+v", l)
	}
	fc, err := ChangeOf(context.Background(), root, "turn.txt", BaseTurn, false, turn, nil)
	if err != nil || !strings.Contains(fc.HTML, "made in the turn") {
		t.Errorf("turn.txt: %+v %v", fc, err)
	}
	other := turn
	other.Top = t.TempDir()
	if _, err := ListChanges(context.Background(), root, BaseTurn, other); !errors.Is(err, ErrNoBase) {
		t.Errorf("a turn of another repository: %v", err)
	}
	if l, _ := ListChanges(context.Background(), root, BaseHead, other); l.TurnAt != "" {
		t.Error("a turn of another repository is offered")
	}
}

func TestChangeOfAFile(t *testing.T) {
	root := changesRepo(t)
	outside := t.TempDir()
	write(t, filepath.Join(outside, "secret.txt"), "outside\n")
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "main.go"), "package main\n\nfunc main() { _ = \"<b>\" + SECRET }\n")
	write(t, filepath.Join(root, "new.txt"), "one <i>\ntwo SECRET\n")
	write(t, filepath.Join(root, "build.log"), "ignored\n")
	write(t, filepath.Join(root, "id_rsa"), "key two\n")
	redact := func(s string) string { return strings.ReplaceAll(s, "SECRET", "[redacted]") }
	ctx := context.Background()

	fc, err := ChangeOf(ctx, root, "main.go", BaseHead, false, TurnRange{}, redact)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<div class="dl del"><span class="g"><i>3</i><i></i>`, `<div class="dl add"><span class="g"><i></i><i>3</i>`,
		"&lt;b&gt;", "[redacted]", `class="dl hunk"`} {
		if !strings.Contains(fc.HTML, want) {
			t.Errorf("main.go lacks %q:\n%s", want, fc.HTML)
		}
	}
	if strings.Contains(fc.HTML, "SECRET") || strings.Contains(fc.HTML, "<b>") {
		t.Error("main.go shows a secret or raw HTML")
	}

	fc, err = ChangeOf(ctx, root, "new.txt", BaseHead, true, TurnRange{}, redact)
	if err != nil || !strings.Contains(fc.HTML, `<i></i><i>2</i></span><span class="s">+</span><span class="t">two [redacted]`) ||
		!strings.Contains(fc.HTML, "&lt;i&gt;") {
		t.Errorf("new.txt: %+v %v", fc, err)
	}

	if fc, err := ChangeOf(ctx, root, "id_rsa", BaseHead, false, TurnRange{}, redact); err != nil || fc.HTML != "" || fc.Note == "" {
		t.Errorf("a secret's change is shown: %+v %v", fc, err)
	}
	// A new link shows where it leads, as git shows a link; never what is there.
	if fc, err := ChangeOf(ctx, root, "out", BaseHead, true, TurnRange{}, redact); err != nil || fc.Note != "A link." || strings.Contains(fc.HTML, "outside\n") {
		t.Errorf("out: %+v %v", fc, err)
	}
	for _, p := range []string{"build.log", "out/secret.txt", "../" + filepath.Base(outside) + "/secret.txt", "main.go"} {
		if fc, err := ChangeOf(ctx, root, p, BaseHead, true, TurnRange{}, redact); err == nil {
			t.Errorf("%s as a new file: %+v", p, fc)
		}
	}
	if fc, err := ChangeOf(ctx, root, "out/secret.txt", BaseHead, false, TurnRange{}, redact); err == nil && strings.Contains(fc.HTML, "outside") {
		t.Errorf("a file behind a link: %+v", fc)
	}
	// A folder's path would show all its files' changes, the secrets too.
	write(t, filepath.Join(root, "cfg", ".env"), "TOKEN=1\n")
	write(t, filepath.Join(root, "cfg", "app.go"), "package cfg\n")
	gitT(t, root, "add", "cfg")
	gitT(t, root, "commit", "-q", "-m", "cfg")
	write(t, filepath.Join(root, "cfg", ".env"), "TOKEN=2\n")
	write(t, filepath.Join(root, "cfg", "app.go"), "package cfg // 2\n")
	for _, base := range []string{BaseHead, BaseBranch} {
		if fc, err := ChangeOf(ctx, root, "cfg", base, false, TurnRange{}, redact); err == nil && strings.Contains(fc.HTML, "TOKEN") {
			t.Errorf("%s: a folder shows a secret: %+v", base, fc)
		}
	}
	if fc, err := ChangeOf(ctx, root, "cfg/app.go", BaseHead, false, TurnRange{}, redact); err != nil || !strings.Contains(fc.HTML, "// 2") || strings.Contains(fc.HTML, "TOKEN") {
		t.Errorf("cfg/app.go: %+v %v", fc, err)
	}

	// A path is a name, not a pattern.
	if fc, err := ChangeOf(ctx, root, "*.go", BaseHead, false, TurnRange{}, redact); err != nil || fc.HTML != "" {
		t.Errorf("*.go matched as a pattern: %+v %v", fc, err)
	}
	if fc, err := ChangeOf(ctx, root, "old.txt", BaseHead, false, TurnRange{}, redact); err != nil || fc.HTML != "" || fc.Note == "" {
		t.Errorf("an unchanged file: %+v %v", fc, err)
	}
}

func TestListChangesBeforeTheFirstCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	gitT(t, root, "init", "-q")
	write(t, filepath.Join(root, "a.txt"), "a\n")
	gitT(t, root, "add", "a.txt")
	write(t, filepath.Join(root, "b.txt"), "b\n")
	l, err := ListChanges(context.Background(), root, "", TurnRange{})
	if err != nil {
		t.Fatal(err)
	}
	got := byPath(l)
	if got["a.txt"].Status != "added" || got["b.txt"].Status != "untracked" || l.Main != "" {
		t.Errorf("%+v", l)
	}
	if fc, err := ChangeOf(context.Background(), root, "a.txt", BaseHead, false, TurnRange{}, nil); err != nil || !strings.Contains(fc.HTML, `<span class="t">a</span>`) {
		t.Errorf("a.txt: %+v %v", fc, err)
	}
}

func TestListChangesOutsideARepository(t *testing.T) {
	if _, err := ListChanges(context.Background(), t.TempDir(), "", TurnRange{}); !errors.Is(err, ErrNotRepo) {
		t.Errorf("got %v", err)
	}
}

func TestRenderFileDiffNumbersLines(t *testing.T) {
	diff := "diff --git a/f b/f\nindex 1..2 100644\n--- a/f\n+++ b/f\n@@ -10,3 +10,3 @@ func f() {\n a\n-b\n+c\n d\n\\ No newline at end of file\n"
	h, bin := renderFileDiff(diff)
	if bin {
		t.Error("not binary")
	}
	for _, want := range []string{
		`<i>10</i><i>10</i></span><span class="s"> </span><span class="t">a</span>`,
		`<i>11</i><i></i></span><span class="s">-</span><span class="t">b</span>`,
		`<i></i><i>11</i></span><span class="s">+</span><span class="t">c</span>`,
		`<i>12</i><i>12</i></span><span class="s"> </span><span class="t">d</span>`,
		`@@ -10,3 +10,3 @@ func f() {`, `class="dl meta"`,
	} {
		if !strings.Contains(h, want) {
			t.Errorf("lacks %q:\n%s", want, h)
		}
	}
	if strings.Contains(h, "index 1..2") {
		t.Error("the header is shown")
	}
	if _, bin := renderFileDiff("diff --git a/x b/x\nBinary files a/x and b/x differ\n"); !bin {
		t.Error("binary not found")
	}
}

// A change without a changed line says what changed instead.
func TestChangeOfOnlyTheMode(t *testing.T) {
	root := changesRepo(t)
	if err := os.Chmod(filepath.Join(root, "main.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	fc, err := ChangeOf(context.Background(), root, "main.go", BaseHead, false, TurnRange{}, nil)
	if err != nil || fc.HTML != "" || !strings.Contains(fc.Note, "100644 → 100755") {
		t.Errorf("%+v %v", fc, err)
	}
}

// AllChanges reads each base the repository has, in one pass.
func TestAllChanges(t *testing.T) {
	root := changesRepo(t)
	gitT(t, root, "checkout", "-q", "-b", "feature")
	write(t, filepath.Join(root, "f.go"), "package main\n")
	gitT(t, root, "add", "-A")
	gitT(t, root, "commit", "-q", "-m", "f")
	write(t, filepath.Join(root, "g.go"), "package main\n")
	all, err := AllChanges(context.Background(), root, TurnRange{})
	if err != nil || len(all) != 2 || all[0].Base != BaseHead || all[1].Base != BaseBranch {
		t.Fatalf("%+v %v", all, err)
	}
	if all[0].Total != 1 || all[1].Total != 2 {
		t.Errorf("not committed %d, branch %d", all[0].Total, all[1].Total)
	}
}

// A deleted file shows its old lines; a binary file says so; a changed
// picture shows as it is now; a file deleted from the index but still there
// is listed twice, and new=1 shows the file that is there.
func TestChangeOfDeletedBinaryAndImageFiles(t *testing.T) {
	root := changesRepo(t)
	ctx := context.Background()
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR"
	write(t, filepath.Join(root, "pic.png"), png+"1")
	write(t, filepath.Join(root, "data.bin"), "a\x00b")
	gitT(t, root, "add", "-A")
	gitT(t, root, "commit", "-q", "-m", "binaries")
	write(t, filepath.Join(root, "pic.png"), png+"2")
	write(t, filepath.Join(root, "data.bin"), "a\x00c")
	os.Remove(filepath.Join(root, "old.txt"))
	gitT(t, root, "rm", "-q", "--cached", "main.go") // deleted from the index, still there

	l, err := ListChanges(ctx, root, BaseHead, TurnRange{})
	if err != nil {
		t.Fatal(err)
	}
	var mainGo []string
	for _, c := range l.Files {
		if c.Path == "main.go" {
			mainGo = append(mainGo, c.Status)
		}
	}
	if strings.Join(mainGo, ",") != "deleted,untracked" {
		t.Errorf("main.go is listed as %v", mainGo)
	}
	if c := byPath(l)["data.bin"]; !c.Binary {
		t.Errorf("data.bin %+v", c)
	}

	fc, err := ChangeOf(ctx, root, "old.txt", BaseHead, false, TurnRange{}, nil)
	if err != nil || !strings.Contains(fc.HTML, `<div class="dl del"><span class="g"><i>1</i><i></i></span><span class="s">-</span><span class="t">old</span>`) {
		t.Errorf("old.txt: %+v %v", fc, err)
	}
	fc, err = ChangeOf(ctx, root, "data.bin", BaseHead, false, TurnRange{}, nil)
	if err != nil || fc.Note != "Binary file." || fc.HTML != "" {
		t.Errorf("data.bin: %+v %v", fc, err)
	}
	fc, err = ChangeOf(ctx, root, "pic.png", BaseHead, false, TurnRange{}, nil)
	if err != nil || !strings.Contains(fc.HTML, `src="data:image/png;base64,`) {
		t.Errorf("pic.png: %+v %v", fc, err)
	}
	fc, err = ChangeOf(ctx, root, "main.go", BaseHead, false, TurnRange{}, nil)
	if err != nil || !strings.Contains(fc.HTML, `class="dl del"`) || strings.Contains(fc.HTML, `class="dl add"`) {
		t.Errorf("main.go as deleted: %+v %v", fc, err)
	}
	fc, err = ChangeOf(ctx, root, "main.go", BaseHead, true, TurnRange{}, nil)
	if err != nil || !strings.Contains(fc.HTML, `class="dl add"`) || strings.Contains(fc.HTML, `class="dl del"`) {
		t.Errorf("main.go as new: %+v %v", fc, err)
	}
}
