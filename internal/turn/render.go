package turn

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"rex-tg/internal/agents"
)

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func escape(s string) string { return htmlEscaper.Replace(s) }

// truncate cuts s to at most max runes, ending with "…" when cut.
func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:max-1]), " ") + "…"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// italic wraps each non-empty line in underscores.
func italic(s string) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, "_"+l+"_")
		}
	}
	return strings.Join(lines, "\n")
}

func toolText(redact func(string) string, name, label string) string {
	s := redact(name)
	if l := oneLine(redact(label)); l != "" {
		s += " · " + l
	}
	return s
}

func emoji(k agents.ToolKind) string {
	switch k {
	case agents.ToolRead:
		return "📖"
	case agents.ToolEdit:
		return "✏️"
	case agents.ToolShell:
		return "💻"
	case agents.ToolSearch:
		return "🔍"
	case agents.ToolWeb:
		return "🌐"
	case agents.ToolSubagent:
		return "🤖"
	case agents.ToolPlan:
		return "📝"
	}
	return "🧩"
}

func formatElapsed(d time.Duration) string {
	s := int(d / time.Second)
	if s < 0 {
		s = 0
	}
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh %dm", s/3600, s%3600/60)
}

func toolsText(n int) string {
	if n == 1 {
		return "1 tool"
	}
	return fmt.Sprintf("%d tools", n)
}

// rows groups consecutive calls of the same tool and renders each group.
func (t *Tracker) rows() []string {
	type group struct {
		name, label string
		kind        agents.ToolKind
		n           int
		running     bool
		failed      bool
	}
	var gs []group
	for _, c := range t.calls {
		if n := len(gs); n > 0 && gs[n-1].name == c.name {
			g := &gs[n-1]
			g.n++
			g.kind, g.label = c.kind, c.label
			g.running = g.running || c.status == callRunning
			g.failed = g.failed || c.status == callFailed
			continue
		}
		gs = append(gs, group{name: c.name, label: c.label, kind: c.kind, n: 1,
			running: c.status == callRunning, failed: c.status == callFailed})
	}
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		mark := "✅"
		if g.running {
			mark = "▸"
		} else if g.failed {
			mark = "❌"
		}
		name := t.cfg.Redact(g.name)
		if g.n > 1 {
			name += fmt.Sprintf(" ×%d", g.n)
		}
		row := mark + " " + emoji(g.kind) + " " + name
		if l := oneLine(t.cfg.Redact(g.label)); l != "" {
			row += " · " + l
		}
		out = append(out, escape(truncate(row, t.cfg.MaxRowRunes)))
	}
	return out
}

func (t *Tracker) progressHTML(now time.Time) string {
	var b strings.Builder
	switch {
	case t.blocked:
		b.WriteString("⏸ Waiting for your answer")
	default:
		h := firstLine(t.cfg.Redact(t.commentary))
		if h == "" {
			h = "Working…"
		}
		b.WriteString("⚙️ " + escape(truncate(h, t.cfg.MaxRowRunes)))
	}
	rows := t.rows()
	if len(rows) > t.cfg.MaxRows {
		rows = rows[len(rows)-t.cfg.MaxRows:]
	}
	for _, r := range rows {
		b.WriteString("\n" + r)
	}
	b.WriteString("\n⏱ " + formatElapsed(now.Sub(t.start)) + " · " + toolsText(len(t.calls)))
	return b.String()
}

func (t *Tracker) summaryHTML() string {
	elapsed := formatElapsed(t.endedAt.Sub(t.start))
	stats := elapsed + " · " + toolsText(len(t.calls))
	var first string
	switch {
	case t.endKind == "error":
		first = "❌ Error"
		if l := firstLine(t.cfg.Redact(t.errText)); l != "" {
			first += ": " + escape(truncate(l, t.cfg.MaxRowRunes))
		}
	case t.endKind == string(StateIdle) && !t.answered && !t.commentarySeen:
		first = "⏹ Interrupted · " + stats
	default:
		first = "✅ Done · " + stats
	}
	if len(t.calls) == 0 {
		return first
	}
	rows := t.rows()
	if len(rows) > t.cfg.MaxSummaryRows {
		more := len(rows) - t.cfg.MaxSummaryRows
		rows = append(rows[:t.cfg.MaxSummaryRows:t.cfg.MaxSummaryRows], fmt.Sprintf("… and %d more", more))
	}
	return first + "\n<blockquote expandable>" + strings.Join(rows, "\n") + "</blockquote>"
}
