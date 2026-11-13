package daemon_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// tinyPNG makes a small PNG of one colour, built here (no real screenshot).
func tinyPNG(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for x := 0; x < 2; x++ {
		for y := 0; y < 2; y++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func (e *env) photoCount(thread int64) int {
	n := 0
	for _, m := range e.botMessages(thread) {
		if m.Photo {
			n++
		}
	}
	return n
}

// finish ends the test's turn with a marker answer, so a photo that would
// follow the earlier answer has arrived before the marker does.
func (e *env) marker(p *piLog, thread int64, word string) {
	p.User("next " + word)
	p.Final("marker " + word)
	e.waitText(thread, "marker "+word)
}

// An image that pi looked at with a tool, and an answer that names no file:
// the answer comes, then the image as one silent photo.
func TestShownImageComesWithAnAnswerThatNamesNoFile(t *testing.T) {
	e, p, thread := piTopic(t, "pi-shown")
	img := tinyPNG(t, color.RGBA{R: 200, A: 255})
	p.User("take a screenshot")
	p.ToolCall("c1", "shot")
	p.ToolResultImage("c1", "image/png", img)
	p.ToolCall("c2", "shot again")
	p.ToolResultImage("c2", "image/png", img) // the same image again
	p.Final("Here is the screenshot")
	answer := e.waitText(thread, "Here is the screenshot")
	e.waitFor("the photo", func() bool { return e.photoCount(thread) > 0 })
	e.marker(p, thread, "one")
	var ids []int64
	for _, m := range e.botMessages(thread) {
		if m.Photo {
			if !m.Silent {
				t.Errorf("photo is not silent: %+v", m)
			}
			ids = append(ids, m.ID)
		}
	}
	if len(ids) != 1 || ids[0] < answer.ID {
		t.Fatalf("photos %v, answer %d: want one photo after the answer", ids, answer.ID)
	}
}

// When the answer names an image file, only that file comes.
func TestNamedImageWinsOverShownImages(t *testing.T) {
	e, p, thread := piTopic(t, "pi-shown-named")
	path := filepath.Join(t.TempDir(), "named-shot.png")
	if err := os.WriteFile(path, tinyPNG(t, color.RGBA{G: 200, A: 255}), 0o600); err != nil {
		t.Fatal(err)
	}
	p.User("take a screenshot")
	p.ToolCall("c1", "shot")
	p.ToolResultImage("c1", "image/png", tinyPNG(t, color.RGBA{B: 200, A: 255}))
	p.Final("Saved to " + path)
	e.waitFor("the photo", func() bool { return e.photoCount(thread) > 0 })
	e.marker(p, thread, "two")
	n := 0
	for _, m := range e.botMessages(thread) {
		if m.Photo {
			n++
			if m.Text != "named-shot.png" {
				t.Errorf("photo = %+v", m)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d photos, want only the named file", n)
	}
}

// A photo that the user sent from Telegram, which the agent reads, does not
// come back.
func TestUploadReadByAToolIsNotSentBack(t *testing.T) {
	e, p, thread := piTopic(t, "pi-shown-upload")
	up := filepath.Join(e.cfg.UploadDir(), "1", "photo.png")
	p.User("what is in my photo")
	p.ReadCall("c1", up)
	p.ToolResultImage("c1", "image/png", tinyPNG(t, color.RGBA{R: 9, G: 9, A: 255}))
	p.Final("It is a small square")
	e.waitText(thread, "It is a small square")
	e.marker(p, thread, "three")
	if n := e.photoCount(thread); n != 0 {
		t.Fatalf("%d photos, want none", n)
	}
}
