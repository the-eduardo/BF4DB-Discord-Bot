package bot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
)

// capturingTransport stands in for the Discord API: it never fails, so the
// call under test reaches InteractionResponseEdit's real JSON encoding, and it
// keeps the raw request body so the test can measure exactly what would have
// been sent over the wire.
type capturingTransport struct {
	called bool
	body   []byte
}

func (c *capturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.called = true
	if req.Body != nil {
		c.body, _ = io.ReadAll(req.Body)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"id":"1"}`))),
		Header:     make(http.Header),
	}, nil
}

// bulkyPlayers synthesizes enough players, with a long ban reason, to push a
// resultEmbed close to render.go's own per-embed budget (maxEmbedChars=5500)
// without exceeding it — the same shape handleSearch produces for a suspect
// with many linked accounts.
func bulkyPlayers(n int) []bf4db.Player {
	reason := strings.Repeat("Linked account, banido por cheat detectado em multiplas rodadas. ", 4)
	players := make([]bf4db.Player, n)
	for i := range players {
		players[i] = bf4db.Player{
			ID:         bf4db.FlexInt(100000 + i),
			Name:       fmt.Sprintf("jogador-suspeito-%03d", i),
			IsBanned:   bf4db.BanActive,
			BanReason:  reason,
			CheatScore: 100,
		}
	}
	return players
}

// TestEditFitsCombinedEmbedBudget is the wiring test for fitEmbeds: it drives
// the exact path handleSearch uses (two resultEmbed-built embeds passed to
// bot.edit) and inspects the JSON that would actually reach Discord, not the
// in-memory embeds before fitEmbeds runs.
func TestEditFitsCombinedEmbedBudget(t *testing.T) {
	b := newTestBot()

	s, err := discordgo.New("Bot token-de-teste")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	transport := &capturingTransport{}
	s.Client = &http.Client{Transport: transport}

	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		AppID: "1027015041326788659",
		Token: "tok-de-teste",
	}}

	// Each embed alone sits under render.go's per-embed budget (5500); two of
	// them together, the way handleSearch attaches global-search plus
	// discord-user results to the same edit, comfortably exceed the combined
	// 6000 Discord actually enforces across the message.
	embed1 := resultEmbed("Busca: suspeito", bulkyPlayers(25), testTime)
	embed2 := resultEmbed("Contas de usuario-discord-alvo", bulkyPlayers(25), testTime)
	if embedChars(embed1)+embedChars(embed2) <= 6000 {
		t.Fatalf("fixture nao estoura o orcamento sozinho (embed1=%d embed2=%d) — teste nao prova nada",
			embedChars(embed1), embedChars(embed2))
	}

	b.edit(s, i, []*discordgo.MessageEmbed{embed1, embed2}, nil)

	if !transport.called {
		t.Fatal("a requisicao nunca foi enviada — controle positivo falhou, o resto do teste nao prova nada")
	}

	var payload struct {
		Embeds []struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Footer      struct {
				Text string `json:"text"`
			} `json:"footer"`
			Fields []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"fields"`
		} `json:"embeds"`
	}
	if err := json.Unmarshal(transport.body, &payload); err != nil {
		t.Fatalf("payload nao decodificou: %v (body=%s)", err, transport.body)
	}
	if len(payload.Embeds) == 0 {
		t.Fatal("nenhum embed sobrou no payload enviado")
	}

	total := 0
	for _, e := range payload.Embeds {
		total += utf8.RuneCountInString(e.Title) + utf8.RuneCountInString(e.Description) + utf8.RuneCountInString(e.Footer.Text)
		for _, f := range e.Fields {
			total += utf8.RuneCountInString(f.Name) + utf8.RuneCountInString(f.Value)
		}
	}
	if total > 6000 {
		t.Errorf("soma de todos os embeds enviados = %d runas, Discord rejeita a edicao inteira acima de 6000", total)
	}

	// The first (main search) embed must survive intact: fitEmbeds trims from
	// the END of the embed list, not the start.
	if payload.Embeds[0].Title != embed1.Title || len(payload.Embeds[0].Fields) != len(embed1.Fields) {
		t.Errorf("o primeiro embed (busca principal) foi alterado: title=%q fields=%d, want title=%q fields=%d",
			payload.Embeds[0].Title, len(payload.Embeds[0].Fields), embed1.Title, len(embed1.Fields))
	}
}

func TestEmbedCharsCountsFooterAndAuthor(t *testing.T) {
	e := &discordgo.MessageEmbed{
		Title:       "1234",
		Description: "12345",
		Footer:      &discordgo.MessageEmbedFooter{Text: "123"},
		Author:      &discordgo.MessageEmbedAuthor{Name: "12"},
		Fields: []*discordgo.MessageEmbedField{
			{Name: "1", Value: "123456"},
		},
	}
	want := 4 + 5 + 3 + 2 + 1 + 6
	if got := embedChars(e); got != want {
		t.Errorf("embedChars = %d, want %d", got, want)
	}
}

