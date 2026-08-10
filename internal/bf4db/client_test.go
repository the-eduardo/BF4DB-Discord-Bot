package bf4db

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newTestClient(t *testing.T, h http.Handler, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	opts = append([]Option{
		WithBaseURL(srv.URL + "/api"),
		// Point the website fallback at a dead port: no test may reach the
		// real bf4db.com by accident.
		WithWebBaseURL("http://127.0.0.1:1"),
		WithRetryWait(time.Millisecond),
		WithTimeout(2 * time.Second),
	}, opts...)

	c, err := New(token, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNewRequiresToken(t *testing.T) {
	if _, err := New("   "); !errors.Is(err, ErrMissingToken) {
		t.Fatalf("want ErrMissingToken, got %v", err)
	}
}

func TestSearchIPBuildsRequestAndParses(t *testing.T) {
	var gotPath, gotToken, gotAccept, gotUA string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotToken = r.URL.Path, r.URL.Query().Get("api_token")
		gotAccept, gotUA = r.Header.Get("Accept"), r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		// Shape copied from the live API.
		_, _ = w.Write([]byte(`{"data":[
			{"name":"Savork","avatar":"https://x/a.png","is_banned":1,"ban_reason":"Aimbot",
			 "cheat_score":100,"created_at":"2020-04-23T23:53:05.000000Z",
			 "updated_at":"2026-07-17T14:09:03.000000Z","id":1008648998234},
			{"player_id":"1004418722330","name":"upshot_knothoIe","is_banned":-1,
			 "ban_reason":"Not reported","cheat_score":"0","created_at":"2020-04-23 23:53:05"}
		],"meta":{"current_page":1,"last_page":1,"per_page":50,"total":2}}`))
	}))

	players, err := c.SearchIP(context.Background(), "1.1.1.1")
	if err != nil {
		t.Fatalf("SearchIP: %v", err)
	}
	if want := "/api/player/1.1.1.1/search"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotToken != token {
		t.Error("api_token not sent")
	}
	if gotAccept != "application/json" || gotUA != userAgent {
		t.Errorf("headers = %q / %q", gotAccept, gotUA)
	}
	if len(players) != 2 {
		t.Fatalf("got %d players, want 2", len(players))
	}

	banned := players[0]
	if banned.PersonaID() != 1008648998234 || !banned.Banned() || banned.Status() != "banned" {
		t.Errorf("unexpected banned player: %+v", banned)
	}
	if banned.CheatScore != 100 || banned.Reason() != "Aimbot" || banned.CreatedAt.IsZero() {
		t.Errorf("unexpected fields: %+v", banned)
	}

	other := players[1]
	if other.PersonaID() != 1004418722330 {
		t.Errorf("PersonaID = %d, want fallback to player_id", other.PersonaID())
	}
	if other.Banned() || other.Status() != "not reported" {
		t.Errorf("is_banned=-1 should be 'not reported', got %q", other.Status())
	}
	if other.CheatScore != 0 || other.CreatedAt.IsZero() {
		t.Errorf("string cheat_score / space-separated date not handled: %+v", other)
	}
}

// TestBanStatusMapping pins BF4DB's is_banned table. Codes -1/0/1/2 were also
// verified live: 1004843590869=-1, 1008720581702=0, 815195001=1, 988768601=2.
func TestBanStatusMapping(t *testing.T) {
	cases := map[int]struct {
		status string
		banned bool
	}{
		BanNotReported: {"not reported", false},
		BanUnderReview: {"under review", false},
		BanActive:      {"banned", true},
		BanNone:        {"clean", false},
		BanStaff:       {"staff member", false},
		BanGlitch:      {"glitch", false},
		BanExploit:     {"exploit", false},
		9:              {"unknown (9)", false},
	}
	for code, want := range cases {
		p := Player{IsBanned: FlexInt(code)}
		if p.Status() != want.status || p.Banned() != want.banned {
			t.Errorf("is_banned=%d -> %q/%v, want %q/%v", code, p.Status(), p.Banned(), want.status, want.banned)
		}
	}

	// An empty ban_reason falls back to the status, not to a hardcoded
	// "Under review" that would mislabel every other code.
	for code, want := range map[int]string{
		BanUnderReview: "Under review",
		BanNotReported: "Not reported",
		BanStaff:       "Staff member",
		BanGlitch:      "Glitch",
	} {
		if got := (Player{IsBanned: FlexInt(code), BanReason: "  "}).Reason(); got != want {
			t.Errorf("is_banned=%d empty reason = %q, want %q", code, got, want)
		}
	}
	if got := (Player{IsBanned: BanActive, BanReason: "Aimbot"}).Reason(); got != "Aimbot" {
		t.Errorf("reason = %q, want the API value", got)
	}
}

