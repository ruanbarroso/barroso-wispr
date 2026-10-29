package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ruanbarroso/barroso-wispr/internal/atalho"
	"github.com/ruanbarroso/barroso-wispr/internal/gateway"
)

type micFake struct {
	mu      sync.Mutex
	active  bool
	stopped chan struct{}
	err     error
}

func (m *micFake) Iniciar(f func([]byte)) error {
	return m.iniciarComPCM(f, pcmSinteticoFalado(time.Second))
}

func (m *micFake) iniciarComPCM(f func([]byte), pcm []byte) error {
	m.mu.Lock()
	if m.err != nil {
		m.mu.Unlock()
		return m.err
	}
	m.active = true
	m.mu.Unlock()
	if len(pcm) > 0 {
		f(pcm)
	}
	return nil
}

func (m *micFake) Parar() {
	m.mu.Lock()
	if m.active {
		m.active = false
		if m.stopped != nil {
			close(m.stopped)
			m.stopped = nil // o mesmo microfone serve a vários ditados
		}
	}
	m.mu.Unlock()
}

type sessaoFake struct {
	finalizou chan struct{}
}

func (s *sessaoFake) EnviarAudio(context.Context, []byte) error { return nil }
func (s *sessaoFake) Finalizar(context.Context, time.Duration, time.Duration) (string, error) {
	close(s.finalizou)
	return "ola, KIS", nil
}
func (s *sessaoFake) Uso() (int, int)      { return 1, 1 }
func (s *sessaoFake) Fechar()              {}
func (s *sessaoFake) Caiu() (bool, string) { return false, "" }

// sessaoQueCai transcreve um começo e depois perde a conexão sozinha.
type sessaoQueCai struct {
	mu   sync.Mutex
	caiu bool
}

func (s *sessaoQueCai) EnviarAudio(context.Context, []byte) error { return nil }
func (s *sessaoQueCai) Finalizar(context.Context, time.Duration, time.Duration) (string, error) {
	return "só o começo", errors.New("conexão encerrada")
}
func (s *sessaoQueCai) Uso() (int, int) { return 0, 0 }
func (s *sessaoQueCai) Fechar()         {}
func (s *sessaoQueCai) derrubar()       { s.mu.Lock(); s.caiu = true; s.mu.Unlock() }
func (s *sessaoQueCai) Caiu() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.caiu, "status = StatusGoingAway"
}

type bloqueiaFinal struct {
	iniciou chan struct{}
	libera  chan struct{}
}

func (s *bloqueiaFinal) EnviarAudio(context.Context, []byte) error { return nil }
func (s *bloqueiaFinal) Finalizar(context.Context, time.Duration, time.Duration) (string, error) {
	close(s.iniciou)
	<-s.libera
	return "texto final", nil
}
func (s *bloqueiaFinal) Uso() (int, int)      { return 0, 0 }
func (s *bloqueiaFinal) Fechar()              {}
func (s *bloqueiaFinal) Caiu() (bool, string) { return false, "" }

