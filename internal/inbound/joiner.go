package inbound

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Joined is a group of messages for one topic that become one prompt.
type Joined struct {
	ThreadID  int64
	UpdateIDs []int64
	Text      string
}

type group struct {
	threadID int64
	ids      []int64
	texts    []string
	last     time.Time
	long     bool
	closed   bool // a later message started a new group for the thread
}

// Joiner joins messages that arrive close together. It is not safe for
// concurrent use; callers serialize access.
type Joiner struct {
	window, longWindow time.Duration
	longChars          int
	groups             []*group // in order of first arrival
}

// NewJoiner returns a Joiner. Messages for one topic within window of the
// previous one are joined; when a joined message has at least longChars
// characters (runes), longWindow applies instead.
func NewJoiner(window, longWindow time.Duration, longChars int) *Joiner {
	return &Joiner{window: window, longWindow: longWindow, longChars: longChars}
}

func (j *Joiner) windowFor(g *group) time.Duration {
	if g.long {
		return j.longWindow
	}
	return j.window
}

// Add records a message. It joins the open group of the thread when the gap
// to the previous message is within the window that applies to that group.
func (j *Joiner) Add(threadID, updateID int64, text string, at time.Time) {
	var open *group
	for _, g := range j.groups {
		if g.threadID == threadID && !g.closed {
			open = g
		}
	}
	if open != nil && at.Sub(open.last) > j.windowFor(open) {
		open.closed = true
		open = nil
	}
	if open == nil {
		open = &group{threadID: threadID}
		j.groups = append(j.groups, open)
	}
	open.ids = append(open.ids, updateID)
	open.texts = append(open.texts, text)
	open.last = at
	if utf8.RuneCountInString(text) >= j.longChars {
		open.long = true
	}
}

// Due returns the groups that are complete at now, in order of first arrival,
// and removes them. A group is complete when it was closed by a later gap, or
// when its last message is older than the window that applies.
func (j *Joiner) Due(now time.Time) []Joined {
	var out []Joined
	keep := j.groups[:0]
	for _, g := range j.groups {
		if g.closed || now.Sub(g.last) > j.windowFor(g) {
			out = append(out, Joined{ThreadID: g.threadID, UpdateIDs: g.ids, Text: strings.Join(g.texts, "\n")})
		} else {
			keep = append(keep, g)
		}
	}
	for i := len(keep); i < len(j.groups); i++ {
		j.groups[i] = nil
	}
	j.groups = keep
	return out
}
