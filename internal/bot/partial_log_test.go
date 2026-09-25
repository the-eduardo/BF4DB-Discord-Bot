package bot

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
)

// TestHandleSearchLogsPartialAsWarn: o resultado parcial esconde uma falha real
// da API atrás de uma resposta que parece boa; a linha "search partial" em
// WARN é o que sobra dela no log de produção (LOG_LEVEL=info). Rebaixá-la
// para Debug passava pela suíte inteira na drenagem de 25/09/2026.
func TestHandleSearchLogsPartialAsWarn(t *testing.T) {
	b, logs := newTestBotWithLogs()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":1,"name":"p1","is_banned":2}],"meta":{"last_page":3}}`)
	}))
	defer srv.Close()

	client, err := bf4db.New(strings.Repeat("a", 64), bf4db.WithBaseURL(srv.URL+"/api"), bf4db.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("bf4db.New: %v", err)
	}
	b.client = client

	s, err := discordgo.New("Bot token-de-teste")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	s.Client = &http.Client{Transport: &recordingTransport{}}

	i := searchInteraction(stringOption(optionSearch, "1.2.3.4"))
	i.Member.Permissions = discordgo.PermissionAdministrator // maySearchIP exige staff
	b.handleSearch(s, i)

	out := logs.String()
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, `"msg":"search partial"`) {
			line = l
		}
	}
	if !strings.Contains(line, `"level":"WARN"`) {
		t.Fatalf("parcial não chegou ao log em WARN:\n%s", out)
	}
	// Contraprova: o parcial não é registrado como busca completa.
	if strings.Contains(out, `"msg":"search done"`) {
		t.Fatalf("parcial registrado como \"search done\":\n%s", out)
	}
}
