package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"rex-tg/internal/inbound"
	"rex-tg/internal/store"
	"rex-tg/internal/telegram"
)

const (
	pairingTTL         = 10 * time.Minute
	pairingMaxAttempts = 5
	// pairingAlphabet has no look-alike characters (0/O, 1/I/L).
	pairingAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
)

// NewPairingCode creates a one-time pairing code, stores its hash with a
// 10-minute expiry, and returns the code for `rex-tg login` to print.
func NewPairingCode(ctx context.Context, st *store.Store, now time.Time) (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("daemon: pairing code: %w", err)
	}
	code := make([]byte, 0, 9)
	for i, b := range raw {
		if i == 4 {
			code = append(code, '-')
		}
		code = append(code, pairingAlphabet[int(b)%len(pairingAlphabet)])
	}
	c := string(code)
	if err := st.Set(ctx, "pairing_code_hash", codeHash(c)); err != nil {
		return "", fmt.Errorf("daemon: save pairing code: %w", err)
	}
	if err := st.Set(ctx, "pairing_expires_at", strconv.FormatInt(now.Add(pairingTTL).UnixMilli(), 10)); err != nil {
		return "", fmt.Errorf("daemon: save pairing expiry: %w", err)
	}
	if err := st.Set(ctx, "pairing_attempts", "0"); err != nil {
		return "", fmt.Errorf("daemon: save pairing attempts: %w", err)
	}
	return c, nil
}

// codeHash hashes a code after the spelling is normalized (case, dashes,
// spaces).
func codeHash(code string) string {
	code = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code))
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// tryPair handles a message before the bot has an owner. Only `/start <code>`
// does anything. A wrong code gets no answer, as design section 11 says
// ("Until then, the bot answers nobody").
func (d *daemon) tryPair(m *telegram.Message) {
	if m.From == nil || m.Chat.ID != m.From.ID {
		return
	}
	r := inbound.RouteText(m.Text, false, d.botUser)
	if r.Kind != inbound.RouteCommand || r.Command != "start" || r.Args == "" {
		return
	}
	ctx := d.ctx
	want, ok, err := d.st.Get(ctx, "pairing_code_hash")
	if err != nil || !ok || want == "" {
		return
	}
	attempts := d.kvInt("pairing_attempts")
	if attempts >= pairingMaxAttempts {
		d.log.Warn("pairing is locked; run rex-tg login again", "from", m.From.ID)
		return
	}
	if exp := d.kvInt("pairing_expires_at"); exp == 0 || d.now().UnixMilli() > exp {
		d.log.Warn("pairing code expired", "from", m.From.ID)
		return
	}
	got := codeHash(r.Args)
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		d.kvSetInt("pairing_attempts", attempts+1)
		d.log.Warn("wrong pairing code", "from", m.From.ID, "attempts", attempts+1)
		return
	}
	d.kvSetInt("owner_user_id", m.From.ID)
	d.kvSetInt("owner_chat_id", m.Chat.ID)
	for _, k := range []string{"pairing_code_hash", "pairing_expires_at", "pairing_attempts"} {
		if err := d.st.Delete(ctx, k); err != nil {
			d.log.Warn("clear pairing value", "key", k, "err", err)
		}
	}
	d.ownerUser, d.ownerChat = m.From.ID, m.Chat.ID
	d.log.Info("paired", "owner", m.From.ID)
	d.say(m.Chat.ID, 0, "✅ Paired. Use /agents to list the agents in your Rex panes. /help lists the commands.", nil)
}
