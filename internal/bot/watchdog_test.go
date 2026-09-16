package bot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// TestWatchdogFiresAfterSustainedOffline prova que watchGateway fecha stuck
// depois de offlineFatalAfter ticks consecutivos sem conexão — o caminho que,
// em produção, leva Run a devolver ErrGatewayStuck e o processo a sair,
// deixando `restart: unless-stopped` subir um novo (o incidente de 15-16/09:
// 26h45 de gateway morto e healthcheck verde, sem isso).
//
// Mutação que reproduz o defeito de antes desta mudança: trocar
// `offlineTicks >= offlineFatalAfter` por uma condição que nunca é
// verdadeira (ex. `offlineTicks >= offlineFatalAfter+1000`), ou zerar
// offlineTicks a cada tick independente do resultado de liveness — em
// qualquer um dos dois, stuck nunca fecha e este teste estoura o timeout.
func TestWatchdogFiresAfterSustainedOffline(t *testing.T) {
	b := newTestBot()
	b.watchdogInterval = 5 * time.Millisecond
	// b.connected começa em false (zero value) e nunca é setado — gateway
	// permanentemente offline, como no incidente real.

	stuck := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go b.watchGateway(ctx, stuck)

	select {
	case <-stuck:
		// esperado
	case <-time.After(2 * time.Second):
		t.Fatal("watchGateway não fechou stuck após ficar offline por muito mais que offlineFatalAfter ticks")
	}
}

// TestWatchdogIgnoresBlip é a contraprova: sem ela, um watchdog que dispara
// sempre (ex. ignorando liveness()) passaria pelo teste acima igualzinho.
// O gateway fica offline por offlineFatalAfter-1 ticks, volta por um tick e
// cai de novo por só mais 1-2 ticks; o contador tem de ZERAR na reconexão,
// não só parar de incrementar — se ele só pausasse, os 9 ticks antigos
// "sobrariam" e um único tick offline depois da reconexão já fecharia stuck.
//
// Mutação que este teste pega (e a primeira versão dele não pegava): trocar
// `offlineTicks = 0` por um `continue` sem zerar no ramo em que liveness()
// reporta ok. Confirmado por provocação: com a mutação aplicada, este teste
// falhou (stuck fechou); com `offlineTicks = 0` restaurado, passou.
func TestWatchdogIgnoresBlip(t *testing.T) {
	b := newTestBot()
	b.watchdogInterval = 20 * time.Millisecond
	// heartbeat() precisa de uma sessão não-nula assim que connected virar
	// true: liveness() chama b.heartbeat() -> b.session.HeartbeatLatency().
	b.session = &discordgo.Session{}
	// offline desde o início (zero value), como um blip real de reconexão.

	stuck := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go b.watchGateway(ctx, stuck)

	// offlineFatalAfter=10: 9 ticks offline (t=20..180ms) levam offlineTicks
	// a 9. Reconecta em t=190ms (antes do tick 10, em t=200ms) — esse tick
	// tem de zerar o contador. Cai de novo em t=210ms (antes do tick 11, em
	// t=220ms): com o reset correto, esse é só o 1º tick offline do novo
	// episódio; sem reset, seria o 10º acumulado e fecharia stuck.
	time.AfterFunc(190*time.Millisecond, func() { b.connected.Store(true) })
	time.AfterFunc(210*time.Millisecond, func() { b.connected.Store(false) })

	select {
	case <-stuck:
		t.Fatal("watchGateway fechou stuck com só 1-2 ticks offline após a reconexão — o contador não zerou na volta")
	case <-time.After(400 * time.Millisecond):
		// esperado: stuck segue aberto (o segundo episódio offline é curto
		// demais para disparar por conta própria)
	}
}

// TestWaitReturnsErrGatewayStuck é a fiação do caminho que chama o watchdog:
// prova que wait() — o que Run de fato bloqueia em, extraído justamente
// porque Run inteiro exige session.Open() real — reage a stuck fechado
// devolvendo ErrGatewayStuck e logando em nível que chega a produção
// (LOG_LEVEL=info), sem chamar removeCommands (queda não é shutdown limpo).
//
// Mutação que este teste pega: trocar o select de wait() para observar só
// ctx.Done(), ignorando stuck — o teste trava no timeout de deadline do
// próprio `go test`, porque wait() nunca retorna.
func TestWaitReturnsErrGatewayStuck(t *testing.T) {
	b, logs := newTestBotWithLogs()

	stuck := make(chan struct{})
	close(stuck) // já fechado: simula o watchdog tendo decidido "preso"

	err := b.wait(context.Background(), stuck, false, nil)

	if !errors.Is(err, ErrGatewayStuck) {
		t.Fatalf("wait() err = %v, want ErrGatewayStuck", err)
	}
	if !strings.Contains(logs.String(), `"msg":"gateway stuck offline"`) {
		t.Fatalf("log não contém \"gateway stuck offline\":\n%s", logs.String())
	}
}
