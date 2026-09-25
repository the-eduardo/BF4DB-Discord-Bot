package bot

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

func userOption(name, userID string) *discordgo.ApplicationCommandInteractionDataOption {
	return &discordgo.ApplicationCommandInteractionDataOption{
		Name:  name,
		Type:  discordgo.ApplicationCommandOptionUser,
		Value: userID,
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

// TestLookupFallsBackToWebsiteOnTransportError e' a fiacao da mudanca em
// bf4db.SearchName: prova que quem se beneficia do fallback num erro de
// TRANSPORTE (nao um status HTTP) e' b.lookup (commands.go), o caminho real
// que handleSearch chama -- nao so a funcao SearchName isolada.
func TestLookupFallsBackToWebsiteOnTransportError(t *testing.T) {
	b := newTestBot()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search") {
			// Fecha a conexao sem responder: erro de transporte, sem status HTTP.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("ResponseWriter nao suporta Hijack")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			conn.Close()
			return
		}
		w.WriteHeader(http.StatusNotFound)
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
		bf4db.WithBaseURL(api.URL+"/api"), bf4db.WithWebBaseURL(web.URL), bf4db.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("bf4db.New: %v", err)
	}
	b.client = client

	players, err := b.lookup(context.Background(), "eduardo")
	if err != nil {
		t.Fatalf("lookup: %v, want the website fallback to cover the transport error", err)
	}
	if len(players) != 1 || players[0].PersonaID() != 172015112 {
		t.Errorf("players = %+v", players)
	}
}

// TestPaginatedTitleCapped é a fiação do teto de título, não a função pura:
// resultEmbed() já tem o próprio teste em render_test.go, mas o caminho real
// que handleSearch chama é b.paginated (commands.go:166), e um corte
// introduzido só no construtor de embed isolado não prova que a paginação —
// que reusa o título guardado no cache por 15min — também fica coberta.
func TestPaginatedTitleCapped(t *testing.T) {
	b := newTestBot()

	longTitle := "Busca: " + strings.Repeat("A", 400)
	var players []bf4db.Player
	for i := range 12 { // > pageSize (5), força o caminho paginado
		players = append(players, bf4db.Player{PlayerID: bf4db.FlexInt(i + 1), Name: fmt.Sprintf("p%d", i)})
	}

	embed, comps := b.paginated(longTitle, players, testTime, "u1")
	if n := utf8.RuneCountInString(embed.Title); n > maxEmbedTitle {
		t.Errorf("title is %d runes, over Discord's %d limit", n, maxEmbedTitle)
	}
	if len(comps) == 0 {
		t.Fatalf("test setup did not trigger pagination, adjust the player count")
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

// discordSessionTransport intercepta as chamadas do discordgo.Session (usadas
// por opt.UserValue para resolver o usuário) sem deixar nada sair para a rede;
// tudo que não é /users/{id} cai no comportamento de recordingTransport.
type discordSessionTransport struct {
	rt       *recordingTransport
	username string
}

func (t *discordSessionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Path, "/users/") {
		body := fmt.Sprintf(`{"id":%q,"username":%q}`, strings.TrimPrefix(req.URL.Path[strings.LastIndex(req.URL.Path, "/"):], "/"), t.username)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}
	return t.rt.RoundTrip(req)
}

// TestHandleSearchDiscordOptionGetsOwnDeadline é a fiação do orçamento por
// opção, não a aritmética isolada de context.WithTimeout: prova que quando
// global-search consome o timeout inteiro (fallback lento de verdade, aqui
// simulado por um handler que bloqueia até o ctx do cliente expirar),
// discord-user — combinável com a primeira, ambas Required: false — ainda
// consegue rodar com o PRÓPRIO orçamento em vez de herdar um ctx já estourado.
// Mutação que reproduz o defeito de commands.go antes desta mudança: voltar a
// compartilhar um único `ctx, cancel := context.WithTimeout(...)` entre os dois
// blocos faz este teste falhar, porque o handler de discordAccount nunca
// recebe a requisição a tempo e o log de sucesso não aparece.
func TestHandleSearchDiscordOptionGetsOwnDeadline(t *testing.T) {
	b, logs := newTestBotWithLogs()
	b.timeout = 200 * time.Millisecond

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "discordAccount") {
			fmt.Fprint(w, `{"data":[{"player_id":1,"name":"X","is_banned":2}]}`)
			return
		}
		// Simula o caminho lento de global-search (fallback de scraping no
		// nome): a requisição só termina quando o ctx do cliente expira, o que
		// garante que o orçamento inteiro de b.timeout foi consumido por ela.
		<-r.Context().Done()
	}))
	defer api.Close()

	client, err := bf4db.New(strings.Repeat("a", 64), bf4db.WithBaseURL(api.URL+"/api"))
	if err != nil {
		t.Fatalf("bf4db.New: %v", err)
	}
	b.client = client

	s, err := discordgo.New("Bot token-de-teste")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	rt := &recordingTransport{}
	s.Client = &http.Client{Transport: &discordSessionTransport{rt: rt, username: "fulano"}}

	b.handleSearch(s, searchInteraction(
		stringOption(optionSearch, "988768601"),
		userOption(optionDiscord, "987654321"),
	))

	logged := logs.String()
	if !strings.Contains(logged, `"msg":"discord search done"`) {
		t.Fatalf("log não contém \"discord search done\" — discord-user herdou o ctx já gasto pela primeira busca\nlogs:\n%s", logged)
	}
	if strings.Contains(logged, `"msg":"discord search failed"`) {
		t.Fatalf("log contém \"discord search failed\" — discord-user deveria ter seu próprio orçamento\nlogs:\n%s", logged)
	}
}
