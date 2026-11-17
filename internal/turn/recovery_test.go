package turn

import (
	"reflect"
	"testing"
)

func TestClonePreservesLiveFlagsAndCopiesCalls(t *testing.T) {
	original := &Tracker{
		cfg:     Config{ProgressDelay: 3},
		set:     Settings{Mode: "progress"},
		ph:      phActive,
		calls:   []call{{id: "call-1", name: "bash", label: "go test", status: callFailed}},
		spin:    5,
		removed: true,
	}
	clone := original.Clone()
	if !reflect.DeepEqual(clone, original) {
		t.Fatalf("clone differs from source: %#v != %#v", clone, original)
	}
	if &clone.calls[0] == &original.calls[0] {
		t.Fatal("clone shares the calls slice with its source")
	}

	clone.calls[0].label = "changed"
	clone.spin++
	clone.removed = false
	if original.calls[0].label != "go test" || original.spin != 5 || !original.removed {
		t.Fatalf("mutating clone changed source: calls=%+v spin=%d removed=%v", original.calls, original.spin, original.removed)
	}
}
