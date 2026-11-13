package daemon

import "time"

// Timing holds the daemon's fixed waits. A zero field keeps its production
// value. Tests shorten them, so they do not sleep through real-time waits;
// nothing else should set them.
type Timing struct {
	Tick         time.Duration // main loop tick (250 ms)
	Prep         time.Duration // least time between two readiness checks of a topic (500 ms)
	TailPoll     time.Duration // transcript poll when no file event comes (500 ms)
	TailerGrace  time.Duration // how long the tailer of an ended session keeps reading (2 s)
	TailRestart  time.Duration // pause before a follower that failed starts again (5 s)
	VimPoll      time.Duration // screen read interval after a vim key; vimPolls reads (40 ms)
	VimRetry     time.Duration // pause before the next try after a vim editor did not react (3 s)
	Spool        time.Duration // spool folder poll when no file event comes (1 s)
	AnswerGrace  time.Duration // a turn's wait for its answer after idle (5 s)
	StoppedGrace time.Duration // a turn's wait for the last lines after its agent stopped (2 s)
	Typing       time.Duration // "typing…" refresh (5 s)
	NewAgentWait time.Duration // how long /new waits for the new agent to report its session (90 s)
	NewAgentPoll time.Duration // how often /new looks for that report (1 s)
	CheckDelay   time.Duration // the wait before a reply whose send got no answer is checked (60 s)
	UpdateCheck  time.Duration // how often the daemon checks whether its binary was replaced (20 s)
	BoardEdit    time.Duration // least time between two edits of the live /agents board (3 s)
	BoardRefresh time.Duration // the board is redrawn this often, so its times stay right (60 s)
	RemindCheck  time.Duration // how often open alerts are checked for a reminder (5 s)
	Draft        time.Duration // least time between two drafts of a topic's text (0: [display] stream_interval)
	DraftKeep    time.Duration // a draft that did not change is sent again after this, before it fades (20 s)
	LiveCheck    time.Duration // how often the live folder of pi's extension is read (400 ms)
	DeadlineWarn time.Duration // a prompt with a time limit is reminded of this long before it blocks (30 s)
	AlertFollow  time.Duration // after an answer, when the screen is read for the next question; again at 3x (1 s)
	LongPoll     time.Duration // how long a getUpdates call waits for updates, whole seconds (25 s)
}

// withDefaults fills the zero fields with the production values.
func (t Timing) withDefaults() Timing {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&t.Tick, 250*time.Millisecond)
	def(&t.Prep, 500*time.Millisecond)
	def(&t.TailPoll, 500*time.Millisecond)
	def(&t.TailerGrace, 2*time.Second)
	def(&t.TailRestart, 5*time.Second)
	def(&t.VimPoll, 40*time.Millisecond)
	def(&t.VimRetry, 3*time.Second)
	def(&t.Spool, time.Second)
	def(&t.AnswerGrace, 5*time.Second)
	def(&t.StoppedGrace, 2*time.Second)
	def(&t.Typing, 5*time.Second)
	def(&t.NewAgentWait, 90*time.Second)
	def(&t.NewAgentPoll, time.Second)
	def(&t.CheckDelay, 60*time.Second)
	def(&t.UpdateCheck, 20*time.Second)
	def(&t.BoardEdit, 3*time.Second)
	def(&t.BoardRefresh, 60*time.Second)
	def(&t.RemindCheck, 5*time.Second)
	def(&t.DraftKeep, 20*time.Second)
	def(&t.LiveCheck, 400*time.Millisecond)
	def(&t.DeadlineWarn, 30*time.Second)
	def(&t.AlertFollow, time.Second)
	def(&t.LongPoll, longPollSeconds*time.Second)
	return t
}
