package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"rex-tg/extensions"
)

const testBin = "/opt/bin/rex-tg"

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type call struct{ name, args string }

type fakeRunner struct {
	calls []call
	fail  map[string]error // by first argument
	out   map[string]string
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{name, strings.Join(args, " ")})
	return []byte(f.out[args[0]]), f.fail[args[0]]
}

func newPaths(t *testing.T) Paths {
	t.Helper()
	home := t.TempDir()
	p := DefaultPaths(home, testBin)
	var n int
	base := time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC)
	p.Now = func() time.Time { n++; return base.Add(time.Duration(n) * time.Second) }
	p.UID = "501"
	return p
}

func backups(t *testing.T, path string) []string {
	t.Helper()
	m, err := filepath.Glob(path + backupInfix + "*")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDefaultPaths(t *testing.T) {
	p := DefaultPaths("/h", "/b/rex-tg")
	want := Paths{Home: "/h", Binary: "/b/rex-tg", ClaudeSettings: "/h/.claude/settings.json", CodexHooks: "/h/.codex/hooks.json",
		PiExtension: "/h/.pi/agent/extensions/rex-tg.ts", LaunchAgent: "/h/Library/LaunchAgents/local.rex-tg.plist",
		Label: "local.rex-tg", LogDir: "/h/.local/state/rex-tg/logs"}
	if p.Home != want.Home || p.Binary != want.Binary || p.ClaudeSettings != want.ClaudeSettings || p.CodexHooks != want.CodexHooks ||
		p.PiExtension != want.PiExtension || p.LaunchAgent != want.LaunchAgent || p.Label != want.Label || p.LogDir != want.LogDir {
		t.Fatalf("got %+v", p)
	}
}

func TestInstallClaudeMissingFile(t *testing.T) {
	p := newPaths(t)
	ch, err := InstallClaude(p)
	if err != nil || !ch.Changed || ch.Backup != "" {
		t.Fatalf("ch=%+v err=%v", ch, err)
	}
	got := read(t, p.ClaudeSettings)
	for _, ev := range claudeEvents {
		g := gjson.Get(got, "hooks."+ev)
		if len(g.Array()) != 1 || g.Get("0.hooks.0.command").String() != testBin+" hook claude" ||
			g.Get("0.hooks.0.timeout").Int() != 5 || g.Get("0.hooks.0.type").String() != "command" {
			t.Errorf("%s: %s", ev, g.Raw)
		}
	}
	fi, _ := os.Stat(p.ClaudeSettings)
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", fi.Mode())
	}
}

func TestInstallClaudeKeepsOthersByteForByte(t *testing.T) {
	p := newPaths(t)
	orig := fixture(t, "claude_other.json")
	write(t, p.ClaudeSettings, orig, 0o644)
	ch, err := InstallClaude(p)
	if err != nil || !ch.Changed {
		t.Fatalf("ch=%+v err=%v", ch, err)
	}
	if got, want := read(t, p.ClaudeSettings), fixture(t, "claude_other.installed.golden"); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if read(t, ch.Backup) != orig {
		t.Error("backup differs from original")
	}
}

func TestInstallTwiceChangesNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		install func(Paths) (Change, error)
		path    func(Paths) string
		file    string
	}{
		"claude": {InstallClaude, func(p Paths) string { return p.ClaudeSettings }, "claude_other.json"},
		"codex":  {InstallCodex, func(p Paths) string { return p.CodexHooks }, "codex_other.json"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPaths(t)
			write(t, tc.path(p), fixture(t, tc.file), 0o644)
			if ch, err := tc.install(p); err != nil || !ch.Changed {
				t.Fatalf("first: %+v %v", ch, err)
			}
			after := read(t, tc.path(p))
			nb := len(backups(t, tc.path(p)))
			ch, err := tc.install(p)
			if err != nil || ch.Changed || ch.Backup != "" {
				t.Fatalf("second: %+v %v", ch, err)
			}
			if read(t, tc.path(p)) != after || len(backups(t, tc.path(p))) != nb {
				t.Error("second install touched the file")
			}
		})
	}
}

