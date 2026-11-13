package pi_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"rex-tg/internal/agents"
	"rex-tg/internal/agents/pi"
)

func TestToolResultImages(t *testing.T) {
	good := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nxx"))
	line := `{"type":"message","id":"r1","message":{"role":"toolResult","toolCallId":"c1","content":[` +
		`{"type":"text","text":"Read image"},{"type":"image","mimeType":"image/png","data":"` + good + `"},` +
		`{"type":"image","mimeType":"image/png","data":"@@not base64@@"}]}}`
	evs, err := pi.New().ParseLine([]byte(line), agents.LineContext{SessionID: "S"})
	if err != nil || len(evs) != 1 || evs[0].Kind != agents.ToolResult {
		t.Fatalf("events %+v, err %v", evs, err)
	}
	if got := evs[0].Images; len(got) != 1 || got[0].MIME != "image/png" || !strings.HasPrefix(string(got[0].Data), "\x89PNG") {
		t.Errorf("images = %+v, want the one valid image", got)
	}
	if evs[0].Tool.Output != "Read image" {
		t.Errorf("output = %q", evs[0].Tool.Output)
	}
}

func TestUserImagesAreNotToolImages(t *testing.T) {
	good := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nxx"))
	line := `{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"look"},{"type":"image","mimeType":"image/png","data":"` + good + `"}]}}`
	evs, err := pi.New().ParseLine([]byte(line), agents.LineContext{SessionID: "S"})
	if err != nil || len(evs) != 1 || len(evs[0].Images) != 0 {
		t.Fatalf("events %+v, err %v: a prompt's image is not kept", evs, err)
	}
}

func TestReadCallTarget(t *testing.T) {
	line := `{"type":"message","id":"a1","message":{"role":"assistant","content":[{"type":"toolCall","id":"c1","name":"read","arguments":{"path":"/x/y.png"}}],"stopReason":"toolUse"}}`
	evs, _ := pi.New().ParseLine([]byte(line), agents.LineContext{SessionID: "S"})
	if len(evs) != 1 || evs[0].Tool.Target != "/x/y.png" {
		t.Fatalf("events %+v", evs)
	}
}
