package daemon

import (
	"strings"
	"testing"
	"time"
)

func TestParseAt(t *testing.T) {
	loc := time.FixedZone("T", 2*3600)
	now := time.Date(2026, 3, 10, 14, 30, 0, 0, loc)
	cases := []struct {
		in   string
		at   time.Time
		text string
		bad  bool
	}{
		{in: "09:00 run the tests", at: time.Date(2026, 3, 11, 9, 0, 0, 0, loc), text: "run the tests"},
		{in: "16:45 later today", at: time.Date(2026, 3, 10, 16, 45, 0, 0, loc), text: "later today"},
		{in: "9:05 one digit", at: time.Date(2026, 3, 11, 9, 5, 0, 0, loc), text: "one digit"},
		{in: "14:30 same minute rolls over", at: time.Date(2026, 3, 11, 14, 30, 0, 0, loc), text: "same minute rolls over"},
		{in: "30m hello", at: now.Add(30 * time.Minute), text: "hello"},
		{in: "2h hello there", at: now.Add(2 * time.Hour), text: "hello there"},
		{in: "1h30m x", at: now.Add(90 * time.Minute), text: "x"},
		{in: "5s x", at: now.Add(5 * time.Second), text: "x"},
		{in: "", bad: true},
		{in: "09:00", bad: true},
		{in: "30m", bad: true},
		{in: "25:00 x", bad: true},
		{in: "09:61 x", bad: true},
		{in: "0m x", bad: true},
		{in: "-5m x", bad: true},
		{in: "soon x", bad: true},
		{in: "999h x", bad: true},
		{in: "30 x", bad: true},
	}
	for _, c := range cases {
		at, text, err := parseAt(c.in, now)
		if c.bad {
			if err == nil {
				t.Errorf("parseAt(%q) = %v, want an error", c.in, at)
			}
			continue
		}
		if err != nil || !at.Equal(c.at) || text != c.text {
			t.Errorf("parseAt(%q) = %v, %q, %v; want %v, %q", c.in, at, text, err, c.at, c.text)
		}
	}
}

func TestInText(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Second:             "30s",
		30 * time.Minute:             "30m",
		2 * time.Hour:                "2h",
		7*time.Hour + 12*time.Minute: "7h 12m",
		49*time.Hour + 5*time.Minute: "2d 1h",
	} {
		if got := inText(d); got != want {
			t.Errorf("inText(%v) = %q, want %q", d, got, want)
		}
	}
	if !strings.Contains(errAtUsage.Error(), "/at") {
		t.Error("usage text")
	}
}
