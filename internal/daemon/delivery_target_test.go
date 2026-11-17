package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"rex-tg/internal/agents"
	"rex-tg/internal/rex"
	"rex-tg/internal/store"
)

type targetRex struct {
	rexClient
	pane         store.Pane
	process      func()
	capture      func()
	paste        func()
	pastes, keys int
}

func (r *targetRex) Process(context.Context, string, string) (rex.Process, error) {
	if r.process != nil {
		r.process()
	}
	return rex.Process{Foreground: rex.ProcInfo{PID: r.pane.PID, Name: r.pane.ProcessName, Argv0: "pi"}}, nil
}
func (r *targetRex) Capture(context.Context, string, string) (string, error) {
	if r.capture != nil {
		r.capture()
	}
	return "Working…\n> ", nil
}
func (r *targetRex) Paste(context.Context, string, string, string) error {
	r.pastes++
	if r.paste != nil {
		r.paste()
	}
	return nil
}
func (r *targetRex) SendKeys(context.Context, string, string, ...string) error {
	r.keys++
	return nil
}

// Changing only the conversation is enough to revoke a delivery, even when
// the foreground PID and runtime are unchanged (for example, /clear).
func TestDeliveryBindingIsRecheckedAfterExternalCalls(t *testing.T) {
	for _, change := range []string{"close", "takeover", "occupant"} {
		for _, at := range []string{"process", "screen", "paste"} {
			t.Run(change+"/"+at, func(t *testing.T) {
				f := newRenameFixture(t, false, false)
				r := &targetRex{pane: f.pane}
				f.d.rx = r
				mutate := func() {
					if change == "close" {
						if err := f.d.st.SetThreadStatus(f.d.ctx, f.th.ID, store.ThreadClosed); err != nil {
							t.Fatal(err)
						}
						return
					}
					c, err := f.d.st.UpsertConversation(f.d.ctx, store.Conversation{Runtime: agents.Pi, NativeID: "replacement", Transcript: "/synthetic/replacement", FirstSeenAt: time.Now()})
					if err != nil {
						t.Fatal(err)
					}
					if change == "takeover" {
						err = f.d.st.TakeOver(f.d.ctx, f.th.ID, c.ID)
					} else {
						err = f.d.st.SetPaneOccupant(f.d.ctx, f.pane.ID, c.ID, f.pane.PID, f.pane.ProcessName, "hook")
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				switch at {
				case "process":
					r.process = mutate
				case "screen":
					r.capture = mutate
				case "paste":
					r.paste = mutate
				}
				err := f.d.typeInto(f.d.ctx, f.th.ID, f.pane, "do the old task", 0, "enter", false, false, true)
				want := errTargetBefore
				wantPastes := 0
				if at == "paste" {
					want, wantPastes = errStoppedAfter, 1
				}
				if !errors.Is(err, want) || r.pastes != wantPastes || r.keys != 0 {
					t.Fatalf("result %v, pastes %d, keys %d; want %v, %d, 0", err, r.pastes, r.keys, want, wantPastes)
				}
			})
		}
	}
}

func TestDeliveryBindingAllowsTheUnchangedTarget(t *testing.T) {
	f := newRenameFixture(t, false, false)
	f.d.rx = &targetRex{pane: f.pane}
	if err := f.d.deliveryCheck(f.d.ctx, f.th.ID, f.pane); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.d.ctx)
	cancel()
	if err := f.d.deliveryCheck(ctx, f.th.ID, f.pane); !errors.Is(err, errTargetBefore) {
		t.Fatalf("cancelled check = %v", err)
	}
}
