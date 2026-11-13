package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// A turn's changes are the difference of two snapshots of the working
// files, tracked and new. Taking them leaves the user's index as it was.
func TestTurnChangesLeaveTheIndexAlone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "start")
	ctx := context.Background()

	top, before, err := gitTree(ctx, filepath.Join(dir)) // the turn starts
	if err != nil {
		t.Fatal(err)
	}
	// The agent works: an edit, a new file, and an ignored file.
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\nTWO\nthree\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte("new\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("secret\n"), 0o644)
	statusBefore := runGit(t, dir, "status", "--porcelain")

	_, after, err := gitTree(ctx, dir) // the turn ends
	if err != nil || after == before {
		t.Fatalf("after = %q (%v), before %q", after, err, before)
	}
	st, diff, err := gitChanges(ctx, top, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if st.files != 2 || st.added != 3 || st.removed != 1 || st.label() != "2 files · +3 −1" {
		t.Errorf("stat = %+v (%s)", st, st.label())
	}
	if !strings.Contains(diff, "+TWO") || !strings.Contains(diff, "b/b.txt") || strings.Contains(diff, "secret") {
		t.Errorf("diff:\n%s", diff)
	}
	if got := runGit(t, dir, "status", "--porcelain"); got != statusBefore {
		t.Errorf("the user's index changed:\nbefore %q\nafter  %q", statusBefore, got)
	}
	if _, _, err := gitTree(ctx, t.TempDir()); err == nil {
		t.Error("a folder that is no repository gave a snapshot")
	}
}
