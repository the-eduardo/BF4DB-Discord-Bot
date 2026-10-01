package bot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
)

// TestHandleSearchNeverLogsTheSearchedIP is the wiring test, not a unit test
// of the redactor: internal/redact's own tests prove the pattern works in
// isolation, but handleSearch has its own defense against echoing an IP
// (query_kind, the "Busca por IP" title) — none of that stops the *url.Error
// a transport failure produces, since BF4DB puts the search value straight
// into the request path (/api/player/{ip}/search).
//
// The httptest server is closed before the call, not left answering 404: a
// 404 becomes an *APIError, which never carries a URL. Only a transport-level
// failure (connection refused here) produces the *url.Error that is the
// actual leak vector.
func TestHandleSearchNeverLogsTheSearchedIP(t *testing.T) {
	const ip = "203.0.113.77" // TEST-NET-3: never a real host, safe to assert on

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	srv.Close()

	b, buf := newTestBotWithLogs()
	client, err := bf4db.New(strings.Repeat("a", 64), bf4db.WithBaseURL(srv.URL+"/api"), bf4db.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("bf4db.New: %v", err)
	}
	b.client = client

	rt := &recordingTransport{}
	s, err := discordgo.New("Bot token-de-teste")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	s.Client = &http.Client{Transport: rt}

	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type:  discordgo.InteractionApplicationCommand,
		ID:    "1",
		AppID: "2",
		Token: "tok",
		Data: discordgo.ApplicationCommandInteractionData{
			Name: "bf4db",
			Options: []*discordgo.ApplicationCommandInteractionDataOption{
				{
					Name:  optionSearch,
					Type:  discordgo.ApplicationCommandOptionString,
					Value: ip,
				},
			},
		},
		Member: &discordgo.Member{Permissions: discordgo.PermissionManageServer},
	}}

	b.handleSearch(s, i)

	out := buf.String()
	if strings.Contains(out, ip) {
		t.Errorf("the searched IP reached the log: %s", out)
	}
	// Positive controls: without them, "the IP is absent" and "nothing got
	// logged at all" (or handleSearch silently stopped calling the client)
	// would look identical.
	if !strings.Contains(out, `"msg":"search failed"`) {
		t.Fatalf("the failure was not logged at all, so the assertion above proves nothing: %s", out)
	}
	if !strings.Contains(out, `"query_kind":"ip"`) {
		t.Errorf("expected query_kind=ip alongside the redacted error: %s", out)
	}
}
