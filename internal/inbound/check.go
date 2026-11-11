package inbound

// Action is something the user asks for that may type into a pane.
type Action int

// Actions.
const (
	// ActText is a plain text message.
	ActText Action = iota
	// ActSendNow is the "Send now" button.
	ActSendNow
	// ActForce is /force <text>.
	ActForce
	// ActKeys is /key, /esc, /interrupt.
	ActKeys
	// ActPromptAnswer is a prompt button, /approve, or /deny.
	ActPromptAnswer
	// ActReadOnly is /screen, /info, /mode, and other bot commands.
	ActReadOnly
)

// Facts are the things Check looks at.
type Facts struct {
	FromOwner    bool // the sender is the owner
	OwnerChat    bool // the chat is the owner's private chat
	TopicLive    bool
	PaneExists   bool
	AgentInFront bool // the agent is the pane's foreground process or an ancestor of it
	PromptOpen   bool // the agent shows a permission prompt
	AlertMatches bool // the alert the user answered is still the open one
	Paused       bool // typing is paused
}

// Failure reasons returned by Check.
const (
	ReasonNotOwner      = "only the owner can use this bot"
	ReasonNotOwnerChat  = "this is not the owner's private chat"
	ReasonPaused        = "typing is paused; use /unpause"
	ReasonTopicNotLive  = "this topic is not linked any more"
	ReasonNoPane        = "the pane no longer exists"
	ReasonAgentNotFront = "the pane now runs a shell, not the agent"
	ReasonPromptOpen    = "the agent shows a prompt; use the buttons or /force"
	ReasonNoPrompt      = "no prompt is open"
	ReasonAlertStale    = "this prompt is no longer the open one"
)

// Check runs the checks of design section 9.5 for an action. It returns
// ok=false and a short reason for the first failing check.
func Check(a Action, f Facts) (ok bool, reason string) {
	if !f.FromOwner {
		return false, ReasonNotOwner
	}
	if !f.OwnerChat {
		return false, ReasonNotOwnerChat
	}
	switch a {
	case ActText, ActSendNow, ActForce, ActKeys, ActPromptAnswer:
	default: // ActReadOnly and anything unknown
		return true, ""
	}
	if f.Paused {
		return false, ReasonPaused
	}
	if !f.TopicLive {
		return false, ReasonTopicNotLive
	}
	if !f.PaneExists {
		return false, ReasonNoPane
	}
	if !f.AgentInFront {
		return false, ReasonAgentNotFront
	}
	switch a {
	case ActText, ActSendNow:
		if f.PromptOpen {
			return false, ReasonPromptOpen
		}
	case ActPromptAnswer:
		if !f.PromptOpen {
			return false, ReasonNoPrompt
		}
		if !f.AlertMatches {
			return false, ReasonAlertStale
		}
	}
	return true, ""
}
