package bot

import (
	"context"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Open() e CloseWithCode() do discordgo v0.29.0 seguram s.Lock() da sessao
// durante I/O de rede SEM deadline (wsapi.go: Open fica em ReadMessage esperando
// o READY; CloseWithCode espera o wsMutex). Um RLock novo em liveness() passa a
// bloquear pelo mesmo tempo: watchGateway nunca conta os ticks, stuck nunca
// fecha, wait() nunca retorna e o pusher do Kuma congela junto -- um zumbi que
// nunca reinicia. Estes testes seguram o Lock por TODA a duracao do teste
// (destravado so no t.Cleanup, para nao vazar goroutine bloqueada na suite).

// lockedSessionBot devolve um Bot cuja sessao esta com Lock() seguro, como
// durante um Open()/Close() pendurado, e a solta no fim do teste.
func lockedSessionBot(t *testing.T, connected bool) *Bot {
	t.Helper()
	b := newTestBot()
	b.session = &discordgo.Session{DataReady: true, LastHeartbeatAck: time.Now()}
	b.connected.Store(connected)
	b.session.Lock()
	t.Cleanup(b.session.Unlock)
	return b
}

// TestLivenessDoesNotBlockOnSessionLock: liveness() com o Lock seguro tem de
// voltar rapido e reportar "nao vivo", nos dois valores de connected.
//
// Mutacao que este teste pega: trocar TryRLock por RLock em snapshot() --
// liveness() bloqueia, o select estoura os 500ms.
func TestLivenessDoesNotBlockOnSessionLock(t *testing.T) {
	for _, connected := range []bool{false, true} {
		connected := connected
		t.Run(map[bool]string{false: "connected=false", true: "connected=true"}[connected], func(t *testing.T) {
			b := lockedSessionBot(t, connected)

			type result struct {
				ok  bool
				lat time.Duration
			}
			done := make(chan result, 1)
			go func() {
				ok, lat := b.liveness()
				done <- result{ok, lat}
			}()

			select {
			case r := <-done:
				if r.ok || r.lat != 0 {
					t.Fatalf("liveness() = (%v, %v) com o Lock da sessao seguro, want (false, 0)", r.ok, r.lat)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("liveness() bloqueou no lock da sessao: watchGateway e o pusher congelariam junto")
			}
		})
	}
}

// TestWatchdogFiresWhenSessionLockedForever: a consequencia que importa. Com o
// Lock preso, watchGateway tem de continuar contando ticks e fechar stuck.
func TestWatchdogFiresWhenSessionLockedForever(t *testing.T) {
	for _, connected := range []bool{false, true} {
		connected := connected
		t.Run(map[bool]string{false: "connected=false", true: "connected=true"}[connected], func(t *testing.T) {
			b := lockedSessionBot(t, connected)
			b.watchdogInterval = 5 * time.Millisecond

			stuck := make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			go b.watchGateway(ctx, stuck)

			select {
			case <-stuck:
			case <-time.After(2 * time.Second):
				t.Fatal("watchGateway nao fechou stuck com o lock da sessao preso: zumbi que nunca reinicia")
			}
		})
	}
}

// TestLivenessLockFreeStillWorks e a contraprova positiva: o mesmo bot, com o
// lock LIVRE, tem de continuar reportando vivo -- senao um liveness() que
// sempre devolvesse false passaria pelos testes acima.
func TestLivenessLockFreeStillWorks(t *testing.T) {
	for _, connected := range []bool{false, true} {
		b := newTestBot()
		b.session = &discordgo.Session{DataReady: true, LastHeartbeatSent: time.Now().Add(-150 * time.Millisecond), LastHeartbeatAck: time.Now()}
		b.connected.Store(connected)
		if ok, _ := b.liveness(); !ok {
			t.Fatalf("liveness() = false com lock livre e sessao viva (connected=%v), want true", connected)
		}
	}
}
