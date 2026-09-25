package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
)

// Fiação de client.player → cachedLookup → errorEmbed, não só a checagem
// isolada em bf4db.ErrPlayerNotFound: sem este teste, remover o `if
// parsed.Data == (Player{})` de client.go deixaria a suíte deste pacote
// inteira verde, porque nenhum teste de bot exercitava um payload "data":null.
type editBody struct {
	Embeds []*discordgo.MessageEmbed `json:"embeds"`
}

func TestHandleSearchEmptyPlayerDataDoesNotFabricateGhost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":null}`)
	}))
	defer srv.Close()

	b := newTestBot()
	client, err := bf4db.New(strings.Repeat("a", 64), bf4db.WithBaseURL(srv.URL+"/api"), bf4db.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("bf4db.New: %v", err)
	}
	b.client = client

	s, err := discordgo.New("Bot token-de-teste")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	rt := &recordingTransport{}
	s.Client = &http.Client{Transport: rt}

	b.handleSearch(s, searchInteraction(stringOption(optionSearch, "988768601")))

	if len(rt.bodies) != 2 {
		t.Fatalf("handleSearch mandou %d respostas, want 2 (defer + edit)", len(rt.bodies))
	}

	var edit editBody
	if err := json.Unmarshal(rt.bodies[1], &edit); err != nil {
		t.Fatalf("edit não é JSON: %v", err)
	}
	if len(edit.Embeds) != 1 {
		t.Fatalf("edit tem %d embeds, want 1", len(edit.Embeds))
	}
	got := edit.Embeds[0]
	if !strings.Contains(got.Description, "Nenhum jogador com esse id") {
		t.Errorf("description = %q, want a mensagem de not-found", got.Description)
	}
	if strings.Contains(got.Description, "sem nome") || strings.Contains(got.Title+got.Description, "em análise") {
		t.Errorf("embed = %+v, want nenhum vestígio de jogador fantasma", got)
	}
	if len(got.Fields) != 0 {
		t.Errorf("embed tem %d fields, want 0 — não deveria renderizar um resultado", len(got.Fields))
	}
}
