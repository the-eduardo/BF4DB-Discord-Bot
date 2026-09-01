package bf4db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// searchPageFixture mirrors bf4db.com/player/search?query=eduardo: two links per
// row (avatar and name), a ban badge whose tooltip holds the reason, and HTML
// entities in a name.
const searchPageFixture = `<html><body><table><tbody>
<tr>
  <td class="player-td-image"><a href="/player/172015112"><img alt="eduardo"></a></td>
  <td class="player-td-name"><a href="/player/172015112">
      eduardo
  </a></td>
  <td class="pull-right"> </td>
</tr>
<tr>
  <td class="player-td-image"><a href="/player/1053283869"><img alt="eduardo-chopao"></a></td>
  <td class="player-td-name"><a href="/player/1053283869"> eduardo-chopao </a></td>
  <td class="pull-right">
    <a href="https://bf4db.com/player/ban/1053283869" data-toggle="tooltip"
       data-original-title="Aimbot" class="nk-btn">Banned</a>
  </td>
</tr>
<tr>
  <td class="player-td-image"><a href="/player/815195001"><img alt="Eduardo &amp; Co"></a></td>
  <td class="player-td-name"><a href="/player/815195001"> Eduardo &amp; Co </a></td>
  <td class="pull-right"> </td>
</tr>
<tr><td colspan="3">no player here</td></tr>
</tbody></table></body></html>`

func TestParseWebSearch(t *testing.T) {
	players := parseWebSearch(searchPageFixture)
	if len(players) != 3 {
		t.Fatalf("got %d players, want 3 (rows deduped, junk row skipped)", len(players))
	}
	if players[0].PersonaID() != 172015112 || players[0].Name != "eduardo" {
		t.Errorf("first row = %+v", players[0])
	}
	if players[0].Banned() {
		t.Error("row without a badge should not be banned")
	}
	if !players[1].Banned() || players[1].Reason() != "Aimbot" {
		t.Errorf("banned row = %+v, want the tooltip reason", players[1])
	}
	if players[2].Name != "Eduardo & Co" {
		t.Errorf("HTML entity not decoded: %q", players[2].Name)
	}
	if got := parseWebSearch("<html><body>nothing here</body></html>"); got != nil {
		t.Errorf("empty page = %+v, want nil", got)
	}
}

