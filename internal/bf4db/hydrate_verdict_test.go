package bf4db

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Os dois lados da restauração do veredito em hydrate() que o teste do
// banido-sem-is_banned não cobre (drenagem de 25/09/2026: as duas mutações
// abaixo passavam pela suíte inteira).

// TestSearchNameKeepsExplicitUnderReviewForUnbannedStub: a restauração só vale
// quando o scrape achou BANIDO. Um stub sem badge (BanNotReported) não pode
// sobrescrever o 0 da API — senão um jogador "em análise" vira "não
// reportado". Mutação que este teste pega: tirar o
// `players[i].IsBanned == BanActive` da condição.
func TestSearchNameKeepsExplicitUnderReviewForUnbannedStub(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/player/")
		_, _ = fmt.Fprintf(w, `{"data":{"player_id":%s,"name":"hydrated","is_banned":0}}`, id)
	})
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, searchPageFixture)
	})

	c := newNameSearchClient(t, api, web, WithMaxRetries(0))
	players, err := c.SearchName(context.Background(), "eduardo")
	if err != nil {
		t.Fatalf("SearchName: %v", err)
	}
	if len(players) != 3 {
		t.Fatalf("got %d players, want 3", len(players))
	}
	if players[0].IsBanned != BanUnderReview {
		t.Errorf("stub sem badge sobrescreveu o 0 da API: IsBanned=%d, want %d (under review)", players[0].IsBanned, BanUnderReview)
	}
	// Contraprova no mesmo cenário: o stub banido continua restaurado.
	if !players[1].Banned() || players[1].Reason() != "Aimbot" {
		t.Errorf("stub banido perdeu o veredito: %+v", players[1])
	}
}

// TestSearchNameKeepsAPIReasonWhenRestoringScrapedBan: ao restaurar o banido
// do scrape, um motivo que a API trouxe vence o do tooltip. Mutação que este
// teste pega: trocar o `if strings.TrimSpace(full.BanReason) == ""` por
// sobrescrita incondicional.
func TestSearchNameKeepsAPIReasonWhenRestoringScrapedBan(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/player/")
		_, _ = fmt.Fprintf(w, `{"data":{"player_id":%s,"name":"hydrated","ban_reason":"Motivo da API"}}`, id)
	})
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, searchPageFixture)
	})

	c := newNameSearchClient(t, api, web, WithMaxRetries(0))
	players, err := c.SearchName(context.Background(), "eduardo")
	if err != nil {
		t.Fatalf("SearchName: %v", err)
	}
	if len(players) != 3 {
		t.Fatalf("got %d players, want 3", len(players))
	}
	if !players[1].Banned() {
		t.Fatalf("stub banido perdeu o veredito: %+v", players[1])
	}
	if got := players[1].Reason(); got != "Motivo da API" {
		t.Errorf("Reason() = %q, want o motivo da API (o tooltip só entra quando a API não traz nenhum)", got)
	}
}
