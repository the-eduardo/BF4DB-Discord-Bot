package bot

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/cache"
)

// newTestBot builds a Bot with caches but no Discord session.
func newTestBot() *Bot {
	return &Bot{
		log:         slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		timeout:     5 * time.Second,
		lookups:     cache.New[[]bf4db.Player](time.Minute, 50),
		suggestions: cache.New[[]*discordgo.ApplicationCommandOptionChoice](time.Minute, 50),
		results:     cache.New[resultSet](time.Minute, 50),
	}
}

const suggestPage = `<table><tbody>
<tr><td class="player-td-image"><a href="/player/111"><img></a></td>
    <td class="player-td-name"><a href="/player/111"> eduardo </a></td><td class="pull-right"></td></tr>
<tr><td class="player-td-image"><a href="/player/222"><img></a></td>
    <td class="player-td-name"><a href="/player/222"> eduardo-chopao </a></td>
    <td class="pull-right"><a href="https://bf4db.com/player/ban/222" data-original-title="Aimbot">Banned</a></td></tr>
</tbody></table>`

func withWebStub(t *testing.T, b *Bot, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client, err := bf4db.New(strings.Repeat("a", 64), bf4db.WithWebBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	b.client = client
}

func TestSuggestBuildsChoicesWithPersonaIDs(t *testing.T) {
	b := newTestBot()
	var calls atomic.Int32
	withWebStub(t, b, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.URL.Query().Get("query"); got != "eduardo" {
			t.Errorf("query = %q", got)
		}
		fmt.Fprint(w, suggestPage)
	})

	choices := b.suggest("eduardo")
	if len(choices) != 2 {
		t.Fatalf("got %d choices, want 2", len(choices))
	}
	// The value is the persona id, so picking a suggestion becomes an exact
	// lookup instead of another name search.
	if choices[0].Value != "111" || choices[0].Name != "eduardo" {
		t.Errorf("first choice = %+v", choices[0])
	}
	if choices[1].Value != "222" || !strings.Contains(choices[1].Name, "banido (Aimbot)") {
		t.Errorf("second choice = %+v", choices[1])
	}

	// Repeat keystrokes must not hit bf4db.com again.
	if b.suggest("eduardo"); calls.Load() != 1 {
		t.Errorf("made %d requests, want 1 (second call should be cached)", calls.Load())
	}
	if b.suggest("EDUARDO"); calls.Load() != 1 {
		t.Errorf("cache should be case-insensitive, made %d requests", calls.Load())
	}
}

func TestSuggestSkipsQueriesNotWorthARequest(t *testing.T) {
	b := newTestBot()
	withWebStub(t, b, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made")
	})

	for _, q := range []string{"", "ed", "1.1.1.1", "988768601"} {
		if got := b.suggest(q); got != nil {
			t.Errorf("suggest(%q) = %+v, want nil", q, got)
		}
	}
}

func TestSuggestSurvivesAnOutage(t *testing.T) {
	b := newTestBot()
	withWebStub(t, b, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	if got := b.suggest("eduardo"); got != nil {
		t.Errorf("suggest during an outage = %+v, want nil", got)
	}
}

func TestChoiceLabelTruncation(t *testing.T) {
	long := bf4db.Player{Name: strings.Repeat("x", 200), IsBanned: bf4db.BanActive, BanReason: "Aimbot"}
	if got := truncate(choiceLabel(long), maxChoiceName); len([]rune(got)) > maxChoiceName {
		t.Errorf("choice label is %d chars, over Discord's %d", len([]rune(got)), maxChoiceName)
	}
}