func TestSearchIPFollowsPagination(t *testing.T) {
	var pages atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pages.Add(1)
		switch page {
		case "", "1":
			_, _ = fmt.Fprint(w, `{"data":[{"id":1,"name":"p1"}],"meta":{"current_page":1,"last_page":3}}`)
		case "2":
			_, _ = fmt.Fprint(w, `{"data":[{"id":2,"name":"p2"}],"meta":{"current_page":2,"last_page":3}}`)
		case "3":
			_, _ = fmt.Fprint(w, `{"data":[{"id":3,"name":"p3"}],"meta":{"current_page":3,"last_page":3}}`)
		default:
			t.Errorf("unexpected page %q", page)
		}
	}))

	players, err := c.SearchIP(context.Background(), "1.1.1.1")
	if err != nil {
		t.Fatalf("SearchIP: %v", err)
	}
	if len(players) != 3 || pages.Load() != 3 {
		t.Fatalf("got %d players over %d requests, want 3/3", len(players), pages.Load())
	}
}

func TestSearchIPRespectsPageCap(t *testing.T) {
	var pages atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages.Add(1)
		_, _ = fmt.Fprint(w, `{"data":[{"id":1,"name":"p"}],"meta":{"current_page":1,"last_page":99}}`)
	}), WithMaxPages(2))

	players, err := c.SearchIP(context.Background(), "1.1.1.1")
	if err != nil {
		t.Fatalf("SearchIP: %v", err)
	}
	if pages.Load() != 2 || len(players) != 2 {
		t.Errorf("pages = %d, players = %d, want 2/2", pages.Load(), len(players))
	}
}

func TestSearchDiscordUsesUpstreamConcatenatedPath(t *testing.T) {
	var gotPath string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = fmt.Fprint(w, `{"data":[{"player_id":988768601,"name":"EdUwUardo","is_banned":2,
			"ban_reason":"No Active BF4 Ban"}],"updated_at":"2026-07-26T06:23:53.957095Z"}`)
	}))

	if _, err := c.SearchDiscord(context.Background(), "not-an-id"); err == nil {
		t.Error("want error for non-numeric Discord id")
	}

	players, err := c.SearchDiscord(context.Background(), "274247581801119745")
	if err != nil {
		t.Fatalf("SearchDiscord: %v", err)
	}
	// Verified upstream: inserting a slash before discordAccount returns 404.
	if want := "/api/player/274247581801119745discordAccount/discord"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if len(players) != 1 || players[0].Status() != "clean" {
		t.Errorf("unexpected players: %+v", players)
	}
}

func TestPlayerLookupParsesObjectPayload(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/player/988768601" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"data":{"player_id":988768601,"name":"EdUwUardo","is_banned":2,
			"ban_reason":"No Active BF4 Ban","ea_guid":"EA_D7D74","cheat_score":0}}`)
	}))

	player, err := c.Player(context.Background(), "988768601")
	if err != nil {
		t.Fatalf("Player: %v", err)
	}
	if player.PersonaID() != 988768601 || player.Name != "EdUwUardo" || player.EaGUID != "EA_D7D74" {
		t.Errorf("unexpected player: %+v", player)
	}
	if _, err := c.Player(context.Background(), "abc"); err == nil {
		t.Error("want error for non-numeric player id")
	}
}

func TestSearchNameDoesNotRetryTheAPIOutage(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"message":"Server Error"}`)
	}), WithRetryWait(time.Hour), WithWebFallback(false))

	_, err := c.SearchName(context.Background(), "Ranger")
	if !errors.Is(err, ErrNameSearchUnavailable) {
		t.Fatalf("want ErrNameSearchUnavailable, got %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (500 on name search must not be retried)", calls.Load())
	}
}

func TestServerErrorsAreRetriedForOtherLookups(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":1,"name":"ok"}],"meta":{"last_page":1}}`)
	}))

	players, err := c.SearchIP(context.Background(), "1.1.1.1")
	if err != nil {
		t.Fatalf("SearchIP: %v", err)
	}
	if calls.Load() != 2 || len(players) != 1 {
		t.Errorf("calls = %d, players = %d, want 2/1", calls.Load(), len(players))
	}
}

