package bf4db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is the public BF4DB API root. Override it with the
// BF4DB_BASE_URL environment variable or WithBaseURL (useful behind a proxy).
const DefaultBaseURL = "https://bf4db.com/api"

const (
	defaultTimeout    = 20 * time.Second
	defaultMaxRetries = 3
	defaultRetryWait  = 60 * time.Second
	defaultMaxPages   = 20
	maxBodyBytes      = 8 << 20 // far above any real response
	bodySnippetLimit  = 240
	userAgent         = "BF4DB-Search-Tool"
)

var (
	// ErrMissingToken is returned by New when no API token is supplied.
	ErrMissingToken = errors.New("bf4db: missing API token")

	// ErrNameSearchUnavailable reports that neither by-name route worked: the
	// API endpoint answers HTTP 500 for every name (verified 2026-07-26) and
	// the website fallback failed or was disabled.
	ErrNameSearchUnavailable = errors.New("bf4db: name search is unavailable (the API answers HTTP 500 for every name)")
)

// APIError is a non-2xx response from BF4DB.
type APIError struct {
	StatusCode int
	Status     string
	Body       string // truncated and token-redacted
}

func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("bf4db: unexpected response %s", e.Status)
	}
	return fmt.Sprintf("bf4db: unexpected response %s: %s", e.Status, e.Body)
}

// Unauthorized reports whether the token was rejected.
func (e *APIError) Unauthorized() bool {
	return e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden
}

// NotFound reports whether the resource does not exist.
func (e *APIError) NotFound() bool { return e.StatusCode == http.StatusNotFound }

// RateLimit is the quota BF4DB last reported through its X-RateLimit headers.
type RateLimit struct {
	Limit     int
	Remaining int
	Seen      time.Time
}

// Client talks to the BF4DB API. It is safe for concurrent use.
type Client struct {
	baseURL     *url.URL
	webBaseURL  *url.URL
	token       string
	httpClient  *http.Client
	maxRetries  int
	retryWait   time.Duration
	maxPages    int
	nameLimit   int
	webFallback bool
	notify      func(format string, args ...any)

	mu        sync.Mutex
	rateLimit RateLimit
}

// Option customizes a Client.
type Option func(*Client) error

// WithBaseURL points the client at a different API root.
func WithBaseURL(raw string) Option {
	return func(c *Client) error {
		if strings.TrimSpace(raw) == "" {
			return nil
		}
		u, err := url.Parse(strings.TrimRight(raw, "/"))
		if err != nil {
			return fmt.Errorf("bf4db: invalid base URL: %w", err)
		}
		c.baseURL = u
		return nil
	}
}

// WithWebBaseURL points the by-name fallback at a different site root.
func WithWebBaseURL(raw string) Option {
	return func(c *Client) error {
		if strings.TrimSpace(raw) == "" {
			return nil
		}
		u, err := url.Parse(strings.TrimRight(raw, "/"))
		if err != nil {
			return fmt.Errorf("bf4db: invalid web base URL: %w", err)
		}
		c.webBaseURL = u
		return nil
	}
}

// WithWebFallback enables or disables resolving names through the website when
// the API's by-name endpoint fails. Enabled by default.
func WithWebFallback(enabled bool) Option {
	return func(c *Client) error {
		c.webFallback = enabled
		return nil
	}
}

// WithNameLimit caps how many name-search hits are hydrated through the API.
func WithNameLimit(n int) Option {
	return func(c *Client) error {
		if n > 0 {
			c.nameLimit = n
		}
		return nil
	}
}

// WithHTTPClient replaces the underlying *http.Client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) error {
		if hc != nil {
			c.httpClient = hc
		}
		return nil
	}
}

// WithTimeout sets the per-request timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) error {
		if d > 0 {
			c.httpClient.Timeout = d
		}
		return nil
	}
}

// WithMaxRetries caps how many extra attempts a retryable failure gets.
func WithMaxRetries(n int) Option {
	return func(c *Client) error {
		if n >= 0 {
			c.maxRetries = n
		}
		return nil
	}
}

// WithRetryWait sets the base delay used when the server sends no Retry-After.
func WithRetryWait(d time.Duration) Option {
	return func(c *Client) error {
		if d > 0 {
			c.retryWait = d
		}
		return nil
	}
}

// WithMaxPages caps how many pages a paginated search follows.
func WithMaxPages(n int) Option {
	return func(c *Client) error {
		if n > 0 {
			c.maxPages = n
		}
		return nil
	}
}

// WithNotifier receives human-readable progress messages (rate limits, retries).
func WithNotifier(fn func(format string, args ...any)) Option {
	return func(c *Client) error {
		if fn != nil {
			c.notify = fn
		}
		return nil
	}
}

