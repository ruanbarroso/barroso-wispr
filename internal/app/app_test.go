package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ruanbarroso/barroso-wispr/internal/gateway"
)

type micFake struct {
	mu      sync.Mutex
	active  bool
	stopped chan struct{}
}

func (m *micFake) Iniciar(func([]byte)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active = true
	return nil
}

func (m *micFake) Parar() {
	m.mu.Lock()
	if m.active {
		m.active = false
		close(m.stopped)
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

type chatFake struct{}

func (chatFake) Chat(context.Context, string, string, string) (string, error) {
	return "Olá, KIS!", nil
}

func TestAtalhoDuploTravaENovoPressionamentoEncerra(t *testing.T) {
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
	defer server.Close()

	mic := &micFake{stopped: make(chan struct{})}
	s := &sessaoFake{finalizou: make(chan struct{})}
	digitado := make(chan string, 1)
	a := Novo(Deps{
		Gateway:   gateway.Novo(server.URL+"/v1", "test"),
		Chat:      chatFake{},
		Abrir:     func(context.Context, string, string, string) (Sessao, error) { return s, nil },
		Microfone: mic,
		Digitar:   func(text string) error { digitado <- text; return nil },
		Dicionario: func() []string {
			return []string{"KIS"}
		},
		ModeloVoz:  "barroso-audio-stream",
		ModeloQuik: "barroso-quick",
	})

	// Toque 1: armar; toque 2: iniciar travado.
	a.Pressionou()
	a.Soltou()
	a.Pressionou()
	a.Soltou() // soltar o segundo toque mantém a gravação travada

	// Aguarda a sessão abrir, como o usuário esperaria após o segundo toque.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("sessão Live não abriu")
		default:
		}
		if mic.active {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// O toque seguinte deve encerrar a gravação imediatamente — mesmo antes
	// de seu KeyUp chegar (o bug relatado dependia desse evento).
	a.Pressionou()
	select {
	case <-mic.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("o novo pressionamento não parou o microfone")
	}
	select {
	case <-s.finalizou:
	case <-time.After(2 * time.Second):
		t.Fatal("sessão Live não foi finalizada")
	}
	select {
	case got := <-digitado:
		if !strings.Contains(got, "KIS") {
			t.Fatalf("texto limpo inesperado: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("texto não foi digitado após destravar")
	}
}
