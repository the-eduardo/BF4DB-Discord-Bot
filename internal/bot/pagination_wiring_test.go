package bot

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
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