// New builds a Client for the given API token.
func New(token string, opts ...Option) (*Client, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, ErrMissingToken
	}
	base, err := url.Parse(DefaultBaseURL)
	if err != nil {
		return nil, err
	}
	webBase, err := url.Parse(DefaultWebBaseURL)
	if err != nil {
		return nil, err
	}
	c := &Client{
		baseURL:     base,
		webBaseURL:  webBase,
		token:       token,
		httpClient:  &http.Client{Timeout: defaultTimeout},
		maxRetries:  defaultMaxRetries,
		retryWait:   defaultRetryWait,
		maxPages:    defaultMaxPages,
		nameLimit:   defaultNameLimit,
		webFallback: true,
		notify:      func(string, ...any) {},
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// RateLimit returns the last quota BF4DB reported, zero-valued until the first
// request completes.
func (c *Client) RateLimit() RateLimit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rateLimit
}

// SearchIP returns every player BF4DB has seen on an IP address, following
// pagination up to the configured page cap.
func (c *Client) SearchIP(ctx context.Context, ip string) ([]Player, error) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return nil, errors.New("bf4db: empty IP")
	}
	return c.searchPaged(ctx, requestOptions{}, "player", ip, "search")
}

// SearchName looks a player up by name.
//
// The API route is tried first and its HTTP 500 is not retried for minutes.
// When it fails, the website's own search page is used instead and every hit is
// hydrated back through the API, which is what makes by-name search work at all
// today. Disable that with WithWebFallback(false).
func (c *Client) SearchName(ctx context.Context, name string) ([]Player, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("bf4db: empty player name")
	}
	players, err := c.searchPaged(ctx, requestOptions{noRetryServerError: true}, "player", name, "search")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		return players, err
	}

	if !c.webFallback {
		return nil, fmt.Errorf("%w: %v", ErrNameSearchUnavailable, apiErr)
	}
	c.notify("The API's by-name endpoint is down (%s); falling back to %s", apiErr.Status, c.webBaseURL.Host)

	players, webErr := c.searchNameWeb(ctx, name, c.nameLimit)
	if webErr != nil {
		return nil, fmt.Errorf("%w: API said %v; website fallback: %v", ErrNameSearchUnavailable, apiErr, webErr)
	}
	return players, nil
}

// SearchDiscord returns the BF4 accounts linked to a Discord user id.
func (c *Client) SearchDiscord(ctx context.Context, discordID string) ([]Player, error) {
	discordID = strings.TrimSpace(discordID)
	if discordID == "" {
		return nil, errors.New("bf4db: empty Discord id")
	}
	if _, err := strconv.ParseUint(discordID, 10, 64); err != nil {
		return nil, fmt.Errorf("bf4db: %q is not a Discord user id", discordID)
	}
	// The upstream route really is "{id}discordAccount/discord"; a slash 404s.
	res, err := c.get(ctx, requestOptions{}, c.endpoint(nil, "player", discordID+"discordAccount", "discord"))
	if err != nil {
		return nil, err
	}
	var parsed listResponse
	if err := c.decode(res, &parsed); err != nil {
		return nil, err
	}
	return parsed.Data, nil
}

// Player fetches a single player by persona id.
func (c *Client) Player(ctx context.Context, personaID string) (Player, error) {
	return c.player(ctx, personaID, requestOptions{})
}

func (c *Client) player(ctx context.Context, personaID string, opts requestOptions) (Player, error) {
	personaID = strings.TrimSpace(personaID)
	if _, err := strconv.ParseUint(personaID, 10, 64); err != nil {
		return Player{}, fmt.Errorf("bf4db: %q is not a player id", personaID)
	}
	res, err := c.get(ctx, opts, c.endpoint(nil, "player", personaID))
	if err != nil {
		return Player{}, err
	}
	var parsed singleResponse
	if err := c.decode(res, &parsed); err != nil {
		return Player{}, err
	}
	if parsed.Data.PersonaID() == 0 {
		parsed.Data.PlayerID = FlexInt(mustInt(personaID))
	}
	return parsed.Data, nil
}

func (c *Client) searchPaged(ctx context.Context, opts requestOptions, parts ...string) ([]Player, error) {
	var all []Player
	for page := 1; page <= c.maxPages; page++ {
		query := url.Values{}
		if page > 1 {
			query.Set("page", strconv.Itoa(page))
		}
		res, err := c.get(ctx, opts, c.endpoint(query, parts...))
		if err != nil {
			return all, err
		}
		var parsed listResponse
		if err := c.decode(res, &parsed); err != nil {
			return all, err
		}
		all = append(all, parsed.Data...)

		last := int(parsed.Meta.LastPage)
		if len(parsed.Data) == 0 || last <= page {
			break
		}
		if page == c.maxPages {
			c.notify("Stopped at page %d of %d (raise -pages to fetch the rest)", c.maxPages, last)
		}
	}
	return all, nil
}

func (c *Client) endpoint(query url.Values, parts ...string) string {
	u := c.baseURL.JoinPath(parts...)
	if query == nil {
		query = url.Values{}
	}
	query.Set("api_token", c.token)
	u.RawQuery = query.Encode()
	return u.String()
}

// requestOptions tweaks retry behaviour for a single call.
type requestOptions struct {
	noRetryServerError bool
	// attempts overrides the client-wide retry budget; 0 keeps it.
	attempts int
}

