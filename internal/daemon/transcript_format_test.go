package daemon

import (
	"testing"

	"rex-tg/internal/config"
	"rex-tg/internal/store"
)

func TestMarkdownPartsUseTheConsistentHTMLRenderer(t *testing.T) {
	cfg := config.Default(t.TempDir())
	cfg.Display.RichMessages = true // legacy config must not select a different visual format
	d := &daemon{cfg: cfg}

	parts := d.markdownParts("**Bold** and `code`", "answer")
	if len(parts) != 1 || parts[0].Format != store.FormatHTML || parts[0].Content != "<b>Bold</b> and <code>code</code>" {
		t.Fatalf("Markdown parts = %+v; want the shared HTML rendering", parts)
	}
}
