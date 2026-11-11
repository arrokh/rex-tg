package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/daemon"
	"rex-tg/internal/rex/rextest"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
	"rex-tg/internal/telegram/tgtest"
)

func TestVersionAndHelp(t *testing.T) {
	te := newTestEnv(t)
	if code := te.run("version"); code != 0 || strings.TrimSpace(te.stdout.String()) != "rex-tg dev" {
		t.Fatalf("version: %d %q", code, te.stdout)
	}
	if code := te.run("help"); code != 0 || !strings.Contains(te.stdout.String(), "doctor") {
		t.Fatalf("help: %d %q", code, te.stdout)
	}
	if code := te.run("bogus"); code != 2 || te.stderr.Len() == 0 {
		t.Fatalf("bogus: %d %q", code, te.stderr)
	}
}

func TestLoginTokenStdin(t *testing.T) {
	srv := tgtest.New(t)
	te := newTestEnv(t)
	te.TelegramBase = srv.URL()
	te.Stdin = strings.NewReader(srv.Token() + "\n")
	if code := te.run("login", "--token-stdin"); code != 0 {
		t.Fatalf("exit %d: %s", code, te.stderr)
	}
	if te.storedToken() != srv.Token() {
		t.Errorf("stored token = %q", te.storedToken())
	}
	if strings.Contains(te.securityArgv(), srv.Token()) {
		t.Error("token in security argv")
	}
	out := te.stdout.String()
	if !strings.Contains(out, "Bot: @") || !strings.Contains(out, "Threaded Mode: on") || !strings.Contains(out, "Send /start ABC-123 to @") || !strings.Contains(out, "within 10 minutes") {
		t.Errorf("output:\n%s", out)
	}
	if strings.Contains(out, srv.Token()) {
		t.Error("token printed")
	}
}

func TestLoginThreadedModeOff(t *testing.T) {
	srv := tgtest.New(t)
	srv.SetTopicsEnabled(false)
	te := newTestEnv(t)
	te.TelegramBase = srv.URL()
	te.Stdin = strings.NewReader(srv.Token() + "\n")
	if code := te.run("login", "--token-stdin"); code != 0 {
		t.Fatalf("exit %d: %s", code, te.stderr)
	}
	if !strings.Contains(te.stdout.String(), "@BotFather") || !strings.Contains(te.stdout.String(), "OFF") {
		t.Errorf("output:\n%s", te.stdout)
	}
}

