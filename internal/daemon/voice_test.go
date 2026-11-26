package daemon

import (
	"context"
	"errors"
	"testing"
)

func TestWaitForHexModelRejectsStoppedHex(t *testing.T) {
	f := newRenameFixture(t, false, false)
	f.d.voiceCommand = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"code":"not-running"}`), errors.New("Hex is not running")
	}
	if err := f.d.waitForHexModel(context.Background(), "hex"); err == nil {
		t.Fatal("waitForHexModel accepted an unverified model after Hex stopped")
	}
}
