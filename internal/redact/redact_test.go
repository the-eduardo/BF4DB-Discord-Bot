package redact

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestStringMasksUrlBorneSecrets(t *testing.T) {
	for _, tc := range []struct {
		name, in, secret, keep string
	}{
		{
			name:   "kuma push token (permanent)",
			in:     `Get "http://uptime-kuma:3001/api/push/SUPERSECRET?status=up": connection refused`,
			secret: "SUPERSECRET",
			keep:   "/api/push/",
		},
		{
			name:   "discord interaction token",
			in:     `Post "https://discord.com/api/v9/interactions/123/INTERACTIONTOK/callback": EOF`,
			secret: "INTERACTIONTOK",
			keep:   "/interactions/123/",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := String(tc.in)
			if strings.Contains(got, tc.secret) {
				t.Errorf("secret survived redaction: %s", got)
			}
			// The prefix has to stay: a log line that masks the whole URL is
			// useless for telling which endpoint failed.
			if !strings.Contains(got, tc.keep) {
				t.Errorf("redaction ate the useful prefix %q: %s", tc.keep, got)
			}
			if !strings.Contains(got, "REDACTED") {
				t.Errorf("no REDACTED marker: %s", got)
			}
		})
	}
}

func TestStringLeavesInnocentTextAlone(t *testing.T) {
	in := `Get "https://bf4db.com/player/988768601": timeout`
	if got := String(in); got != in {
		t.Errorf("rewrote a URL with no secret in it: %s", got)
	}
}

func TestErrRedactsAndStaysUnwrappable(t *testing.T) {
	sentinel := errors.New("connection refused")
	wrapped := Err(&url.Error{
		Op:  "Get",
		URL: "http://uptime-kuma:3001/api/push/SUPERSECRET",
		Err: sentinel,
	})

	if strings.Contains(wrapped.Error(), "SUPERSECRET") {
		t.Errorf("secret survived: %s", wrapped.Error())
	}
	// errors.Is has to keep working through the wrapper, otherwise callers that
	// branch on the cause silently change behaviour when redaction is added.
	if !errors.Is(wrapped, sentinel) {
		t.Error("errors.Is broke through the redaction wrapper")
	}
	var ue *url.Error
	if !errors.As(wrapped, &ue) {
		t.Error("errors.As broke through the redaction wrapper")
	}
}

func TestErrNilStaysNil(t *testing.T) {
	if Err(nil) != nil {
		t.Error("Err(nil) must stay nil so callers can apply it unconditionally")
	}
}

func TestErrWrapsFormattedErrors(t *testing.T) {
	inner := fmt.Errorf("push failed: %w", &url.Error{
		Op: "Get", URL: "http://k:3001/api/push/TOK", Err: errors.New("EOF"),
	})
	if strings.Contains(Err(inner).Error(), "/api/push/TOK") {
		t.Errorf("secret survived inside a wrapped error: %s", Err(inner).Error())
	}
}