func TestLoginRejectsBadToken(t *testing.T) {
	srv := tgtest.New(t)
	te := newTestEnv(t)
	te.TelegramBase = srv.URL()
	te.Stdin = strings.NewReader("123:wrong\n")
	if code := te.run("login", "--token-stdin"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if te.storedToken() != "" {
		t.Error("bad token stored")
	}
	if strings.Contains(te.stderr.String(), "123:wrong") {
		t.Error("token in error output")
	}
}

func TestLoginInteractive(t *testing.T) {
	srv := tgtest.New(t)
	te := newTestEnv(t)
	te.TelegramBase = srv.URL()
	te.Interactive = realSecurityInteractive(srv.Token() + "\n")
	if code := te.run("login"); code != 0 {
		t.Fatalf("exit %d: %s", code, te.stderr)
	}
	if te.storedToken() != srv.Token() {
		t.Errorf("stored token = %q", te.storedToken())
	}
	if !strings.Contains(te.securityArgv(), "add-generic-password -U -s rex-tg -a bot-token -w") {
		t.Errorf("argv:\n%s", te.securityArgv())
	}
}

func TestPair(t *testing.T) {
	srv := tgtest.New(t)
	te := newTestEnv(t)
	te.TelegramBase = srv.URL()
	if err := te.Env.keychain().Set(context.Background(), "rex-tg", "bot-token", srv.Token()); err != nil {
		t.Fatal(err)
	}
	if code := te.run("pair"); code != 0 {
		t.Fatalf("exit %d: %s", code, te.stderr)
	}
	out := te.stdout.String()
	if !strings.HasPrefix(out, "Send /start ") || !strings.Contains(out, " to @") || !strings.Contains(out, "within 10 minutes.") {
		t.Errorf("output %q", out)
	}
}

func TestPairBotNameFallbacks(t *testing.T) {
	te := newTestEnv(t)
	if code := te.run("pair"); code != 0 {
		t.Fatalf("exit %d: %s", code, te.stderr)
	}
	if !strings.Contains(te.stdout.String(), " to your bot within 10 minutes.") {
		t.Errorf("output %q", te.stdout)
	}
	st, err := store.Open(filepath.Join(te.stateDir(), "rex-tg.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Set(context.Background(), "bot_username", "kvbot"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if code := te.run("pair"); code != 0 {
		t.Fatal(code)
	}
	if !strings.Contains(te.stdout.String(), " to @kvbot within") {
		t.Errorf("output %q", te.stdout)
	}
}

func TestInstallUninstall(t *testing.T) {
	te := newTestEnv(t)
	if code := te.run("install"); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, te.stdout, te.stderr)
	}
	out := te.stdout.String()
	for _, w := range []string{"claude", "codex", "pi", "launchd", "trust the new hook entries"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in\n%s", w, out)
		}
	}
	for _, f := range []string{
		".claude/settings.json", ".codex/hooks.json", ".pi/agent/extensions/rex-tg.ts", "Library/LaunchAgents/local.rex-tg.plist",
	} {
		b, err := os.ReadFile(filepath.Join(te.home, f))
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if !strings.Contains(string(b), te.exe) {
			t.Errorf("%s does not mention %s", f, te.exe)
		}
	}
	joined := strings.Join(te.runner.all(), "\n")
	if !strings.Contains(joined, "launchctl bootstrap gui/501") {
		t.Errorf("launchctl calls:\n%s", joined)
	}

	// Uninstall removes it all.
	if code := te.run("uninstall"); code != 0 {
		t.Fatalf("uninstall exit %d\n%s\n%s", code, te.stdout, te.stderr)
	}
	for _, f := range []string{".pi/agent/extensions/rex-tg.ts", "Library/LaunchAgents/local.rex-tg.plist"} {
		if _, err := os.Stat(filepath.Join(te.home, f)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists", f)
		}
	}
	b, _ := os.ReadFile(filepath.Join(te.home, ".claude/settings.json"))
	if strings.Contains(string(b), "rex-tg hook") {
		t.Errorf("claude hooks remain: %s", b)
	}
}

func TestInstallOnlyAndNoLoad(t *testing.T) {
	te := newTestEnv(t)
	if code := te.run("install", "--only", "pi", "--no-load"); code != 0 {
		t.Fatalf("exit %d: %s", code, te.stderr)
	}
	if _, err := os.Stat(filepath.Join(te.home, ".claude/settings.json")); err == nil {
		t.Error("claude settings written with --only pi")
	}
	if _, err := os.Stat(filepath.Join(te.home, ".pi/agent/extensions/rex-tg.ts")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(te.home, "Library/LaunchAgents/local.rex-tg.plist")); err != nil {
		t.Error("launchd agent not installed with --only")
	}
	for _, c := range te.runner.all() {
		if strings.Contains(c, "bootstrap") {
			t.Errorf("--no-load ran %q", c)
		}
	}
	if strings.Contains(te.stdout.String(), "Codex") {
		t.Error("codex reminder with --only pi")
	}
	if code := te.run("install", "--only", "vim"); code != 2 {
		t.Errorf("bad --only exit %d", code)
	}
	// --only on uninstall keeps launchd.
	te.run("uninstall", "--only", "pi")
	if _, err := os.Stat(filepath.Join(te.home, "Library/LaunchAgents/local.rex-tg.plist")); err != nil {
		t.Error("uninstall --only removed launchd agent")
	}
}

func TestUninstallWouldShiftTrust(t *testing.T) {
	te := newTestEnv(t)
	if code := te.run("install", "--only", "codex", "--no-load"); code != 0 {
		t.Fatal(te.stderr.String())
	}
	path := filepath.Join(te.home, ".codex/hooks.json")
	b, _ := os.ReadFile(path)
	// Add a foreign entry after rex-tg's in every event list.
	var doc map[string]any
	if err := jsonUnmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	hooks := doc["hooks"].(map[string]any)
	for ev, v := range hooks {
		list := v.([]any)
		list = append(list, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "/other/tool"}}})
		hooks[ev] = list
	}
	nb, _ := jsonMarshal(doc)
	if err := os.WriteFile(path, nb, 0o644); err != nil {
		t.Fatal(err)
	}
	if code := te.run("uninstall", "--only", "codex"); code != 1 {
		t.Fatalf("exit %d\n%s", code, te.stdout)
	}
	if !strings.Contains(te.stdout.String(), "NOT removed") || !strings.Contains(te.stdout.String(), "--force") {
		t.Errorf("output:\n%s", te.stdout)
	}
	if code := te.run("uninstall", "--only", "codex", "--force"); code != 0 {
		t.Fatalf("force exit %d\n%s", code, te.stdout)
	}
}

