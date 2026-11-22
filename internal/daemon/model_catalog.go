package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"rex-tg/internal/store"
)

// codexConfigModels uses the home that owns the linked transcript, not the
// daemon's environment (which can belong to a different Codex profile).
func codexConfigModels(conv store.Conversation) []string {
	root := transcriptProfile(conv.Transcript, "sessions", "archived_sessions")
	if root == "" {
		return nil
	}
	var settings struct {
		Catalog string `toml:"model_catalog_json"`
	}
	path := filepath.Join(root, "models_cache.json")
	if b, err := readCatalogFile(filepath.Join(root, "config.toml")); err == nil {
		if _, err := toml.Decode(string(b), &settings); err != nil {
			return nil
		}
		if settings.Catalog != "" {
			path = settings.Catalog
			if !filepath.IsAbs(path) {
				path = filepath.Join(root, path)
			}
		}
	} else if !os.IsNotExist(err) {
		return nil
	}
	var catalog struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	b, err := readCatalogFile(path)
	if err != nil || json.Unmarshal(b, &catalog) != nil {
		return nil
	}
	var out []string
	for _, m := range catalog.Models {
		if m.Visibility == "list" {
			out = append(out, m.Slug)
		}
	}
	return out
}

func transcriptProfile(path string, markers ...string) string {
	if !filepath.IsAbs(path) {
		return ""
	}
	for dir := filepath.Dir(path); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		for _, marker := range markers {
			if filepath.Base(dir) == marker {
				return filepath.Dir(dir)
			}
		}
	}
	return ""
}

// claudeConfigModels reads configured choices rather than guessing which
// aliases the account enables. A managed allowlist overrides personal lists.
func claudeConfigModels(conv store.Conversation, managed string) []string {
	read := func(path string) ([]string, bool, bool) {
		var cfg struct {
			Models *[]string `json:"availableModels"`
		}
		b, err := readCatalogFile(path)
		if os.IsNotExist(err) {
			return nil, false, true
		}
		if err != nil || json.Unmarshal(b, &cfg) != nil {
			return nil, false, false
		}
		if cfg.Models == nil {
			return nil, false, true
		}
		return *cfg.Models, true, true
	}
	if models, found, ok := read(managed); !ok || found {
		return models
	}
	var out []string
	var paths []string
	if profile := transcriptProfile(conv.Transcript, "projects"); profile != "" {
		paths = append(paths, filepath.Join(profile, "settings.json"))
	}
	if cwd := conv.CWD; filepath.IsAbs(cwd) {
		paths = append(paths, filepath.Join(cwd, ".claude", "settings.json"), filepath.Join(cwd, ".claude", "settings.local.json"))
	}
	for _, path := range paths {
		models, _, ok := read(path)
		if !ok {
			return nil
		}
		for _, model := range models {
			if strings.TrimSpace(model) != "" {
				out = append(out, model)
			}
		}
	}
	return out
}