func TestInstallCodexAppendsAtEnd(t *testing.T) {
	p := newPaths(t)
	write(t, p.CodexHooks, fixture(t, "codex_other.json"), 0o644)
	if _, err := InstallCodex(p); err != nil {
		t.Fatal(err)
	}
	got := read(t, p.CodexHooks)
	if want := fixture(t, "codex_other.installed.golden"); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	ss := gjson.Get(got, "hooks.SessionStart").Array()
	if len(ss) != 3 || ss[2].Get("hooks.0.command").String() != testBin+" hook codex" {
		t.Errorf("not appended at end: %s", gjson.Get(got, "hooks.SessionStart").Raw)
	}
	if gjson.Get(got, "hooks.Notification").Exists() {
		t.Error("codex must not get Notification")
	}
}

func TestUninstallClaudeRemovesOnlyOurs(t *testing.T) {
	p := newPaths(t)
	orig := fixture(t, "claude_other.json")
	write(t, p.ClaudeSettings, orig, 0o644)
	if _, err := InstallClaude(p); err != nil {
		t.Fatal(err)
	}
	ch, err := UninstallClaude(p)
	if err != nil || !ch.Changed {
		t.Fatalf("%+v %v", ch, err)
	}
	if got := read(t, p.ClaudeSettings); got != orig {
		t.Fatalf("not restored:\n%s", got)
	}
	ch, err = UninstallClaude(p)
	if err != nil || ch.Changed {
		t.Fatalf("second uninstall: %+v %v", ch, err)
	}
}

func TestUninstallMissingFileDoesNotCreate(t *testing.T) {
	p := newPaths(t)
	ch, err := UninstallClaude(p)
	if err != nil || ch.Changed {
		t.Fatalf("%+v %v", ch, err)
	}
	if _, err := os.Stat(p.ClaudeSettings); err == nil {
		t.Error("file was created")
	}
}

func TestUninstallClaudeMixedGroup(t *testing.T) {
	p := newPaths(t)
	write(t, p.ClaudeSettings, `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/x/other"},{"type":"command","command":"`+testBin+` hook claude"}]}]}}`, 0o644)
	if _, err := UninstallClaude(p); err != nil {
		t.Fatal(err)
	}
	got := read(t, p.ClaudeSettings)
	if want := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/x/other"}]}]}}`; got != want {
		t.Fatalf("got %s", got)
	}
}

func TestCodexUninstallRefusesWhenOthersFollow(t *testing.T) {
	p := newPaths(t)
	write(t, p.CodexHooks, fixture(t, "codex_other.json"), 0o644)
	if _, err := InstallCodex(p); err != nil {
		t.Fatal(err)
	}
	// Another tool appends after us in SessionStart.
	cur := read(t, p.CodexHooks)
	cur, _ = appendToArrayForTest(cur, "hooks.SessionStart", `{"hooks":[{"type":"command","command":"/x/later"}]}`)
	write(t, p.CodexHooks, cur, 0o644)

	ch, err := UninstallCodex(p, false)
	if !errors.Is(err, ErrWouldShiftTrust) || ch.Changed {
		t.Fatalf("want refusal, got %+v %v", ch, err)
	}
	if !strings.Contains(err.Error(), "session_start:3:0") {
		t.Errorf("error should name the shifted entry: %v", err)
	}
	if read(t, p.CodexHooks) != cur {
		t.Error("file changed despite refusal")
	}
	ch, err = UninstallCodex(p, true)
	if err != nil || !ch.Changed {
		t.Fatalf("force: %+v %v", ch, err)
	}
	got := read(t, p.CodexHooks)
	if strings.Contains(got, "hook codex") || !strings.Contains(got, "/x/later") || !strings.Contains(got, "/opt/fake/other") {
		t.Errorf("force result wrong:\n%s", got)
	}
	if gjson.Get(got, "hooks.Stop").Exists() {
		t.Error("empty Stop list should be dropped")
	}
}

func appendToArrayForTest(doc, path, raw string) (string, error) {
	out, err := appendToArray([]byte(doc), path, []byte(raw))
	return string(out), err
}

func TestCodexUninstallOkWhenOursAreLast(t *testing.T) {
	p := newPaths(t)
	orig := fixture(t, "codex_other.json")
	write(t, p.CodexHooks, orig, 0o644)
	if _, err := InstallCodex(p); err != nil {
		t.Fatal(err)
	}
	if ch, err := UninstallCodex(p, false); err != nil || !ch.Changed {
		t.Fatalf("%+v %v", ch, err)
	}
	if got := read(t, p.CodexHooks); got != orig {
		t.Fatalf("not restored:\n%s", got)
	}
}

