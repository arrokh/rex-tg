package daemon

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"

	"rex-tg/internal/miniapp"
)

// The buttons' key opens pages on any device that has it. In a shared
// tailnet (a company's, say) other people's devices reach the Mini App too,
// so the key works only from the owner's own Tailscale logins:
//   - [miniapp] tailnet_users names them, or else
//   - rex-tg learns them: a login whose request carried the owner's
//     Telegram signature (a page opened from a chat button), and, while none
//     is known, the login of the first request with the key (the key lives
//     only in the owner's chat).
//
// /rekey forgets the learned logins with the old key.

const miniLoginsKey = "mini_logins" // kv: the learned logins, JSON

// maxMiniLogins caps the learned logins; the newest win.
const maxMiniLogins = 8

// miniLogins is the daemon's miniapp.Logins. Its methods run on HTTP
// goroutines.
type miniLogins struct {
	d      *daemon
	mu     sync.Mutex
	loaded bool
	known  []string
}

var _ miniapp.Logins = (*miniLogins)(nil)

func (l *miniLogins) fixed() []string { return l.d.cfg.MiniApp.TailnetUsers }

// hasLogin tells whether login is in list, without regard to case or the
// spaces around it.
func hasLogin(list []string, login string) bool {
	login = strings.TrimSpace(login)
	return slices.ContainsFunc(list, func(s string) bool { return strings.EqualFold(strings.TrimSpace(s), login) })
}

// load reads the learned logins once (under mu).
func (l *miniLogins) load() {
	if l.loaded {
		return
	}
	l.loaded = true
	if v, ok, err := l.d.st.Get(context.Background(), miniLoginsKey); err == nil && ok {
		_ = json.Unmarshal([]byte(v), &l.known)
	}
}

// add learns a login and keeps the list (under mu).
func (l *miniLogins) add(login string) {
	login = strings.TrimSpace(login)
	if login == "" || hasLogin(l.known, login) {
		return
	}
	l.known = append(l.known, login)
	if len(l.known) > maxMiniLogins {
		l.known = l.known[len(l.known)-maxMiniLogins:]
	}
	if b, err := json.Marshal(l.known); err == nil {
		if err := l.d.st.Set(context.Background(), miniLoginsKey, string(b)); err != nil {
			l.d.log.Warn("mini app: keep the login", "err", err)
		}
	}
	l.d.log.Info("mini app: learned your Tailscale login for the buttons", "logins", len(l.known))
}

// Owner learns a login that the owner's Telegram signature vouches for.
func (l *miniLogins) Owner(login string) {
	if login == "" || len(l.fixed()) > 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	l.add(login)
}

// KeyLoginOK tells whether a request with key may come from login. The key
// is checked again under mu: /rekey stores the new key before it forgets
// the logins (under mu), so an old key cannot teach a login.
func (l *miniLogins) KeyLoginOK(key, login string) bool {
	if strings.TrimSpace(login) == "" {
		return false
	}
	if f := l.fixed(); len(f) > 0 {
		return l.d.keyOK(key) && hasLogin(f, login)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.d.keyOK(key) {
		return false
	}
	l.load()
	if len(l.known) == 0 {
		l.add(login) // the first use of the key: the key lives only in the owner's chat
		return true
	}
	return hasLogin(l.known, login)
}

// forget drops the learned logins (/rekey).
func (l *miniLogins) forget() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.loaded, l.known = true, nil
	if err := l.d.st.Delete(context.Background(), miniLoginsKey); err != nil {
		l.d.log.Warn("mini app: forget the logins", "err", err)
	}
}
