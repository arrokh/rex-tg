package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"rex-tg/internal/config"
)

func miniOut(t *testing.T, f func(d *doctorOut)) (string, int) {
	t.Helper()
	var b bytes.Buffer
	d := &doctorOut{env: Env{Stdout: &b}}
	f(d)
	return b.String(), d.fail
}

func TestServeLine(t *testing.T) {
	const listen = "127.0.0.1:8770"
	cases := []struct {
		name, url, json, want string
		fail                  int
	}{
		{"ok", "https://mac.tn.ts.net:8443", `{"Web":{"mac.tn.ts.net:8443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:8770"}}}}}`, "✓ tailscale serve sends mac.tn.ts.net:8443 to rex-tg, tailnet only", 0},
		{"localhost", "https://mac.tn.ts.net:8443", `{"Web":{"mac.tn.ts.net:8443":{"Handlers":{"/":{"Proxy":"http://localhost:8770"}}}}}`, "✓ tailscale serve sends", 0},
		{"no / handler", "https://mac.tn.ts.net:8443", `{"Web":{"mac.tn.ts.net:8443":{"Handlers":{"/other":{"Proxy":"http://127.0.0.1:8770"}}}}}`, "✗ tailscale serve has no handler for / on mac.tn.ts.net:8443", 1},
		{"no port", "https://mac.tn.ts.net", `{"Web":{"mac.tn.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:8770"}}}}}`, "✓ tailscale serve sends mac.tn.ts.net:443", 0},
		{"no web", "https://mac.tn.ts.net:8443", `{"TCP":{"8443":{"HTTPS":true}}}`, "✗ tailscale serve does not serve mac.tn.ts.net:8443; run: tailscale serve --bg --https=8443 http://127.0.0.1:8770", 1},
		{"other host", "https://mac.tn.ts.net:8443", `{"Web":{"mac.tn.ts.net:443":{}}}`, "✗ tailscale serve does not serve", 1},
		{"wrong proxy", "https://mac.tn.ts.net:8443", `{"Web":{"mac.tn.ts.net:8443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:9999"}}}}}`, "✗ tailscale serve sends mac.tn.ts.net:8443 to http://127.0.0.1:9999, not to rex-tg (127.0.0.1:8770)", 1},
		{"funnel", "https://mac.tn.ts.net:8443", `{"Web":{"mac.tn.ts.net:8443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:8770"}}}},"AllowFunnel":{"mac.tn.ts.net:8443":true}}`, "✗ Funnel is on for mac.tn.ts.net:8443: it is public, and rex-tg refuses those requests; run: tailscale funnel --https=8443 off, then tailscale serve --bg --https=8443 http://127.0.0.1:8770", 1},
		{"funnel false", "https://mac.tn.ts.net:8443", `{"Web":{"mac.tn.ts.net:8443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:8770"}}}},"AllowFunnel":{"mac.tn.ts.net:8443":false}}`, "✓ tailscale serve sends", 0},
		{"garbage", "https://mac.tn.ts.net:8443", `not json`, "! could not read tailscale serve status", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, _ := url.Parse(c.url)
			out, fail := miniOut(t, func(d *doctorOut) { serveLine(d, []byte(c.json), u, listen) })
			if !strings.Contains(out, c.want) || fail != c.fail {
				t.Fatalf("got %q fail=%d, want %q fail=%d", out, fail, c.want, c.fail)
			}
		})
	}
}

func miniCfg(u string) config.Config {
	var c config.Config
	c.MiniApp.URL = u
	c.MiniApp.Listen = "127.0.0.1:8770"
	c.MiniApp.TailnetOnly = true
	return c
}

func TestMiniAppLines(t *testing.T) {
	probe := miniAppProbe{
		ServeStatus: func(context.Context) ([]byte, error) { return nil, errors.New("no tailscale") },
		HTTP:        http.DefaultClient,
	}
	run := func(c config.Config, running bool) (string, int) {
		return miniOut(t, func(d *doctorOut) { miniAppLines(context.Background(), d, c, running, probe) })
	}
	if out, fail := run(miniCfg(""), false); !strings.HasPrefix(out, "✓ Mini App is off") || fail != 0 {
		t.Fatalf("empty url: %q", out)
	}
	c := miniCfg("https://mac.tn.ts.net:8443")
	c.MiniApp.Listen = "0.0.0.0:8770"
	if out, fail := run(c, false); !strings.HasPrefix(out, "✗") || !strings.Contains(out, "the Mini App stays off") || fail != 1 || strings.Count(out, "\n") != 1 {
		t.Fatalf("problem: %q", out)
	}
	// A plain http test server stands in for the Mini App (not .ts.net: no serve check).
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	c = miniCfg(srv.URL)
	c.MiniApp.TailnetOnly = false
	out, _ := run(c, true)
	if !strings.Contains(out, "! tailnet_only is off") {
		t.Fatalf("tailnet_only off: %q", out)
	}
	// serve status failure on a ts.net address, then the live check refuses.
	c = miniCfg("https://mac.tn.ts.net:8443")
	if out, _ := run(c, false); !strings.Contains(out, "! could not check tailscale serve: no tailscale") {
		t.Fatalf("serve error: %q", out)
	}
}

func TestLiveLine(t *testing.T) {
	var path string
	cases := []struct {
		name    string
		status  int
		body    string
		running bool
		want    string
		fail    int
	}{
		{"ok", 200, "page", true, "✓ the Mini App answers at ", 0},
		{"tailnet", 403, "not from the tailnet", true, "✗ the Mini App refused this Mac's request as not from the tailnet", 1},
		{"other 403", 403, "nope", true, "✗ the Mini App at", 1},
		{"500", 500, "", true, "answered 500", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			out, fail := miniOut(t, func(d *doctorOut) { liveLine(context.Background(), d, srv.Client(), srv.URL+"/", c.running) })
			if !strings.Contains(out, c.want) || fail != c.fail || path != "/app" {
				t.Fatalf("got %q fail=%d path=%q", out, fail, path)
			}
		})
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	out, fail := miniOut(t, func(d *doctorOut) { liveLine(context.Background(), d, http.DefaultClient, base, false) })
	if out != "! the Mini App does not answer (no daemon is running)\n" || fail != 0 {
		t.Fatalf("no daemon: %q", out)
	}
	out, fail = miniOut(t, func(d *doctorOut) { liveLine(context.Background(), d, http.DefaultClient, base, true) })
	if !strings.HasPrefix(out, "✗ the Mini App does not answer at "+base+": ") || fail != 1 {
		t.Fatalf("daemon: %q", out)
	}
}
