package install

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	backupInfix = ".rex-tg-bak-"
	keepBackups = 5
	maxTries    = 3
)

// readState reads a file. A missing file is not an error.
func readState(path string) (data []byte, mode fs.FileMode, exists bool, err error) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0o644, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, 0, false, err
	}
	return data, fi.Mode().Perm(), true, nil
}

// editJSON applies mutate to the JSON file at path. A missing or empty file is
// treated as "{}" (create=true) or left alone (create=false). The write is
// guarded: back up, re-read, retry when the file changed, then temp file,
// fsync, and rename.
func (p Paths) editJSON(path string, create bool, mutate func(src []byte) ([]byte, string, error)) (Change, error) {
	ch := Change{Path: path}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	for try := 0; try < maxTries; try++ {
		orig, mode, exists, err := readState(path)
		if err != nil {
			return ch, fmt.Errorf("read %s: %w", path, err)
		}
		if !exists && !create {
			ch.Note = "file does not exist"
			return ch, nil
		}
		src := orig
		if len(bytes.TrimSpace(src)) == 0 {
			src = []byte("{}")
		}
		if !gjson.ValidBytes(src) {
			return ch, fmt.Errorf("%s is not valid JSON; refusing to edit it", path)
		}
		if r := gjson.ParseBytes(src); !r.IsObject() {
			return ch, fmt.Errorf("%s is not a JSON object; refusing to edit it", path)
		}
		out, note, err := mutate(src)
		if err != nil {
			return ch, fmt.Errorf("edit %s: %w", path, err)
		}
		if bytes.Equal(out, src) && (exists || !create) {
			ch.Note = note
			return ch, nil
		}
		if p.beforeWrite != nil {
			p.beforeWrite(path)
		}
		cur, _, curExists, err := readState(path)
		if err != nil {
			return ch, fmt.Errorf("re-read %s: %w", path, err)
		}
		if curExists != exists || !bytes.Equal(cur, orig) {
			continue // changed under us: start over
		}
		if exists {
			bak := path + backupInfix + p.now().UTC().Format("20060102T150405.000000000Z")
			if err := writeAtomic(bak, orig, mode, false); err != nil {
				return ch, fmt.Errorf("back up %s: %w", path, err)
			}
			ch.Backup = bak
		}
		if err := writeAtomic(path, out, mode, true); err != nil {
			return ch, fmt.Errorf("write %s: %w", path, err)
		}
		if exists {
			rotateBackups(path)
		}
		ch.Changed = true
		ch.Note = note
		return ch, nil
	}
	return ch, fmt.Errorf("%s kept changing while editing; gave up after %d tries", path, maxTries)
}

// writeAtomic writes data through a temporary file in the same folder, fsyncs,
// and renames it into place. With replace=false it refuses to overwrite.
func writeAtomic(path string, data []byte, mode fs.FileMode, replace bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if !replace {
		// A check followed by Rename races another installer and can replace
		// its backup. Linking the finished file publishes it only if the
		// destination is still absent, atomically on the same filesystem.
		err := os.Link(name, path)
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// rotateBackups keeps the newest keepBackups backups of path.
func rotateBackups(path string) {
	matches, err := filepath.Glob(escapeGlob(path) + backupInfix + "*")
	if err != nil || len(matches) <= keepBackups {
		return
	}
	sort.Strings(matches)
	for _, m := range matches[:len(matches)-keepBackups] {
		_ = os.Remove(m)
	}
}

func escapeGlob(s string) string {
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`).Replace(s)
}