func TestFitEmbedsDropsWholeEmbedWhenNoBudgetLeft(t *testing.T) {
	first := &discordgo.MessageEmbed{Title: strings.Repeat("a", maxMessageChars)}
	second := &discordgo.MessageEmbed{Title: "sobra"}

	fitted := fitEmbeds([]*discordgo.MessageEmbed{first, second})
	if len(fitted) != 1 {
		t.Fatalf("got %d embeds, want 1 (second should be dropped entirely)", len(fitted))
	}
	if fitted[0].Title != first.Title {
		t.Errorf("the surviving embed changed: %q", fitted[0].Title)
	}
}

// TestEditFallsBackWhenFitEmbedsEmptiesTheList cobre a ordem entre o corte e o
// fallback de "nenhum embed" dentro de edit(). Com o fallback ANTES de
// fitEmbeds, este caso mandava `"embeds":[]` ao Discord — o 400 e o
// "Thinking..." eterno que a mudança existe para evitar. A asserção é sobre o
// corpo REAL que iria ao Discord, não sobre a fatia em memória.
func TestEditFallsBackWhenFitEmbedsEmptiesTheList(t *testing.T) {
	b := newTestBot()

	s, err := discordgo.New("Bot token-de-teste")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	transport := &capturingTransport{}
	s.Client = &http.Client{Transport: transport}

	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		AppID: "1027015041326788659",
		Token: "tok-de-teste",
	}}

	// Embed sem campos e maior que o orçamento inteiro: fitEmbed não tem field
	// nenhum para cortar, então fitEmbeds devolve lista vazia.
	huge := &discordgo.MessageEmbed{Description: strings.Repeat("a", maxMessageChars+1)}
	if len(fitEmbeds([]*discordgo.MessageEmbed{huge})) != 0 {
		t.Fatal("fixture nao esvazia a lista — o teste nao prova nada")
	}

	b.edit(s, i, []*discordgo.MessageEmbed{huge}, nil)

	if !transport.called {
		t.Fatal("a requisicao nunca foi enviada — controle positivo falhou")
	}
	var payload struct {
		Embeds []struct {
			Title string `json:"title"`
		} `json:"embeds"`
	}
	if err := json.Unmarshal(transport.body, &payload); err != nil {
		t.Fatalf("payload nao decodificou: %v (body=%s)", err, transport.body)
	}
	if len(payload.Embeds) != 1 {
		t.Fatalf("edit mandou %d embeds ao Discord, want 1 (o fallback); body=%s",
			len(payload.Embeds), transport.body)
	}
	if payload.Embeds[0].Title != "Sem resultados" {
		t.Errorf("embed enviado = %q, want o fallback \"Sem resultados\"", payload.Embeds[0].Title)
	}
}

// TestFitEmbedDropsEmbedInsteadOfSendingEmptyBox: um embed que TINHA campos e
// só caberia sem nenhum deles vira uma caixa vazia (título + rodapé) no
// Discord. O comentário de fitEmbed sempre prometeu descartar nesse caso; a
// implementação enviava a caixa.
func TestFitEmbedDropsEmbedInsteadOfSendingEmptyBox(t *testing.T) {
	e := &discordgo.MessageEmbed{
		Title: "Busca",
		Fields: []*discordgo.MessageEmbedField{
			{Name: "jogador", Value: strings.Repeat("b", 200)},
		},
	}
	// O orçamento tem de caber o embed JÁ esvaziado (título + o rodapé
	// "resposta truncada" que fitEmbed acrescenta ao cortar) e NÃO caber o
	// campo. Sem a folga do rodapé o fitEmbed sairia pelo ramo antigo
	// ("não cabe nem vazio") e o teste passaria sem nunca exercitar a guarda —
	// foi o que aconteceu na primeira versão deste teste.
	emptied := *e
	emptied.Fields = nil
	emptied.Footer = truncatedFooter(e.Footer)
	budget := embedChars(&emptied) + 10
	if budget >= embedChars(e) {
		t.Fatalf("fixture invalida: o orcamento (%d) ja comporta o embed inteiro (%d)",
			budget, embedChars(e))
	}

	fitted, chars, ok := fitEmbed(e, budget)
	if ok {
		t.Fatalf("fitEmbed devolveu ok=true com fields=%d chars=%d — caixa vazia foi enviada",
			len(fitted.Fields), chars)
	}
	if fitted != nil {
		t.Errorf("fitEmbed devolveu embed nao-nil junto com ok=false: %+v", fitted)
	}
}

// TestEmbedBudgetsAreCompatible codifica a restrição que hoje é coincidência
// numérica: fitEmbeds só garante que o PRIMEIRO embed nunca é aparado porque
// render.go limita um embed a maxEmbedChars (+ maxEmbedTitle) e isso cabe em
// maxMessageChars. Mexer num dos dois números sem olhar o outro quebra a
// garantia em silêncio — este teste é o alarme.
func TestEmbedBudgetsAreCompatible(t *testing.T) {
	if maxEmbedChars+maxEmbedTitle >= maxMessageChars {
		t.Fatalf("orçamentos incompatíveis: maxEmbedChars(%d)+maxEmbedTitle(%d) = %d >= maxMessageChars(%d); "+
			"o primeiro embed passa a poder ser aparado por fitEmbeds",
			maxEmbedChars, maxEmbedTitle, maxEmbedChars+maxEmbedTitle, maxMessageChars)
	}
}
