package claude_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"rex-tg/internal/agents"
	"rex-tg/internal/agents/claude"
)

func TestToolResultImages(t *testing.T) {
	good := base64.StdEncoding.EncodeToString([]byte("\xff\xd8\xffxx"))
	line := `{"type":"user","uuid":"u1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[` +
		`{"type":"text","text":"shot"},{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"` + good + `"}},` +
		`{"type":"image","source":{"type":"url","url":"https://example.invalid/x.png"}}]}]}}`
	evs, err := claude.New().ParseLine([]byte(line), agents.LineContext{SessionID: "S"})
	if err != nil || len(evs) != 1 || evs[0].Kind != agents.ToolResult {
		t.Fatalf("events %+v, err %v", evs, err)
	}
	if got := evs[0].Images; len(got) != 1 || got[0].MIME != "image/jpeg" || !strings.HasPrefix(string(got[0].Data), "\xff\xd8\xff") {
		t.Errorf("images = %+v, want the one base64 image", got)
	}
}

func TestPromptImagesAreNotToolImages(t *testing.T) {
	good := base64.StdEncoding.EncodeToString([]byte("\xff\xd8\xffxx"))
	line := `{"type":"user","uuid":"u2","message":{"role":"user","content":[{"type":"text","text":"look"},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"` + good + `"}}]}}`
	evs, err := claude.New().ParseLine([]byte(line), agents.LineContext{SessionID: "S"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if len(e.Images) != 0 {
			t.Fatalf("event %+v: a prompt's image is not kept", e)
		}
	}
}

func TestReadCallTarget(t *testing.T) {
	line := `{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/x/y.png"}}]}}`
	evs, _ := claude.New().ParseLine([]byte(line), agents.LineContext{SessionID: "S"})
	if len(evs) != 1 || evs[0].Tool.Target != "/x/y.png" {
		t.Fatalf("events %+v", evs)
	}
}
