package registry

import (
	"path/filepath"
	"slices"
	"strings"

	"rex-tg/internal/agents"
	"rex-tg/internal/rex"
)

// Rules are the process detection rules (design section 5, spike S2).
type Rules struct {
	PiArgv0        []string // pi runs as node; argv0 is "pi"
	ClaudeArgv0    []string // the process name is a version string, so use argv0 or the invoked path
	ClaudeExecGlob []string // executables of Claude versions, for example ~/.local/share/claude/versions/*
	CodexNames     []string // the process name
}

// unsupportedNames are foreground processes that look like agents but are not
// supported (T3 Code panes report app=claude while running tr3x).
var unsupportedNames = []string{"tr3x"}

func base(p string) string {
	p = strings.TrimPrefix(p, "-") // login shells: -zsh
	return filepath.Base(p)
}

func baseOrEmpty(p string) string {
	if p == "" {
		return ""
	}
	return base(p)
}

// Detect tells which runtime a foreground process belongs to. It never looks
// at the Rex status "app". Shells and tr3x give false.
func Detect(rules Rules, p rex.ProcInfo) (agents.Runtime, bool) {
	argv0 := baseOrEmpty(p.Argv0)
	if slices.Contains(unsupportedNames, p.Name) || slices.Contains(unsupportedNames, argv0) {
		return "", false
	}
	if argv0 != "" && slices.Contains(rules.PiArgv0, argv0) {
		return agents.Pi, true
	}
	if argv0 != "" && slices.Contains(rules.ClaudeArgv0, argv0) {
		return agents.Claude, true
	}
	if inv := baseOrEmpty(p.InvokedPath); inv != "" && slices.Contains(rules.ClaudeArgv0, inv) {
		return agents.Claude, true
	}
	for _, g := range rules.ClaudeExecGlob {
		for _, path := range []string{p.Path, p.InvokedPath} {
			if path == "" {
				continue
			}
			if ok, err := filepath.Match(g, path); err == nil && ok {
				return agents.Claude, true
			}
		}
	}
	if p.Name != "" && slices.Contains(rules.CodexNames, p.Name) {
		return agents.Codex, true
	}
	if argv0 != "" && slices.Contains(rules.CodexNames, argv0) {
		return agents.Codex, true
	}
	return "", false
}

// AgentInFront reports whether the process with this PID and name is the
// pane's foreground process, or one of its ancestors or its child (design
// section 9.5, check 3). PID and name must both match, because PIDs are
// reused.
func AgentInFront(p rex.Process, pid int, name string) bool {
	if pid == 0 {
		return false
	}
	match := func(i rex.ProcInfo) bool { return i.PID == pid && i.Name == name }
	if match(p.Foreground) {
		return true
	}
	if p.Child != nil && match(*p.Child) {
		return true
	}
	return slices.ContainsFunc(p.Ancestors, match)
}
