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

const piPlaceholder = "__REX_TG_BIN__"

// piSource returns the extension source with the binary path filled in as a
// JSON string literal, which is also a valid TypeScript string literal.
func (p Paths) piSource() []byte {
	var lit bytes.Buffer
	enc := json.NewEncoder(&lit)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(p.Binary)
	return bytes.ReplaceAll(extensions.PiExtension, []byte(piPlaceholder), bytes.TrimSpace(lit.Bytes()))
}

// InstallPi writes the pi extension file.
func InstallPi(p Paths) (Change, error) {
	return p.installFile(p.PiExtension, p.piSource(), 0o644)
}

// UninstallPi removes the pi extension file and nothing else.
func UninstallPi(p Paths) (Change, error) {
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
