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

	// Caso positivo: a asserção acima só reprova latência negativa, então um
	// liveness() que devolvesse ping fixo 0 para sempre passaria por ela — e é
	// esse valor que vai ao push do Kuma. A medida saudável tem de atravessar
	// liveness() intacta.
	b.session = &discordgo.Session{LastHeartbeatSent: now.Add(-150 * time.Millisecond), LastHeartbeatAck: now}
	if ok, lat := b.liveness(); !ok || lat != 150*time.Millisecond {
		t.Fatalf("liveness() = (%v, %v), want (true, 150ms)", ok, lat)
	}
}

// TestLivenessRejectsStaleHeartbeatAck prova o gate novo: b.connected mentindo
// "conectado" enquanto o gateway parou de ackar heartbeats (o zumbi do
// incidente de 15-16/09/2026, sem Disconnect nunca disparado) tem de reprovar.
func TestLivenessRejectsStaleHeartbeatAck(t *testing.T) {
	b := newTestBot()
	now := time.Now().UTC()
	b.connected.Store(true)
	b.session = &discordgo.Session{LastHeartbeatSent: now, LastHeartbeatAck: now.Add(-10 * time.Minute)}

	if ok, _ := b.liveness(); ok {
		t.Fatal("liveness() = true com ack de 10min, want false (gateway zumbi)")
	}
}

// Controles positivos: sem eles um liveness() que sempre devolvesse false
// passaria pelo teste acima.
func TestLivenessAcceptsFreshOrUnmeasuredAck(t *testing.T) {
	b := newTestBot()
	now := time.Now().UTC()
	b.connected.Store(true)

	// Ack fresco: a latencia clampada tem que atravessar intacta.
	b.session = &discordgo.Session{LastHeartbeatSent: now.Add(-150 * time.Millisecond), LastHeartbeatAck: now}
	if ok, lat := b.liveness(); !ok || lat != 150*time.Millisecond {
		t.Fatalf("liveness() = (%v, %v), want (true, 150ms)", ok, lat)
	}

	// Sessao recem-construida (valor zero de LastHeartbeatAck, como um teste
	// que nao passou por discordgo.New) nao pode ser lida como morta -- em
	// producao isso nunca e zero (discordgo.New semeia LastHeartbeatAck=now),
	// mas o gate nao pode presumir isso.
	b.session = &discordgo.Session{}
	if ok, _ := b.liveness(); !ok {
		t.Fatal("liveness() = false com LastHeartbeatAck zero, want true (sem medicao ainda != morto)")
	}
}

// TestLivenessResyncsFlagStuckAtFalse prova o gate de ressincronia:
// b.connected fica false para sempre se o handler de Disconnect for entregue
// DEPOIS do handler de Resumed do mesmo ciclo (discordgo despacha os dois em
// goroutines sem ordem entre si). Com a sessão real de pé (DataReady=true,
// ack fresco), liveness() tem que perceber e levantar a flag sozinho, em vez
// de deixar o watchdog matar um processo saudável.
func TestLivenessResyncsFlagStuckAtFalse(t *testing.T) {
	b := newTestBot()
	now := time.Now().UTC()
	b.connected.Store(false)
	b.session = &discordgo.Session{DataReady: true, LastHeartbeatSent: now.Add(-150 * time.Millisecond), LastHeartbeatAck: now}

	ok, _ := b.liveness()
	if !ok {
		t.Fatal("liveness() = false com sessão viva (DataReady=true, ack fresco), want true (ressincronia)")
	}
	if !b.connected.Load() {
		t.Fatal("liveness() não levantou b.connected de volta a true")
	}
}

// TestLivenessNaoRessincronizaComAckVelho é a contraprova do zumbi: sem ela, o
// gate acima mascararia o incidente de 15-16/09/2026 (sessão com DataReady
// ainda true mas heartbeats mortos há minutos) como uma simples dessincronia.
func TestLivenessNaoRessincronizaComAckVelho(t *testing.T) {
	b := newTestBot()
	now := time.Now().UTC()
	b.connected.Store(false)
	b.session = &discordgo.Session{DataReady: true, LastHeartbeatSent: now.Add(-11 * time.Minute), LastHeartbeatAck: now.Add(-10 * time.Minute)}

	if ok, _ := b.liveness(); ok {
		t.Fatal("liveness() = true com ack de 10min e connected=false, want false (zumbi real, não ressincronizar)")
	}
	if b.connected.Load() {
		t.Fatal("liveness() levantou connected com ack velho — mascararia o zumbi de 15-16/09")
	}
}

// TestLivenessNaoRessincronizaSemDataReady é a segunda contraprova: um
// Disconnect real zera DataReady (wsapi.go) antes de emitir o próprio evento,
// então connected=false com DataReady=false é uma queda genuína, não uma
// corrida entre handlers — não deve ressincronizar mesmo com ack recente
// (herdado da sessão anterior).
func TestLivenessNaoRessincronizaSemDataReady(t *testing.T) {
	b := newTestBot()
	now := time.Now().UTC()
	b.connected.Store(false)
	b.session = &discordgo.Session{DataReady: false, LastHeartbeatSent: now.Add(-150 * time.Millisecond), LastHeartbeatAck: now}

	if ok, _ := b.liveness(); ok {
		t.Fatal("liveness() = true com DataReady=false, want false (queda genuína)")
	}
	if b.connected.Load() {
		t.Fatal("liveness() levantou connected sem DataReady")
	}
}

// pingCapturingTransport records the body of every PATCH
// (InteractionResponseEdit hits the API with PATCH) and answers every request
// with an empty, valid JSON body so discordgo's response decoding doesn't fail
// the test. The name is prefixed because internal/bot has more than one
// capturing transport across test files and they share the package namespace.
type pingCapturingTransport struct {
	mu    sync.Mutex
	edits [][]byte
}

func (c *pingCapturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
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

	transport := &pingCapturingTransport{}
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

	// Segundo caso, positivo: exigir só "API: 0ms" deixaria passar um
	// handlePing com a latência hardcoded em zero. Com a sessão do Bot medindo
	// 150ms, esse valor tem de chegar ao corpo enviado ao Discord.
	b.session = &discordgo.Session{LastHeartbeatSent: now.Add(-150 * time.Millisecond), LastHeartbeatAck: now}
	transport.edits = nil
	b.handlePing(s, i)
	if len(transport.edits) != 1 || !strings.Contains(string(transport.edits[0]), "API: 150ms") {
		t.Fatalf("latência saudável não chegou ao corpo: %s", transport.edits)
	}
}
