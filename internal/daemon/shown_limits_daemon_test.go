package daemon_test

import (
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

// Only the last turn_images images come, and the files are private.
func TestShownImagesKeepTheLastN(t *testing.T) {
	e, p, thread := piTopicCfg(t, "pi-shown-n", func(e *env) { e.cfg.Display.TurnImages = 2 })
	p.User("take screenshots")
	for i, c := range []color.RGBA{{R: 10, A: 255}, {R: 20, A: 255}, {R: 30, A: 255}} {
		id := string(rune('a' + i))
		p.ToolCall(id, "shot")
		p.ToolResultImage(id, "image/png", tinyPNG(t, c))
	}
	p.Final("Done with the screenshots")
	e.waitFor("the photos", func() bool { return e.photoCount(thread) >= 2 })
	e.marker(p, thread, "n")
	if n := e.photoCount(thread); n != 2 {
		t.Fatalf("%d photos, want the last 2", n)
	}
	dir := e.cfg.ShownDir()
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("shown dir: %v %v, want mode 0700", fi, err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.png"))
	if len(files) == 0 {
		t.Fatal("no image files in the shown folder")
	}
	for _, f := range files {
		if fi, err := os.Stat(f); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v, want mode 0600", f, fi, err)
		}
	}
}

// turn_images = 0 and send_images = false turn the photos off.
func TestShownImagesCanBeTurnedOff(t *testing.T) {
	for name, tweak := range map[string]func(*env){
		"turn_images 0":     func(e *env) { e.cfg.Display.TurnImages = 0 },
		"send_images false": func(e *env) { e.cfg.Display.SendImages = false },
	} {
		t.Run(name, func(t *testing.T) {
			e, p, thread := piTopicCfg(t, "pi-shown-off", tweak)
			p.User("take a screenshot")
			p.ToolCall("c1", "shot")
			p.ToolResultImage("c1", "image/png", tinyPNG(t, color.RGBA{R: 1, A: 255}))
			p.Final("Here is the screenshot")
			e.waitText(thread, "Here is the screenshot")
			e.marker(p, thread, "off")
			if n := e.photoCount(thread); n != 0 {
				t.Fatalf("%d photos, want none", n)
			}
		})
	}
}
