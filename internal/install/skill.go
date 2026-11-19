package install

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"rex-tg/extensions"
)

const skillMarker = "<!-- rex-tg:managed-skill -->"

// InstallClaudeSkill installs the portable skill in Claude Code's personal skills folder.
func InstallClaudeSkill(p Paths) (Change, error) {
	return p.installSkill(p.ClaudeSkill)
}

// InstallSharedSkill writes one skill discovered by both Codex and Pi.
func InstallSharedSkill(p Paths) (Change, error) {
	return p.installSkill(p.SharedSkill)
}

func (p Paths) installSkill(path string) (Change, error) {
	ch := Change{Path: path}
	if err := p.validate(); err != nil {
		return ch, err
	}
	want := extensions.RexTgSkill
	for try := 0; try < maxTries; try++ {
		orig, mode, exists, err := readSkill(path)
		if err != nil {
			return ch, fmt.Errorf("read skill %s: %w", path, err)
		}
		if exists {
			if bytes.Equal(orig, want) {
				ch.Note = "already installed"
				return ch, nil
			}
			if !isManagedSkill(orig) {
				return ch, fmt.Errorf("%s already exists and is not managed by rex-tg; leaving it unchanged: %w", path, ErrNotOurs)
			}
		}
		if p.beforeWrite != nil {
			p.beforeWrite(path)
		}
		cur, _, curExists, err := readSkill(path)
		if err != nil {
			return ch, fmt.Errorf("re-read skill %s: %w", path, err)
		}
		if curExists != exists || !bytes.Equal(cur, orig) {
			continue
		}
		if exists {
			bak := path + backupInfix + p.now().UTC().Format("20060102T150405.000000000Z")
			if err := writeAtomic(bak, orig, mode, false); err != nil {
				return ch, fmt.Errorf("back up skill %s: %w", path, err)
			}
			if err := writeAtomic(path, want, mode, true); err != nil {
				return ch, fmt.Errorf("write skill %s: %w", path, err)
			}
			rotateBackups(path)
			ch.Backup = bak
		} else {
			if err := writeAtomic(path, want, 0o644, false); err != nil {
				if errors.Is(err, fs.ErrExist) {
					continue
				}
				return ch, fmt.Errorf("write skill %s: %w", path, err)
			}
		}
		ch.Changed = true
		ch.Note = "written"
		return ch, nil
	}
	return ch, fmt.Errorf("%s kept changing while installing; gave up after %d tries", path, maxTries)
}

// UninstallClaudeSkill removes the marked Claude Code skill and saves a backup.
func UninstallClaudeSkill(p Paths) (Change, error) {
	return p.uninstallSkill(p.ClaudeSkill)
}

// UninstallSharedSkill removes the marked Codex/Pi skill and saves a backup.
func UninstallSharedSkill(p Paths) (Change, error) {
	return p.uninstallSkill(p.SharedSkill)
}

func (p Paths) uninstallSkill(path string) (Change, error) {
	ch := Change{Path: path}
	for try := 0; try < maxTries; try++ {
		orig, mode, exists, err := readSkill(path)
		if err != nil {
			return ch, fmt.Errorf("read skill %s: %w", path, err)
		}
		if !exists {
			ch.Note = "not installed"
			return ch, nil
		}
		if !isManagedSkill(orig) {
			ch.Note = "left in place"
			return ch, fmt.Errorf("%s was not written by rex-tg; leaving it in place: %w", path, ErrNotOurs)
		}
		cur, _, curExists, err := readSkill(path)
		if err != nil {
			return ch, fmt.Errorf("re-read skill %s: %w", path, err)
		}
		if !curExists || !bytes.Equal(cur, orig) {
			continue
		}
		bak := path + backupInfix + p.now().UTC().Format("20060102T150405.000000000Z")
		if err := writeAtomic(bak, orig, mode, false); err != nil {
			return ch, fmt.Errorf("back up skill %s: %w", path, err)
		}
		if err := os.Remove(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return ch, fmt.Errorf("remove skill %s: %w", path, err)
		}
		rotateBackups(path)
		ch.Changed = true
		ch.Note = "removed"
		ch.Backup = bak
		return ch, nil
	}
	return ch, fmt.Errorf("%s kept changing while uninstalling; gave up after %d tries", path, maxTries)
}

func isManagedSkill(content []byte) bool {
	return bytes.Contains(content, []byte(skillMarker))
}

func readSkill(path string) (data []byte, mode fs.FileMode, exists bool, err error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0o644, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, 0, false, fmt.Errorf("%s is a symbolic link; refusing to replace it", path)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, 0, false, err
	}
	return data, fi.Mode().Perm(), true, nil
}
