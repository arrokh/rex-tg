package daemon

import (
	"strings"
	"testing"
	"time"

	"rex-tg/internal/store"
)

func TestParseDeadline(t *testing.T) {
	tests := []struct {
		line  string
		want  time.Duration
		rough bool
	}{
		{"⚠ Claude Code will automatically block this request in 2:00, to avoid holding up an unattended session", 2 * time.Minute, false},
		{"Claude Code will automatically block this request in 0:45, to avoid", 45 * time.Second, false},
		{"Claude Code will automatically block this request in about 3 minutes, to avoid", 3 * time.Minute, true},
		{"Claude Code will automatically block this request in about 1 second, to avoid", time.Second, true},
	}
	for _, tc := range tests {
		got, rough, ok := parseDeadline([]string{"Bash command", tc.line, "Do you want to proceed?", "❯ 1. Yes"})
		if !ok || got != tc.want || rough != tc.rough {
			t.Errorf("parseDeadline(%q) = %v %v %v, want %v %v", tc.line, got, rough, ok, tc.want, tc.rough)
		}
	}
	if _, _, ok := parseDeadline([]string{"Do you want to proceed?", "❯ 1. Yes"}); ok {
		t.Error("a deadline where there is none")
	}
	// The phrase in the agent's output, far from any prompt, does not count.
	quoted := []string{"> Claude Code will automatically block this request in 2:00, to avoid", "", "Some notes", "more", "and more", "Done."}
	if _, _, ok := parseDeadline(quoted); ok {
		t.Error("a deadline from quoted output")
	}
}

func TestApproves(t *testing.T) {
	for text, want := range map[string]bool{
		"yes, go ahead": true, "OK do it": true, "iya lanjut": true, "Gas!": true,
		"use trash instead": false, "no": false, "why rm?": false, "": false,
	} {
		if got := approves(text); got != want {
			t.Errorf("approves(%q) = %v, want %v", text, got, want)
		}
	}
}

// Codex prints the key of each answer; it is pressed instead of the digit,
// and the answer that presses Esc takes the place of the Esc button.
func TestParseOptionsUsesPrintedKeys(t *testing.T) {
	screen := []string{
		"Would you like to run the following command?",
		"› 1. Yes, proceed (y)",
		"  2. Yes, and don't ask again for this command (p)",
		"  3. No, and tell Codex what to do differently (esc)",
	}
	var got []string
	for _, o := range withEsc(parseOptions(screen, true)) {
		got = append(got, o.Keys[0])
	}
	if strings.Join(got, ",") != "y,p,escape" {
		t.Errorf("keys %v, want y,p,escape", got)
	}
	got = nil
	for _, o := range parseOptions(screen, false) {
		got = append(got, o.Keys[0])
	}
	if strings.Join(got, ",") != "1,2,3" {
		t.Errorf("question keys %v, want the digits", got)
	}
	claude := []string{"❯ 1. Yes", "  2. Yes, allow all edits during this session (shift+tab)", "  3. No"}
	got = nil
	for _, o := range parseOptions(claude, true) {
		got = append(got, o.Keys[0])
	}
	if strings.Join(got, ",") != "1,2,3" {
		t.Errorf("Claude Code keys %v, want the digits", got)
	}
}

func TestNoteAnswer(t *testing.T) {
	opts := func(labels ...string) store.Alert {
		var a store.Alert
		for _, l := range labels {
			a.Options = append(a.Options, store.AlertOption{Label: l, Keys: []string{"x"}})
		}
		return a
	}
	perm := opts("1 Yes", "2 Yes, and don't ask again", "3 No")
	perm.Kind = store.AlertPermission
	perm.Options = withEsc(perm.Options)
	if i, ok := noteAnswer(perm); !ok || perm.Options[i].Label != "Esc" {
		t.Errorf("Claude Code: %d %v, want Esc", i, ok)
	}
	q := withEsc(opts("1 Blue", "2 No preference").Options)
	if i, ok := noteAnswer(store.Alert{Kind: store.AlertQuestion, Options: q}); !ok || i != 2 {
		t.Errorf("a question: %d %v, want Esc, never the answer \"No preference\"", i, ok)
	}
}

func TestApproveAnswerRequiresAClearOneTimeChoice(t *testing.T) {
	alert := func(kind string, labels ...string) store.Alert {
		var opts []store.AlertOption
		for i, label := range labels {
			opts = append(opts, store.AlertOption{Label: label, Keys: []string{string(rune('1' + i))}})
		}
		return store.Alert{Kind: kind, Options: withEsc(opts)}
	}
	tests := []struct {
		name string
		al   store.Alert
		want int
		ok   bool
	}{
		{"question with unrelated choices", alert(store.AlertQuestion, "1 Blue", "2 No preference"), 0, false},
		{"yes no question", alert(store.AlertQuestion, "1 Yes", "2 No"), 0, true},
		{"clear allow", alert(store.AlertPermission, "1 Allow"), 0, true},
		{"destructive choice without an affirmative label", alert(store.AlertPermission, "1 Delete all files", "2 Cancel"), 0, false},
		{"permission with only No", alert(store.AlertPermission, "1 No"), 0, false},
		{"explicit one-time choice before lasting rule", alert(store.AlertPermission, "1 Yes, and don't ask again", "2 Yes, proceed", "3 No"), 1, true},
		{"session-wide allow needs a tap", alert(store.AlertPermission, "1 Allow for this session"), 0, false},
		{"future allow needs a tap", alert(store.AlertPermission, "1 Yes, allow this host in the future"), 0, false},
		{"broad allow needs a tap", alert(store.AlertPermission, "1 Yes, allow all edits"), 0, false},
		{"continue is not a clear yes", alert(store.AlertPermission, "1 Continue", "2 Cancel"), 0, false},
		{"proceed is not a clear yes", alert(store.AlertPermission, "1 Proceed", "2 Cancel"), 0, false},
		{"run is not a clear yes", alert(store.AlertPermission, "1 Run", "2 Cancel"), 0, false},
		{"default allow needs a tap", alert(store.AlertPermission, "1 Allow and set as default"), 0, false},
		{"trusted-folder allow needs a tap", alert(store.AlertPermission, "1 Yes, trust this folder"), 0, false},
	}
	for _, tc := range tests {
		i, ok := approveAnswer(tc.al)
		if i != tc.want || ok != tc.ok {
			t.Errorf("%s: approveAnswer = %d, %v; want %d, %v", tc.name, i, ok, tc.want, tc.ok)
		}
	}
}

