package kuma

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// testLoggerWithBuffer captura registros JSON (o mesmo handler de produção)
// pra o teste poder afirmar o NÍVEL emitido, não só que algo foi logado.
func testLoggerWithBuffer() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})), &buf
}

func TestNewPusherIsOptional(t *testing.T) {
	if p := NewPusher("", testLogger()); p != nil {
		t.Error("an empty URL should disable the heartbeat")
	}
	// A nil Pusher must be safe to run.
	var p *Pusher
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.Run(ctx, func() (bool, time.Duration) { return true, 0 })
}

func TestPushSendsStatusAndPing(t *testing.T) {
	var (
		calls  atomic.Int32
		status atomic.Value
		ping   atomic.Value
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		status.Store(r.URL.Query().Get("status"))
		ping.Store(r.URL.Query().Get("ping"))
	}))
	defer srv.Close()

	p := NewPusher(srv.URL+"/api/push/abc123", testLogger())
	if err := p.push(context.Background(), 42*time.Millisecond); err != nil {
		t.Fatalf("push: %v", err)
	}
	if calls.Load() != 1 || status.Load() != "up" || ping.Load() != "42" {
		t.Errorf("calls=%d status=%v ping=%v", calls.Load(), status.Load(), ping.Load())
	}
}

// The whole point of the dead-man switch is that a process which is up but
// disconnected from Discord must stop pushing, so Kuma marks it DOWN.
func TestNoPushWhileDisconnected(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer srv.Close()

	p := NewPusher(srv.URL, testLogger())
	p.pushIfAlive(context.Background(), func() (bool, time.Duration) { return false, 0 })

	if calls.Load() != 0 {
		t.Errorf("pushed %d times while disconnected, want 0", calls.Load())
	}
}

func TestRunPushesImmediatelyAndStopsWithContext(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer srv.Close()

	p := NewPusher(srv.URL, testLogger())
	p.interval = time.Hour // only the immediate push should land

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx, func() (bool, time.Duration) { return true, time.Millisecond })
		close(done)
	}()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop with the context")
	}
	if calls.Load() != 1 {
		t.Errorf("pushed %d times, want 1 (immediate push only)", calls.Load())
	}
}

// The known failure mode: the push endpoint answers 404 once (Kuma's own DB
// cleanup, louislam/uptime-kuma#2746) and recovers on the very next try. A
// single failed attempt must not become a WARN, and no beat should be lost.
func TestPushIfAliveRetriesOnceBeforeLoggingFailure(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := NewPusher(srv.URL, testLogger())
	p.retryDelay = time.Millisecond

	p.pushIfAlive(context.Background(), func() (bool, time.Duration) { return true, 0 })

	if calls.Load() != 2 {
		t.Fatalf("esperava 2 tentativas (falha + retry), veio %d", calls.Load())
	}
	if p.consecutive.Load() != 0 {
		t.Errorf("consecutive deveria zerar apos sucesso no retry, veio %d", p.consecutive.Load())
	}
}

// Duas falhas seguidas (404 na 1a e na 2a tentativa) tem que contar como UMA
// falha consecutiva -- e' isso que separa "blip" de "queda de verdade".
func TestPushIfAliveCountsConsecutiveFailuresAfterBothAttemptsFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	p := NewPusher(srv.URL, testLogger())
	p.retryDelay = time.Millisecond

	p.pushIfAlive(context.Background(), func() (bool, time.Duration) { return true, 0 })
	if p.consecutive.Load() != 1 {
		t.Fatalf("esperava 1 falha consecutiva, veio %d", p.consecutive.Load())
	}

	p.pushIfAlive(context.Background(), func() (bool, time.Duration) { return true, 0 })
	if p.consecutive.Load() != 2 {
		t.Fatalf("esperava 2 falhas consecutivas, veio %d", p.consecutive.Load())
	}
}

func TestPushReportsBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	p := NewPusher(srv.URL, testLogger())
	if err := p.push(context.Background(), 0); err == nil {
		t.Error("want an error for a non-200 push")
	}
}

// TestDefaultRetryDelayIs20Seconds prova o valor calibrado em produção
// (16/08/2026): sem este teste, o construtor podia voltar para o antigo 5s
// (ou qualquer outro valor) sem que nenhum teste da suíte percebesse — os
// testes de retry substituem p.retryDelay por time.Millisecond antes de
// exercitar pushIfAlive, então nunca leem o default de fato usado em
// produção. Mutação que prova o buraco: trocar defaultRetryDelay de volta
// para 5*time.Second faz este teste falhar; sem ele, a suíte inteira
// permanecia verde com a regressão.
func TestDefaultRetryDelayIs20Seconds(t *testing.T) {
	p := NewPusher("http://example.invalid/push", testLogger())
	if p.retryDelay != 20*time.Second {
		t.Errorf("retryDelay = %v, want 20s", p.retryDelay)
	}
}

// A janela desconectada quebra a contiguidade das falhas: sem isto, falhas de
// push separadas por horas de gateway caído somam no mesmo contador e viram
// um ERROR falso de "3 falhas consecutivas" quando na verdade foram blips
// isolados intercalados com desconexões.
func TestConsecutiveResetsWhenGatewayDisconnects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	p := NewPusher(srv.URL, testLogger())
	p.retryDelay = time.Millisecond

	p.pushIfAlive(context.Background(), func() (bool, time.Duration) { return true, 0 })
	p.pushIfAlive(context.Background(), func() (bool, time.Duration) { return true, 0 })
	if p.consecutive.Load() != 2 {
		t.Fatalf("esperava 2 falhas consecutivas antes da desconexão, veio %d", p.consecutive.Load())
	}

	p.pushIfAlive(context.Background(), func() (bool, time.Duration) { return false, 0 })
	if p.consecutive.Load() != 0 {
		t.Errorf("consecutive deveria zerar ao desconectar, veio %d", p.consecutive.Load())
	}
}

// Garante que o fix acima não cega o dead-man de verdade: uma queda contínua
// do endpoint de push do Kuma, com o gateway conectado o tempo todo (sem
// nenhuma desconexão zerando o contador no meio), ainda precisa virar ERROR
// na 3a falha consecutiva.
func TestErrorStillRaisedOnTrulyConsecutiveFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	log, logs := testLoggerWithBuffer()
	p := NewPusher(srv.URL, log)
	p.retryDelay = time.Millisecond

	for i := 0; i < 3; i++ {
		p.pushIfAlive(context.Background(), func() (bool, time.Duration) { return true, 0 })
	}
	if p.consecutive.Load() != 3 {
		t.Fatalf("esperava 3 falhas consecutivas, veio %d", p.consecutive.Load())
	}

	out := logs.String()
	if !strings.Contains(out, `"level":"ERROR"`) || !strings.Contains(out, `"consecutive":3`) {
		t.Errorf("esperava um registro ERROR com consecutive=3, veio: %s", out)
	}
}
