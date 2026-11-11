package daemon

import (
	"path/filepath"
	"strings"
	"unicode"

	"rex-tg/internal/agents"
	"rex-tg/internal/format"
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
// else the session name, else the project folder, else the runtime.
func topicName(paneLabel string, rt agents.Runtime, cwd, title string) string {
	for _, s := range []string{paneName(paneLabel), title, folderBase(cwd), string(rt)} {
		if s = strings.Join(strings.Fields(s), " "); s != "" {
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
	for i := len(cut) - 1; i >= max/2; i-- {
		if cut[i] == ' ' {
			cut = cut[:i]
			break
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
