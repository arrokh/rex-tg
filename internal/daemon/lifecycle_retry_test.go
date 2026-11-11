package daemon

import (
	"errors"
	"testing"
	"time"

	"rex-tg/internal/telegram"
)

func TestRetryDelay(t *testing.T) {
	notSent := &telegram.NotSentError{Method: "getUpdates", Err: errors.New("TLS handshake timeout")}
	unknown := errors.New("connection reset")
	api := &telegram.APIError{Method: "getUpdates", Code: 500, Description: "oops"}
	limited := &telegram.APIError{Method: "getUpdates", Code: 429, Description: "Too Many Requests", RetryAfter: 40 * time.Second}
	tests := []struct {
		name  string
		err   error
		fails int
		want  time.Duration
	}{
		{"net 1", notSent, 1, 500 * time.Millisecond},
		{"net 2", unknown, 2, time.Second},
		{"net 3", notSent, 3, 2 * time.Second},
		{"net capped", unknown, 9, 2 * time.Second},
		{"api 1", api, 1, time.Second},
		{"api 3", api, 3, 4 * time.Second},
		{"api capped", api, 20, 30 * time.Second},
		{"429 honors retry_after", limited, 1, 40 * time.Second},
	}
	for _, tc := range tests {
		if got := retryDelay(tc.err, tc.fails); got != tc.want {
			t.Errorf("%s: delay = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestOutage(t *testing.T) {
	var o outage
	t0 := time.Unix(1000, 0)
	if _, _, ok := o.recovered(t0); ok {
		t.Fatal("no outage yet")
	}
	err := &telegram.NotSentError{Method: "getMe", Err: errors.New("x")}
	o.fail(t0, err)
	o.fail(t0.Add(3*time.Second), err)
	down, tries, ok := o.recovered(t0.Add(10 * time.Second))
	if !ok || down != 10*time.Second || tries != 2 {
		t.Fatalf("recovered = %v, %d, %v", down, tries, ok)
	}
	if _, _, ok := o.recovered(t0); ok {
		t.Fatal("outage must reset")
	}
}
