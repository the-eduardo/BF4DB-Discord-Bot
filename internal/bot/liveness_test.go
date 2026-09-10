package bot

import (
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// TestHeartbeatNeverReportsNegativeLatency fixa o clamp: discordgo grava
// LastHeartbeatAck só no Op 11, então no intervalo entre o envio do heartbeat e
// o ack (um RTT) a subtração ainda mede contra o ciclo anterior e dá negativo.
func TestHeartbeatNeverReportsNegativeLatency(t *testing.T) {
	b := newTestBot()
	now := time.Now().UTC()

	// Estado real do discordgo entre o envio do heartbeat e o Op 11: Sent
	// acabou de ser gravado, Ack ainda é o do ciclo anterior (41.25s atrás).
	b.session = &discordgo.Session{LastHeartbeatSent: now, LastHeartbeatAck: now.Add(-41250 * time.Millisecond)}
	if lat := b.heartbeat(); lat < 0 {
		t.Fatalf("heartbeat() = %v, want >= 0", lat)
	}

	// O clamp não pode achatar a medida normal: ack 150ms depois do envio.
	b.session = &discordgo.Session{LastHeartbeatSent: now.Add(-150 * time.Millisecond), LastHeartbeatAck: now}
	if lat := b.heartbeat(); lat != 150*time.Millisecond {
		t.Fatalf("heartbeat() = %v, want 150ms", lat)
	}
}

// TestLivenessUsesTheClampedHeartbeat é a fiação: prova que liveness() (o que
// de fato alimenta o push do Kuma, bot.go:127) chama o heartbeat clampado, não
// b.session.HeartbeatLatency() direto.
func TestLivenessUsesTheClampedHeartbeat(t *testing.T) {
	b := newTestBot()
	now := time.Now().UTC()
	b.session = &discordgo.Session{LastHeartbeatSent: now, LastHeartbeatAck: now.Add(-41250 * time.Millisecond)}
	b.connected.Store(true)

	ok, lat := b.liveness()
	if !ok || lat < 0 {
		t.Fatalf("liveness() = (%v, %v), want (true, >= 0)", ok, lat)
	}
}
