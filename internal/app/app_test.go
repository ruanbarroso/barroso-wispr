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

func (m *micFake) Iniciar(func([]byte)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.active = true
	return nil
}

func (m *micFake) Parar() {
	m.mu.Lock()
	if m.active {
		m.active = false
		if m.stopped != nil {
			close(m.stopped)
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
func (s *sessaoFake) Uso() (int, int) { return 1, 1 }
func (s *sessaoFake) Fechar()         {}

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
func (s *bloqueiaFinal) Uso() (int, int) { return 0, 0 }
func (s *bloqueiaFinal) Fechar()         {}

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
	if e := a.comMaq(func(m *atalho.Maquina) atalho.Evento { return m.Tick(time.Now().Add(atalho.Segurar)) }); e != atalho.Comecar {
		t.Fatalf("hotkey did not recover after failed handoff: %v", e)
	}
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
