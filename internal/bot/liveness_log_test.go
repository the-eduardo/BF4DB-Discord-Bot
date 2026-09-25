package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// TestLivenessLogsStaleAck: o gate de ack parado é o único sinal no log de um
// gateway zumbi (connected=true, nenhum Disconnect) — e o log de produção roda
// em LOG_LEVEL=info, então rebaixar esta linha para Debug a apagaria em
// silêncio (a mutação passava pela suíte inteira na drenagem de 25/09/2026).
func TestLivenessLogsStaleAck(t *testing.T) {
	b, logs := newTestBotWithLogs()
	now := time.Now().UTC()
	b.connected.Store(true)

	// Contraprova: ack fresco não loga nada.
	b.session = &discordgo.Session{LastHeartbeatSent: now.Add(-time.Second), LastHeartbeatAck: now}
	b.liveness()
	if strings.Contains(logs.String(), "sem ack de heartbeat") {
		t.Fatalf("ack fresco gerou o aviso de zumbi:\n%s", logs.String())
	}

	b.session = &discordgo.Session{LastHeartbeatSent: now, LastHeartbeatAck: now.Add(-10 * time.Minute)}
	b.liveness()
	out := logs.String()
	if !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, `"msg":"gateway conectado mas sem ack de heartbeat"`) {
		t.Fatalf("ack de 10min não chegou ao log em WARN:\n%s", out)
	}
}
