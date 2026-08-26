package bot

import (
	"bytes"
	"encoding/json"
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

// newTestBotWithLogs is like newTestBot but captures JSON log records at
// production's LOG_LEVEL=info, so tests can assert on what actually reaches
// prod logs instead of what's merely emitted at Debug and dropped.
func newTestBotWithLogs() (*Bot, *bytes.Buffer) {
	var buf bytes.Buffer
	b := &Bot{
		log:         slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})),
		timeout:     5 * time.Second,
		lookups:     cache.New[[]bf4db.Player](time.Minute, 50),
		suggestions: cache.New[[]*discordgo.ApplicationCommandOptionChoice](time.Minute, 50),
		results:     cache.New[resultSet](time.Minute, 50),
	}
	return b, &buf
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
	b, logs := newTestBotWithLogs()
	withWebStub(t, b, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	if got := b.suggest("eduardo"); got != nil {
		t.Errorf("suggest during an outage = %+v, want nil", got)
	}

	// LOG_LEVEL=info in production drops Debug entirely: a silent bf4db.com
	// block/outage and "nobody used autocomplete" would look identical unless
	// this failure is logged at WARN or above.
	if out := logs.String(); !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, "autocomplete lookup failed") {
		t.Errorf("expected a WARN log for the autocomplete failure, got: %s", out)
	}
}

func TestChoiceLabelTruncation(t *testing.T) {
	long := bf4db.Player{Name: strings.Repeat("x", 200), IsBanned: bf4db.BanActive, BanReason: "Aimbot"}
	if got := truncate(choiceLabel(long), maxChoiceName); len([]rune(got)) > maxChoiceName {
		t.Errorf("choice label is %d chars, over Discord's %d", len([]rune(got)), maxChoiceName)
	}
}

// suggestPageNoName mimics a scraped row where the name anchor is blank
// (<a href="/player/111"> </a>), which webNameRe still matches.
const suggestPageNoName = `<table><tbody>
<tr><td class="player-td-image"><a href="/player/111"><img></a></td>
    <td class="player-td-name"><a href="/player/111"> </a></td><td class="pull-right"></td></tr>
<tr><td class="player-td-image"><a href="/player/222"><img></a></td>
    <td class="player-td-name"><a href="/player/222"> </a></td>
    <td class="pull-right"><a href="https://bf4db.com/player/ban/222" data-original-title="Aimbot">Banned</a></td></tr>
</tbody></table>`

func TestSuggestNeverEmitsEmptyChoiceName(t *testing.T) {
	b := newTestBot()
	withWebStub(t, b, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, suggestPageNoName)
	})

	// Exercises the full chain (scraper -> suggest -> choiceLabel), not just
	// the formatter in isolation: Discord rejects the ENTIRE autocomplete
	// response with a 400 if any single choice has an empty name.
	got := b.suggest("eduardo")
	if len(got) != 2 {
		t.Fatalf("got %d choices, want 2", len(got))
	}
	// Pin the label instead of just "not empty": a fix that filtered the unnamed
	// row out, or replaced it with any other placeholder, would pass a
	// non-emptiness check while changing what the user actually sees. The banned
	// row covers the second branch of choiceLabel, where the " — banido" suffix
	// alone would already keep the name non-empty and hide a missed fix.
	if got[0].Name != "(sem nome)" {
		t.Errorf("unbanned label = %q, want %q", got[0].Name, "(sem nome)")
	}
	if got[1].Name != "(sem nome) — banido (Aimbot)" {
		t.Errorf("banned label = %q, want %q", got[1].Name, "(sem nome) — banido (Aimbot)")
	}
}

// failingTransport makes every Discord REST call fail at the transport layer,
// which is what an InteractionRespond rejection looks like from inside
// handleAutocomplete.
type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("discord rejected the autocomplete response")
}

func autocompleteInteraction(query string) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type:  discordgo.InteractionApplicationCommandAutocomplete,
		ID:    "1",
		AppID: "2",
		Token: "tok",
		Data: discordgo.ApplicationCommandInteractionData{
			Name: "bf4db",
			Options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: optionSearch, Type: discordgo.ApplicationCommandOptionString, Value: query, Focused: true},
			},
		},
	}}
}

// TestHandleAutocompleteLogsRejectionAtWarn is the wiring test for the log
// level, not for the formatter: a rejected InteractionRespond wipes out the
// whole choice list for the user, and at LOG_LEVEL=info (production) a Debug
// line is dropped entirely — "Discord refused every suggestion" and "nobody
// used autocomplete" would look identical in the logs. Same reasoning already
// applied to suggest() in 2.3.1; this covers the response path.
func TestHandleAutocompleteLogsRejectionAtWarn(t *testing.T) {
	b, logs := newTestBotWithLogs()

	s, err := discordgo.New("Bot token-de-teste")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	s.Client = &http.Client{Transport: failingTransport{}}

	// "ed" is under minAutocompleteLen, so suggest() returns without touching
	// the network: this isolates the response/logging path being tested.
	b.handleAutocomplete(s, autocompleteInteraction("ed"))

	// Assert on a SINGLE record, not two independent Contains: with the level
	// and the message checked separately, any unrelated WARN in the buffer
	// satisfies the first half while the message itself is emitted at a lower
	// level, and the test goes green on a demoted log.
	if !hasLogRecord(logs, "WARN", "autocomplete response failed") {
		t.Errorf("expected one WARN record with that exact message, got: %s", logs.String())
	}
}

// hasLogRecord reports whether the captured JSON log holds a record matching
// BOTH the level and the message.
func hasLogRecord(logs *bytes.Buffer, level, msg string) bool {
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var rec struct {
			Level string `json:"level"`
			Msg   string `json:"msg"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec.Level == level && rec.Msg == msg {
			return true
		}
	}
	return false
}

// TestHandleAutocompleteDoesNotLogTheInteractionToken is the wiring counterpart
// for the Discord side: InteractionRespond posts to
// /interactions/{id}/{token}/callback, and a transport-level failure produces a
// *url.Error whose Error() embeds that whole URL. Promoting this log to Warn
// (2.3.2) made it visible in production, so the token would reach Loki.
func TestHandleAutocompleteDoesNotLogTheInteractionToken(t *testing.T) {
	const token = "INTERACTIONTOKENSECRET"

	b, logs := newTestBotWithLogs()
	s, err := discordgo.New("Bot token-de-teste")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	s.Client = &http.Client{Transport: failingTransport{}}

	i := autocompleteInteraction("ed")
	i.Token = token
	b.handleAutocomplete(s, i)

	out := logs.String()
	if strings.Contains(out, token) {
		t.Errorf("the interaction token reached the log: %s", out)
	}
	// Positive control: proves the failure path actually ran.
	if !hasLogRecord(logs, "WARN", "autocomplete response failed") {
		t.Fatalf("the rejection was not logged, so the assertion above proves nothing: %s", out)
	}
}
