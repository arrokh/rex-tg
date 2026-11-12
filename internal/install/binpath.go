package install

import (
	"os"
	"path/filepath"
	"strings"
)

// StablePath maps a Homebrew Cellar path to the stable opt path, so hooks and
// the launchd job survive "brew upgrade". The Cellar path
// <prefix>/Cellar/<name>/<version>/bin/<file> becomes
// <prefix>/opt/<name>/bin/<file>, but only when exists reports that file.
// Any other path is returned unchanged.
func StablePath(exe string, exists func(string) bool) string {
	parts := strings.Split(exe, string(filepath.Separator))
	n := len(parts)
	// ... / Cellar / name / version / bin / file
	if n < 6 || parts[n-2] != "bin" || parts[n-5] != "Cellar" {
		return exe
	}
	prefix := strings.Join(parts[:n-5], string(filepath.Separator))
	opt := filepath.Join(prefix, "opt", parts[n-4], "bin", parts[n-1])
	if exists(opt) {
		return opt
	}
	return exe
}

// FileExists reports whether p is an existing file (symlinks are followed).
func FileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
