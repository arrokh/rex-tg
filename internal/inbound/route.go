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

var topicCommands = setOf("info", "screen", "approve", "deny", "esc", "interrupt",
	"key", "force", "model", "effort", "mode", "verbose", "reasoning", "resend", "unlink", "mute", "unmute", "help")

var mainCommands = setOf("start", "agents", "new", "topics", "status", "pause", "unpause", "help")

func setOf(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

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