// newNameSearchClient wires an API stub and a website stub into one client.
func newNameSearchClient(t *testing.T, api http.Handler, web http.Handler, opts ...Option) *Client {
	t.Helper()
	apiSrv := httptest.NewServer(api)
	webSrv := httptest.NewServer(web)
	t.Cleanup(apiSrv.Close)
	t.Cleanup(webSrv.Close)

	opts = append([]Option{
		WithBaseURL(apiSrv.URL + "/api"),
		WithWebBaseURL(webSrv.URL),
	}, opts...)

	c, err := New(token, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestSearchNameFallsBackToWebsiteAndHydrates(t *testing.T) {
	var (
		searchCalls   atomic.Int32
		hydrateCalls  atomic.Int32
		gotWebPath    string
		gotWebQuery   string
		notifications []string
	)

	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search") {
			searchCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"message":"Server Error"}`)
			return
		}
		hydrateCalls.Add(1)
		id := strings.TrimPrefix(r.URL.Path, "/api/player/")
		// The API knows more than the page: real code, score and guid.
		_, _ = fmt.Fprintf(w, `{"data":{"player_id":%s,"name":"hydrated-%s","is_banned":4,
			"ban_reason":"Glitching","cheat_score":73,"ea_guid":"EA_X"}}`, id, id)
	})
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotWebPath, gotWebQuery = r.URL.Path, r.URL.Query().Get("query")
		_, _ = fmt.Fprint(w, searchPageFixture)
	})

	c := newNameSearchClient(t, api, web, WithNotifier(func(format string, args ...any) {
		notifications = append(notifications, fmt.Sprintf(format, args...))
	}))

	players, err := c.SearchName(context.Background(), "eduardo")
	if err != nil {
		t.Fatalf("SearchName: %v", err)
	}
	if searchCalls.Load() != 1 {
		t.Errorf("API search called %d times, want 1 (no retry storm)", searchCalls.Load())
	}
	if gotWebPath != "/player/search" || gotWebQuery != "eduardo" {
		t.Errorf("website request = %q?query=%q", gotWebPath, gotWebQuery)
	}
	if len(players) != 3 || hydrateCalls.Load() != 3 {
		t.Fatalf("got %d players from %d hydrations, want 3/3", len(players), hydrateCalls.Load())
	}
	// Order must survive the worker pool.
	if players[0].PersonaID() != 172015112 || players[2].PersonaID() != 815195001 {
		t.Errorf("result order changed: %+v", players)
	}
	// Hydrated fields must win over the scraped stub.
	if players[1].Status() != "glitch" || players[1].CheatScore != 73 || players[1].EaGUID != "EA_X" {
		t.Errorf("player not hydrated from the API: %+v", players[1])
	}
	if !strings.Contains(strings.Join(notifications, " "), "falling back") {
		t.Errorf("user was not told about the fallback: %v", notifications)
	}
}

// TestSearchNameFallsBackOnAnyServerError guards against a regression where
// the fallback only triggered on HTTP 500 exactly. BF4DB sits behind
// Cloudflare, which answers with 502/503/504 whenever the origin trips —
// attempt() (client.go) already treats every >=500 as one retryable class,
// and SearchName's fallback check must match that, or Cloudflare hiccups
// kill by-name search entirely instead of falling back to the website.
func TestSearchNameFallsBackOnAnyServerError(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var searchCalls atomic.Int32
			api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/search") {
					searchCalls.Add(1)
					w.WriteHeader(status)
					return
				}
				id := strings.TrimPrefix(r.URL.Path, "/api/player/")
				_, _ = fmt.Fprintf(w, `{"data":{"player_id":%s,"name":"p","is_banned":2}}`, id)
			})
			web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprint(w, searchPageFixture)
			})

			c := newNameSearchClient(t, api, web)
			players, err := c.SearchName(context.Background(), "eduardo")
			if err != nil {
				t.Fatalf("SearchName: %v", err)
			}
			if len(players) != 3 {
				t.Fatalf("got %d players, want 3 (fallback should have hydrated the scrape)", len(players))
			}
			if searchCalls.Load() != 1 {
				t.Errorf("API search called %d times, want 1 (no retry storm)", searchCalls.Load())
			}
		})
	}
}

func TestSearchNameKeepsStubWhenHydrationFails(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"message":"No query results"}`)
	})
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, searchPageFixture)
	})

	c := newNameSearchClient(t, api, web, WithMaxRetries(0))
	players, err := c.SearchName(context.Background(), "eduardo")
	if err != nil {
		t.Fatalf("SearchName: %v", err)
	}
	if len(players) != 3 {
		t.Fatalf("got %d players, want the 3 scraped stubs", len(players))
	}
	if players[1].Name != "eduardo-chopao" || !players[1].Banned() || players[1].Reason() != "Aimbot" {
		t.Errorf("stub lost when hydration failed: %+v", players[1])
	}
}

func TestSearchNameRespectsLimit(t *testing.T) {
	var hydrations atomic.Int32
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		hydrations.Add(1)
		id := strings.TrimPrefix(r.URL.Path, "/api/player/")
		_, _ = fmt.Fprintf(w, `{"data":{"player_id":%s,"name":"p","is_banned":2}}`, id)
	})
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, searchPageFixture)
	})

	c := newNameSearchClient(t, api, web, WithNameLimit(2))
	players, err := c.SearchName(context.Background(), "eduardo")
	if err != nil {
		t.Fatalf("SearchName: %v", err)
	}
	if len(players) != 2 || hydrations.Load() != 2 {
		t.Errorf("got %d players from %d hydrations, want 2/2", len(players), hydrations.Load())
	}
}

