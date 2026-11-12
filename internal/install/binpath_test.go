package install

import "testing"

func TestStablePath(t *testing.T) {
	opt := "/opt/homebrew/opt/rex-tg/bin/rex-tg"
	cellar := "/opt/homebrew/Cellar/rex-tg/0.1.0/bin/rex-tg"
	cases := []struct {
		name, in, want string
		have           []string
	}{
		{"cellar maps to opt", cellar, opt, []string{opt}},
		{"intel prefix", "/usr/local/Cellar/rex-tg/0.1.0/bin/rex-tg", "/usr/local/opt/rex-tg/bin/rex-tg", []string{"/usr/local/opt/rex-tg/bin/rex-tg"}},
		{"local bin unchanged", "/Users/a/.local/bin/rex-tg", "/Users/a/.local/bin/rex-tg", []string{opt}},
		{"missing opt unchanged", cellar, cellar, nil},
		{"not in bin unchanged", "/opt/homebrew/Cellar/rex-tg/0.1.0/libexec/rex-tg", "/opt/homebrew/Cellar/rex-tg/0.1.0/libexec/rex-tg", []string{opt}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exists := func(p string) bool {
				for _, h := range c.have {
					if h == p {
						return true
					}
				}
				return false
			}
			if got := StablePath(c.in, exists); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
