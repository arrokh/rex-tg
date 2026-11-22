package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

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