func TestClockText(t *testing.T) {
	for d, want := range map[time.Duration]string{2 * time.Minute: "2:00", 45 * time.Second: "0:45", 29600 * time.Millisecond: "0:30"} {
		if got := clockText(d); got != want {
			t.Errorf("clockText(%v) = %q, want %q", d, got, want)
		}
	}
}

// The answer that /deny and a note press, on each agent's layouts (Claude
// Code 2.1.323 and Codex 0.167.1): Esc, which both read as a refusal. It
// never allows, never makes a lasting rule, and never starts something else.
func TestDenyAnswer(t *testing.T) {
	alert := func(kind string, screen ...string) store.Alert {
		return store.Alert{Kind: kind, Options: withEsc(parseOptions(screen, kind == store.AlertPermission))}
	}
	tests := []struct {
		name string
		al   store.Alert
	}{
		{"Claude Code permission", alert(store.AlertPermission, "❯ 1. Yes", "  2. Yes, and don't ask again for rm commands", "  3. No")},
		{"Claude Code safety check", alert(store.AlertPermission, "❯ 1. Yes", "  2. No")},
		{"Claude Code plan", alert(store.AlertPermission, "❯ 1. Yes, auto-accept edits", "  2. No, refine with Ultraplan in a cloud session", "  3. No, keep planning")},
		{"Codex command", alert(store.AlertPermission, "› 1. Yes, proceed (y)", "  2. Yes, and don't ask again for commands that start with `git` (p)", "  3. No, and tell Codex what to do differently (esc)")},
		{"Codex network", alert(store.AlertPermission, "› 1. Yes, just this once (y)", "  2. Yes, and allow this host in the future (p)", "  3. No, and block this host in the future", "  4. No, and tell Codex what to do differently (esc)")},
		{"Codex app tool", alert(store.AlertPermission, "› 1. Allow", "  2. Allow for this session", "  3. Allow and don't ask me again")},
		{"a question", alert(store.AlertQuestion, "❯ 1. Blue", "  2. No preference")},
	}
	for _, tc := range tests {
		i, ok := denyAnswer(tc.al)
		if !ok || tc.al.Options[i].Keys[0] != "escape" {
			t.Errorf("%s: picked %+v %v, want Esc", tc.name, tc.al.Options[i], ok)
		}
	}
	// A menu without Esc (configured answers) falls back to a plain No.
	plain := store.Alert{Kind: store.AlertPermission, Options: []store.AlertOption{
		{Label: "1 Yes", Keys: []string{"1"}},
		{Label: "2 No, refine with Ultraplan in a cloud session", Keys: []string{"2"}},
		{Label: "3 No", Keys: []string{"3"}},
	}}
	if i, ok := denyAnswer(plain); !ok || plain.Options[i].Keys[0] != "3" {
		t.Errorf("without Esc: picked %d %v, want 3 No", i, ok)
	}
	for _, label := range []string{"No, remember this rule", "No, trust this folder", "No, set this as default", "No, and tell the agent what to do differently"} {
		al := store.Alert{Kind: store.AlertPermission, Options: []store.AlertOption{{Label: label, Keys: []string{"2"}}}}
		if _, ok := denyAnswer(al); ok {
			t.Errorf("without Esc, chose lasting refusal %q", label)
		}
	}
}

// The countdown of a Claude Code safety check changes every second; the
// screen check must not take that for another prompt.
func TestScreenHashIgnoresTheCountdown(t *testing.T) {
	screen := func(left string) []string {
		return []string{"Bash command", "  rm -f x", "⚠ Claude Code will automatically block this request in " + left + ", to avoid holding up an unattended session", "Do you want to proceed?", "❯ 1. Yes", "  2. No"}
	}
	if screenHash(screen("2:00")) != screenHash(screen("1:37")) {
		t.Error("the countdown changed the hash")
	}
	if screenHash(screen("2:00")) == screenHash([]string{"Bash command", "  rm -f y", "Do you want to proceed?", "❯ 1. Yes", "  2. No"}) {
		t.Error("another prompt has the same hash")
	}
}

// A numbered line above the prompt, such as one of a diff, does not shadow
// the prompt's own answers.
func TestParseOptionsTakesTheLastList(t *testing.T) {
	got := parseOptions([]string{"  1. step one of the script", "  2. step two", "Do you want to proceed?", "❯ 1. Yes", "  2. No"}, true)
	if len(got) != 2 || got[0].Label != "1 Yes" || got[1].Label != "2 No" {
		t.Errorf("options %+v, want 1 Yes and 2 No", got)
	}
}
