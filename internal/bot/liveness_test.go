package bot

import (
	"io"
	"net/http"
	"strings"
	"sync"
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

// capturingTransport records the body of every PATCH (InteractionResponseEdit
// hits the API with PATCH) and answers every request with an empty, valid
// JSON body so discordgo's response decoding doesn't fail the test.
type capturingTransport struct {
	mu    sync.Mutex
	edits [][]byte
}

func (c *capturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPatch {
		body, _ := io.ReadAll(req.Body)
		c.mu.Lock()
		c.edits = append(c.edits, body)
		c.mu.Unlock()
	}
	return &http.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Body:       io.NopCloser(strings.NewReader("{}")),
		Header:     make(http.Header),
	}, nil
}

// TestHandlePingUsesClampedHeartbeat é a fiação que faltava: prova que o /ping
// de fato entregue ao Discord (handlePing, commands.go) usa b.heartbeat()
// clampado para montar "API: Xms", e não s.HeartbeatLatency() lida direto da
// *discordgo.Session recebida como parâmetro — o caminho que a mutação de
// commands.go revelou não estar coberto por nenhum teste anterior.
func TestHandlePingUsesClampedHeartbeat(t *testing.T) {
	b := newTestBot()
	now := time.Now().UTC()
	// b.session (o campo do Bot, o que heartbeat() lê) está no meio do ciclo:
	// HeartbeatLatency() crua aqui seria negativa.
	b.session = &discordgo.Session{LastHeartbeatSent: now, LastHeartbeatAck: now.Add(-41250 * time.Millisecond)}

	transport := &capturingTransport{}
	s, err := discordgo.New("Bot token-de-teste")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	s.Client = &http.Client{Transport: transport}

	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		AppID: "1027015041326788659",
		Token: "TESTTOKEN",
	}}

	b.handlePing(s, i)

	if len(transport.edits) != 1 {
		t.Fatalf("esperava exatamente 1 edit, recebi %d", len(transport.edits))
	}
	body := string(transport.edits[0])
	if strings.Contains(body, "-41") {
		t.Errorf("handlePing mandou a latência negativa crua: %s", body)
	}
	// Controle positivo: prova que o campo "API: Xms" chegou de fato com o
	// valor clampado, não que ele sumiu do corpo (o que também "passaria").
	if !strings.Contains(body, "API: 0ms") {
		t.Fatalf("esperava 0ms clampado no corpo, recebi: %s", body)
	}
}