// get performs the request with retries and returns the raw body.
func (c *Client) get(ctx context.Context, opts requestOptions, endpoint string) ([]byte, error) {
	var lastErr error

	maxRetries := c.maxRetries
	if opts.attempts > 0 {
		maxRetries = opts.attempts - 1
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			wait := retryDelay(lastErr, c.retryWait, attempt)
			// If the caller's deadline can't outlast the backoff, sleeping
			// only buys ctx.Err() instead of the real failure. Return it now
			// so a short-lived caller (e.g. autocomplete's 2s budget) gets an
			// actual error instead of a generic timeout after a dead wait.
			if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= wait {
				return nil, lastErr
			}
			c.notify("Retrying in %s (attempt %d/%d)", wait.Round(time.Second), attempt+1, maxRetries+1)
			if err := sleepCtx(ctx, wait); err != nil {
				return nil, err
			}
		}

		body, err := c.attempt(ctx, opts, endpoint)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !retryable(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("bf4db: gave up after %d attempts: %w", maxRetries+1, lastErr)
}

func (c *Client) attempt(ctx context.Context, opts requestOptions, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, c.redact(err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &retryableError{err: c.redact(err)}
	}
	defer res.Body.Close()

	c.recordRateLimit(res.Header)
	body, readErr := io.ReadAll(io.LimitReader(res.Body, maxBodyBytes))

	if res.StatusCode != http.StatusOK {
		apiErr := &APIError{
			StatusCode: res.StatusCode,
			Status:     res.Status,
			Body:       redactToken(snippet(body), c.token),
		}
		after, hasAfter := parseRetryAfter(res.Header.Get("Retry-After"))
		switch {
		case res.StatusCode == http.StatusTooManyRequests:
			c.notify("Rate limited by BF4DB (%s)", res.Status)
			return nil, &retryableError{err: apiErr, after: after, hasAfter: hasAfter}
		case res.StatusCode >= http.StatusInternalServerError && !opts.noRetryServerError:
			return nil, &retryableError{err: apiErr, after: after, hasAfter: hasAfter}
		default:
			return nil, apiErr
		}
	}
	if readErr != nil {
		return nil, &retryableError{err: fmt.Errorf("bf4db: reading response: %w", c.redact(readErr))}
	}
	return body, nil
}

func (c *Client) decode(body []byte, out any) error {
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("bf4db: invalid JSON response (an HTML page here usually means a rejected API token): %w\nbody: %s",
			err, redactToken(snippet(body), c.token))
	}
	return nil
}

func (c *Client) recordRateLimit(h http.Header) {
	limit, errLimit := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	remaining, errRemaining := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	if errLimit != nil && errRemaining != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if errLimit == nil {
		c.rateLimit.Limit = limit
	}
	if errRemaining == nil {
		c.rateLimit.Remaining = remaining
	}
	c.rateLimit.Seen = time.Now()
}

// retryableError marks a failure worth another attempt, optionally carrying a
// server-supplied delay. hasAfter distinguishes "Retry-After: 0" (retry now)
// from a missing header.
type retryableError struct {
	err      error
	after    time.Duration
	hasAfter bool
}

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

func retryable(err error) bool {
	var re *retryableError
	return errors.As(err, &re)
}

func retryDelay(err error, base time.Duration, attempt int) time.Duration {
	var re *retryableError
	if errors.As(err, &re) && re.hasAfter {
		return re.after
	}
	// Exponential backoff: base, 2×base, 4×base…
	wait := base << (attempt - 1)
	if wait <= 0 || wait > 10*time.Minute {
		return 10 * time.Minute
	}
	return wait
}

// parseRetryAfter reads the Retry-After header in both its delay-seconds and
// HTTP-date forms. ok is false when the header is absent or unparseable.
func parseRetryAfter(header string) (d time.Duration, ok bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(header); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if when, err := http.ParseTime(header); err == nil {
		if d := time.Until(when); d > 0 {
			return d, true
		}
		return 0, true // the date already passed: retry now
	}
	return 0, false
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// redactedError hides the API token from messages produced by net/http, which
// echo the full request URL.
type redactedError struct {
	err   error
	token string
}

func (e *redactedError) Error() string { return redactToken(e.err.Error(), e.token) }
func (e *redactedError) Unwrap() error { return e.err }

func (c *Client) redact(err error) error {
	if err == nil {
		return nil
	}
	return &redactedError{err: err, token: c.token}
}

var tokenParamRe = regexp.MustCompile(`(?i)(api_token=)[^&\s"'<>]+`)

func redactToken(s, token string) string {
	if token != "" {
		s = strings.ReplaceAll(s, token, "REDACTED")
	}
	return tokenParamRe.ReplaceAllString(s, "${1}REDACTED")
}

func snippet(body []byte) string {
	s := strings.Join(strings.Fields(string(body)), " ")
	if len(s) > bodySnippetLimit {
		return s[:bodySnippetLimit] + "…"
	}
	return s
}

func mustInt(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
