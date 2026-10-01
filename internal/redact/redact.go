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
//
// BF4DB is a variant of the same leak with a different payload: the searched
// player name or IP sits directly in the request path/query, not in a header,
// so a transport failure during a search logs it right alongside the
// query_kind field callers use specifically to keep names and IPs out of logs.
package redact

import "regexp"

// Each pattern keeps the parts that are useful in a log (a recognizable
// prefix, and sometimes a suffix) and masks the searched-for value that sits
// between them. Most patterns mask a credential; the last two mask a player
// name or IP, which BF4DB puts straight into the request path/query instead
// of a header — the same shape of leak, different kind of secret.
var patterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	// Discord interaction callback: /interactions/{id}/{token}/callback
	{regexp.MustCompile(`(/interactions/\d+/)[^/\s"]+`), "${1}REDACTED"},
	// Discord interaction followup/edit: /webhooks/{app_id}/{token}/messages/@original
	{regexp.MustCompile(`(/webhooks/\d+/)[^/?\s"]+`), "${1}REDACTED"},
	// Uptime Kuma dead-man switch: /api/push/{token}
	{regexp.MustCompile(`(/api/push/)[^/?\s"]+`), "${1}REDACTED"},
	// BF4DB search-by-name/IP: /player/{name-or-ip}/search. The /search suffix
	// has to be kept in the replacement: without it, this pattern would also
	// eat the harmless /player/{id} route (no /search after it).
	{regexp.MustCompile(`(/player/)[^/\s"]+(/search)`), "${1}REDACTED${2}"},
	// Website search-page fallback: /player/search?query={name}, percent-encoded.
	{regexp.MustCompile(`([?&]query=)[^&\s"]+`), "${1}REDACTED"},
}

// String masks every known secret-bearing URL segment in s.
func String(s string) string {
	for _, p := range patterns {
		s = p.re.ReplaceAllString(s, p.repl)
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
