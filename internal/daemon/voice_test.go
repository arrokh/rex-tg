package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTranscodeVoiceToWAVRejectsOutputAtLimit(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "voice.oga")
	if err := os.WriteFile(source, []byte("synthetic audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &daemon{voiceCommand: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) != 17 || args[14] != "-fs" || args[15] != "134217728" {
			t.Fatalf("ffmpeg arguments = %q", args)
		}
		return nil, os.Truncate(args[16], maxVoiceWAVBytes)
	}}
	if _, err := d.transcodeVoiceToWAV(context.Background(), "ffmpeg", source); !errors.Is(err, errVoiceWAVTooLarge) {
		t.Fatalf("transcode error = %v, want WAV size limit", err)
	}
	left, err := filepath.Glob(filepath.Join(dir, ".rex-tg-voice-*.wav"))
	if err != nil || len(left) != 0 {
		t.Fatalf("temporary WAV files left = %q, %v", left, err)
	}
}

func TestMissingFFmpegRequestsVoiceSetup(t *testing.T) {
	if !isVoiceSetupError(errFFmpegUnavailable) {
		t.Fatal("missing FFmpeg did not request voice setup")
	}
}

func TestWaitForHexModelRejectsStoppedHex(t *testing.T) {
	f := newRenameFixture(t, false, false)
	f.d.voiceCommand = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"code":"not-running"}`), errors.New("Hex is not running")
	}
	if err := f.d.waitForHexModel(context.Background(), "hex"); err == nil {
		t.Fatal("waitForHexModel accepted an unverified model after Hex stopped")
	}
}