func TestRetriesOnRateLimitAndHonorsRetryAfter(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":1,"name":"After Retry"}],"meta":{"last_page":1}}`)
	}), WithRetryWait(time.Hour)) // must not be used: Retry-After wins

	start := time.Now()
	players, err := c.SearchIP(context.Background(), "1.1.1.1")
	if err != nil {
		t.Fatalf("SearchIP: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Retry-After ignored, waited %s", elapsed)
	}
	if calls.Load() != 2 || len(players) != 1 {
		t.Errorf("calls = %d, players = %d, want 2/1", calls.Load(), len(players))
	}
}

func TestGivesUpAfterMaxRetries(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}), WithMaxRetries(2))

	_, err := c.SearchIP(context.Background(), "1.1.1.1")
	if err == nil {
		t.Fatal("want error")
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3 (1 + 2 retries)", calls.Load())
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("want wrapped APIError 503, got %v", err)
	}
}

func TestUnauthorizedAndNotFoundAreNotRetried(t *testing.T) {
	for _, tc := range []struct {
		name  string
		code  int
		check func(*APIError) bool
	}{
		{"unauthorized", http.StatusUnauthorized, (*APIError).Unauthorized},
		{"forbidden", http.StatusForbidden, (*APIError).Unauthorized},
		{"not found", http.StatusNotFound, (*APIError).NotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.code)
				_, _ = fmt.Fprint(w, `{"message":"nope"}`)
			}))

			_, err := c.SearchIP(context.Background(), "1.1.1.1")
			var apiErr *APIError
			if !errors.As(err, &apiErr) || !tc.check(apiErr) {
				t.Fatalf("want classified APIError, got %v", err)
			}
			if calls.Load() != 1 {
				t.Errorf("calls = %d, want 1", calls.Load())
			}
		})
	}
}

func TestRateLimitHeadersAreRecorded(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "70")
		w.Header().Set("X-RateLimit-Remaining", "62")
		_, _ = fmt.Fprint(w, `{"data":[],"meta":{"last_page":1}}`)
	}))

	if rl := c.RateLimit(); !rl.Seen.IsZero() {
		t.Error("rate limit should be empty before the first request")
	}
	if _, err := c.SearchIP(context.Background(), "1.1.1.1"); err != nil {
		t.Fatalf("SearchIP: %v", err)
	}
	rl := c.RateLimit()
	if rl.Limit != 70 || rl.Remaining != 62 || rl.Seen.IsZero() {
		t.Errorf("rate limit = %+v, want 70/62 with a timestamp", rl)
	}
}

func TestHTMLResponseYieldsHelpfulRedactedError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html><body>invalid api_token="+token+"</body></html>")
	}))

	_, err := c.SearchIP(context.Background(), "1.1.1.1")
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "API token") {
		t.Errorf("error should hint at the token: %v", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Error("error leaks the API token")
	}
}

func TestTransportErrorsAreRedacted(t *testing.T) {
	c, err := New(token, WithBaseURL("http://127.0.0.1:1/api"), WithMaxRetries(0), WithTimeout(time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.SearchIP(context.Background(), "1.1.1.1")
	if err == nil {
		t.Fatal("want connection error")
	}
	if strings.Contains(err.Error(), token) {
		t.Error("transport error leaks the API token")
	}
	if !strings.Contains(err.Error(), "REDACTED") {
		t.Errorf("want REDACTED marker, got %v", err)
	}
}

func TestContextCancellationStopsRetries(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}), WithRetryWait(10*time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.SearchIP(ctx, "1.1.1.1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestRetryStopsWhenDeadlineIsShorterThanBackoff(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}), WithRetryWait(60*time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.player(ctx, "12345", requestOptions{})
	elapsed := time.Since(start)

	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (no point retrying past the deadline)", calls.Load())
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadGateway {
		t.Errorf("want wrapped APIError 502, got %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want the real 502, got context.DeadlineExceeded instead")
	}
	if elapsed >= time.Second {
		t.Errorf("took %s, want well under the 60s backoff (deadline check should short-circuit)", elapsed)
	}
}

func TestEmptyQueriesRejectedWithoutRequest(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made")
	}))
	if _, err := c.SearchIP(context.Background(), "  "); err == nil {
		t.Error("want error for empty IP")
	}
	if _, err := c.SearchName(context.Background(), ""); err == nil {
		t.Error("want error for empty name")
	}
	if _, err := c.SearchDiscord(context.Background(), ""); err == nil {
		t.Error("want error for empty Discord id")
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got, ok := parseRetryAfter("30"); got != 30*time.Second || !ok {
		t.Errorf("seconds form = %v, %v", got, ok)
	}
	if got, ok := parseRetryAfter("0"); got != 0 || !ok {
		t.Errorf(`"0" = %v, %v; want 0 with ok (retry immediately, not "no header")`, got, ok)
	}
	if _, ok := parseRetryAfter(""); ok {
		t.Error("empty header should not be ok")
	}
	if _, ok := parseRetryAfter("soon"); ok {
		t.Error("garbage header should not be ok")
	}
	if got, ok := parseRetryAfter("Mon, 02 Jan 2006 15:04:05 GMT"); got != 0 || !ok {
		t.Errorf("past date = %v, %v; want 0 with ok", got, ok)
	}
	future := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	if got, ok := parseRetryAfter(future); got < 30*time.Second || !ok {
		t.Errorf("future date = %v, %v; want ~90s", got, ok)
	}
}

