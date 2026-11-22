// Package inbound holds pure helpers for incoming Telegram messages: text
// routing, the safety checks before typing, message joining, and echo matching.
package inbound

import "strings"

// RouteKind says where an incoming text goes.
type RouteKind int

// Route kinds.
const (
	// RouteIgnore means the text is dropped.
	RouteIgnore RouteKind = iota
	// RouteCommand means the bot handles the text as a command.
	RouteCommand
	// RouteAgentText means the text is typed into the agent.
	RouteAgentText
)

// Special command names returned in Route.Command.
const (
	// CmdUnknown is returned in the main chat for a command the bot does not have.
	// Args holds the command name as typed (lower case).
	CmdUnknown = "unknown"
	// CmdTopicOnly is returned in the main chat for a topic command.
	// Args holds the command name.
	CmdTopicOnly = "topic-only"
)

// Route is the result of RouteText.
type Route struct {
	Kind    RouteKind
	Command string // lower-case command name for RouteCommand
	Args    string // text after the command (trimmed); see CmdUnknown and CmdTopicOnly
	Text    string // text to type for RouteAgentText
}

var topicCommands = setOf("info", "screen", "files", "changes", "approve", "deny", "esc", "interrupt",
	"key", "force", "model", "effort", "mode", "verbose", "reasoning", "resend", "unlink", "mute", "unmute", "rename", "at", "queue", "help")

var mainCommands = setOf("start", "agents", "attach", "new", "topics", "status", "pause", "unpause", "rt", "annotate", "templates", "rekey", "help")

// Button is one button of the reply keyboard: its label, and the command
// that a tap runs.
type Button struct {
	Label   string
	Command string
}

// Buttons are the reply keyboard's buttons, in their order. A tap sends the
// label as the user's message, and RouteText reads it as the command, so a
// button never goes to the agent as text.
var Buttons = []Button{
	{"🎛 Panel", "rt"},
	{"🖥 Screen", "screen"},
	{"📁 Project", "project"},
	{"⚙️ Config", "config"},
	{"✍️ Annotate", "annotate"},
	{"🧩 Templates", "templates"},
}

// buttonCommands maps a label, without emoji variation selectors, to its
// command. The first keyboard's labels were the commands themselves
// ("/screen"), so a keyboard that an app still shows keeps working.
var buttonCommands = func() map[string]string {
	m := map[string]string{"📂 Files": "files", "📝 Changes": "changes", "📥 Queue": "queue"}
	for _, b := range Buttons {
		m[noVariation(b.Label)] = b.Command
	}
	return m
}()

// noVariation drops emoji variation selectors (U+FE0F), which an app may add
// to or leave out of an emoji.
func noVariation(s string) string { return strings.ReplaceAll(s, "\uFE0F", "") }

func setOf(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// SlashCommand tells whether text is an agent's slash command: "/" and a name
// of ASCII letters, digits, "-", "_", and ":", which starts with a letter,
// alone or followed by its arguments. Examples: "/restart", "/compact keep
// the tests", "/cux:switch 2". A path such as "/tmp/a.log" or "/README.md"
// is not one; a one-word path such as "/tmp" cannot be told apart. steerable
// and TakesQuote take every text that starts with "/" on purpose: a path
// also waits for an idle agent and takes no quote. An agent runs such
// a command without writing it as a prompt: Claude Code writes "/restart" as
// a system line, and "/new" as "/clear" (seen live, 2.1.322). So no echo
// confirms it.
func SlashCommand(text string) bool {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "/") {
		return false
	}
	name := t[1:]
	if i := strings.IndexAny(name, " \t\r\n"); i >= 0 {
		name = name[:i]
	}
	if name == "" || !isASCIILetter(name[0]) {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !isASCIILetter(c) && (c < '0' || c > '9') && !strings.ContainsRune("-_:", rune(c)) {
			return false
		}
	}
	return true
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// RouteText decides what to do with an incoming text message.
//
// In a topic, the topic commands and the main-chat commands are bot commands;
// "//x" types "/x"; any other text is typed unchanged. In the main chat only
// commands are accepted: plain text is ignored, topic commands answer
// CmdTopicOnly, and unknown commands answer CmdUnknown. A command addressed to
// another bot is ignored.
func RouteText(text string, inTopic bool, botUsername string) Route {
	t := strings.TrimSpace(text)
	if t == "" {
		return Route{Kind: RouteIgnore}
	}
	if c, ok := buttonCommands[noVariation(t)]; ok {
		if c == "project" || c == "config" {
			return Route{Kind: RouteCommand, Command: c}
		}
		t = "/" + c // a tap on a keyboard button
	}
	if !strings.HasPrefix(t, "/") {
		if inTopic {
			return Route{Kind: RouteAgentText, Text: t}
		}
		return Route{Kind: RouteIgnore}
	}
	if inTopic && strings.HasPrefix(t, "//") {
		return Route{Kind: RouteAgentText, Text: t[1:]}
	}

	head, args := t[1:], ""
	if i := strings.IndexAny(head, " \t\r\n"); i >= 0 {
		head, args = head[:i], strings.TrimSpace(head[i:])
	}
	name := head
	if i := strings.Index(head, "@"); i >= 0 {
		name = head[:i]
		target := head[i+1:]
		if !strings.EqualFold(target, strings.TrimPrefix(botUsername, "@")) || botUsername == "" {
			return Route{Kind: RouteIgnore}
		}
	}
	name = strings.ToLower(name)

	if inTopic {
		if name != "" && (topicCommands[name] || mainCommands[name]) {
			return Route{Kind: RouteCommand, Command: name, Args: args}
		}
		return Route{Kind: RouteAgentText, Text: t}
	}
	switch {
	case mainCommands[name]:
		return Route{Kind: RouteCommand, Command: name, Args: args}
	case topicCommands[name]:
		return Route{Kind: RouteCommand, Command: CmdTopicOnly, Args: name}
	default:
		return Route{Kind: RouteCommand, Command: CmdUnknown, Args: name}
	}
}
