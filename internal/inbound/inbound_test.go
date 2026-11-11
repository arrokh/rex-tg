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
		{"topic command args", "/screen 50", true, cmd("screen", "50")},
		{"topic command newline args", "/force\nrun tests", true, cmd("force", "run tests")},
		{"topic command with bot", "/approve@RexBot", true, cmd("approve", "")},
		{"topic command with bot args", "/mode@rexbot final", true, cmd("mode", "final")},
		{"topic command uppercase", "/INFO", true, cmd("info", "")},
		{"topic other bot", "/info@otherbot", true, ign},
		{"topic double slash", "//help", true, txt("/help")},
		{"topic double slash args", "//compact now", true, txt("/compact now")},
		{"topic unknown", "/compact", true, txt("/compact")},
		{"topic unknown args", "  /model gpt  ", true, txt("/model gpt")},
		{"topic main command", "/agents", true, cmd("agents", "")},
		{"topic new is the bot's", "/new pi ~/app", true, cmd("new", "pi ~/app")},
		{"topic double slash new", "//new", true, txt("/new")},
		{"topic help", "/help", true, cmd("help", "")},
		{"topic pause", "/pause", true, cmd("pause", "")},
		{"main command", "/agents", false, cmd("agents", "")},
		{"main start args", "/start abc123", false, cmd("start", "abc123")},
		{"main command with bot", "/status@rexbot", false, cmd("status", "")},
		{"main other bot", "/status@otherbot", false, ign},
		{"main help", "/help", false, cmd("help", "")},
		{"main topic command", "/screen 10", false, cmd(inbound.CmdTopicOnly, "screen")},
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

func TestRouteTextAllCommands(t *testing.T) {
	for _, c := range []string{"info", "screen", "approve", "deny", "esc", "interrupt", "key", "force", "mode", "verbose", "reasoning", "resend", "unlink", "mute", "unmute", "help"} {
		if r := inbound.RouteText("/"+c, true, "b"); r.Kind != inbound.RouteCommand || r.Command != c {
			t.Errorf("topic /%s: %+v", c, r)
		}
	}
	for _, c := range []string{"start", "agents", "new", "topics", "status", "pause", "unpause", "help"} {
		if r := inbound.RouteText("/"+c, false, "b"); r.Kind != inbound.RouteCommand || r.Command != c {
			t.Errorf("main /%s: %+v", c, r)
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
		{"exact in the future", "x", []inbound.Delivered{d(1, "x", -time.Second)}, 0, false},
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
