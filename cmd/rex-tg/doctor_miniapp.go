package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"rex-tg/internal/config"
)

const tailscaleAppBin = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"

// miniAppProbe holds what the Mini App checks take from the outside, so tests
// can replace it.
type miniAppProbe struct {
	// ServeStatus returns the output of `tailscale serve status --json`.
	ServeStatus func(ctx context.Context) ([]byte, error)
	// HTTP does the live request.
	HTTP *http.Client
}

// newMiniAppProbe is replaced in tests.
var newMiniAppProbe = defaultMiniAppProbe

func defaultMiniAppProbe() miniAppProbe {
	return miniAppProbe{
		ServeStatus: func(ctx context.Context) ([]byte, error) {
			bin, err := exec.LookPath("tailscale")
			if err != nil {
				bin = tailscaleAppBin
			}
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			// Read only. Never run a command that changes the setup.
			out, err := exec.CommandContext(ctx, bin, "serve", "status", "--json").Output()
			if err != nil {
				return nil, err
			}
			return out, nil
		},
		HTTP: &http.Client{Timeout: 5 * time.Second},
	}
}

// miniAppLines prints the Mini App section of doctor.
func miniAppLines(ctx context.Context, d *doctorOut, cfg config.Config, daemonRunning bool, p miniAppProbe) {
	raw := strings.TrimSpace(cfg.MiniApp.URL)
	if raw == "" {
		d.ok("Mini App is off ([miniapp] url is empty); the buttons are text buttons")
		return
	}
	if problem := cfg.MiniAppProblem(); problem != "" {
		d.bad("Mini App: %s; the Mini App stays off", problem)
		return
	}
	if !cfg.MiniApp.TailnetOnly {
		d.warn("tailnet_only is off: anyone who reaches %s with a button's address can read the pages; keep it on unless the address is private", raw)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		d.bad("[miniapp] url %q is not a valid address", raw)
		return
	}
	if strings.HasSuffix(strings.ToLower(u.Hostname()), ".ts.net") {
		out, err := p.ServeStatus(ctx)
		if err != nil {
			d.warn("could not check tailscale serve: %v", err)
		} else {
			serveLine(d, out, u, cfg.MiniApp.Listen)
		}
	}
	liveLine(ctx, d, p.HTTP, raw, daemonRunning)
}

type serveStatus struct {
	Web map[string]struct {
		Handlers map[string]struct {
			Proxy string
		}
	}
	AllowFunnel map[string]bool
}

// hostPort returns the host and port of a URL, with 443 for https.
func urlHostPort(u *url.URL) (string, string) {
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return strings.ToLower(u.Hostname()), port
}

func serveLine(d *doctorOut, out []byte, u *url.URL, listen string) {
	var st serveStatus
	if err := json.Unmarshal(out, &st); err != nil {
		d.warn("could not read tailscale serve status: %v", err)
		return
	}
	host, port := urlHostPort(u)
	hp := net.JoinHostPort(host, port)
	var key string
	for k := range st.Web {
		if strings.EqualFold(k, hp) {
			key = k
			break
		}
	}
	web, found := st.Web[key]
	if !found {
		d.bad("tailscale serve does not serve %s; run: tailscale serve --bg --https=%s http://%s", hp, port, listen)
		return
	}
	h, ok := web.Handlers["/"]
	if !ok {
		d.bad("tailscale serve has no handler for / on %s; run: tailscale serve --bg --https=%s http://%s", hp, port, listen)
		return
	}
	if !sameHostPort(h.Proxy, listen) {
		d.bad("tailscale serve sends %s to %s, not to rex-tg (%s)", hp, h.Proxy, listen)
		return
	}
	for k, on := range st.AllowFunnel {
		if on && strings.EqualFold(k, hp) {
			d.bad("Funnel is on for %s: it is public, and rex-tg refuses those requests; run: tailscale funnel --https=%s off, then tailscale serve --bg --https=%s http://%s", hp, port, port, listen)
			return
		}
	}
	d.ok("tailscale serve sends %s to rex-tg, tailnet only", hp)
}

// sameHostPort compares a proxy target such as http://127.0.0.1:8770 with a
// listen address. localhost counts as 127.0.0.1.
func sameHostPort(proxy, listen string) bool {
	norm := func(hp string) string {
		h, p, err := net.SplitHostPort(hp)
		if err != nil {
			return hp
		}
		// localhost, 127.0.0.1 and ::1 all mean this Mac; the live check
		// below finds a target that does not answer.
		if ip := net.ParseIP(h); strings.EqualFold(h, "localhost") || (ip != nil && ip.IsLoopback()) {
			h = "loopback"
		}
		return net.JoinHostPort(h, p)
	}
	hp := proxy
	if i := strings.Index(hp, "://"); i >= 0 {
		hp = hp[i+3:]
	}
	if i := strings.IndexByte(hp, '/'); i >= 0 {
		hp = hp[:i]
	}
	return norm(hp) == norm(listen)
}

// liveLine asks the Mini App for its page. It sends no key and no init data,
// so a healthy server answers the shell page.
func liveLine(ctx context.Context, d *doctorOut, c *http.Client, base string, daemonRunning bool) {
	target := strings.TrimRight(base, "/") + "/app"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		d.bad("Mini App address %s: %v", base, err)
		return
	}
	resp, err := c.Do(req)
	if err != nil {
		if !daemonRunning {
			d.warn("the Mini App does not answer (no daemon is running)")
		} else {
			d.bad("the Mini App does not answer at %s: %v", base, err)
		}
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == http.StatusOK:
		d.ok("the Mini App answers at %s", base)
	case resp.StatusCode == http.StatusForbidden && (strings.Contains(strings.ToLower(string(body)), "tailnet") || strings.Contains(string(body), "Tailscale")):
		d.bad("the Mini App refused this Mac's request as not from the tailnet: is the address going through Funnel or another proxy?")
	default:
		d.bad("the Mini App at %s answered %d", base, resp.StatusCode)
	}
}
