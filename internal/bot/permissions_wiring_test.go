package bot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
)

// Fiação da guarda de IP, não a função pura: TestMaySearchIP (permissions_test.go)
// prova o que maySearchIP decide, mas apagar a checagem em handleSearch
// (commands.go) deixava a suíte inteira verde — handleSearch não era chamado
// por nenhum teste (grep confirmou zero ocorrências fora de live_test.go, que
// tem //go:build live). É a mesma classe de buraco documentada em
// pagination_wiring_test.go, e a de IP é a barreira de permissão mais sensível
// do bot: revela quem divide residência com quem.
//
// A primeira resposta que o handler envia ao Discord decide se a guarda
// bloqueou: bloqueio responde direto (ChannelMessageWithSource); passar da
// guarda defere (DeferredChannelMessageWithSource) e só depois edita com o
// resultado da busca. Por isso os testes olham bodies[0], não a última
// resposta gravada — quando a guarda deixa passar, a última é o edit final,
// que usa outro formato de payload (WebhookEdit, sem campo "type").
func searchIPInteraction(member *discordgo.Member) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
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
					Value: "1.2.3.4",
				},
			},
		},
		Member: member,
	}}
}

func firstResponse(t *testing.T, rt *recordingTransport) recordedResponse {
	t.Helper()
	if len(rt.bodies) == 0 {
		t.Fatal("nenhuma resposta chegou ao Discord fake")
	}
	var r recordedResponse
	if err := json.Unmarshal(rt.bodies[0], &r); err != nil {
		t.Fatalf("primeira resposta gravada não é JSON: %v", err)
	}
	return r
}

func TestHandleSearchBlocksIPForNonModerator(t *testing.T) {
	// O client aponta para um servidor que nunca deveria ser chamado: se a
	// guarda cair (mutação), o handler segue até aqui em vez de panicar num
	// client nil, e o teste falha por asserção legível, não por SIGSEGV.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b := newTestBot()
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

	b.handleSearch(s, searchIPInteraction(&discordgo.Member{}))

	got := firstResponse(t, rt)
	if got.Type != int(discordgo.InteractionResponseChannelMessageWithSource) {
		t.Fatalf("resposta type=%d; a guarda não interceptou (Deferred=%d significa que a busca por IP seguiu em frente)",
			got.Type, discordgo.InteractionResponseDeferredChannelMessageWithSource)
	}
	if !strings.Contains(got.Data.Content, "restrita a moderadores") {
		t.Errorf("mensagem de bloqueio inesperada: %q", got.Data.Content)
	}
	if got.Data.Flags&int(discordgo.MessageFlagsEphemeral) == 0 {
		t.Error("bloqueio deveria ser efêmero")
	}
	if len(rt.bodies) != 1 {
		t.Errorf("guarda bloqueou mas o handler mandou %d respostas, não 1 — seguiu executando depois do bloqueio", len(rt.bodies))
	}
}

func TestHandleSearchLetsModeratorSearchIP(t *testing.T) {
	// Contraprova: sem ela, um handler que bloqueia todo mundo passaria no
	// teste acima.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b := newTestBot()
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

	b.handleSearch(s, searchIPInteraction(&discordgo.Member{Permissions: discordgo.PermissionManageServer}))

	got := firstResponse(t, rt)
	if got.Type != int(discordgo.InteractionResponseDeferredChannelMessageWithSource) {
		t.Fatalf("resposta type=%d; a moderadora foi bloqueada em vez de passar da guarda", got.Type)
	}
}
