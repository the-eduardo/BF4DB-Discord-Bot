package bot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
)

// Fiação da guarda de dono, não a função pura: TestResultOwnedBy prova o que
// resultOwnedBy decide, mas trocar a checagem do handleComponent por `if false`
// deixava a suíte inteira verde (verificado por mutação em 15/08/2026) — e
// qualquer um voltaria a paginar a busca dos outros. Aqui o handler roda de
// verdade, com uma Session cujo transporte grava o que seria enviado ao
// Discord sem deixar nada sair para a rede.

type recordedResponse struct {
	Type int `json:"type"`
	Data struct {
		Content string `json:"content"`
		Flags   int    `json:"flags"`
	} `json:"data"`
}

type recordingTransport struct{ bodies [][]byte }

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		req.Body.Close()
	}
	rt.bodies = append(rt.bodies, body)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader("{}")),
		Request:    req,
	}, nil
}

func (rt *recordingTransport) last(t *testing.T) recordedResponse {
	t.Helper()
	if len(rt.bodies) == 0 {
		t.Fatal("nenhuma resposta chegou ao Discord fake")
	}
	var r recordedResponse
	if err := json.Unmarshal(rt.bodies[len(rt.bodies)-1], &r); err != nil {
		t.Fatalf("resposta gravada não é JSON: %v", err)
	}
	return r
}

func componentInteraction(userID, customID string) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type:   discordgo.InteractionMessageComponent,
		ID:     "1",
		AppID:  "2",
		Token:  "tok",
		Data:   discordgo.MessageComponentInteractionData{CustomID: customID},
		Member: &discordgo.Member{User: &discordgo.User{ID: userID}},
	}}
}

func pagingFixture(t *testing.T, owner string) (*Bot, *discordgo.Session, string) {
	t.Helper()
	b := newTestBot()
	key := newResultKey()
	b.results.Set(key, resultSet{title: "Busca: x", players: players(12), created: time.Now(), owner: owner})

	s, err := discordgo.New("Bot token-de-teste")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	return b, s, customIDPrefix + key + ":1"
}

func TestHandleComponentBlocksAnotherUsersSearch(t *testing.T) {
	b, s, customID := pagingFixture(t, "dono")
	rt := &recordingTransport{}
	s.Client = &http.Client{Transport: rt}

	b.handleComponent(s, componentInteraction("intruso", customID))

	got := rt.last(t)
	if got.Type != int(discordgo.InteractionResponseChannelMessageWithSource) {
		t.Fatalf("resposta type=%d; a guarda não interceptou (UpdateMessage=%d significa que o intruso paginou)",
			got.Type, discordgo.InteractionResponseUpdateMessage)
	}
	if !strings.Contains(got.Data.Content, "outra pessoa") {
		t.Errorf("mensagem de bloqueio inesperada: %q", got.Data.Content)
	}
	if got.Data.Flags&int(discordgo.MessageFlagsEphemeral) == 0 {
		t.Error("bloqueio deveria ser efêmero")
	}
}

func TestHandleComponentLetsTheOwnerPage(t *testing.T) {
	// Contraprova: o dono pagina normalmente — garante que o teste acima
	// detecta a guarda, não um handler que bloqueia todo mundo.
	b, s, customID := pagingFixture(t, "dono")
	rt := &recordingTransport{}
	s.Client = &http.Client{Transport: rt}

	b.handleComponent(s, componentInteraction("dono", customID))

	if got := rt.last(t); got.Type != int(discordgo.InteractionResponseUpdateMessage) {
		t.Fatalf("dono recebeu type=%d em vez de UpdateMessage", got.Type)
	}
}

// discordUserStubTransport answers GET /users/{id} (UserValue's lookup) with
// a real user object instead of recordingTransport's blank "{}" — otherwise
// UserValue silently swaps the id we sent for an empty one and the search
// goes to SearchDiscord with "", which errors before ever reaching the
// pagination bug this file is testing.
type discordUserStubTransport struct {
	rt *recordingTransport
}

func (d *discordUserStubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/users/") {
		id := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		body := fmt.Sprintf(`{"id":%q,"username":"linked"}`, id)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}
	return d.rt.RoundTrip(req)
}

