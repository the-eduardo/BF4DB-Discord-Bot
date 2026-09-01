package bf4db

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// DefaultWebBaseURL is the site root used for the by-name fallback.
const DefaultWebBaseURL = "https://bf4db.com"

// webSearchLimit is how many rows bf4db.com/player/search returns; the page
// ignores ?page, so this is the hard ceiling of a name search.
const webSearchLimit = 40

// defaultNameLimit caps how many hits get hydrated through the API.
const defaultNameLimit = 25

// suggestZeroRowStreak is how many consecutive zero-row SuggestNames replies
// it takes to log a warning, and thereafter the log fires again on every
// further multiple of the streak. Zero rows from a single suggestion is
// normal (a prefix with no real match), but a long run of them is the same
// layout-change signal searchNameWeb already logs on the by-name path — the
// one this autocomplete path lacks despite carrying far more traffic to
// bf4db.com. A permanent layout break never lands on the streak count again
// once it passes it, so warning only at n == streak would log exactly once
// and then go silent for as long as the container runs.
const suggestZeroRowStreak = 20

// hydrateWorkers is the parallelism used to turn ids into full records. Kept
// low so a name search cannot burn the 70 requests/minute quota.
const hydrateWorkers = 4

var (
	// Each result row carries the persona id and the display name.
	webRowRe  = regexp.MustCompile(`(?s)<tr.*?</tr>`)
	webNameRe = regexp.MustCompile(`(?s)<td class="player-td-name">.*?<a href="/player/(\d+)"\s*>\s*(.*?)\s*</a>`)
	// Banned rows carry a badge whose tooltip holds the ban reason.
	webBanRe = regexp.MustCompile(`(?s)/player/ban/\d+.*?data-original-title="([^"]*)"`)
)

// searchNameWeb resolves a player name through the website's search page.
//
// The API's own by-name route (/api/player/{name}/search) answers HTTP 500,
// while https://bf4db.com/player/search?query= works, so names are resolved
// there and each hit is then hydrated through the working API endpoint.
func (c *Client) searchNameWeb(ctx context.Context, name string, limit int) ([]Player, error) {
	if limit <= 0 {
		limit = defaultNameLimit
	}

	u := c.webBaseURL.JoinPath("player", "search")
	u.RawQuery = url.Values{"query": {name}}.Encode()

	body, err := c.get(ctx, requestOptions{}, u.String())
	if err != nil {
		return nil, fmt.Errorf("bf4db: name search through %s: %w", c.webBaseURL.Host, err)
	}

	hits := parseWebSearch(string(body))
	if len(hits) == 0 {
		// No status/format check above this: a 200 with a changed page layout
		// looks identical to a genuine "no matches" response. This is the only
		// signal that separates the two, since the caller sees "no results"
		// either way.
		c.log.Error("bf4db web search returned zero rows", "name", name, "body_len", len(body))
		return nil, nil
	}
	if len(hits) > limit {
		c.notify("Showing %d of %d matches (raise -limit for more)", limit, len(hits))
		hits = hits[:limit]
	}
	return c.hydrate(ctx, hits), nil
}

// SuggestNames returns the raw matches for a name straight from the website's
// search page, without hydrating them through the API.
//
// It exists for Discord autocomplete, which must answer within three seconds:
// one HTML request instead of one request per hit. The records carry only the
// name, the persona id and whether the site shows a ban badge.
func (c *Client) SuggestNames(ctx context.Context, name string, limit int) ([]Player, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	if limit <= 0 || limit > webSearchLimit {
		limit = webSearchLimit
	}

	u := c.webBaseURL.JoinPath("player", "search")
	u.RawQuery = url.Values{"query": {name}}.Encode()

	body, err := c.get(ctx, requestOptions{attempts: 1}, u.String())
	if err != nil {
		return nil, fmt.Errorf("bf4db: suggesting names through %s: %w", c.webBaseURL.Host, err)
	}

	hits := parseWebSearch(string(body))
	if len(hits) == 0 {
		// Zero rows on one suggestion is normal: a prefix with no real match
		// returns exactly that. Zero rows many times in a row is not — this is
		// the only layout-change detector on the highest-volume path to the
		// site; searchNameWeb's own detector only covers full name search,
		// which can go idle for days.
		if n := c.suggestMisses.Add(1); n >= suggestZeroRowStreak && n%suggestZeroRowStreak == 0 {
			c.log.Warn("bf4db suggest returned zero rows repeatedly", "consecutive", n, "body_len", len(body))
		}
	} else {
		c.suggestMisses.Store(0)
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// parseWebSearch extracts the result rows of the website's search page.
func parseWebSearch(page string) []Player {
	var (
		players []Player
		seen    = map[int]bool{}
	)
	for _, row := range webRowRe.FindAllString(page, -1) {
		match := webNameRe.FindStringSubmatch(row)
		if match == nil {
			continue
		}
		id, err := strconv.Atoi(match[1])
		if err != nil || id == 0 || seen[id] {
			continue
		}
		seen[id] = true

		player := Player{
			PlayerID: FlexInt(id),
			Name:     strings.TrimSpace(html.UnescapeString(match[2])),
			IsBanned: BanNotReported,
		}
		if ban := webBanRe.FindStringSubmatch(row); ban != nil {
			player.IsBanned = BanActive
			player.BanReason = strings.TrimSpace(html.UnescapeString(ban[1]))
		}
		players = append(players, player)
	}
	return players
}

// hydrate replaces each scraped stub with the full API record, keeping the
// stub when the lookup fails so a partial outage still returns useful rows.
func (c *Client) hydrate(ctx context.Context, stubs []Player) []Player {
	players := make([]Player, len(stubs))
	copy(players, stubs)

	var (
		wg   sync.WaitGroup
		work = make(chan int)
	)
	for range min(hydrateWorkers, len(stubs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				// Hydration is best effort: one attempt each, so a flaky API
				// degrades to the scraped row instead of stalling the search
				// for retries × backoff × results.
				full, err := c.player(ctx, strconv.Itoa(players[i].PersonaID()), requestOptions{attempts: 1})
				if err != nil || full.PersonaID() == 0 {
					continue
				}
				if full.Name == "" {
					full.Name = players[i].Name
				}
				players[i] = full
			}
		}()
	}
	for i := range players {
		if ctx.Err() != nil {
			break
		}
		work <- i
	}
	close(work)
	wg.Wait()

	return players
}
