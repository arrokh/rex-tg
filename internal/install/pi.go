package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"rex-tg/extensions"
)

const (
	piPlaceholder     = "__REX_TG_BIN__"
	piLivePlaceholder = "__REX_TG_LIVE__"
)

// piMarker starts every extension file that rex-tg writes. A file at the same
// path without it belongs to the user: install backs it up first, and
// uninstall leaves it alone.
const piMarker = "// Written by `rex-tg install`"

// ErrNotOurs means a file at a rex-tg path was not written by rex-tg.
var ErrNotOurs = errors.New("the file was not written by rex-tg")

// piSource returns the extension source with the binary path and the live
// folder filled in as JSON string literals, which are also valid TypeScript
// string literals.
func (p Paths) piSource() []byte {
	lit := func(s string) []byte {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(s)
		return bytes.TrimSpace(b.Bytes())
	}
	src := bytes.ReplaceAll(extensions.PiExtension, []byte(piPlaceholder), lit(p.Binary))
	return bytes.ReplaceAll(src, []byte(piLivePlaceholder), lit(p.LiveDir))
}

// InstallPi writes the pi extension file. A file of the user's at that path
// is backed up first.
func InstallPi(p Paths) (Change, error) {
	if cur, err := os.ReadFile(p.PiExtension); err == nil && !bytes.HasPrefix(cur, []byte(piMarker)) {
		if err := p.validate(); err != nil {
			return Change{Path: p.PiExtension}, err
		}
		bak := p.PiExtension + backupInfix + p.now().UTC().Format("20060102T150405.000000000Z")
		if err := writeAtomic(bak, cur, 0o600, false); err != nil {
			return Change{Path: p.PiExtension}, fmt.Errorf("back up %s: %w", p.PiExtension, err)
		}
		ch, err := p.installFile(p.PiExtension, p.piSource(), 0o644)
		ch.Backup = bak
		return ch, err
	}
	return p.installFile(p.PiExtension, p.piSource(), 0o644)
}

// UninstallPi removes the pi extension file, only when rex-tg wrote it.
func UninstallPi(p Paths) (Change, error) {
	cur, err := os.ReadFile(p.PiExtension)
	if err == nil && !bytes.HasPrefix(cur, []byte(piMarker)) {
		return Change{Path: p.PiExtension, Note: "left in place"}, fmt.Errorf("%s: %w", p.PiExtension, ErrNotOurs)
	}
	return removeFile(p.PiExtension)
}

// installFile writes a rex-tg-owned file unless it already has the content.
func (p Paths) installFile(path string, content []byte, mode fs.FileMode) (Change, error) {
	ch := Change{Path: path}
	if err := p.validate(); err != nil {
		return ch, err
	}
	cur, err := os.ReadFile(path)
	if err == nil && bytes.Equal(cur, content) {
		ch.Note = "already installed"
		return ch, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ch, fmt.Errorf("read %s: %w", path, err)
	}
	if err := writeAtomic(path, content, mode, true); err != nil {
		return ch, fmt.Errorf("write %s: %w", path, err)
	}
	ch.Changed = true
	ch.Note = "written"
	return ch, nil
}

func removeFile(path string) (Change, error) {
	ch := Change{Path: path}
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		ch.Note = "not installed"
		return ch, nil
	}
	if err != nil {
		return ch, fmt.Errorf("remove %s: %w", path, err)
	}
	ch.Changed = true
	ch.Note = "removed"
	return ch, nil
}
