// Package install adds and removes rex-tg's integration points: hooks in
// Claude Code and Codex configs, agent skills, the pi extension, and launchd.
//
// Edits to shared files touch only rex-tg's own entries, keep the rest of the
// file byte for byte, and are written atomically after a backup.
package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrWouldShiftTrust is returned by UninstallCodex when removing rex-tg's
// entries would move other Codex hook entries to new positions. Codex trusts
// hooks by position, so they would lose trust. Use force to remove anyway.
var ErrWouldShiftTrust = errors.New("removing rex-tg hooks would shift the trust of other codex hook entries")

// Runner runs an external command and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Paths holds every location the installers touch, plus injection points.
type Paths struct {
	Home           string
	Binary         string // absolute path of the rex-tg binary
	ClaudeSettings string
	CodexHooks     string
	PiExtension    string
	ClaudeSkill    string
	SharedSkill    string // shared by Codex and Pi through ~/.agents/skills
	LaunchAgent    string
	Label          string
	LogDir         string
	// LiveDir is where the pi extension writes the text pi is writing
	// (config.Config.LiveDir).
	LiveDir string

	// Runner runs launchctl. Nil means os/exec.
	Runner Runner
	// UID is the user ID for gui/<uid>. Empty means the current user.
	UID string
	// Now supplies the time used in backup names. Nil means time.Now.
	Now func() time.Time
	// Sleep waits between launchctl tries. Nil means time.Sleep.
	Sleep func(time.Duration)

	// beforeWrite is a test hook called after an edit is computed and before
	// the file is re-read for the lost-write check.
	beforeWrite func(path string)
}

// Change describes the outcome of one install or uninstall step.
type Change struct {
	Path    string // file that was (or would be) changed
	Changed bool   // false when nothing needed to change
	Backup  string // backup file, if one was made
	Note    string // short human-readable summary
}

// DefaultPaths returns the standard locations under home for the binary.
func DefaultPaths(home, binary string) Paths {
	return Paths{
		Home:           home,
		Binary:         binary,
		ClaudeSettings: filepath.Join(home, ".claude", "settings.json"),
		CodexHooks:     filepath.Join(home, ".codex", "hooks.json"),
		PiExtension:    filepath.Join(home, ".pi", "agent", "extensions", "rex-tg.ts"),
		ClaudeSkill:    filepath.Join(home, ".claude", "skills", "rex-tg", "SKILL.md"),
		SharedSkill:    filepath.Join(home, ".agents", "skills", "rex-tg", "SKILL.md"),
		LaunchAgent:    filepath.Join(home, "Library", "LaunchAgents", "local.rex-tg.plist"),
		Label:          "local.rex-tg",
		LogDir:         filepath.Join(home, ".local", "state", "rex-tg", "logs"),
		LiveDir:        filepath.Join(home, ".local", "state", "rex-tg", "live"),
	}
}

func (p Paths) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p Paths) sleep(d time.Duration) {
	if p.Sleep != nil {
		p.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (p Paths) uid() string {
	if p.UID != "" {
		return p.UID
	}
	return strconv.Itoa(os.Getuid())
}

func (p Paths) run(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if p.Runner != nil {
		return p.Runner(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// hookCommand returns the exact command string for a runtime.
func (p Paths) hookCommand(runtime string) string {
	return shellQuote(p.Binary) + " hook " + runtime
}

// shellQuote single-quotes s when it holds characters a shell would split on.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`&;|<>()*?[]{}!#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// isOurs reports whether cmd is a rex-tg hook command for runtime. The exact
// flag is true when it matches the current binary; otherwise the command is an
// unambiguous older rex-tg path (absolute, basename rex-tg, same suffix).
func (p Paths) isOurs(cmd, runtime string) (ours, exact bool) {
	if cmd == p.hookCommand(runtime) {
		return true, true
	}
	suffix := " hook " + runtime
	if !strings.HasSuffix(cmd, suffix) {
		return false, false
	}
	bin := strings.TrimSuffix(cmd, suffix)
	if len(bin) >= 2 && bin[0] == '\'' && bin[len(bin)-1] == '\'' {
		bin = strings.ReplaceAll(bin[1:len(bin)-1], `'\''`, "'")
	}
	if !filepath.IsAbs(bin) || filepath.Base(bin) != "rex-tg" {
		return false, false
	}
	return true, false
}

func (p Paths) validate() error {
	if p.Binary == "" || !filepath.IsAbs(p.Binary) {
		return fmt.Errorf("install: binary path must be absolute, got %q", p.Binary)
	}
	return nil
}
