package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/config"
)

func TestLogStartupIncludesVersion(t *testing.T) {
	old := version
	version = "test-1.2.3"
	defer func() { version = old }()

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	logStartup(log)

	out := buf.String()
	if !strings.Contains(out, `"version":"test-1.2.3"`) {
		t.Fatalf("expected version in startup log, got: %s", out)
	}
}

func TestLoadConfigLogsErrorToInjectedLogger(t *testing.T) {
	t.Setenv(config.EnvBotToken, "")
	t.Setenv(config.EnvBF4DBToken, "")

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	if _, ok := loadConfig(log, ""); ok {
		t.Fatal("expected loadConfig to fail with required vars unset")
	}

	if !strings.Contains(buf.String(), `"level":"ERROR"`) {
		t.Fatalf("config error did not reach the structured logger, got: %q", buf.String())
	}
}

// TestBF4DBNotifierRedactsSearchedName e' a fiacao do notifier de producao
// (main.go, WithNotifier): client.go:304 chama c.notify com o *url.Error de
// uma falha de TRANSPORTE no by-name endpoint, cuja string carrega
// /player/{name}/search. Prova que o caminho real (bf4db.New com o notifier
// que main.go instala) nao publica o nome buscado, nao so a funcao isolada.
func TestBF4DBNotifierRedactsSearchedName(t *testing.T) {
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

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	client, err := bf4db.New(strings.Repeat("a", 64),
		bf4db.WithBaseURL(api.URL+"/api"),
		bf4db.WithWebBaseURL(web.URL),
		bf4db.WithMaxRetries(0),
		bf4db.WithNotifier(bf4dbNotifier(log)),
	)
	if err != nil {
		t.Fatalf("bf4db.New: %v", err)
	}

	if _, err := client.SearchName(context.Background(), "eduardo"); err != nil {
		t.Fatalf("SearchName: %v, want the website fallback to cover the transport error", err)
	}

	out := buf.String()
	if strings.Contains(out, "eduardo") {
		t.Fatalf("searched name leaked into the production log: %s", out)
	}
	// Controles positivos: sem eles, "ausente" e "nada foi logado" ficam
	// identicos (nada prova que o notify sequer rodou).
	if !strings.Contains(out, `"source":"bf4db"`) {
		t.Fatalf("expected source=bf4db field, got: %s", out)
	}
	if !strings.Contains(out, `"level":"INFO"`) {
		t.Fatalf("expected INFO level, got: %s", out)
	}
	if !strings.Contains(out, "REDACTED") {
		t.Fatalf("expected redaction marker, got: %s", out)
	}
}
