package sender_test

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rex-tg/internal/sender"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram/tgtest"
)

// pngBytes makes a PNG of the given size.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// writeImage writes a file in a scratch folder and returns its path.
func writeImage(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *env) enqueueImage(th store.Thread, key, path string) int64 {
	e.t.Helper()
	id, _, err := e.st.Enqueue(ctx, store.OutboxItem{
		ThreadID: th.ID, Key: key, Kind: "image", Silent: true,
		Parts: []store.PartContent{{Content: path, Format: store.FormatImage, Filename: filepath.Base(path)}},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestImageSentAsPhotoWithCaption(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	data := pngBytes(t, 40, 30)
	id := e.enqueueImage(th, "k", writeImage(t, "shot.png", data))
	e.start()
	e.waitState(id, store.OutboxSent)
	ms := e.srv.Messages(chat)
	if len(ms) != 1 || !ms[0].Photo || ms[0].Text != "shot.png" || ms[0].Filename != "shot.png" ||
		!bytes.Equal(ms[0].File, data) || ms[0].ThreadID != 1 || !ms[0].Silent {
		t.Fatalf("messages = %+v", ms)
	}
	if len(e.srv.Calls("sendDocument")) != 0 {
		t.Fatal("a photo must not also go out as a document")
	}
}

func TestImageThatIsNoPhotoGoesAsDocument(t *testing.T) {
	big := append(pngBytes(t, 10, 10), make([]byte, 11<<20)...) // a PNG with 11 MB of trailing bytes
	for _, c := range []struct {
		name, file string
		data       []byte
	}{
		{"wrong extension", "pic.gif", pngBytes(t, 10, 10)},
		{"too large", "pic.png", big},
		{"too narrow", "pic.png", pngBytes(t, 600, 20)},
		{"not a picture", "pic.png", []byte("not a png")},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, sender.Options{})
			th := e.topic(1)
			id := e.enqueueImage(th, "k", writeImage(t, c.file, c.data))
			e.start()
			e.waitState(id, store.OutboxSent)
			ms := e.srv.Messages(chat)
			if len(ms) != 1 || ms[0].Photo || ms[0].Filename != c.file || ms[0].Text != c.file || len(ms[0].File) != len(c.data) {
				t.Fatalf("messages = %+v", ms)
			}
			if len(e.srv.Calls("sendPhoto")) != 0 {
				t.Fatal("sendPhoto must not be called")
			}
		})
	}
}

// Telegram answers 400 to a photo: nothing was posted, so the same file goes
// out as a document in the same attempt, with no notice.
func TestPhotoRefusedFallsBackToDocument(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	id := e.enqueueImage(th, "k", writeImage(t, "shot.png", pngBytes(t, 40, 30)))
	e.srv.FailNext("sendPhoto", 1, 400, "Bad Request: PHOTO_INVALID_DIMENSIONS", 0)
	e.start()
	e.waitState(id, store.OutboxSent)
	ms := e.srv.Messages(chat)
	if len(ms) != 1 || ms[0].Photo || ms[0].Filename != "shot.png" {
		t.Fatalf("messages = %+v", ms)
	}
	if n := len(e.srv.Calls("sendPhoto")); n != 1 {
		t.Fatalf("sendPhoto calls = %d", n)
	}
}

func TestMissingImageFailsWithNotice(t *testing.T) {
	e := newEnv(t, sender.Options{})
	th := e.topic(1)
	id := e.enqueueImage(th, "k", filepath.Join(t.TempDir(), "gone.png"))
	e.start()
	e.waitState(id, store.OutboxFailed)
	waitFor(t, "the notice", func() bool { return len(e.srv.Calls("sendMessage")) == 1 })
	text, _ := e.srv.Calls("sendMessage")[0].Params["text"].(string)
	if !strings.Contains(text, "could not be sent") || !strings.Contains(text, "the file is gone") {
		t.Errorf("notice = %q", text)
	}
	if len(e.srv.Calls("sendPhoto"))+len(e.srv.Calls("sendDocument")) != 0 {
		t.Fatal("nothing should be uploaded")
	}
}

// An image whose send got no answer goes through the message-number check
// like any part: not posted means it is sent again once; posted means the
// notice and no second copy.
func TestImageUnknownOutcomeIsChecked(t *testing.T) {
	photos := func(ms []tgtest.Message) (n int) {
		for _, m := range ms {
			if m.Photo {
				n++
			}
		}
		return n
	}
	t.Run("not posted", func(t *testing.T) {
		e := newEnv(t, sender.Options{})
		th := e.topic(1)
		id := e.enqueueImage(th, "k", writeImage(t, "shot.png", pngBytes(t, 40, 30)))
		e.srv.FailNext("sendPhoto", 1, 500, "Internal Server Error", 0)
		e.start()
		e.waitState(id, store.OutboxSent)
		waitFor(t, "the note to go", func() bool { return len(e.visible()) == 1 })
		if n := photos(e.visible()); n != 1 {
			t.Fatalf("photos = %d, want 1", n)
		}
	})
	t.Run("posted", func(t *testing.T) {
		e := newEnv(t, sender.Options{})
		th := e.topic(1)
		id := e.enqueueImage(th, "k", writeImage(t, "shot.png", pngBytes(t, 40, 30)))
		e.srv.LoseAnswerNext("sendPhoto", 1)
		e.start()
		waitFor(t, "the notice", func() bool {
			ms := e.visible()
			return len(ms) == 2 && ms[1].Text == noticeText(id, 0)
		})
		if n := photos(e.visible()); n != 1 || len(e.srv.Calls("sendPhoto")) != 1 {
			t.Fatalf("photos = %d after %d calls, want 1 and 1", n, len(e.srv.Calls("sendPhoto")))
		}
		if st := e.rowState(id); st != store.OutboxUnknown {
			t.Fatalf("state = %s", st)
		}
	})
}
