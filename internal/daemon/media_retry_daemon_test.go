package daemon_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"rex-tg/internal/telegram"
	"rex-tg/internal/telegram/tgtest"
)

func TestFailedPhotoCanBeRetried(t *testing.T) {
	e, _, thread := piTopic(t, "pi-photo-retry")
	data := []byte("\xff\xd8\xffretryable photo")
	e.tg.AddFile("retry-photo", "photos/file_1.jpg", data)
	e.tg.FailNext("getFile", 1, 500, "temporary error", 0)
	update := e.tg.PushUpdate(telegram.Update{Message: &telegram.Message{
		MessageID: 9201, MessageThreadID: thread, IsTopicMessage: true,
		From: &telegram.User{ID: ownerID}, Chat: telegram.Chat{ID: chatID, Type: "private"},
		Photo: []telegram.PhotoSize{{FileID: "retry-photo", FileSize: int64(len(data))}}, Caption: "inspect this",
	}})

	first := e.waitText(thread, "❌ Could not download the file.")
	retry := buttonData(first, "Retry")
	if want := fmt.Sprintf("fr:%d", update.UpdateID); retry != want {
		t.Fatalf("retry callback = %q, want %q", retry, want)
	}
	e.never("typing before the retry", 150*time.Millisecond, func() bool { return len(e.pastes()) > 0 })

	// The retry information survives a daemon restart. Two taps on the same
	// button while the download is in flight must start only one download.
	e.stop()
	e.start()
	e.tg.Delay("getFile", 250*time.Millisecond)
	e.tg.FailNext("getFile", 1, 500, "temporary error", 0)
	e.pressButton(first, retry)
	e.pressButton(first, retry)
	e.waitFor("the duplicate tap to be declined", func() bool {
		for _, text := range e.callbackTexts() {
			if strings.Contains(text, "already in progress") {
				return true
			}
		}
		return false
	})
	e.waitFor("the retry failure notice", func() bool { return e.countText(thread, "❌ Could not download the file.") >= 2 })
	if got := len(e.tg.Calls("getFile")); got != 2 {
		t.Fatalf("getFile called %d times after duplicate taps, want initial attempt plus one retry", got)
	}

	var second tgtest.Message
	for _, m := range e.botMessages(thread) {
		if strings.Contains(m.Text, "❌ Could not download the file.") && m.ID != first.ID {
			second = m
		}
	}
	if second.ID == 0 || buttonData(second, "Retry") == "" {
		t.Fatalf("retry failure did not offer another retry: %+v", second)
	}

	e.tg.Delay("getFile", 0)
	e.pressButton(second, retry)
	e.waitFor("the file path to be typed once", func() bool { return len(e.pastes()) == 1 })
	got := e.pastes()[0]
	lines := strings.Split(got, "\n")
	if len(lines) != 2 || lines[0] != "inspect this" {
		t.Fatalf("pasted %q, want caption and path", got)
	}
	contents, err := os.ReadFile(lines[1])
	if err != nil || string(contents) != string(data) {
		t.Fatalf("downloaded file = %q, %v", contents, err)
	}
	e.waitFor("Enter after the retry", func() bool { return len(e.keys()) == 1 })
	if got := len(e.tg.Calls("getFile")); got != 3 {
		t.Fatalf("getFile called %d times after success, want 3", got)
	}

	// A stale button cannot download or type the same file again.
	e.pressButton(second, retry)
	e.waitFor("the stale retry to be answered", func() bool {
		for _, text := range e.callbackTexts() {
			if strings.Contains(text, "no longer available") {
				return true
			}
		}
		return false
	})
	if got := len(e.tg.Calls("getFile")); got != 3 || len(e.pastes()) != 1 {
		t.Fatalf("stale retry caused %d downloads and %d pastes", got, len(e.pastes()))
	}
}