func TestSearchNameAPIOnlyReportsTheOutage(t *testing.T) {
	var webCalls atomic.Int32
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"message":"Server Error"}`)
	})
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		webCalls.Add(1)
	})

	c := newNameSearchClient(t, api, web, WithWebFallback(false))
	_, err := c.SearchName(context.Background(), "eduardo")
	if !errors.Is(err, ErrNameSearchUnavailable) {
		t.Fatalf("want ErrNameSearchUnavailable, got %v", err)
	}
	if webCalls.Load() != 0 {
		t.Error("website must not be contacted with the fallback disabled")
	}
}

func TestSearchNameReportsBothFailures(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	c := newNameSearchClient(t, api, web, WithMaxRetries(0))
	_, err := c.SearchName(context.Background(), "eduardo")
	if !errors.Is(err, ErrNameSearchUnavailable) {
		t.Fatalf("want ErrNameSearchUnavailable, got %v", err)
	}
	if !strings.Contains(err.Error(), "website fallback") {
		t.Errorf("error should mention both routes: %v", err)
	}
}

func TestSearchNameWebLogsWhenScrapeMatchesNothing(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A 200 whose layout no longer matches webRowRe/webNameRe: the exact
		// failure mode this log line exists to surface.
		_, _ = fmt.Fprint(w, `<html><body><div class="new-layout">no rows here</div></body></html>`)
	})

	c := newNameSearchClient(t, api, web, WithLogger(log))
	players, err := c.SearchName(context.Background(), "eduardo")
	if err != nil {
		t.Fatalf("SearchName: %v", err)
	}
	if players != nil {
		t.Errorf("players = %+v, want nil (unchanged behaviour)", players)
	}

	out := buf.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "bf4db web search returned zero rows") {
		t.Fatalf("expected an ERROR log line for the zero-row scrape, got: %q", out)
	}
	if !strings.Contains(out, "name=eduardo") {
		t.Errorf("log line missing name field: %q", out)
	}
	if !strings.Contains(out, "body_len=") {
		t.Errorf("log line missing body_len field: %q", out)
	}
}

func TestSearchNameWebDoesNotLogWhenScrapeFindsRows(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/player/")
		_, _ = fmt.Fprintf(w, `{"data":{"player_id":%s,"name":"p","is_banned":2}}`, id)
	})
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, searchPageFixture)
	})

	c := newNameSearchClient(t, api, web, WithLogger(log))
	if _, err := c.SearchName(context.Background(), "eduardo"); err != nil {
		t.Fatalf("SearchName: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no log output on a healthy scrape, got: %q", buf.String())
	}
}

// suggestClient wires only the website stub, since SuggestNames never touches
// the API route.
func suggestClient(t *testing.T, web http.Handler, opts ...Option) *Client {
	t.Helper()
	webSrv := httptest.NewServer(web)
	t.Cleanup(webSrv.Close)

	opts = append([]Option{WithWebBaseURL(webSrv.URL)}, opts...)
	c, err := New(token, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestSuggestNamesWarnsAfterConsecutiveZeroRows(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	// A 200 whose layout no longer matches webRowRe/webNameRe: the same
	// "changed layout" condition searchNameWeb detects on the by-name path,
	// simulated here since a real layout break cannot be provoked in a test.
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `<html><body><div class="new-layout">no rows here</div></body></html>`)
	})
	c := suggestClient(t, web, WithLogger(log))

	for i := 1; i < suggestZeroRowStreak; i++ {
		if _, err := c.SuggestNames(context.Background(), "eduardo", 0); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if buf.Len() != 0 {
			t.Fatalf("logged after only %d consecutive zero-row replies, want silence before %d: %s", i, suggestZeroRowStreak, buf.String())
		}
	}

	if _, err := c.SuggestNames(context.Background(), "eduardo", 0); err != nil {
		t.Fatalf("call %d: %v", suggestZeroRowStreak, err)
	}
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "bf4db suggest returned zero rows repeatedly") {
		t.Fatalf("expected a WARN log at the %dth consecutive zero-row reply, got: %q", suggestZeroRowStreak, out)
	}
	if !strings.Contains(out, fmt.Sprintf("consecutive=%d", suggestZeroRowStreak)) {
		t.Errorf("log line missing consecutive field: %q", out)
	}
}

func TestSuggestNamesZeroRowStreakResets(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	// Serves zero-row replies for everything except call number
	// suggestZeroRowStreak (a single hit right in the middle of two
	// near-streaks). Note this is NOT enough calls to trigger the warning on
	// its own: (streak-1) zero-rows, then a hit, then (streak-1) more
	// zero-rows never reaches 20-in-a-row UNLESS the hit failed to reset the
	// counter, in which case the second half's first miss lands exactly on
	// the old count and fires early. A version of this test that only ran
	// (streak-1) misses after the hit (without the (streak-1) misses before
	// it) would pass even with the reset silently removed — it wouldn't have
	// accumulated anything for the reset to matter.
	var calls atomic.Int32
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == suggestZeroRowStreak {
			_, _ = fmt.Fprint(w, searchPageFixture)
			return
		}
		_, _ = fmt.Fprint(w, `<html><body><div class="new-layout">no rows here</div></body></html>`)
	})
	c := suggestClient(t, web, WithLogger(log))

	total := 2*suggestZeroRowStreak - 1
	for i := 1; i <= total; i++ {
		if _, err := c.SuggestNames(context.Background(), "eduardo", 0); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	if buf.Len() != 0 {
		t.Errorf("expected no log (the hit at call %d should have reset the streak), got: %q", suggestZeroRowStreak, buf.String())
	}
}

// A permanent layout break never lands on n == suggestZeroRowStreak again
// once it passes it, so warning only at that exact count logs exactly once
// and then goes silent for as long as the container runs — indistinguishable
// from a healthy scraper. This proves the warning keeps firing on every
// further multiple of the streak instead.
func TestSuggestNamesKeepsWarningWhileTheStreakContinues(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `<html><body><div class="new-layout">no rows here</div></body></html>`)
	})
	c := suggestClient(t, web, WithLogger(log))

	for i := 1; i <= 2*suggestZeroRowStreak; i++ {
		if _, err := c.SuggestNames(context.Background(), "eduardo", 0); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	out := buf.String()
	if got := strings.Count(out, "bf4db suggest returned zero rows repeatedly"); got != 2 {
		t.Fatalf("esperava 2 avisos (em n=%d e n=%d) numa quebra permanente, veio %d: %q", suggestZeroRowStreak, 2*suggestZeroRowStreak, got, out)
	}
	if !strings.Contains(out, fmt.Sprintf("consecutive=%d", 2*suggestZeroRowStreak)) {
		t.Errorf("log line missing consecutive=%d: %q", 2*suggestZeroRowStreak, out)
	}
	if c.suggestMisses.Load() != int64(2*suggestZeroRowStreak) {
		t.Errorf("suggestMisses = %d, want %d", c.suggestMisses.Load(), 2*suggestZeroRowStreak)
	}
}

func TestSearchNameUsesAPIWhenItWorks(t *testing.T) {
	var webCalls atomic.Int32
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":[{"id":42,"name":"FromAPI","is_banned":2}],"meta":{"last_page":1}}`)
	})
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		webCalls.Add(1)
	})

	c := newNameSearchClient(t, api, web)
	players, err := c.SearchName(context.Background(), "eduardo")
	if err != nil {
		t.Fatalf("SearchName: %v", err)
	}
	if len(players) != 1 || players[0].Name != "FromAPI" {
		t.Errorf("players = %+v", players)
	}
	if webCalls.Load() != 0 {
		t.Error("website must not be used while the API works")
	}
}