// retryDelay must prefer the server's hint over the local backoff, including
// when that hint is zero. Getting this wrong hangs the client for a full
// backoff period, which is how this test earned its place.
func TestRetryDelayPrefersRetryAfter(t *testing.T) {
	server := &retryableError{err: errors.New("429"), after: 2 * time.Second, hasAfter: true}
	if got := retryDelay(server, time.Hour, 1); got != 2*time.Second {
		t.Errorf("delay = %v, want the server value 2s", got)
	}
	immediate := &retryableError{err: errors.New("429"), after: 0, hasAfter: true}
	if got := retryDelay(immediate, time.Hour, 1); got != 0 {
		t.Errorf("delay = %v, want 0 for Retry-After: 0", got)
	}
	local := &retryableError{err: errors.New("503")}
	if got := retryDelay(local, time.Second, 3); got != 4*time.Second {
		t.Errorf("delay = %v, want exponential 4s", got)
	}
	if got := retryDelay(local, time.Hour, 20); got != 10*time.Minute {
		t.Errorf("delay = %v, want the 10m cap", got)
	}
}

func TestLinks(t *testing.T) {
	now := time.Date(2026, 7, 26, 15, 4, 0, 0, time.UTC)
	if got, want := ProfileURL(42), "https://bf4db.com/player/42/"; got != want {
		t.Errorf("ProfileURL = %q, want %q", got, want)
	}
	if got, want := CheatReportURL(42, now), "https://bf4cheatreport.com/?pid=42&uid=&cnt=200&startdate=202607261504"; got != want {
		t.Errorf("CheatReportURL = %q, want %q", got, want)
	}
	if got, want := AgencyURL(42), "https://battlefield.agency/player/by-persona_id/bf4/42"; got != want {
		t.Errorf("AgencyURL = %q, want %q", got, want)
	}
}

func TestTimestampAndFlexIntEdgeCases(t *testing.T) {
	var ts Timestamp
	for _, in := range []string{`null`, `""`, `"2024-01-02"`, `"2024-01-02 03:04:05"`, `"2024-01-02T03:04:05Z"`, `"2026-07-17T14:09:03.000000Z"`} {
		if err := ts.UnmarshalJSON([]byte(in)); err != nil {
			t.Errorf("Timestamp(%s): %v", in, err)
		}
	}
	if err := ts.UnmarshalJSON([]byte(`"yesterday"`)); err == nil {
		t.Error("want error for unparseable timestamp")
	}
	if data, err := (Timestamp{}).MarshalJSON(); err != nil || string(data) != "null" {
		t.Errorf("zero Timestamp marshals to %q, %v", data, err)
	}

	var n FlexInt
	for in, want := range map[string]FlexInt{`5`: 5, `"5"`: 5, `null`: 0, `true`: 1, `false`: 0, `5.0`: 5, `-1`: -1} {
		if err := n.UnmarshalJSON([]byte(in)); err != nil || n != want {
			t.Errorf("FlexInt(%s) = %d, %v; want %d", in, n, err, want)
		}
	}
	if err := n.UnmarshalJSON([]byte(`"nope"`)); err == nil {
		t.Error("want error for unparseable number")
	}
}