// Queda da Live no meio do ditado: sem o usuário soltar o atalho, a captura
// para, toca o som de erro (não o de fim), a borda apaga e o que já foi
// transcrito é colado.
func TestQuedaDaConexaoEncerraAvisaEColaOParcial(t *testing.T) {
	sess := &sessaoQueCai{}
	colado := make(chan string, 1)
	a := newAppTest(t, sess, func(s string) error { colado <- s; return nil })
	a.d.Chat = chatEco{}
	sons := make(chan string, 4)
	a.d.BeepReady = func() { sons <- "ready" }
	a.d.BeepDone = func() { sons <- "done" }
	a.d.BeepErro = func() { sons <- "erro" }
	estados := make(chan bool, 4)
	a.d.EstadoGravacao = func(ativo bool) { estados <- ativo }

	a.comecar()
	if got := receberEstado(t, estados); !got {
		t.Fatal("borda não acendeu")
	}
	<-sons // ready
	sess.derrubar()
	if got := receberEstado(t, estados); got {
		t.Fatal("borda não apagou após a queda")
	}
	select {
	case got := <-sons:
		if got != "erro" {
			t.Fatalf("som = %q, want erro", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sem som de erro após a queda")
	}
	select {
	case got := <-colado:
		if got != "só o começo" {
			t.Fatalf("colado = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("o parcial não foi colado")
	}
	// A colagem acontece antes de o ditado sair da contagem de ativos.
	for limite := time.Now().Add(time.Second); !a.Ocioso(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(limite) {
			t.Fatal("app não voltou ao ocioso")
		}
	}
	// O próximo Ctrl+Alt começa um ditado novo.
	a.mu.Lock()
	gravando := a.maq.Gravando()
	a.mu.Unlock()
	if gravando {
		t.Fatal("máquina do atalho ficou presa em gravando")
	}
}

func TestEsperaFinalCresceComAGravacao(t *testing.T) {
	q1, t1 := esperaFinal(3 * time.Second)
	q2, t2 := esperaFinal(95 * time.Second)
	if q1 < time.Second || q2 <= q1 || t2 <= t1 {
		t.Fatalf("curta=(%v,%v) longa=(%v,%v)", q1, t1, q2, t2)
	}
	if q2 > 4*time.Second || t2 > 30*time.Second {
		t.Fatalf("longa passou do teto: (%v,%v)", q2, t2)
	}
}

// sessaoTexto devolve um texto fixo quando liberada.
type sessaoTexto struct {
	texto  string
	libera chan struct{}
}

func (s *sessaoTexto) EnviarAudio(context.Context, []byte) error { return nil }
func (s *sessaoTexto) Finalizar(context.Context, time.Duration, time.Duration) (string, error) {
	<-s.libera
	return s.texto, nil
}
func (s *sessaoTexto) Uso() (int, int)      { return 0, 0 }
func (s *sessaoTexto) Fechar()              {}
func (s *sessaoTexto) Caiu() (bool, string) { return false, "" }

// Ditados que se sobrepõem (o próximo começa antes de o anterior ser colado)
// são colados uma única vez, juntos e na ordem de fala, mesmo que o último a
// terminar o processamento seja o primeiro a ter sido falado.
func TestDitadosSobrepostosSaoColadosJuntosNaOrdem(t *testing.T) {
	sessoes := []*sessaoTexto{
		{texto: "Primeiro.", libera: make(chan struct{})},
		{texto: "Segundo.", libera: make(chan struct{})},
		{texto: "Terceiro.", libera: make(chan struct{})},
	}
	var mu sync.Mutex
	n := 0
	colado := make(chan string, 4)
	a := newAppTest(t, nil, func(s string) error { colado <- s; return nil })
	a.d.Chat = chatEco{}
	a.d.Abrir = func(context.Context, string, string, string) (Sessao, error) {
		mu.Lock()
		defer mu.Unlock()
		s := sessoes[n]
		n++
		return s, nil
	}
	gravarESoltar := func() {
		a.comecar()
		a.mu.Lock()
		g := a.rec
		a.mu.Unlock()
		<-g.pronta
		go a.parar(true)
		for {
			a.mu.Lock()
			livre := a.rec == nil
			a.mu.Unlock()
			if livre {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	gravarESoltar()
	gravarESoltar()
	gravarESoltar()
	close(sessoes[2].libera)
	close(sessoes[1].libera)
	select {
	case got := <-colado:
		t.Fatalf("colou %q antes de o primeiro terminar", got)
	case <-time.After(100 * time.Millisecond):
	}
	close(sessoes[0].libera)
	select {
	case got := <-colado:
		if got != "Primeiro. Segundo. Terceiro." {
			t.Fatalf("colado = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("o lote não foi colado")
	}
	select {
	case got := <-colado:
		t.Fatalf("colagem extra: %q", got)
	case <-time.After(100 * time.Millisecond):
	}
	// Depois do lote colado, um ditado novo é colado sozinho.
	sessoes = append(sessoes, &sessaoTexto{texto: "Quarto.", libera: make(chan struct{})})
	close(sessoes[3].libera)
	gravarESoltar()
	select {
	case got := <-colado:
		if got != "Quarto." {
			t.Fatalf("colado = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ditado novo não foi colado")
	}
}

// Enquanto há uma gravação em curso, o ditado anterior já processado espera.
func TestNaoColaEnquantoOutroDitadoGrava(t *testing.T) {
	primeira := &sessaoTexto{texto: "Antes.", libera: make(chan struct{})}
	segunda := &sessaoTexto{texto: "Depois.", libera: make(chan struct{})}
	close(primeira.libera)
	close(segunda.libera)
	abertas := 0
	colado := make(chan string, 2)
	a := newAppTest(t, nil, func(s string) error { colado <- s; return nil })
	a.d.Chat = chatEco{}
	a.d.Abrir = func(context.Context, string, string, string) (Sessao, error) {
		abertas++
		if abertas == 1 {
			return primeira, nil
		}
		return segunda, nil
	}
	a.comecar()
	a.mu.Lock()
	g1 := a.rec
	a.mu.Unlock()
	<-g1.pronta
	a.mu.Lock()
	a.rec = nil
	a.mu.Unlock()
	a.comecar() // segunda começa antes de a primeira terminar de processar
	a.mu.Lock()
	g2 := a.rec
	a.mu.Unlock()
	<-g2.pronta
	go a.encerrar(g1, true)
	select {
	case got := <-colado:
		t.Fatalf("colou %q com a segunda ainda gravando", got)
	case <-time.After(300 * time.Millisecond):
	}
	go a.parar(true)
	select {
	case got := <-colado:
		if got != "Antes. Depois." {
			t.Fatalf("colado = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nada colado")
	}
}

type chatEco struct{}

func (chatEco) Chat(_ context.Context, _, _, cru string) (string, error) { return cru, nil }

type micFalho struct {
	iniciar chan struct{}
	liberar chan struct{}
	err     error
}

func (m *micFalho) Iniciar(func([]byte)) error {
	close(m.iniciar)
	<-m.liberar
	return m.err
}
func (*micFalho) Parar() {}

type chatFake struct{}

func (chatFake) Chat(context.Context, string, string, string) (string, error) {
	return "Olá, KIS!", nil
}

func newAppTest(t *testing.T, sess Sessao, paste func(string) error) *App {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/audio/live-token":
			w.Header().Set("x-served-model", "gemini-3.5-transcribe-live")
			_ = json.NewEncoder(w).Encode(gateway.Token{Token: "ephemeral", Model: "barroso-audio-stream", RequestID: "test"})
		case "/v1/audio/live-usage":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return Novo(Deps{
		Gateway:    gateway.Novo(server.URL+"/v1", "test"),
		Chat:       chatFake{},
		Abrir:      func(context.Context, string, string, string) (Sessao, error) { return sess, nil },
		Microfone:  &micFake{stopped: make(chan struct{})},
		Digitar:    paste,
		Dicionario: func() []string { return []string{"KIS"} },
		ModeloVoz:  "barroso-audio-stream", ModeloQuik: "barroso-quick",
	})
}

func TestHandoffWaitsForFinalizationAndPaste(t *testing.T) {
	sess := &bloqueiaFinal{iniciou: make(chan struct{}), libera: make(chan struct{})}
	pasteEntered := make(chan struct{})
	pasteContinue := make(chan struct{})
	a := newAppTest(t, sess, func(string) error {
		close(pasteEntered)
		<-pasteContinue
		return nil
	})
	a.comecar()
	a.mu.Lock()
	g := a.rec
	a.mu.Unlock()
	select {
	case <-g.pronta:
	case <-time.After(time.Second):
		t.Fatal("session setup did not finish")
	}
	handoff := make(chan struct{}, 1)
	a.DefinirParada(func() {})
	a.PedirTroca(func() bool { handoff <- struct{}{}; return true })
	go a.parar(true)
	select {
	case <-sess.iniciou:
	case <-time.After(time.Second):
		t.Fatal("finalization did not start")
	}
	select {
	case <-handoff:
		t.Fatal("handoff started during Live finalization")
	default:
	}
	close(sess.libera)
	select {
	case <-pasteEntered:
	case <-time.After(time.Second):
		t.Fatal("paste did not start")
	}
	select {
	case <-handoff:
		t.Fatal("handoff started before paste completed")
	default:
	}
	close(pasteContinue)
	select {
	case <-handoff:
	case <-time.After(time.Second):
		t.Fatal("handoff did not start after paste completed")
	}
}

func TestPendingHandoffPreventsNewRecording(t *testing.T) {
	sess := &sessaoFake{finalizou: make(chan struct{})}
	a := newAppTest(t, sess, func(string) error { return nil })
	a.mu.Lock()
	a.ativos = 1 // um ditado em curso: a troca fica pendente
	a.mu.Unlock()
	a.PedirTroca(func() bool { return true })
	a.Pressionou()
	a.Tick()
	if a.rec != nil {
		t.Fatal("recording started while handoff is pending")
	}
}

func TestHandoffFailureResetsHotkey(t *testing.T) {
	a := newAppTest(t, &sessaoFake{finalizou: make(chan struct{})}, func(string) error { return nil })
	a.DefinirParada(func() {})
	a.PedirTroca(func() bool { return false })
	a.Pressionou()
	a.Tick()
	if a.rec != nil {
		t.Fatal("failed handoff started a recording")
	}
	a.comMaq(func(m *atalho.Maquina) atalho.Evento { return m.Pressionou(time.Now()) })
	x := a.comMaq(func(m *atalho.Maquina) atalho.Evento { return m.Tick(time.Now().Add(atalho.Segurar)) })
	if x.comecar == nil {
		t.Fatal("hotkey did not recover after failed handoff")
	}
}

// TestToqueRapidoNaoDeixaGravacaoLigada reproduz o caso que travou a máquina:
// três toques rápidos Ctrl+Alt, o último dado enquanto o timer ainda estava no
// meio da promoção apertado→gravando. O sintoma era a gravação ficar ligada
// com o atalho ocioso, sem nenhum toque a desligar.
func TestToqueRapidoNaoDeixaGravacaoLigada(t *testing.T) {
	sess := &sessaoFake{finalizou: make(chan struct{})}
	a := newAppTest(t, sess, func(string) error { return nil })
	a.DefinirParada(func() {})

	// Toque 1: pressiona e solta antes de 200 ms (toque curto, vira candidato a travamento).
	a.Pressionou()
	a.Soltou()
	// Toque 2: dentro da janela, trava e começa a gravar.
	a.Pressionou()
	a.Soltou()
	// Toque 3: no estado travado, um Pressionou encerra a gravação.
	a.Pressionou()
	a.Soltou()

	for i := 0; i < 40; i++ {
		a.Tick()
		time.Sleep(10 * time.Millisecond)
	}
	a.mu.Lock()
	rec := a.rec
	gravando := a.maq.Gravando()
	a.mu.Unlock()
	if rec != nil {
		t.Fatal("recording left active after rapid taps")
	}
	if gravando {
		t.Fatal("hotkey still reporting a recording after rapid taps")
	}
}

// TestRecAtivaComAtalhoOciosoEncerraNoProximoToque é a rede de segurança: se
// por alguma corrida a gravação ficar ligada com o atalho ocioso, um toque
// simples tem de encerrá-la em vez de ser ignorado.
func TestRecAtivaComAtalhoOciosoEncerraNoProximoToque(t *testing.T) {
	sess := &sessaoFake{finalizou: make(chan struct{})}
	a := newAppTest(t, sess, func(string) error { return nil })

	// Simula a divergência: gravação ligada, máquina do atalho em ocioso.
	a.mu.Lock()
	g := a.reservar()
	a.mu.Unlock()
	a.iniciar(g)

	<-g.pronta
	a.mu.Lock()
	if a.maq.Gravando() || a.rec == nil {
		a.mu.Unlock()
		t.Fatal("precondition failed: want leftover recording with idle hotkey")
	}
	a.mu.Unlock()

	// Um toque simples: a rede de segurança encerra a gravação que sobrou.
	a.Pressionou()
	limite := time.Now().Add(2 * time.Second)
	for time.Now().Before(limite) {
		a.mu.Lock()
		rec := a.rec
		a.mu.Unlock()
		if rec == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("next tap did not end the leftover recording")
}

func TestAtalhoDuploTravaENovoPressionamentoEncerra(t *testing.T) {
	sess := &sessaoFake{finalizou: make(chan struct{})}
	digitado := make(chan string, 1)
	beeps := make(chan string, 2)
	a := newAppTest(t, sess, func(text string) error { digitado <- text; return nil })
	a.d.BeepReady = func() { beeps <- "ready" }
	a.d.BeepDone = func() { beeps <- "done" }

	a.Pressionou()
	a.Soltou()
	a.Pressionou()
	a.Soltou()
	// O duplo toque só vira trava depois da janela em que um terceiro toque
	// ainda poderia descartar o gesto: o tick fecha essa janela.
	a.tratar(a.comMaq(func(m *atalho.Maquina) atalho.Evento {
		return m.Tick(time.Now().Add(atalho.ToqueDuplo + time.Millisecond))
	}))
	select {
	case got := <-beeps:
		if got != "ready" {
			t.Fatalf("first beep = %q, want ready", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ready beep not played")
	}
	a.Pressionou()
	select {
	case got := <-beeps:
		if got != "done" {
			t.Fatalf("second beep = %q, want done", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("done beep not played")
	}
	select {
	case <-sess.finalizou:
	case <-time.After(2 * time.Second):
		t.Fatal("Live session not finalized")
	}
	select {
	case got := <-digitado:
		if !strings.Contains(got, "KIS") {
			t.Fatalf("unexpected cleanup: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("text not pasted")
	}
}

func TestCallbackEstadoGravacaoAcompanhaCaptura(t *testing.T) {
	sess := &sessaoFake{finalizou: make(chan struct{})}
	a := newAppTest(t, sess, func(string) error { return nil })
	estados := make(chan bool, 2)
	a.d.EstadoGravacao = func(ativo bool) { estados <- ativo }

	a.comecar()
	if got := receberEstado(t, estados); !got {
		t.Fatal("callback inicial = false, want true após iniciar o microfone")
	}

	a.mu.Lock()
	g := a.rec
	a.mu.Unlock()
	select {
	case <-g.pronta:
	case <-time.After(time.Second):
		t.Fatal("session setup did not finish")
	}
	go a.parar(true)
	if got := receberEstado(t, estados); got {
		t.Fatal("callback de parada = true, want false")
	}
	select {
	case <-sess.finalizou:
	case <-time.After(time.Second):
		t.Fatal("Live session not finalized")
	}
}

func TestCallbackEstadoGravacaoCancelaCaptura(t *testing.T) {
	sess := &sessaoFake{finalizou: make(chan struct{})}
	a := newAppTest(t, sess, func(string) error { return nil })
	estados := make(chan bool, 2)
	a.d.EstadoGravacao = func(ativo bool) { estados <- ativo }

	a.comecar()
	if got := receberEstado(t, estados); !got {
		t.Fatal("callback inicial = false, want true após iniciar o microfone")
	}
	a.mu.Lock()
	g := a.rec
	a.mu.Unlock()
	select {
	case <-g.pronta:
	case <-time.After(time.Second):
		t.Fatal("session setup did not finish")
	}
	go a.parar(false)
	if got := receberEstado(t, estados); got {
		t.Fatal("callback de cancelamento = true, want false")
	}
	select {
	case <-sess.finalizou:
		t.Fatal("cancelamento não deve finalizar a sessão")
	case <-time.After(30 * time.Millisecond):
	}
}

func TestCallbackEstadoGravacaoFalhaDeMicrofoneNotificaFalseAposIniciar(t *testing.T) {
	sess := &sessaoFake{finalizou: make(chan struct{})}
	a := newAppTest(t, sess, func(string) error { return nil })
	mic := &micFalho{iniciar: make(chan struct{}), liberar: make(chan struct{}), err: errors.New("falha de captura")}
	a.d.Microfone = mic
	estados := make(chan bool, 2)
	a.d.EstadoGravacao = func(ativo bool) { estados <- ativo }

	go a.comecar()
	select {
	case <-mic.iniciar:
	case <-time.After(time.Second):
		t.Fatal("Iniciar não começou")
	}
	assertSemEstado(t, estados)
	close(mic.liberar)
	if got := receberEstado(t, estados); got {
		t.Fatal("falha ao iniciar microfone notificou true")
	}
	assertSemEstado(t, estados)
}

func TestCallbackEstadoGravacaoNaoIncluiFinalizacaoNemColagem(t *testing.T) {
	sess := &bloqueiaFinal{iniciou: make(chan struct{}), libera: make(chan struct{})}
	colado := make(chan struct{})
	a := newAppTest(t, sess, func(string) error { close(colado); return nil })
	estados := make(chan bool, 4)
	a.d.EstadoGravacao = func(ativo bool) { estados <- ativo }

	a.comecar()
	if got := receberEstado(t, estados); !got {
		t.Fatal("callback inicial = false, want true")
	}
	a.mu.Lock()
	g := a.rec
	a.mu.Unlock()
	select {
	case <-g.pronta:
	case <-time.After(time.Second):
		t.Fatal("session setup did not finish")
	}
	go a.parar(true)
	if got := receberEstado(t, estados); got {
		t.Fatal("callback de parada = true, want false")
	}
	select {
	case <-sess.iniciou:
	case <-time.After(time.Second):
		t.Fatal("transcrição não começou")
	}
	assertSemEstado(t, estados)
	close(sess.libera)
	select {
	case <-colado:
	case <-time.After(time.Second):
		t.Fatal("texto não foi colado")
	}
	assertSemEstado(t, estados)
}

func TestEstadoProcessandoCobreDoFimDaGravacaoAteAColagem(t *testing.T) {
	sess := &bloqueiaFinal{iniciou: make(chan struct{}), libera: make(chan struct{})}
	colando := make(chan struct{})
	liberaColagem := make(chan struct{})
	a := newAppTest(t, sess, func(string) error { close(colando); <-liberaColagem; return nil })
	estados := make(chan bool, 4)
	a.d.EstadoProcessando = func(ativo bool) { estados <- ativo }

	a.comecar()
	assertSemEstado(t, estados) // gravando ainda não é processar
	go a.parar(true)
	if got := receberEstado(t, estados); !got {
		t.Fatal("processamento não começou ao fim da gravação")
	}
	select {
	case <-sess.iniciou:
	case <-time.After(time.Second):
		t.Fatal("transcrição não começou")
	}
	close(sess.libera)
	select {
	case <-colando:
	case <-time.After(time.Second):
		t.Fatal("colagem não começou")
	}
	assertSemEstado(t, estados) // continua processando durante a colagem
	close(liberaColagem)
	if got := receberEstado(t, estados); got {
		t.Fatal("processamento não terminou após a colagem")
	}
}

func TestEstadoProcessandoNaoTocaNoCancelamento(t *testing.T) {
	sess := &sessaoFake{finalizou: make(chan struct{})}
	a := newAppTest(t, sess, func(string) error { return nil })
	estados := make(chan bool, 2)
	a.d.EstadoProcessando = func(ativo bool) { estados <- ativo }

	a.comecar()
	a.mu.Lock()
	g := a.rec
	a.mu.Unlock()
	<-g.pronta
	a.parar(false)
	assertSemEstado(t, estados)
}

func receberEstado(t *testing.T, estados <-chan bool) bool {
	t.Helper()
	select {
	case estado := <-estados:
		return estado
	case <-time.After(time.Second):
		t.Fatal("callback de estado de gravação não recebido")
		return false
	}
}

func assertSemEstado(t *testing.T, estados <-chan bool) {
	t.Helper()
	select {
	case got := <-estados:
		t.Fatalf("callback de estado tardio: %t", got)
	case <-time.After(30 * time.Millisecond):
	}
}

// A borda e o som de entrada só aparecem depois que a sessão Live abriu.
func TestBordaSoAcendeDepoisDaSessaoAberta(t *testing.T) {
	sess := &sessaoFake{finalizou: make(chan struct{})}
	a := newAppTest(t, sess, func(string) error { return nil })
	libera := make(chan struct{})
	a.d.Abrir = func(context.Context, string, string, string) (Sessao, error) {
		<-libera
		return sess, nil
	}
	estados := make(chan bool, 2)
	a.d.EstadoGravacao = func(ativo bool) { estados <- ativo }
	sons := make(chan string, 2)
	a.d.BeepReady = func() { sons <- "ready" }

	a.comecar()
	assertSemEstado(t, estados)
	select {
	case <-sons:
		t.Fatal("som de entrada antes de a sessão abrir")
	case <-time.After(30 * time.Millisecond):
	}
	close(libera)
	if got := receberEstado(t, estados); !got {
		t.Fatal("borda não acendeu com a sessão aberta")
	}
	select {
	case <-sons:
	case <-time.After(time.Second):
		t.Fatal("som de entrada não tocou")
	}
}

// Soltar antes de a sessão abrir não acende a borda depois.
func TestSoltarAntesDaSessaoNaoAcendeBorda(t *testing.T) {
	sess := &sessaoFake{finalizou: make(chan struct{})}
	a := newAppTest(t, sess, func(string) error { return nil })
	libera := make(chan struct{})
	a.d.Abrir = func(context.Context, string, string, string) (Sessao, error) {
		<-libera
		return sess, nil
	}
	estados := make(chan bool, 4)
	a.d.EstadoGravacao = func(ativo bool) { estados <- ativo }
	a.comecar()
	go a.parar(true)
	if got := receberEstado(t, estados); got {
		t.Fatal("estado = true, want false ao soltar")
	}
	close(libera)
	assertSemEstado(t, estados)
}

type sessaoVazia struct{}

func (sessaoVazia) EnviarAudio(context.Context, []byte) error { return nil }
func (sessaoVazia) Finalizar(context.Context, time.Duration, time.Duration) (string, error) {
	return "", nil
}
func (sessaoVazia) Uso() (int, int)      { return 0, 0 }
func (sessaoVazia) Fechar()              {}
func (sessaoVazia) Caiu() (bool, string) { return false, "" }

type micComAudio struct {
	micFake
	pcm []byte
}

func (m *micComAudio) Iniciar(f func([]byte)) error {
	return m.micFake.iniciarComPCM(f, m.pcm)
}

// Live sem texto: o áudio inteiro vai para a transcrição de reserva e é colado.
func TestLiveSemTextoUsaReserva(t *testing.T) {
	colado := make(chan string, 1)
	a := newAppTest(t, sessaoVazia{}, func(s string) error { colado <- s; return nil })
	a.d.Chat = chatEco{}
	a.d.Microfone = &micComAudio{pcm: pcmSinteticoFalado(time.Second)}
	recebido := 0
	a.d.Transcrever = func(_ context.Context, pcm []byte) (string, error) {
		recebido = len(pcm)
		return "texto da reserva", nil
	}
	a.comecar()
	a.mu.Lock()
	g := a.rec
	a.mu.Unlock()
	<-g.pronta
	go a.parar(true)
	select {
	case got := <-colado:
		if got != "texto da reserva" {
			t.Fatalf("colado = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reserva não colou")
	}
	if recebido != 32000 {
		t.Fatalf("reserva recebeu %d bytes, want 32000", recebido)
	}
}

func TestAudioCurtoOuSilenciosoIgnorado(t *testing.T) {
	casos := []struct {
		nome string
		pcm  []byte
	}{
		{nome: "menos de um segundo", pcm: pcmSinteticoFalado(900 * time.Millisecond)},
		{nome: "silêncio", pcm: make([]byte, 2*bytesPorSegundo)},
	}
	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			colagens := 0
			a := newAppTest(t, sessaoVazia{}, func(string) error { colagens++; return nil })
			a.d.Microfone = &micComAudio{pcm: tc.pcm}
			aberturas, transcricoes := 0, 0
			a.d.Abrir = func(context.Context, string, string, string) (Sessao, error) {
				aberturas++
				return sessaoVazia{}, nil
			}
			a.d.Transcrever = func(context.Context, []byte) (string, error) {
				transcricoes++
				return "não deveria transcrever", nil
			}
			a.comecar()
			a.parar(true)
			if aberturas != 0 {
				t.Errorf("abriu %d sessão(ões), esperava nenhuma", aberturas)
			}
			if transcricoes != 0 {
				t.Errorf("transcreveu %d vez(es), esperava nenhuma", transcricoes)
			}
			if colagens != 0 {
				t.Errorf("colou %d vez(es), esperava nenhuma", colagens)
			}
		})
	}
}
