package telegram

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// route is a fake route to Telegram. It counts the requests it gets and
// fails them the way mode says.
type route struct {
	mu    sync.Mutex
	mode  string // "ok", "notsent", or "unknown"
	calls int
}

func (r *route) set(mode string) {
	r.mu.Lock()
	r.mode = mode
	r.mu.Unlock()
}

func (r *route) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *route) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.calls++
	mode := r.mode
	r.mu.Unlock()
	switch mode {
	case "notsent":
		return nil, errors.New("net/http: TLS handshake timeout")
	case "unknown":
		if tr := httptrace.ContextClientTrace(req.Context()); tr != nil && tr.WroteRequest != nil {
			tr.WroteRequest(httptrace.WroteRequestInfo{})
		}
		return nil, errors.New("connection reset by peer")
	}
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":1}}`)),
		Request:    req,
	}, nil
}

func failoverClient(t *testing.T) (c *Client, proxy, direct *route, logs *bytes.Buffer) {
	t.Helper()
	logs = &bytes.Buffer{}
	pu, _ := url.Parse("socks5h://127.0.0.1:1")
	c = New("123:secret", WithProxy(pu), WithFallback(true), WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
	if c.fo == nil {
		t.Fatal("failover is off")
	}
	proxy, direct = &route{mode: "ok"}, &route{mode: "ok"}
	c.fo.clients[routeProxy] = &http.Client{Transport: proxy}
	c.fo.clients[routeDirect] = &http.Client{Transport: direct}
	return
}

func TestFailoverSwitchesAndSwitchesBack(t *testing.T) {
	c, proxy, direct, logs := failoverClient(t)
	ctx := context.Background()

	if _, err := c.GetMe(ctx); err != nil {
		t.Fatal(err)
	}
	if proxy.count() != 1 || direct.count() != 0 {
		t.Fatalf("default route: proxy %d, direct %d", proxy.count(), direct.count())
	}

	// The proxy fails: each call is retried at once on the direct route, so
	// it still succeeds. After a run of failures, the direct route is current.
	proxy.set("notsent")
	for i := 0; i < switchAfter; i++ {
		if _, err := c.GetMe(ctx); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if r, _ := c.fo.current(); r != routeDirect {
		t.Fatalf("route = %d, want direct", r)
	}
	before := proxy.count()
	if _, err := c.GetMe(ctx); err != nil {
		t.Fatal(err)
	}
	if proxy.count() != before {
		t.Error("the proxy was tried although the direct route is current")
	}
	if !strings.Contains(logs.String(), "Telegram route: direct") || !strings.Contains(logs.String(), "not_sent") {
		t.Errorf("log: %q", logs.String())
	}

	// The proxy is back and the direct route fails: switch back.
	proxy.set("ok")
	direct.set("notsent")
	for i := 0; i < switchAfter; i++ {
		if _, err := c.GetMe(ctx); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if r, _ := c.fo.current(); r != routeProxy {
		t.Fatalf("route = %d, want proxy", r)
	}
	if n := strings.Count(logs.String(), "Telegram route:"); n != 2 {
		t.Errorf("%d route lines, want 2: %q", n, logs.String())
	}
	if strings.Contains(logs.String(), "secret") {
		t.Error("the token is in the log")
	}
}

func TestFailoverBothRoutesDownReturnsNotSent(t *testing.T) {
	c, proxy, direct, _ := failoverClient(t)
	proxy.set("notsent")
	direct.set("notsent")
	_, err := c.GetUpdates(context.Background(), 0, 0, nil)
	if Classify(err) != ClassNotSent {
		t.Fatalf("err = %v", err)
	}
	if proxy.count() != 1 || direct.count() != 1 {
		t.Errorf("proxy %d, direct %d, want one try each", proxy.count(), direct.count())
	}
}

func TestFailoverUnknownOutcomeIsNotSentAgain(t *testing.T) {
	c, proxy, direct, logs := failoverClient(t)
	proxy.set("unknown")
	for i := 0; i < 2*switchAfter; i++ {
		_, err := c.SendMessage(context.Background(), SendMessageParams{ChatID: 1, Text: "x"})
		if Classify(err) != ClassUnknownOutcome {
			t.Fatalf("err = %v", err)
		}
	}
	if direct.count() != 0 {
		t.Errorf("an unknown outcome was sent again on the direct route %d times", direct.count())
	}
	if proxy.count() != 2*switchAfter {
		t.Errorf("proxy calls = %d", proxy.count())
	}
	if r, _ := c.fo.current(); r != routeProxy || strings.Contains(logs.String(), "Telegram route") {
		t.Errorf("route changed on unknown outcomes: %d %q", r, logs.String())
	}
}

func TestFailoverAnswerResetsTheRun(t *testing.T) {
	c, proxy, _, _ := failoverClient(t)
	ctx := context.Background()
	// Failures that are not in a row never switch the route.
	for i := 0; i < 3*switchAfter; i++ {
		proxy.set("notsent")
		_, _ = c.GetMe(ctx)
		proxy.set("ok")
		if _, err := c.GetMe(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if r, _ := c.fo.current(); r != routeProxy {
		t.Errorf("route = %d, want proxy", r)
	}
}

func TestFailoverOffWithoutProxyOrSwitch(t *testing.T) {
	pu, _ := url.Parse("socks5h://127.0.0.1:1")
	if c := New("t", WithProxy(pu)); c.fo != nil {
		t.Error("failover is on without WithFallback")
	}
	if c := New("t", WithFallback(true)); c.fo != nil {
		t.Error("failover is on without a proxy")
	}
	if c := New("t", WithProxy(pu), WithFallback(true), WithHTTPClient(&http.Client{})); c.fo != nil {
		t.Error("failover is on with a custom HTTP client")
	}
}

func TestDirectRouteIgnoresTheEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	pu, _ := url.Parse("socks5h://127.0.0.1:1")
	c := New("t", WithProxy(pu), WithFallback(true))
	req, _ := http.NewRequest("GET", "https://api.telegram.org/x", nil)
	if tr := c.fo.clients[routeDirect].Transport.(*http.Transport); tr.Proxy != nil {
		got, _ := tr.Proxy(req)
		t.Errorf("direct route uses proxy %v", got)
	}
}
