package daemon_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"rex-tg/internal/daemon"
	"rex-tg/internal/store"
)

func pairingCode(t *testing.T, e *env) string {
	t.Helper()
	var code string
	e.withStore(func(st *store.Store) {
		var err error
		if code, err = daemon.NewPairingCode(context.Background(), st, time.Now()); err != nil {
			t.Fatal(err)
		}
	})
	return code
}

func waitAttempts(e *env, want string) {
	e.t.Helper()
	e.waitFor("wrong codes counted: "+want, func() bool {
		v, _, _ := e.peek().Get(context.Background(), "pairing_attempts")
		return v == want
	})
}

// Five wrong codes from a stranger do not stop the owner from pairing.
func TestPairingStrangerCannotLockOutTheOwner(t *testing.T) {
	e := newEnv(t)
	code := pairingCode(t, e)
	e.start()
	const stranger = 555
	for i := 0; i < 5; i++ {
		e.tg.PushMessage(stranger, 0, stranger, fmt.Sprintf("/start WRONG-%d", i))
	}
	waitAttempts(e, "5")
	e.tg.PushMessage(chatID, 0, ownerID, "/start "+code)
	e.waitText(0, "Paired")
	st := e.peek()
	if v, ok, _ := st.Get(context.Background(), "owner_user_id"); !ok || v != fmt.Sprint(ownerID) {
		t.Fatalf("owner_user_id = %q, %v", v, ok)
	}
	for _, k := range []string{"pairing_code_hash", "pairing_expires_at", "pairing_attempts", "pairing_senders"} {
		if _, ok, _ := st.Get(context.Background(), k); ok {
			t.Errorf("%s is still stored after pairing", k)
		}
	}
}

// A locked sender is refused on the sixth try, even with the right code.
func TestPairingLockedSenderIsRefusedEvenWithTheRightCode(t *testing.T) {
	e := newEnv(t)
	code := pairingCode(t, e)
	e.start()
	const stranger = 555
	for i := 0; i < 5; i++ {
		e.tg.PushMessage(stranger, 0, stranger, fmt.Sprintf("/start WRONG-%d", i))
	}
	waitAttempts(e, "5")
	e.tg.PushMessage(stranger, 0, stranger, "/start "+code)
	e.never("pairing a locked sender", 700*time.Millisecond, func() bool {
		v, ok, _ := e.peek().Get(context.Background(), "owner_user_id")
		return ok && v != "" || len(e.botMessages(0)) > 0
	})
	// The refused try is not counted, and the code is still valid for others.
	waitAttempts(e, "5")
	e.tg.PushMessage(chatID, 0, ownerID, "/start "+code)
	e.waitText(0, "Paired")
}

// After 200 wrong codes from many senders, pairing is locked for everyone.
func TestPairingGlobalCapLocksEveryone(t *testing.T) {
	e := newEnv(t)
	code := pairingCode(t, e)
	e.start()
	for i := 0; i < 200; i++ {
		id := int64(2000 + i)
		e.tg.PushMessage(id, 0, id, "/start WRONG-X")
	}
	waitAttempts(e, "200")
	e.tg.PushMessage(chatID, 0, ownerID, "/start "+code)
	e.never("pairing after the global cap", 700*time.Millisecond, func() bool {
		v, ok, _ := e.peek().Get(context.Background(), "owner_user_id")
		return ok && v != "" || len(e.botMessages(0)) > 0
	})
}

// A new code clears the counts, so a locked sender can try again.
func TestPairingNewCodeClearsCounts(t *testing.T) {
	e := newEnv(t)
	pairingCode(t, e)
	e.start()
	for i := 0; i < 5; i++ {
		e.tg.PushMessage(chatID, 0, ownerID, fmt.Sprintf("/start WRONG-%d", i))
	}
	waitAttempts(e, "5")
	code := pairingCode(t, e) // as `rex-tg pair` does
	if v, _, _ := e.peek().Get(context.Background(), "pairing_senders"); v != "" {
		t.Fatalf("pairing_senders = %q after a new code", v)
	}
	e.tg.PushMessage(chatID, 0, ownerID, "/start "+code)
	e.waitText(0, "Paired")
}