// webhookEditBody decodes the PATCH b.edit sends to complete a deferred
// interaction — recordedResponse only covers the immediate ack/update shape.
type webhookEditBody struct {
	Embeds []struct {
		Title string `json:"title"`
	} `json:"embeds"`
	Components []struct {
		Components []struct {
			CustomID string `json:"custom_id"`
		} `json:"components"`
	} `json:"components"`
}

// updateMessageBody decodes handleComponent's InteractionResponseUpdateMessage,
// which webhookEditBody doesn't cover because it nests under "data".
type updateMessageBody struct {
	Type int `json:"type"`
	Data struct {
		Embeds []struct {
			Title string `json:"title"`
		} `json:"embeds"`
	} `json:"data"`
}

// Fiação, não a função pura: /bf4db aceita global-search e discord-user juntos
// (commands.go:37-51). Quando a busca por nome pagina, handleComponent
// (pagination.go) respondia a virada de página com um único embed — o de
// "Contas de <user>" desaparecia da mensagem sem erro, porque
// InteractionResponseUpdateMessage substitui a lista inteira. Este teste
// exercita o fluxo real (handleSearch monta os dois embeds, guarda o extra
// no resultSet, handleComponent os dois de volta), não só resultEmbed()
// isolado.
func TestHandleSearchKeepsDiscordUserEmbedAcrossPageTurn(t *testing.T) {
	b := newTestBot()

	bfSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "discordAccount") {
			fmt.Fprint(w, `{"data":[{"player_id":1,"name":"linked","is_banned":0}]}`)
			return
		}
		var sb strings.Builder
		sb.WriteString(`{"data":[`)
		for i := range 7 { // > pageSize (5), força a paginação
			if i > 0 {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, `{"player_id":%d,"name":"p%d","is_banned":0}`, i+1, i+1)
		}
		sb.WriteString(`],"meta":{"current_page":1,"last_page":1,"per_page":50,"total":7}}`)
		fmt.Fprint(w, sb.String())
	}))
	defer bfSrv.Close()

	client, err := bf4db.New(strings.Repeat("a", 64), bf4db.WithBaseURL(bfSrv.URL+"/api"))
	if err != nil {
		t.Fatalf("bf4db.New: %v", err)
	}
	b.client = client

	s, err := discordgo.New("Bot token-de-teste")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	rt := &recordingTransport{}
	s.Client = &http.Client{Transport: &discordUserStubTransport{rt: rt}}

	b.handleSearch(s, searchInteraction(
		stringOption(optionSearch, "eduardo"),
		&discordgo.ApplicationCommandInteractionDataOption{
			Name:  optionDiscord,
			Type:  discordgo.ApplicationCommandOptionUser,
			Value: "555",
		},
	))

	var edit webhookEditBody
	if err := json.Unmarshal(rt.bodies[len(rt.bodies)-1], &edit); err != nil {
		t.Fatalf("corpo da edição não é JSON: %v", err)
	}
	if len(edit.Embeds) != 2 {
		t.Fatalf("edição mandou %d embeds, want 2 (busca por nome + contas do discord)", len(edit.Embeds))
	}
	if !strings.HasPrefix(edit.Embeds[1].Title, "Contas de") {
		t.Errorf("segundo embed = %q, want prefixo \"Contas de\"", edit.Embeds[1].Title)
	}
	if len(edit.Components) == 0 || len(edit.Components[0].Components) < 3 {
		t.Fatal("botões de paginação ausentes na edição")
	}
	customID := edit.Components[0].Components[2].CustomID

	rt2 := &recordingTransport{}
	s.Client = &http.Client{Transport: rt2}
	b.handleComponent(s, componentInteraction("solicitante", customID))

	var upd updateMessageBody
	if err := json.Unmarshal(rt2.bodies[len(rt2.bodies)-1], &upd); err != nil {
		t.Fatalf("corpo da virada de página não é JSON: %v", err)
	}
	if upd.Type != int(discordgo.InteractionResponseUpdateMessage) {
		t.Fatalf("type = %d, want UpdateMessage", upd.Type)
	}
	if len(upd.Data.Embeds) != 2 {
		t.Fatalf("virar a página deixou %d embeds, want 2 — o embed de discord-user não pode sumir", len(upd.Data.Embeds))
	}
	if !strings.HasPrefix(upd.Data.Embeds[1].Title, "Contas de") {
		t.Errorf("segundo embed após virar a página = %q", upd.Data.Embeds[1].Title)
	}
}
