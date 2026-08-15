// Package kuma implements the dead-man switch heartbeat used by the other bots
// on this host: Uptime Kuma marks the monitor DOWN when the pushes stop.
package kuma

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"
)

// DefaultInterval is half the heartbeat window configured on the monitor, so a
// single missed push does not raise an alert.
const DefaultInterval = 60 * time.Second

// defaultRetryDelay is how long pushIfAlive waits before retrying a single
// failed push. Kept short: the known failure mode (Kuma's own push endpoint
// answering 404 right after its DB cleanup, louislam/uptime-kuma#2746) clears
// within seconds, well inside the 60s interval to the next scheduled push.
const defaultRetryDelay = 5 * time.Second

// Pusher periodically reports liveness to an Uptime Kuma push monitor.
type Pusher struct {
	url        string
	interval   time.Duration
	retryDelay time.Duration
	client     *http.Client
	log        *slog.Logger

	consecutive atomic.Int64 // falhas de push consecutivas, apos a 2a tentativa
}

// NewPusher returns nil when no URL is configured, which makes the heartbeat
// entirely optional.
func NewPusher(pushURL string, log *slog.Logger) *Pusher {
	if pushURL == "" {
		return nil
	}
	return &Pusher{
		url:        pushURL,
		interval:   DefaultInterval,
		retryDelay: defaultRetryDelay,
		client:     &http.Client{Timeout: 10 * time.Second},
		log:        log,
	}
}

// Run pushes every interval for as long as alive reports true, and stops with
// ctx. A nil Pusher is a no-op so callers need no special case.
func (p *Pusher) Run(ctx context.Context, alive func() (bool, time.Duration)) {
	if p == nil {
		return
	}
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	p.pushIfAlive(ctx, alive)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pushIfAlive(ctx, alive)
		}
	}
}

func (p *Pusher) pushIfAlive(ctx context.Context, alive func() (bool, time.Duration)) {
	// Only a connected gateway counts as alive: a process that is up but
	// disconnected from Discord answers no commands, and the whole point of
	// this monitor is to catch exactly that.
	ok, latency := alive()
	if !ok {
		return
	}
	if err := p.push(ctx, latency); err != nil {
		// Uma falha isolada de push nao deve virar WARN nem beat perdido: o
		// endpoint de push do Kuma responde 404 logo apos o proprio DB cleanup
		// dele (louislam/uptime-kuma#2746), que nao e' retryable por natureza,
		// mas passa na tentativa seguinte segundos depois.
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.retryDelay):
		}
		if err = p.push(ctx, latency); err != nil {
			n := p.consecutive.Add(1)
			if n >= 3 {
				p.log.Error("kuma push failed twice in a row", "err", err, "consecutive", n)
			} else {
				p.log.Warn("kuma push failed twice in a row", "err", err, "consecutive", n)
			}
			return
		}
	}
	p.consecutive.Store(0)
}

func (p *Pusher) push(ctx context.Context, latency time.Duration) error {
	endpoint, err := url.Parse(p.url)
	if err != nil {
		return fmt.Errorf("invalid push URL: %w", err)
	}
	q := endpoint.Query()
	q.Set("status", "up")
	q.Set("msg", "gateway connected")
	q.Set("ping", fmt.Sprintf("%d", latency.Milliseconds()))
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	res, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<10))

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("push returned %s", res.Status)
	}
	return nil
}
