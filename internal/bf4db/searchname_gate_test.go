package bf4db

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// As duas guardas que o gate novo de SearchName mantém ANTES do fallback —
// ctx morto e 4xx real — não tinham teste: apagar qualquer uma deixava a suíte
// inteira verde (drenagem de 25/09/2026). O caminho positivo (erro de
// transporte cai no scraper) é TestSearchNameFallsBackOnTransportError.

// TestSearchNameDoesNotFallBackOnDeadContext: com o prazo estourado, o
// fallback só compraria outro erro — e embrulhado em ErrNameSearchUnavailable
// com %v, o DeadlineExceeded se perde e o usuário lê "busca por nome fora do
// ar" em vez de "demorou demais".
func TestSearchNameDoesNotFallBackOnDeadContext(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // a API segura até o prazo do cliente estourar
	})
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, searchPageFixture)
	})

	c := newNameSearchClient(t, api, web, WithMaxRetries(0))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := c.SearchName(ctx, "eduardo")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded preservado", err)
	}
	if errors.Is(err, ErrNameSearchUnavailable) {
		t.Fatalf("err = %v: ctx morto passou pelo fallback", err)
	}
}

// TestSearchNameDoesNotFallBackOnClientError: 401/404 são respostas reais da
// API, não indisponibilidade — o scraper não pode ser consultado nem mascarar
// o erro.
func TestSearchNameDoesNotFallBackOnClientError(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = fmt.Fprint(w, `{"message":"x"}`)
			})
			var webCalls atomic.Int32
			web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				webCalls.Add(1)
				_, _ = fmt.Fprint(w, searchPageFixture)
			})

			c := newNameSearchClient(t, api, web, WithMaxRetries(0))
			players, err := c.SearchName(context.Background(), "eduardo")

			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != status {
				t.Fatalf("err = %v, want *APIError %d", err, status)
			}
			if n := webCalls.Load(); n != 0 || len(players) != 0 {
				t.Fatalf("scraper consultado %d vez(es), %d jogadores: um %d virou fallback", n, len(players), status)
			}
			if strings.Contains(err.Error(), "website fallback") {
				t.Fatalf("err = %v: menciona o fallback", err)
			}
		})
	}
}
