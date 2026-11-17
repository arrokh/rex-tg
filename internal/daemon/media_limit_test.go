package daemon

import "testing"

func TestMediaWorkIsBoundedAndReleasesCapacity(t *testing.T) {
	d := &daemon{mediaSlots: make(chan struct{}, maxMediaWorkers)}
	for range maxMediaWorkers {
		if !d.tryMediaSlot() {
			t.Fatal("capacity was refused before the limit")
		}
	}
	if d.tryMediaSlot() {
		t.Fatal("excess media work was accepted")
	}
	d.releaseMediaSlot()
	if !d.tryMediaSlot() {
		t.Fatal("completed work did not release capacity")
	}
	for range maxMediaWorkers {
		d.releaseMediaSlot()
	}
	if len(d.mediaSlots) != 0 {
		t.Fatal("media slots were leaked")
	}
}
