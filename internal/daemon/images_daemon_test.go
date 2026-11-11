package daemon_test

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// The agent's final answer names a PNG on the Mac: the topic gets the answer,
// then the picture, as a photo.
func TestFinalAnswerImageIsSentAfterTheAnswer(t *testing.T) {
	e, p, thread := piTopic(t, "pi-images")
	path := filepath.Join(t.TempDir(), "screen-shot.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 32, 24))); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	p.User("take a screenshot")
	p.Final("Saved the screenshot to " + path + ".")
	e.waitText(thread, "Saved the screenshot")
	e.waitFor("the photo", func() bool {
		for _, m := range e.botMessages(thread) {
			if m.Photo {
				return true
			}
		}
		return false
	})
	var ids []int64
	for _, m := range e.botMessages(thread) {
		if m.Photo {
			if m.Text != "screen-shot.png" || !m.Silent {
				t.Errorf("photo = %+v", m)
			}
			ids = append(ids, m.ID)
		}
	}
	answer := e.waitText(thread, "Saved the screenshot")
	if len(ids) != 1 || ids[0] < answer.ID {
		t.Fatalf("photos %v, answer %d: want one photo after the answer", ids, answer.ID)
	}
}