func TestUpdatesOlderBinaryPath(t *testing.T) {
	p := newPaths(t)
	write(t, p.ClaudeSettings, `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/old/place/rex-tg hook claude","timeout":5}]},{"hooks":[{"type":"command","command":"/old/place/other hook claude"}]}]}}`, 0o644)
	st, _ := Inspect(p)
	if st.Claude.Events["Stop"] != StateOutdated || st.Claude.Events["SessionEnd"] != StateMissing {
		t.Fatalf("inspect: %+v", st.Claude.Events)
	}
	if _, err := InstallClaude(p); err != nil {
		t.Fatal(err)
	}
	got := read(t, p.ClaudeSettings)
	stop := gjson.Get(got, "hooks.Stop").Array()
	if len(stop) != 2 || stop[0].Get("hooks.0.command").String() != testBin+" hook claude" ||
		stop[1].Get("hooks.0.command").String() != "/old/place/other hook claude" {
		t.Fatalf("Stop: %s", gjson.Get(got, "hooks.Stop").Raw)
	}
	if strings.Contains(got, "/old/place/rex-tg") {
		t.Error("old path remains")
	}
}

func TestInvalidJSONRefused(t *testing.T) {
	p := newPaths(t)
	bad := `{"hooks": [oops`
	write(t, p.ClaudeSettings, bad, 0o644)
	if _, err := InstallClaude(p); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("err=%v", err)
	}
	if _, err := UninstallClaude(p); err == nil {
		t.Fatal("uninstall should refuse too")
	}
	if read(t, p.ClaudeSettings) != bad || len(backups(t, p.ClaudeSettings)) != 0 {
		t.Error("file touched")
	}
	r, _ := Inspect(p)
	if r.Claude.Err == "" {
		t.Error("inspect should report the error")
	}
}

func TestFileChangedBetweenReadAndWriteRetries(t *testing.T) {
	p := newPaths(t)
	write(t, p.ClaudeSettings, `{"a":1}`, 0o644)
	n := 0
	p.beforeWrite = func(path string) {
		n++
		if n == 1 {
			write(t, path, `{"a":1,"injected":true}`, 0o644)
		}
	}
	ch, err := InstallClaude(p)
	if err != nil || !ch.Changed {
		t.Fatalf("%+v %v", ch, err)
	}
	got := read(t, p.ClaudeSettings)
	if !gjson.Get(got, "injected").Bool() || !gjson.Get(got, "hooks.Stop").Exists() {
		t.Fatalf("lost write: %s", got)
	}
	if n != 2 {
		t.Errorf("tries=%d", n)
	}
	if b := backups(t, p.ClaudeSettings); len(b) != 1 || read(t, b[0]) != `{"a":1,"injected":true}` {
		t.Errorf("backup should hold the version that was replaced: %v", b)
	}
}

func TestFileKeepsChangingGivesUp(t *testing.T) {
	p := newPaths(t)
	write(t, p.ClaudeSettings, `{}`, 0o644)
	n := 0
	p.beforeWrite = func(path string) {
		n++
		write(t, path, fmt.Sprintf(`{"n":%d}`, n), 0o644)
	}
	if _, err := InstallClaude(p); err == nil {
		t.Fatal("want error")
	}
	if n != 3 {
		t.Errorf("tries=%d", n)
	}
	if strings.Contains(read(t, p.ClaudeSettings), "hook claude") {
		t.Error("wrote anyway")
	}
}

func TestBackupRotationKeepsFive(t *testing.T) {
	p := newPaths(t)
	write(t, p.ClaudeSettings, `{}`, 0o644)
	for i := 1; i <= 7; i++ {
		write(t, fmt.Sprintf("%s%s20250101T00000%d.000000000Z", p.ClaudeSettings, backupInfix, i), "old", 0o644)
	}
	ch, err := InstallClaude(p)
	if err != nil {
		t.Fatal(err)
	}
	b := backups(t, p.ClaudeSettings)
	if len(b) != 5 {
		t.Fatalf("got %d backups: %v", len(b), b)
	}
	found := false
	for _, f := range b {
		if filepath.Base(f) == filepath.Base(ch.Backup) {
			found = true
		}
		if strings.Contains(f, "20250101T00000") && strings.ContainsAny(f[len(f)-12:len(f)-11], "123") {
			t.Errorf("oldest kept: %s", f)
		}
	}
	if !found {
		t.Error("new backup was removed")
	}
	if !strings.Contains(filepath.Base(ch.Backup), ".rex-tg-bak-20261111T0300") {
		t.Errorf("backup name %s", ch.Backup)
	}
}

