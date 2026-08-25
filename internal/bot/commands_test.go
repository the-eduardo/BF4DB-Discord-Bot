package bot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
)

func stringOption(name, value string) *discordgo.ApplicationCommandInteractionDataOption {
	return &discordgo.ApplicationCommandInteractionDataOption{
		Name:  name,
		Type:  discordgo.ApplicationCommandOptionString,
		Value: value,
	}
}

func keysOf(m map[string]*discordgo.ApplicationCommandInteractionDataOption) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// A busca só com espaços não é uma busca: searchOptions descarta esse
// global-search para que ele caia no embed "nada para buscar" já existente,
// em vez de virar uma consulta em branco no BF4DB. Mutação que reproduz o
// bug de commands.go:73-76: remover o `continue` da guarda faz o caso 1
// devolver um mapa com 1 entrada e este teste falha.
func TestSearchOptionsDropsBlankQuery(t *testing.T) {
	cases := []struct {
		name string
		opts []*discordgo.ApplicationCommandInteractionDataOption
		want []string
	}{
		{
			name: "só espaços",
			opts: []*discordgo.ApplicationCommandInteractionDataOption{stringOption(optionSearch, "   ")},
			want: nil,
		},
		{
			name: "busca real",
			opts: []*discordgo.ApplicationCommandInteractionDataOption{stringOption(optionSearch, "eduardo")},
			want: []string{optionSearch},
		},
		{
			name: "busca em branco ao lado de discord-user",
			opts: []*discordgo.ApplicationCommandInteractionDataOption{
				stringOption(optionSearch, "  "),
				{Name: optionDiscord, Type: discordgo.ApplicationCommandOptionUser, Value: "123"},
			},
			want: []string{optionDiscord},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := searchOptions(discordgo.ApplicationCommandInteractionData{Options: tc.opts})
			if len(got) != len(tc.want) {
				t.Fatalf("searchOptions() chaves = %v, want %v", keysOf(got), tc.want)
			}
			for _, k := range tc.want {
				if _, ok := got[k]; !ok {
					t.Errorf("chave %q ausente em %v", k, keysOf(got))
				}
			}
		})
	}
}

func searchInteraction(opts ...*discordgo.ApplicationCommandInteractionDataOption) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type:   discordgo.InteractionApplicationCommand,
		ID:     "1",
		AppID:  "2",
		Token:  "tok",
		Data:   discordgo.ApplicationCommandInteractionData{Name: "bf4db", Options: opts},
		Member: &discordgo.Member{User: &discordgo.User{ID: "solicitante"}},
	}}
}

// Fiação, não só a função pura: se alguém trocar searchOptions(...) de volta
// pelo laço antigo direto em handleSearch (commands.go:73), o teste acima
// continua verde e este é quem quebra. b.client fica nil de propósito — se a
// busca em branco chegasse ao lookup, o teste travaria com um nil pointer em
// vez de silenciosamente passar.
func TestHandleSearchBlankQueryShowsNothingToSearch(t *testing.T) {
	b := newTestBot()

	s, err := discordgo.New("Bot token-de-teste")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	rt := &recordingTransport{}
	s.Client = &http.Client{Transport: rt}

	b.handleSearch(s, searchInteraction(stringOption(optionSearch, "   ")))

	// O caminho com bug manda DUAS respostas: o ack adiado e depois a edição
	// com o erro "busca vazia". O caminho corrigido manda só uma.
	if len(rt.bodies) != 1 {
		t.Fatalf("handleSearch mandou %d respostas, want 1 (2 respostas = a busca em branco chegou ao lookup)", len(rt.bodies))
	}
	if got := rt.last(t); got.Type != int(discordgo.InteractionResponseChannelMessageWithSource) {
		t.Fatalf("resposta type=%d, want ChannelMessageWithSource (%d)",
			got.Type, discordgo.InteractionResponseChannelMessageWithSource)
	}
}

// Fiação do fallback de 5xx: garante que é b.lookup (via commands.go:233)
// quem se beneficia da correção em client.go, não só SearchName isolado.
// Mutação que prova: reverter client.go para `!= http.StatusInternalServerError`
// faz este teste falhar com "bf4db: unexpected response 502 Bad Gateway".
func TestLookupFallsBackToWebsiteOnCloudflare5xx(t *testing.T) {
	b := newTestBot()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search") {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/player/")
		fmt.Fprintf(w, `{"data":{"player_id":%s,"name":"p","is_banned":2}}`, id)
	}))
	defer api.Close()
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body><table><tbody>
<tr><td class="player-td-image"><a href="/player/172015112"><img></a></td>
    <td class="player-td-name"><a href="/player/172015112"> eduardo </a></td>
    <td class="pull-right"></td></tr>
</tbody></table></body></html>`)
	}))
	defer web.Close()

	client, err := bf4db.New(strings.Repeat("a", 64),
		bf4db.WithBaseURL(api.URL+"/api"), bf4db.WithWebBaseURL(web.URL))
	if err != nil {
		t.Fatalf("bf4db.New: %v", err)
	}
	b.client = client

	players, err := b.lookup(context.Background(), "eduardo")
	if err != nil {
		t.Fatalf("lookup: %v, want the website fallback to cover the 502", err)
	}
	if len(players) != 1 || players[0].PersonaID() != 172015112 {
		t.Errorf("players = %+v", players)
	}
}

// Contraprova: uma busca de verdade não deve ser descartada nem virar dois
// tipos de resposta por acidente — só garante que o teste acima detecta a
// guarda, não um handler que trata toda busca como vazia.
func TestHandleSearchRealQueryIsNotDropped(t *testing.T) {
	b := newTestBot()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"player_id":988768601,"name":"EdUwUardo","is_banned":2}}`)
	}))
	defer srv.Close()
	client, err := bf4db.New(strings.Repeat("a", 64), bf4db.WithBaseURL(srv.URL+"/api"))
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

	// "988768601" é um id de persona: bate no endpoint de player direto, sem
	// depender do fallback de scraping do site que a busca por nome usa.
	b.handleSearch(s, searchInteraction(stringOption(optionSearch, "988768601")))

	// defer (ack) + edit (resultado) = a busca real não foi descartada pela
	// guarda que só deveria pegar query em branco.
	if len(rt.bodies) != 2 {
		t.Fatalf("handleSearch mandou %d respostas, want 2 (defer + edit de uma busca real)", len(rt.bodies))
	}
}
