package bot

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

type refusingTransport struct{}

func (refusingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	// net/http wraps whatever a transport returns in a *url.Error, and that
	// type's Error() embeds the FULL request URL — including the interaction
	// token InteractionResponseEdit puts in the path.
	return nil, errors.New("connection refused")
}

// TestEditNeverLogsTheInteractionToken is the wiring test, not a unit test of
// the redactor: every /bf4db search ends in bot.edit (commands.go defers, then
// bot.go:edit completes the response), and it logs at Error — visible at
// production's LOG_LEVEL=info. discordgo's InteractionResponseEdit hits
// /webhooks/{app_id}/{token}/messages/@original, a path redact.go's patterns
// didn't cover until this fix.
func TestEditNeverLogsTheInteractionToken(t *testing.T) {
	const token = "INTERACTIONTOK"

	b, buf := newTestBotWithLogs()

	s, err := discordgo.New("Bot token-de-teste")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	s.Client = &http.Client{Transport: refusingTransport{}}

	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		AppID: "1027015041326788659",
		Token: token,
	}}

	b.edit(s, i, nil, nil)

	out := buf.String()
	if strings.Contains(out, token) {
		t.Errorf("the interaction token reached the log: %s", out)
	}
	// Positive control: without it, this test would also pass if the log line
	// vanished entirely or edit stopped running — "the secret is absent" and
	// "nothing happened" look identical otherwise.
	if !strings.Contains(out, "editing response") {
		t.Fatalf("the failure was not logged at all, so the assertion above proves nothing: %s", out)
	}
	if !strings.Contains(out, "/webhooks/1027015041326788659/REDACTED") {
		t.Errorf("expected the endpoint prefix kept and the token masked: %s", out)
	}
}
