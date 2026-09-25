package bot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/config"
)

// Fiação do watchdog, não as peças isoladas: watchdog_test.go prova
// watchGateway e wait() soltos, mas apagar o `go b.watchGateway(ctx, stuck)`
// de Run — ou a linha que dá a New o intervalo de produção — deixava a suíte
// inteira verde (verificado por mutação na drenagem de 25/09/2026). Aqui Run
// roda de verdade contra um Discord falso: REST por RoundTripper (nada sai
// para a rede) e o gateway por um websocket local que responde Hello + READY.

// wdRESTTransport answers the two REST calls Run makes before blocking:
// GET /gateway (pointing the session at the local websocket) and the
// ApplicationCommandCreate POSTs from registerCommands.
type wdRESTTransport struct{ wsURL string }

func (t *wdRESTTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		req.Body.Close()
	}
	body := `{"id":"1","name":"cmd"}`
	if strings.HasSuffix(req.URL.Path, "/gateway") {
		body = `{"url":"` + t.wsURL + `"}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

// wdFakeGateway speaks just enough of the gateway protocol for
// discordgo.Session.Open to succeed: Hello (op 10), read Identify, READY.
// Then it drains whatever the client sends (heartbeats) until it hangs up.
func wdFakeGateway(t *testing.T) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		if err := c.WriteMessage(websocket.TextMessage, []byte(`{"op":10,"d":{"heartbeat_interval":45000}}`)); err != nil {
			return
		}
		if _, _, err := c.ReadMessage(); err != nil { // Identify
			return
		}
		ready := `{"op":0,"t":"READY","s":1,"d":{"v":10,"session_id":"s","user":{"id":"42","username":"bot"},"guilds":[]}}`
		if err := c.WriteMessage(websocket.TextMessage, []byte(ready)); err != nil {
			return
		}
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
}

// TestNewSetsProductionWatchdogInterval: sem o intervalo vindo de New, o
// time.NewTicker(0) de watchGateway entra em pânico no primeiro Run de
// produção — os testes de unidade nunca passam por New, então só este pega.
func TestNewSetsProductionWatchdogInterval(t *testing.T) {
	b, err := New(config.Config{BotToken: "token-de-teste", Timeout: time.Second},
		nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if b.watchdogInterval != watchdogInterval {
		t.Fatalf("New deixou watchdogInterval = %v, want %v", b.watchdogInterval, watchdogInterval)
	}
}

// TestRunWiresGatewayWatchdog: Run tem de devolver ErrGatewayStuck sozinho
// quando o gateway fica offline além da janela — é o que faz main.go sair e o
// `restart: unless-stopped` subir um processo novo. Sem o watchdog ligado em
// Run, ele só voltaria no cancelamento do ctx (a falha do incidente 15-16/09).
//
// Contraprova no mesmo teste: Run segue bloqueado enquanto o gateway está
// conectado, então o retorno não vem de um Run que simplesmente cai sempre.
func TestRunWiresGatewayWatchdog(t *testing.T) {
	gw := wdFakeGateway(t)
	defer gw.Close()

	b, err := New(config.Config{BotToken: "token-de-teste", Timeout: time.Second},
		nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	b.session.Client = &http.Client{Transport: &wdRESTTransport{wsURL: "ws" + strings.TrimPrefix(gw.URL, "http")}}
	b.watchdogInterval = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx, false) }()

	// Espera o READY marcar o gateway como conectado (o handler roda numa
	// goroutine própria do discordgo).
	deadline := time.Now().Add(5 * time.Second)
	for !b.connected.Load() {
		if time.Now().After(deadline) {
			t.Fatal("o gateway falso nunca chegou a READY")
		}
		select {
		case err := <-done:
			t.Fatalf("Run voltou antes do READY: %v", err)
		case <-time.After(5 * time.Millisecond):
		}
	}

	// Conectado: muitas vezes offlineFatalAfter ticks e Run continua de pé.
	select {
	case err := <-done:
		t.Fatalf("Run voltou com o gateway conectado: %v", err)
	case <-time.After(20 * offlineFatalAfter * b.watchdogInterval):
	}

	// Queda sem volta (o que o Disconnect do discordgo faz com a flag).
	b.connected.Store(false)

	select {
	case err := <-done:
		if !errors.Is(err, ErrGatewayStuck) {
			t.Fatalf("Run = %v, want ErrGatewayStuck", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run não voltou com o gateway offline: o watchdog não está ligado em Run")
	}
}
