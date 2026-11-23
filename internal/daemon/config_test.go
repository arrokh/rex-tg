package daemon

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"rex-tg/internal/agents"
	"rex-tg/internal/agents/claude"
	"rex-tg/internal/agents/codex"
	"rex-tg/internal/agents/pi"
	"rex-tg/internal/config"
	"rex-tg/internal/store"
)

func TestConfigControlUsesPiAndCodexSessionCatalogs(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default(root)
	d := &daemon{
		cfg:  cfg,
		home: root,
		adapters: map[agents.Runtime]agents.Adapter{
			agents.Pi:     pi.New(),
			agents.Codex:  codex.New(),
			agents.Claude: claude.New(),
		},
	}
	if err := os.MkdirAll(filepath.Join(cfg.LiveDir(), "models"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.LiveDir(), "models", "session-a.json"), []byte(`["scoped/model"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	piControl, _, ok := d.configControl(store.Conversation{Runtime: agents.Pi, NativeID: "session-a"}, "model")
	if !ok || piControl.Free || !slices.Equal(piControl.Choices, []string{"scoped/model"}) {
		t.Fatalf("Pi Config choices = %+v, ok %v", piControl, ok)
	}

	profile := filepath.Join(root, "codex-profile")
	transcript := filepath.Join(profile, "sessions", "2026", "run.jsonl")
	writeCatalogFixture(t, filepath.Join(profile, "models_cache.json"), `{"models":[{"slug":"gpt-current","visibility":"list"},{"slug":"gpt-hidden","visibility":"hide"}]}`)
	codexControl, _, ok := d.configControl(store.Conversation{Runtime: agents.Codex, Transcript: transcript}, "model")
	if !ok || codexControl.Free || !slices.Equal(codexControl.Choices, []string{"gpt-current"}) {
		t.Fatalf("Codex Config choices = %+v, ok %v", codexControl, ok)
	}
}

func TestPiConfigModelsAreScopedToSession(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "models"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models", "session-a.json"), []byte(`["provider/model"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := piConfigModels(dir, "session-a"); len(got) != 1 || got[0] != "provider/model" {
		t.Fatalf("session catalog = %v", got)
	}
	for _, id := range []string{"session-b", "../models/session-a", ""} {
		if got := piConfigModels(dir, id); len(got) != 0 {
			t.Errorf("unexpected catalog for %q: %v", id, got)
		}
	}
}
