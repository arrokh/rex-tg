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
		if m.Visibility == "list" && strings.TrimSpace(m.Slug) != "" {
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

type claudePickerOption struct {
	Model string `json:"model"`
}

type claudeModelPicker struct {
	Options               []claudePickerOption `json:"options"`
	ReplaceBuiltInOptions bool                 `json:"replaceBuiltInOptions"`
}

type claudeModelSettings struct {
	AvailableModels *[]string          `json:"availableModels"`
	ModelPicker     *claudeModelPicker `json:"modelPicker"`
}

// claudeModelChoices returns explicit Claude settings when available, and the
// adapter's built-in aliases otherwise. Empty configured lists remain empty.
func claudeModelChoices(conv store.Conversation, managed string, defaults []string) []string {
	models, configured := claudeConfigModels(conv, managed, defaults)
	if !configured {
		return append([]string(nil), defaults...)
	}
	return models
}

// claudeConfigModels resolves managed and session-local settings. availableModels
// remains an allowlist; modelPicker supplies the ordered picker lineup.
func claudeConfigModels(conv store.Conversation, managed string, defaults []string) ([]string, bool) {
	read := func(path string) (claudeModelSettings, bool, bool) {
		var cfg claudeModelSettings
		b, err := readCatalogFile(path)
		if os.IsNotExist(err) {
			return cfg, false, true
		}
		if err != nil || json.Unmarshal(b, &cfg) != nil {
			return cfg, false, false
		}
		return cfg, true, true
	}
	managedCfg, _, ok := read(managed)
	if !ok {
		return nil, true
	}

	var allowed []string
	allowedFound := false
	if managedCfg.AvailableModels != nil {
		allowed = append(allowed, (*managedCfg.AvailableModels)...)
		allowedFound = true
	}

	var userCfg claudeModelSettings
	var paths []struct {
		path string
		user bool
	}
	if profile := transcriptProfile(conv.Transcript, "projects"); profile != "" {
		paths = append(paths, struct {
			path string
			user bool
		}{filepath.Join(profile, "settings.json"), true})
	}
	if filepath.IsAbs(conv.CWD) {
		paths = append(paths,
			struct {
				path string
				user bool
			}{filepath.Join(conv.CWD, ".claude", "settings.json"), false},
			struct {
				path string
				user bool
			}{filepath.Join(conv.CWD, ".claude", "settings.local.json"), false},
		)
	}
	for _, source := range paths {
		cfg, _, ok := read(source.path)
		if !ok {
			return nil, true
		}
		if source.user {
			userCfg = cfg
		}
		if cfg.AvailableModels != nil && managedCfg.AvailableModels == nil {
			allowed = append(allowed, (*cfg.AvailableModels)...)
			allowedFound = true
		}
	}

	picker := managedCfg.ModelPicker
	if picker == nil {
		picker = userCfg.ModelPicker
	}
	if picker != nil {
		choices := claudePickerChoices(*picker, defaults)
		if allowedFound {
			choices = filterClaudeAllowed(choices, allowed)
		}
		return choices, true
	}
	if allowedFound {
		return cleanModelNames(allowed), true
	}
	return nil, false
}

func claudePickerChoices(picker claudeModelPicker, defaults []string) []string {
	var choices []string
	if !picker.ReplaceBuiltInOptions {
		choices = append(choices, defaults...)
	}
	for _, option := range picker.Options {
		choices = append(choices, option.Model)
	}
	return cleanModelNames(choices)
}

func cleanModelNames(models []string) []string {
	out := make([]string, 0, len(models))
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		key := strings.ToLower(model)
		if model != "" && !seen[key] {
			out = append(out, model)
			seen[key] = true
		}
	}
	return out
}

func filterClaudeAllowed(choices, allowed []string) []string {
	set := make(map[string]bool, len(allowed))
	for _, model := range allowed {
		if model = strings.TrimSpace(model); model != "" {
			set[strings.ToLower(model)] = true
		}
	}
	out := make([]string, 0, len(choices))
	for _, model := range choices {
		if set[strings.ToLower(model)] {
			out = append(out, model)
		}
	}
	return out
}
