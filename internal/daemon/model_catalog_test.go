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
	defaults := []string{"opus", "sonnet", "haiku", "fable"}
	if got := claudeModelChoices(conv, managed, defaults); !slices.Equal(got, defaults) {
		t.Fatalf("built-in aliases: %v", got)
	}
	writeCatalogFixture(t, filepath.Join(profile, "settings.json"), `{"availableModels":["sonnet"]}`)
	writeCatalogFixture(t, filepath.Join(cwd, ".claude", "settings.local.json"), `{"availableModels":["haiku"]}`)
	if got := claudeModelChoices(conv, managed, defaults); !slices.Equal(got, []string{"sonnet", "haiku"}) {
		t.Fatalf("user and project scopes: %v", got)
	}
	writeCatalogFixture(t, managed, `{"availableModels":["opus"]}`)
	if got := claudeModelChoices(conv, managed, defaults); !slices.Equal(got, []string{"opus"}) {
		t.Fatalf("managed allowlist: %v", got)
	}
	writeCatalogFixture(t, managed, `{"availableModels":[]}`)
	if got := claudeModelChoices(conv, managed, defaults); len(got) != 0 {
		t.Fatalf("empty managed allowlist must not fall back: %v", got)
	}
}

func TestClaudeModelPickerControlsConfigChoices(t *testing.T) {
	root := t.TempDir()
	profile := filepath.Join(root, "claude-profile")
	conv := store.Conversation{Transcript: filepath.Join(profile, "projects", "cwd", "run.jsonl")}
	managed := filepath.Join(root, "managed-settings.json")
	defaults := []string{"opus", "sonnet", "haiku", "fable"}
	settings := filepath.Join(profile, "settings.json")
	writeCatalogFixture(t, settings, `{"modelPicker":{"options":[{"model":"custom-model"}]}}`)
	if got, want := claudeModelChoices(conv, managed, defaults), []string{"opus", "sonnet", "haiku", "fable", "custom-model"}; !slices.Equal(got, want) {
		t.Fatalf("additive modelPicker: %v, want %v", got, want)
	}
	writeCatalogFixture(t, settings, `{"modelPicker":{"options":[{"model":"custom-model"}],"replaceBuiltInOptions":true}}`)
	if got := claudeModelChoices(conv, managed, defaults); !slices.Equal(got, []string{"custom-model"}) {
		t.Fatalf("replacement modelPicker: %v", got)
	}
	writeCatalogFixture(t, managed, `{"modelPicker":{"options":[{"model":"managed-model"}],"replaceBuiltInOptions":true}}`)
	if got := claudeModelChoices(conv, managed, defaults); !slices.Equal(got, []string{"managed-model"}) {
		t.Fatalf("managed modelPicker: %v", got)
	}
	writeCatalogFixture(t, settings, `{"modelPicker":{"options":[{"model":"custom-model"},{"model":"not-allowed"}],"replaceBuiltInOptions":true}}`)
	writeCatalogFixture(t, managed, `{"availableModels":["custom-model"]}`)
	if got := claudeModelChoices(conv, managed, defaults); !slices.Equal(got, []string{"custom-model"}) {
		t.Fatalf("managed allowlist must filter picker options: %v", got)
	}
}