func TestModePreserved(t *testing.T) {
	p := newPaths(t)
	write(t, p.ClaudeSettings, `{}`, 0o600)
	ch, err := InstallClaude(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{p.ClaudeSettings, ch.Backup} {
		fi, err := os.Stat(f)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v err %v", f, fi.Mode(), err)
		}
	}
}

func TestSymlinkedFileKeepsLink(t *testing.T) {
	p := newPaths(t)
	target := filepath.Join(t.TempDir(), "real.json")
	write(t, target, `{}`, 0o644)
	if err := os.MkdirAll(filepath.Dir(p.ClaudeSettings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p.ClaudeSettings); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallClaude(p); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(p.ClaudeSettings); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink replaced")
	}
	if !strings.Contains(read(t, target), "hook claude") {
		t.Error("target not edited")
	}
}

func TestBinaryPathWithSpaces(t *testing.T) {
	p := newPaths(t)
	p.Binary = "/Users/a b/bin/rex-tg"
	if _, err := InstallClaude(p); err != nil {
		t.Fatal(err)
	}
	want := "'/Users/a b/bin/rex-tg' hook claude"
	if got := gjson.Get(read(t, p.ClaudeSettings), "hooks.Stop.0.hooks.0.command").String(); got != want {
		t.Fatalf("got %q", got)
	}
	if ch, _ := InstallClaude(p); ch.Changed {
		t.Error("second install changed")
	}
	if r, _ := Inspect(p); !r.Claude.Installed() {
		t.Errorf("inspect: %+v", r.Claude)
	}
	if ch, err := UninstallClaude(p); err != nil || !ch.Changed {
		t.Fatalf("%+v %v", ch, err)
	}
	if strings.Contains(read(t, p.ClaudeSettings), "hook claude") {
		t.Error("not removed")
	}
}

func TestPi(t *testing.T) {
	p := newPaths(t)
	p.Binary = `/Users/a b/bin/rex-tg`
	ch, err := InstallPi(p)
	if err != nil || !ch.Changed {
		t.Fatalf("%+v %v", ch, err)
	}
	got := read(t, p.PiExtension)
	if strings.Contains(got, "__REX_TG_BIN__") || !strings.Contains(got, `const REX_TG_BIN: string = "/Users/a b/bin/rex-tg";`) {
		t.Errorf("placeholder not replaced:\n%s", got)
	}
	if want := strings.ReplaceAll(string(extensions.PiExtension), "__REX_TG_BIN__", `"/Users/a b/bin/rex-tg"`); got != want {
		t.Error("content differs from template")
	}
	if fi, _ := os.Stat(p.PiExtension); fi.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", fi.Mode())
	}
	if ch, _ := InstallPi(p); ch.Changed {
		t.Error("second install changed")
	}
	if r, _ := Inspect(p); r.Pi.State != StateCurrent {
		t.Errorf("state %v", r.Pi.State)
	}
	p2 := p
	p2.Binary = "/other/rex-tg"
	if r, _ := Inspect(p2); r.Pi.State != StateOutdated {
		t.Errorf("state %v", r.Pi.State)
	}
	if ch, err := UninstallPi(p); err != nil || !ch.Changed {
		t.Fatalf("%+v %v", ch, err)
	}
	if _, err := os.Stat(p.PiExtension); err == nil {
		t.Error("not removed")
	}
	if _, err := os.Stat(filepath.Dir(p.PiExtension)); err != nil {
		t.Error("folder should stay")
	}
	if ch, _ := UninstallPi(p); ch.Changed {
		t.Error("second uninstall changed")
	}
}

