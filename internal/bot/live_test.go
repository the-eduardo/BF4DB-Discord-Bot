//go:build live

// Live checks against the real BF4DB API. Not part of CI:
//
//	BF4DB_API=<token> go test -tags live ./internal/bot -run TestLive -v
package bot

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
)

func liveBot(t *testing.T) *Bot {
	t.Helper()
	token := strings.TrimSpace(os.Getenv("BF4DB_API"))
	if token == "" {
		t.Skip("BF4DB_API not set")
	}
	client, err := bf4db.New(token, bf4db.WithNameLimit(5))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return &Bot{
		client:  client,
		log:     slog.New(slog.NewTextHandler(os.Stderr, nil)),
		timeout: 30 * time.Second,
	}
}

func TestLiveLookups(t *testing.T) {
	b := liveBot(t)

	cases := []struct {
		query      string
		wantStatus string
	}{
		{"eduardo", ""},             // name search: the feature that was broken
		{"Vanina22", "banido"},      // name -> banned
		{"VeimdaLancha", "análise"}, // name -> under review
		{"988768601", "limpo"},      // persona id
		{"1.1.1.1", ""},             // ip
	}

	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()

			players, err := b.lookup(ctx, tc.query)
			if err != nil {
				t.Fatalf("lookup(%q): %v", tc.query, err)
			}
			if len(players) == 0 {
				t.Fatalf("lookup(%q) returned no players", tc.query)
			}

			embed := resultEmbed("Busca: "+tc.query, players, time.Now())
			t.Logf("%d players, first field: %s | %s",
				len(players), embed.Fields[0].Name, strings.SplitN(embed.Fields[0].Value, "\n", 2)[0])

			if tc.wantStatus != "" && !strings.Contains(embed.Fields[0].Name, tc.wantStatus) {
				t.Errorf("first result = %q, want status %q", embed.Fields[0].Name, tc.wantStatus)
			}
		})
	}
}

func TestLiveDiscordLookup(t *testing.T) {
	b := liveBot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	players, err := b.client.SearchDiscord(ctx, "274247581801119745")
	if err != nil {
		t.Fatalf("SearchDiscord: %v", err)
	}
	if len(players) == 0 {
		t.Fatal("no linked accounts returned")
	}
	embed := resultEmbed("Contas vinculadas", players, time.Now())
	for _, f := range embed.Fields {
		t.Logf("%s", f.Name)
	}
}
