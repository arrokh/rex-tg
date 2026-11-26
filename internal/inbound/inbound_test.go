package inbound_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/inbound"
)

func TestRouteText(t *testing.T) {
	cmd := func(c, a string) inbound.Route { return inbound.Route{Kind: inbound.RouteCommand, Command: c, Args: a} }
	txt := func(s string) inbound.Route { return inbound.Route{Kind: inbound.RouteAgentText, Text: s} }
	ign := inbound.Route{Kind: inbound.RouteIgnore}
	tests := []struct {
		name    string
		text    string
		inTopic bool
		want    inbound.Route
	}{
		{"topic plain", "  hello there \n", true, txt("hello there")},
		{"main plain ignored", "hello", false, ign},
		{"empty", "   ", true, ign},
		{"topic command", "/info", true, cmd("info", "")},
		{"topic last answer", "/last", true, cmd("last", "")},
		{"topic command args", "/screen 50", true, cmd("screen", "50")},
		{"topic command newline args", "/force\nrun tests", true, cmd("force", "run tests")},
		{"topic command with bot", "/approve@RexBot", true, cmd("approve", "")},
		{"topic command with bot args", "/mode@rexbot final", true, cmd("mode", "final")},
		{"topic command uppercase", "/INFO", true, cmd("info", "")},
		{"topic other bot", "/info@otherbot", true, ign},
		{"topic double slash", "//help", true, txt("/help")},
		{"topic double slash args", "//compact now", true, txt("/compact now")},
		{"topic unknown", "/compact", true, txt("/compact")},
		{"topic unknown args", "  /compact now  ", true, txt("/compact now")},
		{"topic model command", "  /model gpt  ", true, cmd("model", "gpt")},
		{"topic effort command", "/effort", true, cmd("effort", "")},
		{"topic double slash model", "//model gpt", true, txt("/model gpt")},
		{"topic main command", "/agents", true, cmd("agents", "")},
		{"topic attach command", "/attach", true, cmd("attach", "")},
		{"topic new is the bot's", "/new pi ~/app", true, cmd("new", "pi ~/app")},
		{"topic double slash new", "//new", true, txt("/new")},
		{"topic help", "/help", true, cmd("help", "")},
		{"topic queue", "/queue", true, cmd("queue", "")},
		{"main queue", "/queue", false, cmd(inbound.CmdTopicOnly, "queue")},
		{"topic pause", "/pause", true, cmd("pause", "")},
		{"main command", "/agents", false, cmd("agents", "")},
		{"main attach command", "/attach", false, cmd("attach", "")},
		{"main start args", "/start abc123", false, cmd("start", "abc123")},
		{"main command with bot", "/status@rexbot", false, cmd("status", "")},
		{"main other bot", "/status@otherbot", false, ign},
		{"main help", "/help", false, cmd("help", "")},
		{"main topic command", "/screen 10", false, cmd(inbound.CmdTopicOnly, "screen")},
		{"main last answer", "/last", false, cmd(inbound.CmdTopicOnly, "last")},
		{"main topic command with bot", "/esc@rexbot", false, cmd(inbound.CmdTopicOnly, "esc")},
		{"main unknown", "/nope arg", false, cmd(inbound.CmdUnknown, "nope")},
		{"main double slash unknown", "//help", false, cmd(inbound.CmdUnknown, "/help")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inbound.RouteText(tt.text, tt.inTopic, "rexbot")
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// A tap on a keyboard button sends its label: it routes as the button's
// command, with or without emoji variation selectors, and never as text for
// the agent. Other text that only starts like a label is text.
func TestRouteTextButtons(t *testing.T) {
	for _, b := range inbound.Buttons {
		for _, label := range []string{b.Label, " " + b.Label + " ", strings.ReplaceAll(b.Label, " ", "\uFE0F ")} {
			if r := inbound.RouteText(label, true, "bot"); r.Kind != inbound.RouteCommand || r.Command != b.Command {
				t.Errorf("topic %q: %+v, want /%s", label, r, b.Command)
			}
		}
	}
	if r := inbound.RouteText("🎛 Panel", false, "bot"); r.Kind != inbound.RouteCommand || r.Command != "rt" {
		t.Errorf("main chat panel: %+v", r)
	}
	if r := inbound.RouteText("🖥 Screen", false, "bot"); r.Command != inbound.CmdTopicOnly || r.Args != "screen" {
		t.Errorf("main chat screen: %+v", r)
	}
	for _, name := range []string{"project", "config"} {
		if r := inbound.RouteText("/"+name, true, "bot"); r.Kind != inbound.RouteAgentText {
			t.Errorf("button-only name /%s was treated as a bot command: %+v", name, r)
		}
	}
	if r := inbound.RouteText("🖥 Screen please", true, "bot"); r.Kind != inbound.RouteAgentText {
		t.Errorf("text that starts like a label: %+v", r)
	}
	if r := inbound.RouteText("⚙️ Config", true, "bot"); r.Kind != inbound.RouteCommand || r.Command != "config" {
		t.Errorf("stale Config button: %+v, want the legacy config action", r)
	}
}

func TestRouteTextAllCommands(t *testing.T) {
	for _, c := range []string{"info", "screen", "files", "changes", "approve", "deny", "esc", "interrupt", "key", "force", "mode", "verbose", "reasoning", "resend", "unlink", "mute", "unmute", "queue", "last", "help"} {
		if r := inbound.RouteText("/"+c, true, "b"); r.Kind != inbound.RouteCommand || r.Command != c {
			t.Errorf("topic /%s: %+v", c, r)
		}
	}
	for _, c := range []string{"start", "agents", "new", "topics", "status", "pause", "unpause", "rt", "help"} {
		if r := inbound.RouteText("/"+c, false, "b"); r.Kind != inbound.RouteCommand || r.Command != c {
			t.Errorf("main /%s: %+v", c, r)
		}
	}
}

func TestSlashCommand(t *testing.T) {
	for text, want := range map[string]bool{
		"/restart":                 true,
		"  /restart  ":             true,
		"/compact keep the tests":  true,
		"/compact\nkeep the tests": true,
		"/cux:switch 2":            true,
		"/frontend-design:design":  true,
		"/skill:my_skill-2 arg":    true,
		"/compact\tnow":            true,
		"/restart\r\n":             true,
		"/Model opus":              true,
		"/tmp/a.log what is this":  false, // a path
		"/Users/me":                false,
		"/README.md what is it":    false,
		"/a.b/c":                   false,
		"//restart":                false, // RouteText strips one slash first
		"/restart@bot":             false,
		"/":                        false,
		"/ restart":                false,
		"/2fa":                     false,
		"/-x":                      false,
		"/héllo":                   false,
		"restart":                  false,
		"please /restart":          false,
		"!ls":                      false,
		"":                         false,
	} {
		if got := inbound.SlashCommand(text); got != want {
			t.Errorf("SlashCommand(%q) = %v, want %v", text, got, want)
		}
	}
}

func allOK() inbound.Facts {
	return inbound.Facts{FromOwner: true, OwnerChat: true, TopicLive: true, PaneExists: true, AgentInFront: true}
}

func TestCheckTable(t *testing.T) {
	typing := []inbound.Action{inbound.ActText, inbound.ActSendNow, inbound.ActForce, inbound.ActKeys, inbound.ActPromptAnswer}
	all := append([]inbound.Action{inbound.ActReadOnly}, typing...)

	// Owner columns apply to every action.
	for _, a := range all {
		f := allOK()
		f.PromptOpen = a == inbound.ActPromptAnswer
		f.AlertMatches = true
		if ok, r := inbound.Check(a, f); !ok {
			t.Errorf("action %d all good: %q", a, r)
		}
		f.FromOwner = false
		if ok, r := inbound.Check(a, f); ok || r != inbound.ReasonNotOwner {
			t.Errorf("action %d not owner: %v %q", a, ok, r)
		}
		f.FromOwner, f.OwnerChat = true, false
		if ok, r := inbound.Check(a, f); ok || r != inbound.ReasonNotOwnerChat {
			t.Errorf("action %d not owner chat: %v %q", a, ok, r)
		}
	}

	// Read-only skips everything else.
	if ok, _ := inbound.Check(inbound.ActReadOnly, inbound.Facts{FromOwner: true, OwnerChat: true, Paused: true}); !ok {
		t.Error("read-only should pass with only owner facts")
	}

	for _, a := range typing {
		base := allOK()
		base.PromptOpen = a == inbound.ActPromptAnswer
		base.AlertMatches = true
		cases := []struct {
			name   string
			mutate func(*inbound.Facts)
			reason string
		}{
			{"paused", func(f *inbound.Facts) { f.Paused = true }, inbound.ReasonPaused},
			{"topic not live", func(f *inbound.Facts) { f.TopicLive = false }, inbound.ReasonTopicNotLive},
			{"no pane", func(f *inbound.Facts) { f.PaneExists = false }, inbound.ReasonNoPane},
			{"agent not in front", func(f *inbound.Facts) { f.AgentInFront = false }, inbound.ReasonAgentNotFront},
		}
		for _, c := range cases {
			f := base
			c.mutate(&f)
			if ok, r := inbound.Check(a, f); ok || r != c.reason {
				t.Errorf("action %d %s: %v %q", a, c.name, ok, r)
			}
		}
	}

	prompt := []struct {
		name        string
		a           inbound.Action
		open, match bool
		wantOK      bool
		wantReason  string
	}{
		{"text, no prompt", inbound.ActText, false, false, true, ""},
		{"text, prompt open", inbound.ActText, true, false, false, inbound.ReasonPromptOpen},
		{"send now, prompt open", inbound.ActSendNow, true, false, false, inbound.ReasonPromptOpen},
		{"send now, no prompt", inbound.ActSendNow, false, false, true, ""},
		{"force, prompt open", inbound.ActForce, true, false, true, ""},
		{"force, no prompt", inbound.ActForce, false, false, true, ""},
		{"keys, prompt open", inbound.ActKeys, true, false, true, ""},
		{"keys, no prompt", inbound.ActKeys, false, false, true, ""},
		{"answer, open and matches", inbound.ActPromptAnswer, true, true, true, ""},
		{"answer, no prompt", inbound.ActPromptAnswer, false, true, false, inbound.ReasonNoPrompt},
		{"answer, stale alert", inbound.ActPromptAnswer, true, false, false, inbound.ReasonAlertStale},
		{"text ignores alert match", inbound.ActText, false, true, true, ""},
	}
	for _, c := range prompt {
		f := allOK()
		f.PromptOpen, f.AlertMatches = c.open, c.match
		ok, r := inbound.Check(c.a, f)
		if ok != c.wantOK || r != c.wantReason {
			t.Errorf("%s: got %v %q", c.name, ok, r)
		}
	}
}

func TestJoiner(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ms := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Millisecond) }
	long := strings.Repeat("x", 10)

	t.Run("short window joins", func(t *testing.T) {
		j := inbound.NewJoiner(300*time.Millisecond, 1500*time.Millisecond, 10)
		j.Add(1, 10, "a", ms(0))
		j.Add(1, 11, "b", ms(200))
		if got := j.Due(ms(400)); len(got) != 0 {
			t.Fatalf("too early: %+v", got)
		}
		got := j.Due(ms(600))
		want := []inbound.Joined{{ThreadID: 1, UpdateIDs: []int64{10, 11}, Text: "a\nb"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v", got)
		}
		if again := j.Due(ms(5000)); len(again) != 0 {
			t.Fatalf("not removed: %+v", again)
		}
	})
	t.Run("a reply's quote comes after all joined parts", func(t *testing.T) {
		j := inbound.NewJoiner(300*time.Millisecond, 1500*time.Millisecond, 10)
		j.AddReply(1, 10, "part one", "the answer", ms(0))
		j.Add(1, 11, "part two", ms(200))
		got := j.Due(ms(600))
		want := []inbound.Joined{{ThreadID: 1, UpdateIDs: []int64{10, 11}, Text: "part one\npart two\n\nIn reply to:\n> the answer"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("a command first takes no quote", func(t *testing.T) {
		j := inbound.NewJoiner(300*time.Millisecond, 1500*time.Millisecond, 10)
		j.Add(1, 10, "/compact", ms(0))
		j.AddReply(1, 11, "and this", "the answer", ms(200))
		if got := j.Due(ms(600)); len(got) != 1 || got[0].Text != "/compact\nand this" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("gap beyond window splits", func(t *testing.T) {
		j := inbound.NewJoiner(300*time.Millisecond, 1500*time.Millisecond, 10)
		j.Add(1, 10, "a", ms(0))
		j.Add(1, 11, "b", ms(500))
		got := j.Due(ms(1000))
		want := []inbound.Joined{
			{ThreadID: 1, UpdateIDs: []int64{10}, Text: "a"},
			{ThreadID: 1, UpdateIDs: []int64{11}, Text: "b"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("long message uses long window", func(t *testing.T) {
		j := inbound.NewJoiner(300*time.Millisecond, 1500*time.Millisecond, 10)
		j.Add(1, 10, long, ms(0))
		j.Add(1, 11, "tail", ms(1200))
		if got := j.Due(ms(2000)); len(got) != 0 {
			t.Fatalf("long window not applied: %+v", got)
		}
		got := j.Due(ms(2800))
		want := []inbound.Joined{{ThreadID: 1, UpdateIDs: []int64{10, 11}, Text: long + "\ntail"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("long window still ends", func(t *testing.T) {
		j := inbound.NewJoiner(300*time.Millisecond, 1500*time.Millisecond, 10)
		j.Add(1, 10, long, ms(0))
		j.Add(1, 11, "late", ms(1600))
		got := j.Due(ms(1700))
		if len(got) != 1 || got[0].Text != long {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("long counts runes", func(t *testing.T) {
		j := inbound.NewJoiner(300*time.Millisecond, 1500*time.Millisecond, 4)
		j.Add(1, 10, "éééé", ms(0)) // 4 runes, 8 bytes
		if got := j.Due(ms(1000)); len(got) != 0 {
			t.Fatalf("should still wait: %+v", got)
		}
	})
	t.Run("separate topics and order", func(t *testing.T) {
		j := inbound.NewJoiner(300*time.Millisecond, 1500*time.Millisecond, 10)
		j.Add(1, 10, "a", ms(0))
		j.Add(2, 20, "x", ms(50))
		j.Add(1, 11, "b", ms(100))
		j.Add(2, 21, "y", ms(120))
		got := j.Due(ms(1000))
		want := []inbound.Joined{
			{ThreadID: 1, UpdateIDs: []int64{10, 11}, Text: "a\nb"},
			{ThreadID: 2, UpdateIDs: []int64{20, 21}, Text: "x\ny"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("only due groups are removed", func(t *testing.T) {
		j := inbound.NewJoiner(300*time.Millisecond, 1500*time.Millisecond, 10)
		j.Add(1, 10, "a", ms(0))
		j.Add(2, 20, "x", ms(400))
		got := j.Due(ms(500))
		if len(got) != 1 || got[0].ThreadID != 1 {
			t.Fatalf("got %+v", got)
		}
		got = j.Due(ms(800))
		if len(got) != 1 || got[0].ThreadID != 2 {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestNormalize(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "hello", "hello"},
		{"trim", "  \n hello \n\n", "hello"},
		{"crlf", "a\r\nb", "a\nb"},
		{"cr", "a\rb", "a\nb"},
		{"trailing spaces", "a  \nb\t \nc", "a\nb\nc"},
		{"blank runs", "a\n\n\n\nb", "a\n\nb"},
		{"blank runs with spaces", "a\n  \n \n\nb", "a\n\nb"},
		{"trailing newline", "a\nb\n", "a\nb"},
		{"leading indent kept", "  a\n  b", "a\n  b"},
		{"empty", "  \n ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inbound.Normalize(tt.in); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHash(t *testing.T) {
	// SHA-256 of "hello".
	const hello = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got := inbound.Hash("hello"); got != hello {
		t.Errorf("got %s", got)
	}
	if got := inbound.Hash("  hello \r\n"); got != hello {
		t.Errorf("normalized hash differs: %s", got)
	}
	if inbound.Hash("hello") == inbound.Hash("hello!") {
		t.Error("different text, same hash")
	}
}

func TestMatchEcho(t *testing.T) {
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	d := func(id int64, body string, ago time.Duration) inbound.Delivered {
		return inbound.Delivered{UpdateID: id, BodyHash: inbound.Hash(body), Body: body, DeliveredAt: at.Add(-ago)}
	}
	p40 := strings.Repeat("a", 40)
	tests := []struct {
		name   string
		prompt string
		items  []inbound.Delivered
		want   int64
		wantOK bool
	}{
		{"exact", "fix the bug", []inbound.Delivered{d(1, "other", time.Minute), d(2, "fix the bug", time.Minute)}, 2, true},
		{"exact normalized", "fix the bug\r\n", []inbound.Delivered{d(1, "fix the bug  ", time.Minute)}, 1, true},
		{"exact oldest first", "same", []inbound.Delivered{d(2, "same", time.Minute), d(1, "same", 5*time.Minute)}, 1, true},
		{"exact at 10 minutes", "x", []inbound.Delivered{d(1, "x", 10*time.Minute)}, 1, true},
		{"exact too old", "x", []inbound.Delivered{d(1, "x", 10*time.Minute+time.Second)}, 0, false},
		// Claude Code stamps a message typed while it works when it queues
		// it, a moment before rex-tg records the delivery (seen live: 1 ms).
		{"exact queued a moment before the delivery", "x", []inbound.Delivered{d(1, "x", -time.Millisecond)}, 1, true},
		{"exact at the skew", "x", []inbound.Delivered{d(1, "x", -3*time.Second)}, 1, true},
		{"exact too far in the future", "x", []inbound.Delivered{d(1, "x", -4*time.Second)}, 0, false},
		{"prefix fallback", p40 + "XYZ", []inbound.Delivered{d(1, p40+"different tail", 10*time.Second)}, 1, true},
		{"prefix fallback first wins", p40 + "1", []inbound.Delivered{d(2, p40+"b", 5*time.Second), d(1, p40+"c", 20*time.Second)}, 1, true},
		{"prefix too old for fallback", p40 + "XYZ", []inbound.Delivered{d(1, p40+"tail", 31*time.Second)}, 0, false},
		{"prefix at 30 seconds", p40 + "XYZ", []inbound.Delivered{d(1, p40+"tail", 30*time.Second)}, 1, true},
		{"prefix differs", "b" + p40, []inbound.Delivered{d(1, p40+"tail", 5*time.Second)}, 0, false},
		{"prefix counts runes", strings.Repeat("é", 40) + "1", []inbound.Delivered{d(1, strings.Repeat("é", 40)+"2", time.Second)}, 1, true},
		{"short different bodies do not match", "hello world", []inbound.Delivered{d(1, "hello there", time.Second)}, 0, false},
		{"hash beats earlier prefix", p40 + "Q", []inbound.Delivered{d(1, p40+"R", 20*time.Second), d(2, p40+"Q", 5*time.Second)}, 2, true},
		{"no items", "x", nil, 0, false},
		{"empty prompt", "", []inbound.Delivered{d(1, "", time.Second)}, 1, true},
		// Claude Code turns a pasted image path into an "[Image #N]" marker
		// at the start (seen live); an agent that does not attach images keeps the
		// path. Both are the photo.
		{"claude image marker", "[Image #3]the red button", []inbound.Delivered{d(1, "the red button\n/tmp/up/7/12-photo.jpg", time.Second)}, 1, true},
		{"claude image without caption", "[Image #1]", []inbound.Delivered{d(1, "/tmp/up/7/5-photo.jpg", time.Second)}, 1, true},
		// All image-only prompts look the same, so an old photo is no match:
		// a pasted image on the Mac minutes later is not its echo.
		{"image only, photo minutes ago", "[Image #1]", []inbound.Delivered{d(1, "/tmp/up/7/5-photo.jpg", 5*time.Minute)}, 0, false},
		{"image path kept", "look\n/tmp/up/7/5-shot.PNG", []inbound.Delivered{d(1, "look\n/tmp/up/7/5-shot.PNG", time.Second)}, 1, true},
		{"image marker with a long caption", "[Image #2]" + p40 + "!", []inbound.Delivered{d(1, p40+"?\n/tmp/up/7/6-photo.jpg", 5*time.Second)}, 1, true},
		{"other file paths still count", "read /tmp/up/7/9-a.pdf", []inbound.Delivered{d(1, "read /tmp/up/7/9-b.pdf", time.Second)}, 0, false},
		{"a different caption", "[Image #3]yes", []inbound.Delivered{d(1, "no\n/tmp/up/7/12-photo.jpg", time.Second)}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := inbound.MatchEcho(tt.prompt, at, tt.items)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("got %d %v, want %d %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
