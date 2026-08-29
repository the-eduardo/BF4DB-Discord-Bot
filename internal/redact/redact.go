// Package redact strips credentials that HTTP errors carry inside URLs.
//
// Go's *url.Error — what net/http returns for any transport-level failure —
// embeds the full request URL in its Error() string. When a secret lives in the
// URL path rather than in a header, logging that error publishes the secret.
// This bit the fleet on 25/08/2026: an Uptime Kuma push URL carries its token in
// the path, and a log-per-transition change shipped it to Loki on every flap.
//
// The Discord bot token is NOT affected (discordgo sends it in the authorization
// header, which no error message prints) — but the per-interaction token IS, and
// the Kuma push token is permanent.
package redact

import "regexp"

// Each pattern keeps its first group (the recognizable prefix, which is useful
// in a log) and masks the secret that follows.
var patterns = []*regexp.Regexp{
	// Discord interaction callback: /interactions/{id}/{token}/callback
	regexp.MustCompile(`(/interactions/\d+/)[^/\s"]+`),
	// Discord interaction followup/edit: /webhooks/{app_id}/{token}/messages/@original
	regexp.MustCompile(`(/webhooks/\d+/)[^/?\s"]+`),
	// Uptime Kuma dead-man switch: /api/push/{token}
	regexp.MustCompile(`(/api/push/)[^/?\s"]+`),
}

// String masks every known secret-bearing URL segment in s.
func String(s string) string {
	for _, re := range patterns {
		s = re.ReplaceAllString(s, "${1}REDACTED")
	}
	return s
}

type redactedError struct{ err error }

func (e *redactedError) Error() string { return String(e.err.Error()) }

// Unwrap keeps errors.Is/errors.As working through the wrapper, so callers can
// still match on the concrete error type.
func (e *redactedError) Unwrap() error { return e.err }

// Err wraps err so that logging it cannot publish a URL-borne credential.
// Returns nil for a nil error, so it is safe to apply unconditionally.
func Err(err error) error {
	if err == nil {
		return nil
	}
	return &redactedError{err: err}
}
