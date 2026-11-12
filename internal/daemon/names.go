package daemon

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"rex-tg/internal/agents"
	"rex-tg/internal/format"
	"rex-tg/internal/rex"
)

// maxTopicName is the longest topic name that rex-tg gives. Telegram allows
// 128 characters, but a phone shows far fewer in the topic list.
const maxTopicName = 32

// genericPaneLabels are pane names that say nothing about the work. Rex names
// a new terminal pane "shell".
var genericPaneLabels = map[string]bool{
	"shell": true, "terminal": true, "zsh": true, "bash": true, "fish": true, "sh": true,
	"node": true, "pi": true, "claude": true, "codex": true,
}

// paneName returns the name that the user gave a Rex pane, or "" when the pane
// has a default name.
func paneName(label string) string {
	l := strings.Join(strings.Fields(label), " ")
	if genericPaneLabels[strings.ToLower(l)] {
		return ""
	}
	return l
}

// topicName is a topic's short name: the pane's own name when it has one,
// else the session name, else the project folder, else the runtime. A
// session name can come from a prompt, so secrets are redacted.
func topicName(paneLabel string, rt agents.Runtime, cwd, title string) string {
	for _, s := range []string{paneName(paneLabel), title, folderBase(cwd), string(rt)} {
		if s = strings.Join(strings.Fields(format.Redact(s)), " "); s != "" {
			return shortName(s, maxTopicName)
		}
	}
	return "agent"
}

// folderBase is the last element of a folder path, or "".
func folderBase(cwd string) string {
	if cwd == "" {
		return ""
	}
	if b := filepath.Base(cwd); b != "." && b != "/" {
		return b
	}
	return ""
}

// shortName cuts s to at most max characters, at the end of a word when one
// is near, and marks the cut with "…".
func shortName(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	cut := r[:max-1]
	if r[max-1] != ' ' { // the cut is inside a word: end at the word before
		for i := len(cut) - 1; i >= max/2; i-- {
			if cut[i] == ' ' {
				cut = cut[:i]
				break
			}
		}
	}
	return strings.TrimRight(string(cut), " ·-–—,;:") + "…"
}

// agentHTML is "<runtime> · <folder>" with the folder in <code>, because
// Telegram links folder names such as "resume.md".
func agentHTML(rt agents.Runtime, cwd string) string {
	s := format.Escape(string(rt))
	if f := folderBase(cwd); f != "" {
		s += " · <code>" + format.Escape(f) + "</code>"
	}
	return s
}

// noAutoLink stops Telegram from turning names such as "resume.md" into web
// links: it puts a zero-width space after a dot that sits between a letter or
// digit and a letter. It is for display text only.
func noAutoLink(s string) string {
	r := []rune(s)
	var b strings.Builder
	for i, c := range r {
		b.WriteRune(c)
		if c == '.' && i > 0 && i+1 < len(r) &&
			(unicode.IsLetter(r[i-1]) || unicode.IsDigit(r[i-1])) && unicode.IsLetter(r[i+1]) {
			b.WriteRune('\u200b')
		}
	}
	return b.String()
}

// tabName is the name that a Rex tab should have for an agent, given the tab's
// current name. It is the topic's name. When the topic has only the folder
// fallback (no pane name and no session name), the tab keeps the form
// "<folder> · <agent>", so the agent stays visible: a tab that already has
// that form for the folder is kept as it is.
func tabName(paneLabel string, rt agents.Runtime, cwd, title, current string) string {
	if paneName(paneLabel) != "" || strings.TrimSpace(format.Redact(title)) != "" {
		return cleanLabel(topicName(paneLabel, rt, cwd, title))
	}
	// A " #2" from a twin that has gone is dropped; uniqueTabName adds one
	// back when it is still needed.
	current = tabSuffix.ReplaceAllString(current, "")
	if f := cleanLabel(folderBase(cwd)); f != "" && strings.HasPrefix(current, f+" · ") && strings.TrimSpace(strings.TrimPrefix(current, f+" · ")) != "" {
		return current
	}
	return tabLabel(cwd, string(rt))
}

// tabOwner is the pane that names a tab: the first pane of the tab, in Rex's
// order, that runs an agent. Without such a pane it is self (a stopped agent
// marks its own tab). One owner per tab keeps two agents in one tab from
// renaming it in turns at every check.
func tabOwner(blocks []rex.Block, window, self string, hasAgent func(block string) bool) string {
	for _, b := range blocks {
		if b.WindowID == window && hasAgent(b.ID) {
			return b.ID
		}
	}
	return self
}

// tabSuffix is the " #N" that uniqueTabName adds.
var tabSuffix = regexp.MustCompile(` #\d+$`)

// offlineTabName is the name of the tab of a stopped agent: its current name
// with the red mark in front.
func offlineTabName(current string) string {
	return cleanLabel(offlineMark + strings.TrimPrefix(current, offlineMark))
}

// uniqueTabName makes a tab name differ from the names of the other tabs of
// the session: it adds " #2", " #3", and so on. A name that no other tab has
// stays as it is.
func uniqueTabName(name string, others []string) string {
	if name == "" || !slices.Contains(others, name) {
		return name
	}
	for n := 2; ; n++ {
		suffix := fmt.Sprintf(" #%d", n)
		base := []rune(name)
		if max := maxLabel - len([]rune(suffix)); len(base) > max {
			base = base[:max]
		}
		if cand := strings.TrimSpace(string(base)) + suffix; !slices.Contains(others, cand) {
			return cand
		}
	}
}
