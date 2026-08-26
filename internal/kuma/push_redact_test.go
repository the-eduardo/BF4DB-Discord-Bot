package kuma

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

type refusingTransport struct{}

func (refusingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	// net/http wraps whatever a transport returns in a *url.Error, and that
	// type's Error() embeds the FULL request URL — which is how the push token
	// reaches the log.
	return nil, errors.New("connection refused")
}

// TestPushFailureNeverLogsTheToken is the wiring test, not a unit test of the
// redactor: the Kuma push URL carries a PERMANENT token in its path
// (/api/push/<token>), the failure log fires at Warn on the very first pair of
// consecutive failures, and Warn is visible at production's LOG_LEVEL=info —
// so an outage of the monitoring host would publish the token to Loki on every
// window. Same class as the leak the AppSec panel caught on 25/08/2026 in
// another repo; this one had been missed by that fix.
func TestPushFailureNeverLogsTheToken(t *testing.T) {
	const token = "SUPERSECRETPUSHTOKEN"

	var buf bytes.Buffer
	p := &Pusher{
		url:        "http://uptime-kuma:3001/api/push/" + token,
		interval:   time.Hour,
		retryDelay: time.Millisecond,
		client:     &http.Client{Transport: refusingTransport{}},
		log:        slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}

	p.pushIfAlive(context.Background(), func() (bool, time.Duration) { return true, 12 * time.Millisecond })

	out := buf.String()
	if strings.Contains(out, token) {
		t.Errorf("the push token reached the log: %s", out)
	}
	// Positive control: without it, this test would also pass if the log line
	// vanished entirely or the failure path stopped running — "the secret is
	// absent" and "nothing happened" look identical otherwise.
	if !strings.Contains(out, "kuma push failed twice in a row") {
		t.Fatalf("the failure was not logged at all, so the assertion above proves nothing: %s", out)
	}
	if !strings.Contains(out, "/api/push/REDACTED") {
		t.Errorf("expected the endpoint prefix kept and the token masked: %s", out)
	}
}
