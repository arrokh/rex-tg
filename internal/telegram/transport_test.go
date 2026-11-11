package telegram

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestTransportTimeouts(t *testing.T) {
	tr := newTransport(nil)
	if tr.TLSHandshakeTimeout != 5*time.Second {
		t.Errorf("TLSHandshakeTimeout = %v, want 5s", tr.TLSHandshakeTimeout)
	}
	if dialTimeout != 5*time.Second || keepAliveIdle != 5*time.Second ||
		keepAliveInterval != 3*time.Second || keepAliveCount != 3 {
		t.Errorf("dial %v, keepalive %v/%v/%d", dialTimeout, keepAliveIdle, keepAliveInterval, keepAliveCount)
	}
	if tr.ForceAttemptHTTP2 || len(tr.TLSNextProto) != 0 {
		t.Error("HTTP/2 must stay off")
	}
}

func TestTransportProxy(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://api.telegram.org/x", nil)
	pu, _ := url.Parse("socks5h://127.0.0.1:1080")
	got, err := newTransport(pu).Proxy(req)
	if err != nil || got == nil || got.String() != pu.String() {
		t.Fatalf("proxy = %v, %v, want %v", got, err, pu)
	}
	c := New("t", WithProxy(pu))
	got, _ = c.hc.Transport.(*http.Transport).Proxy(req)
	if got == nil || got.Host != "127.0.0.1:1080" {
		t.Fatalf("client proxy = %v", got)
	}
}

func TestTransportEnvProxyWhenUnset(t *testing.T) {
	if newTransport(nil).Proxy == nil {
		t.Fatal("want the environment proxy func")
	}
}

// An upload gets time for its size on top of the call timeout, so a large
// file on a slow link does not end with an unknown outcome.
func TestUploadTimeoutGrowsWithTheFile(t *testing.T) {
	base := 15 * time.Second
	if got := uploadTimeout(base, 0); got != base {
		t.Errorf("empty upload: %v, want %v", got, base)
	}
	if got := uploadTimeout(base, 10<<20); got != base+40*time.Second {
		t.Errorf("10 MiB upload: %v, want %v", got, base+40*time.Second)
	}
}
