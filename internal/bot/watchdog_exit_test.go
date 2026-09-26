package bot

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestWaitStuckArmsForcedExit prova o mecanismo de (OO): quando wait() decide
// que o gateway está preso, ela tem de armar uma saída forçada — porque o
// defer b.session.Close() de Run pode travar para sempre no mesmo wsMutex que
// um heartbeat morto segura, e sem este escape a decisão de sair nunca vira
// saída de processo de verdade (o zumbi do incidente 15-16/09/2026, agravado).
//
// O bloqueio do caller simula exatamente esse defer travado: wait() já
// retornou ErrGatewayStuck, e "Run" (aqui, o teste) fica parado como ficaria
// dentro de session.Close(). O timer armado tem de disparar mesmo assim.
//
// Mutação que este teste pega: remover o time.AfterFunc do case <-stuck (o
// código de antes desta mudança) — nada chega ao canal, o teste estoura por
// timeout.
func TestWaitStuckArmsForcedExit(t *testing.T) {
	b := newTestBot()
	b.exitGrace = 50 * time.Millisecond
	exited := make(chan int, 1)
	b.exit = func(code int) { exited <- code }

	stuck := make(chan struct{})
	close(stuck)

	err := b.wait(context.Background(), stuck, false, nil)
	if !errors.Is(err, ErrGatewayStuck) {
		t.Fatalf("wait() err = %v, want ErrGatewayStuck", err)
	}

	// Dublê do defer b.session.Close() travado para sempre: wait() já voltou,
	// e o processo real ficaria bloqueado aqui até o escape disparar.
	select {
	case code := <-exited:
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("exit forçado não disparou depois de exitGrace — o defer Close() travado nunca terminaria o processo")
	}
}

// TestWaitCleanShutdownNeverArmsExit é a contraprova: o escape só pode ser
// armado no ramo stuck. Sem ela, mover o time.AfterFunc para o ramo
// ctx.Done() passaria pelo teste acima igualzinho (ele também aciona exit),
// mas produziria um os.Exit(1) forçado em todo shutdown normal — trocando um
// "stopped" limpo por um "shutdown travou" falso alguns segundos depois.
//
// Mutação que este teste pega: mover o time.AfterFunc do case <-stuck para o
// case <-ctx.Done() — este teste falharia recebendo do canal exited.
func TestWaitCleanShutdownNeverArmsExit(t *testing.T) {
	b := newTestBot()
	b.exitGrace = 20 * time.Millisecond
	exited := make(chan int, 1)
	b.exit = func(code int) { exited <- code }

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stuck := make(chan struct{})

	err := b.wait(ctx, stuck, false, nil)
	if err != nil {
		t.Fatalf("wait() err = %v, want nil (shutdown limpo)", err)
	}

	select {
	case code := <-exited:
		t.Fatalf("shutdown limpo armou saída forçada (code %d) — o escape vazou para fora do ramo stuck", code)
	case <-time.After(200 * time.Millisecond):
		// esperado: nenhuma saída forçada armada num shutdown normal.
	}
}