func TestPiExtensionSource(t *testing.T) {
	src := string(extensions.PiExtension)
	for _, want := range []string{"__REX_TG_BIN__", "REX_BLOCK", `"session_start"`, `"session_shutdown"`, `"agent_start"`, `"agent_end"`, "hook_event_name", "timeout: 3000"} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestLaunchd(t *testing.T) {
	p := newPaths(t)
	fr := &fakeRunner{}
	p.Runner = fr.run
	ch, err := InstallLaunchd(p, true)
	if err != nil || !ch.Changed {
		t.Fatalf("%+v %v", ch, err)
	}
	got := read(t, p.LaunchAgent)
	for _, want := range []string{
		"<string>local.rex-tg</string>",
		"<string>" + testBin + "</string>\n\t\t<string>daemon</string>",
		"<key>RunAtLoad</key>\n\t<true/>", "<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>",
		"<key>ThrottleInterval</key>\n\t<integer>30</integer>",
		"<key>ProcessType</key>\n\t<string>Standard</string>",
		"<string>" + filepath.Join(p.LogDir, "launchd.out.log") + "</string>",
		"<string>" + filepath.Join(p.LogDir, "launchd.err.log") + "</string>",
		"<key>HOME</key>\n\t\t<string>" + p.Home + "</string>",
		"<string>/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin</string>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist missing %q:\n%s", want, got)
		}
	}
	wantCalls := []call{
		{"launchctl", "bootout gui/501/local.rex-tg"},
		{"launchctl", "bootstrap gui/501 " + p.LaunchAgent},
	}
	if fmt.Sprint(fr.calls) != fmt.Sprint(wantCalls) {
		t.Errorf("calls %v", fr.calls)
	}
	if _, err := os.Stat(p.LogDir); err != nil {
		t.Error("log dir not created")
	}

	// Unchanged and loaded: no restart.
	fr.calls = nil
	ch, err = InstallLaunchd(p, true)
	if err != nil || ch.Changed || fmt.Sprint(fr.calls) != fmt.Sprint([]call{{"launchctl", "print gui/501/local.rex-tg"}}) {
		t.Fatalf("%+v %v %v", ch, err, fr.calls)
	}
	// Unchanged but not loaded: bootstrap.
	fr.calls = nil
	fr.fail = map[string]error{"print": errors.New("exit status 113")}
	if _, err := InstallLaunchd(p, true); err != nil || len(fr.calls) != 3 || !strings.HasPrefix(fr.calls[2].args, "bootstrap") {
		t.Fatalf("%v %v", err, fr.calls)
	}
	// Without load: no launchctl.
	fr.calls = nil
	if _, err := InstallLaunchd(p, false); err != nil || len(fr.calls) != 0 {
		t.Fatalf("%v %v", err, fr.calls)
	}

	fr.calls = nil
	fr.fail = nil
	if r, _ := Inspect(p); r.Launchd.State != StateCurrent {
		t.Errorf("state %v", r.Launchd.State)
	}
	ch, err = UninstallLaunchd(p, true)
	if err != nil || !ch.Changed || fmt.Sprint(fr.calls) != fmt.Sprint([]call{{"launchctl", "bootout gui/501/local.rex-tg"}}) {
		t.Fatalf("%+v %v %v", ch, err, fr.calls)
	}
	if _, err := os.Stat(p.LaunchAgent); err == nil {
		t.Error("plist remains")
	}
}

func TestLaunchdBootoutNotLoadedIgnored(t *testing.T) {
	p := newPaths(t)
	fr := &fakeRunner{
		fail: map[string]error{"bootout": errors.New("exit status 3")},
		out:  map[string]string{"bootout": "Boot-out failed: 3: No such process"},
	}
	p.Runner = fr.run
	if _, err := InstallLaunchd(p, true); err != nil {
		t.Fatal(err)
	}
	if len(fr.calls) != 2 {
		t.Errorf("calls %v", fr.calls)
	}
	// A real failure is reported.
	fr.fail = map[string]error{"bootstrap": errors.New("exit status 5")}
	fr.out = nil
	os.Remove(p.LaunchAgent)
	if _, err := InstallLaunchd(p, true); err == nil {
		t.Error("want bootstrap error")
	}
}

func TestInstallRequiresAbsoluteBinary(t *testing.T) {
	p := newPaths(t)
	p.Binary = "rex-tg"
	if _, err := InstallClaude(p); err == nil {
		t.Error("want error")
	}
	if _, err := InstallPi(p); err == nil {
		t.Error("want error")
	}
}