func TestStatus(t *testing.T) {
	te := newTestEnv(t)
	ctx := context.Background()
	if code := te.run("status"); code != 1 {
		t.Fatalf("no database: exit %d", code)
	}
	st, err := store.Open(filepath.Join(te.stateDir(), "rex-tg.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	conv, err := st.UpsertConversation(ctx, store.Conversation{Runtime: "claude", NativeID: "s1", Transcript: "/t.jsonl", FirstSeenAt: now})
	if err != nil {
		t.Fatal(err)
	}
	th, err := st.LinkTopic(ctx, 1, 10, conv.ID, "my-project", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertInbound(ctx, store.Inbound{UpdateID: 1, ChatID: 1, MessageThreadID: 10, TGMessageID: 5, FromID: 2, Kind: "text", Body: "SECRET-MESSAGE-TEXT", ThreadID: th.ID, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAlert(ctx, store.Alert{ThreadID: th.ID, Kind: store.AlertPermission, ScreenHash: "h", State: store.AlertOpen, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	id, _, err := st.Enqueue(ctx, store.OutboxItem{ThreadID: th.ID, Key: "k", Kind: "final", Parts: []store.PartContent{{Content: "SECRET-OUTBOX-TEXT", Format: store.FormatPlain}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkPartUnknown(ctx, id, 0); err != nil {
		t.Fatal(err)
	}
	st.Close()

	if code := te.run("status"); code != 0 {
		t.Fatalf("exit %d: %s", code, te.stderr)
	}
	out := te.stdout.String()
	for _, w := range []string{"live", "claude", "my-project", "queued=1", "Unknown parts: 1", "Open alerts: 1", "Queued inbound messages: 1"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in\n%s", w, out)
		}
	}
	if strings.Contains(out, "SECRET") {
		t.Errorf("message text printed:\n%s", out)
	}
}

func TestDoctor(t *testing.T) {
	srv := tgtest.New(t)
	fx := rextest.New(t)
	fx.On([]string{"whoami"}, `{"account":"a","rex_version":"1.2","session_count":2}`, 0)
	te := newTestEnv(t)
	te.TelegramBase = srv.URL()
	te.writeConfig("[rex]\ncli = \"" + fx.Bin() + "\"\n")
	ctx := context.Background()

	// Everything missing: failures.
	if code := te.run("doctor"); code != 1 {
		t.Fatalf("empty setup exit %d\n%s", code, te.stdout)
	}
	if !strings.Contains(te.stdout.String(), "✗ bot token") {
		t.Errorf("output:\n%s", te.stdout)
	}

	// Set everything up.
	if err := te.Env.keychain().Set(ctx, "rex-tg", "bot-token", srv.Token()); err != nil {
		t.Fatal(err)
	}
	if code := te.run("install"); code != 0 {
		t.Fatalf("install: %s", te.stdout)
	}
	st, err := store.Open(filepath.Join(te.stateDir(), "rex-tg.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Set(ctx, "owner_user_id", "42"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if code := te.run("doctor"); !strings.Contains(te.stdout.String(), "✓ transcript formats: no unknown line types") {
		t.Fatalf("exit %d\n%s", code, te.stdout)
	}
	st, _ = store.Open(filepath.Join(te.stateDir(), "rex-tg.db"))
	_ = st.Set(ctx, "unknown_line_types", `{"claude":{"foo":12,"bar":1}}`)
	st.Close()
	te.run("doctor")
	if !strings.Contains(te.stdout.String(), "! claude: 2 unknown line types (foo ×12, bar ×1)") {
		t.Fatalf("unknown types line missing:\n%s", te.stdout)
	}
	st, _ = store.Open(filepath.Join(te.stateDir(), "rex-tg.db"))
	_ = st.Delete(ctx, "unknown_line_types")
	st.Close()
	if err := os.MkdirAll(filepath.Join(te.stateDir(), "spool"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(te.stateDir(), 0o700); err != nil {
		t.Fatal(err)
	}

	// The command menu: an older list for all private chats hides the
	// default list until the daemon sets one for the owner's chat.
	st, _ = store.Open(filepath.Join(te.stateDir(), "rex-tg.db"))
	_ = st.Set(ctx, "owner_chat_id", "42")
	st.Close()
	srv.SetCommandsFor(telegram.BotCommandScope{Type: "all_private_chats"}, []telegram.BotCommand{{Command: "sessions", Description: "old"}})
	te.run("doctor")
	if !strings.Contains(te.stdout.String(), "! command menu shows 1 other commands (list for all private chats: /sessions)") {
		t.Fatalf("menu line missing:\n%s", te.stdout)
	}
	srv.SetCommandsFor(telegram.BotCommandScope{Type: "chat", ChatID: 42}, daemon.BotCommands())

	if code := te.run("doctor"); code != 0 {
		t.Fatalf("exit %d\n%s", code, te.stdout)
	}
	out := te.stdout.String()
	for _, w := range []string{"✓ command menu has the", "✓ Rex CLI works", "✓ bot token", "Threaded Mode is on", "✓ owner is paired", "✓ Claude hooks installed", "✓ Codex hooks installed", "✓ pi extension is current", "✓ launchd agent local.rex-tg is loaded", "✓ database opens", "mode 0700", "! no daemon is running"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in\n%s", w, out)
		}
	}
	if strings.Contains(out, "✗") {
		t.Errorf("unexpected failure:\n%s", out)
	}

	// Failures: launchd not loaded, bad folder mode, topics off.
	te.runner.fail = func(c string) bool { return strings.HasPrefix(c, "launchctl print") }
	if err := os.Chmod(filepath.Join(te.stateDir(), "spool"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv.SetTopicsEnabled(false)
	if code := te.run("doctor"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	out = te.stdout.String()
	for _, w := range []string{"✗ launchd agent", "✗ folder", "✗ @", "print gui/501/local.rex-tg"} {
		if w == "print gui/501/local.rex-tg" {
			if !strings.Contains(strings.Join(te.runner.all(), "\n"), w) {
				t.Errorf("launchctl call missing")
			}
			continue
		}
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in\n%s", w, out)
		}
	}
}

func TestDoctorDetectsRunningDaemon(t *testing.T) {
	te := newTestEnv(t)
	te.writeConfig("")
	if err := os.MkdirAll(te.stateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(te.stateDir(), "rex-tg.lock")
	held, err := lockHeld(lock)
	if err != nil || held {
		t.Fatalf("absent lock: %v %v", held, err)
	}
	f, err := os.Create(lock)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if held, err := lockHeld(lock); err != nil || held {
		t.Fatalf("free lock: %v %v", held, err)
	}
	if err := flockForTest(f); err != nil {
		t.Fatal(err)
	}
	if held, err := lockHeld(lock); err != nil || !held {
		t.Fatalf("held lock: %v %v", held, err)
	}
}

func TestDaemonExitCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"already running", daemon.ErrAlreadyRunning, 3},
		{"wrapped already running", errors.Join(errors.New("x"), daemon.ErrAlreadyRunning), 3},
		{"failure", errors.New("boom"), 1},
		{"clean", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			te := newTestEnv(t)
			if err := te.Env.keychain().Set(context.Background(), "rex-tg", "bot-token", "123:tok"); err != nil {
				t.Fatal(err)
			}
			var got daemon.Options
			te.RunDaemon = func(_ context.Context, opt daemon.Options) error {
				got = opt
				return tc.err
			}
			if code := te.run("daemon", "--log-stderr"); code != tc.want {
				t.Fatalf("exit %d, want %d\n%s", code, tc.want, te.stderr)
			}
			if got.Token != "123:tok" || got.Home != te.home || got.Logger == nil {
				t.Errorf("options = %+v", got)
			}
			if _, err := os.Stat(filepath.Join(te.stateDir(), "logs", "rex-tg.log")); err != nil {
				t.Errorf("log file: %v", err)
			}
			if strings.Contains(te.stderr.String(), "123:tok") {
				t.Error("token logged")
			}
		})
	}
}

func TestDaemonWithoutToken(t *testing.T) {
	te := newTestEnv(t)
	te.RunDaemon = func(context.Context, daemon.Options) error {
		t.Error("daemon started without a token")
		return nil
	}
	if code := te.run("daemon"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(te.stderr.String(), "rex-tg login") {
		t.Errorf("stderr: %q", te.stderr)
	}
}
