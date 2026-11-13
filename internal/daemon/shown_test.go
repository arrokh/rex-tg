package daemon

import "testing"

func TestSniffImage(t *testing.T) {
	for _, c := range []struct {
		name string
		data string
		ext  string
		ok   bool
	}{
		{"png", "\x89PNG\r\n\x1a\nrest", ".png", true},
		{"jpeg", "\xff\xd8\xff\xe0rest", ".jpg", true},
		{"gif", "GIF89arest", ".gif", true},
		{"webp", "RIFF\x00\x00\x00\x00WEBPrest", ".webp", true},
		{"svg", "<svg xmlns=\"x\"></svg>", "", false},
		{"html", "<html>", "", false},
		{"empty", "", "", false},
		{"riff but not webp", "RIFF\x00\x00\x00\x00WAVEfmt ", "", false},
	} {
		ext, ok := sniffImage([]byte(c.data))
		if ext != c.ext || ok != c.ok {
			t.Errorf("%s: got %q %v, want %q %v", c.name, ext, ok, c.ext, c.ok)
		}
	}
}
