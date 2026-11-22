package daemon

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"rex-tg/internal/store"
)

func writeCatalogFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCodexConfigModelsUseSessionProfile(t *testing.T) {
	root := t.TempDir()
	profile := filepath.Join(root, "custom-profile")
	conv := store.Conversation{Transcript: filepath.Join(profile, "sessions", "2026", "run.jsonl")}
	writeCatalogFixture(t, filepath.Join(profile, "models_cache.json"), `{"models":[{"slug":"cached","visibility":"list"}]}`)
	if got := codexConfigModels(conv); !slices.Equal(got, []string{"cached"}) {
		t.Fatalf("profile cache: %v", got)
	}
	writeCatalogFixture(t, filepath.Join(profile, "config.toml"), `model_catalog_json = "custom.json"`)
	writeCatalogFixture(t, filepath.Join(profile, "custom.json"), `{"models":[{"slug":"configured","visibility":"list"},{"slug":"hidden","visibility":"hide"}]}`)
	if got := codexConfigModels(conv); !slices.Equal(got, []string{"configured"}) {
		t.Fatalf("configured catalog: %v", got)
	}
	if err := os.Remove(filepath.Join(profile, "custom.json")); err != nil {
		t.Fatal(err)
	}
	if got := codexConfigModels(conv); len(got) != 0 {
		t.Fatalf("must not fall back to cache when explicit catalog is missing: %v", got)
	}
}

func TestClaudeConfigModelsUseConfiguredAllowlist(t *testing.T) {
	root := t.TempDir()
	profile := filepath.Join(root, "claude-profile")
	cwd := filepath.Join(root, "repo")
	managed := filepath.Join(root, "managed-settings.json")
	conv := store.Conversation{CWD: cwd, Transcript: filepath.Join(profile, "projects", "cwd", "run.jsonl")}
	if got := claudeConfigModels(conv, managed); len(got) != 0 {
		t.Fatalf("unconfigured aliases must not be guessed: %v", got)
	}
	writeCatalogFixture(t, filepath.Join(profile, "settings.json"), `{"availableModels":["sonnet"]}`)
	writeCatalogFixture(t, filepath.Join(cwd, ".claude", "settings.local.json"), `{"availableModels":["haiku"]}`)
	if got := claudeConfigModels(conv, managed); !slices.Equal(got, []string{"sonnet", "haiku"}) {
		t.Fatalf("user and project scopes: %v", got)
	}
	writeCatalogFixture(t, managed, `{"availableModels":["opus"]}`)
	if got := claudeConfigModels(conv, managed); !slices.Equal(got, []string{"opus"}) {
		t.Fatalf("managed allowlist: %v", got)
	}
	writeCatalogFixture(t, managed, `{"availableModels":[]}`)
	if got := claudeConfigModels(conv, managed); len(got) != 0 {
		t.Fatalf("empty managed allowlist must not fall back: %v", got)
	}
}
