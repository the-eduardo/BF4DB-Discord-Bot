package kuma

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
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