func TestLaunchdLogPermissions(t *testing.T) {
	p := newPaths(t)
	p.Runner = (&fakeRunner{}).run
	existing := filepath.Join(p.LogDir, "launchd.err.log")
	// Pre-existing file must be kept untouched.
	if err := os.MkdirAll(p.LogDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, existing, "keep", 0o644)
	if err := os.Chmod(filepath.Dir(p.LogDir), 0o755); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Dir(p.LogDir))
	if err := os.MkdirAll(p.LogDir, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, existing, "keep", 0o644)
	if _, err := InstallLaunchd(p, false); err != nil {
		t.Fatal(err)
	}
	if read(t, existing) != "keep" {
		t.Error("existing log truncated")
	}
	out := filepath.Join(p.LogDir, "launchd.out.log")
	for path, want := range map[string]os.FileMode{out: 0o600, existing: 0o644} {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: %v %v", path, fi, err)
		}
	}
	// Fresh home: folders are 0700.
	q := newPaths(t)
	if _, err := InstallLaunchd(q, false); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Dir(q.LogDir), q.LogDir} {
		if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v %v", d, fi, err)
		}
	}
	if fi, _ := os.Stat(filepath.Join(q.LogDir, "launchd.err.log")); fi.Mode().Perm() != 0o600 {
		t.Errorf("err log mode %v", fi.Mode())
	}
}

// A settings file without a hooks object comes back exactly as it was after
// install and uninstall, for both agents.
func TestRoundTripWithoutHooksKey(t *testing.T) {
	for _, original := range []string{
		"{\n  \"model\": \"x\"\n}\n",
		`{"a":1}`,
	} {
		p := newPaths(t)
		for _, path := range []string{p.ClaudeSettings, p.CodexHooks} {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := InstallClaude(p); err != nil {
			t.Fatal(err)
		}
		if _, err := InstallCodex(p); err != nil {
			t.Fatal(err)
		}
		if _, err := UninstallClaude(p); err != nil {
			t.Fatal(err)
		}
		if _, err := UninstallCodex(p, false); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{p.ClaudeSettings, p.CodexHooks} {
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != original {
				t.Errorf("%s after a round trip = %q, want %q", filepath.Base(path), got, original)
			}
		}
	}
}

// A file with CRLF line ends keeps them: the new lines use CRLF too, and a
// round trip gives the file back as it was.
func TestCRLFFileKeepsItsLineEnds(t *testing.T) {
	p := newPaths(t)
	original := "{\r\n  \"a\": 1\r\n}\r\n"
	if err := os.MkdirAll(filepath.Dir(p.ClaudeSettings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ClaudeSettings, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallClaude(p); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p.ClaudeSettings)
	if strings.Count(string(got), "\n") != strings.Count(string(got), "\r\n") {
		t.Fatalf("mixed line ends after install: %q", got)
	}
	if _, err := UninstallClaude(p); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p.ClaudeSettings); string(got) != original {
		t.Fatalf("after a round trip = %q, want %q", got, original)
	}
}

// A file of the user's at the extension path is backed up before install
// writes over it, and uninstall never removes a file rex-tg did not write.
func TestPiLeavesTheUsersFile(t *testing.T) {
	p := newPaths(t)
	mine := "// my own extension\nexport default function () {}\n"
	write(t, p.PiExtension, mine, 0o644)
	ch, err := InstallPi(p)
	if err != nil || !ch.Changed || ch.Backup == "" {
		t.Fatalf("install = %+v, %v", ch, err)
	}
	if got, _ := os.ReadFile(ch.Backup); string(got) != mine {
		t.Fatalf("backup = %q, want the user's file", got)
	}
	if _, err := UninstallPi(p); err != nil {
		t.Fatalf("uninstall of our file: %v", err)
	}
	write(t, p.PiExtension, mine, 0o644)
	if _, err := UninstallPi(p); !errors.Is(err, ErrNotOurs) {
		t.Fatalf("uninstall of the user's file: err = %v, want ErrNotOurs", err)
	}
	if got, _ := os.ReadFile(p.PiExtension); string(got) != mine {
		t.Fatalf("the user's file changed: %q", got)
	}
}

// With a current rex-tg hook in place, an old one is left as it is: updating
// it would make the same hook run twice.
func TestCurrentHookKeepsOldOneUnchanged(t *testing.T) {
	p := newPaths(t)
	write(t, p.ClaudeSettings, `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/old/place/rex-tg hook claude"}]},{"hooks":[{"type":"command","command":"`+testBin+` hook claude","timeout":5}]}]}}`, 0o644)
	if _, err := InstallClaude(p); err != nil {
		t.Fatal(err)
	}
	stop := gjson.Get(read(t, p.ClaudeSettings), "hooks.Stop").Array()
	if len(stop) != 2 || stop[0].Get("hooks.0.command").String() != "/old/place/rex-tg hook claude" {
		t.Fatalf("Stop: %v", stop)
	}
}
