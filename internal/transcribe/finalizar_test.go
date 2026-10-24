package transcribe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// servidorLive simula a Live: confirma o setup, manda `antes` enquanto o
// áudio chega e, depois do audioStreamEnd, manda `depois` com `atraso` entre
// as partes e, se turno, fecha o turno.
func servidorLive(t *testing.T, antes, depois []string, atraso time.Duration, turno bool) *Sessao {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := context.Background()
		if _, _, err := c.Read(ctx); err != nil { // setup
			return
		}
		escrever := func(v any) { b, _ := json.Marshal(v); _ = c.Write(ctx, websocket.MessageText, b) }
		parcial := func(s string) {
			escrever(map[string]any{"serverContent": map[string]any{"inputTranscription": map[string]any{"text": s}}})
		}
		escrever(map[string]any{"setupComplete": map[string]any{}})
		for _, s := range antes {
			parcial(s)
		}
		for {
			_, raw, err := c.Read(ctx)
			if err != nil {
				return
			}
			if strings.Contains(string(raw), "audioStreamEnd") {
				break
			}
		}
		for _, s := range depois {
			time.Sleep(atraso)
			parcial(s)
		}
		if turno {
			// Como a Live real: generationComplete, sem turnComplete.
			escrever(map[string]any{"serverContent": map[string]any{"generationComplete": true}})
		}
		_, _, _ = c.Read(ctx) // espera o cliente fechar
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Sessao{conn: conn, terminou: make(chan struct{})}
	_ = conn.Write(ctx, websocket.MessageText, []byte(`{"setup":{}}`))
	if _, _, err := conn.Read(ctx); err != nil { // setupComplete
		t.Fatal(err)
	}
	go s.ler()
	t.Cleanup(s.Fechar)
	time.Sleep(100 * time.Millisecond) // deixa as parciais de "antes" chegarem
	return s
}

// O bug de 2026-10-24: o fim do ditado chegava ~1 s depois do audioStreamEnd e
// o app, com 0,7 s de silêncio, colava só o começo.
func TestFinalizarEsperaOFimQueChegaDepois(t *testing.T) {
	s := servidorLive(t, []string{"Começo do ditado"}, []string{", meio", " e o fim."}, 900*time.Millisecond, false)
	got, err := s.Finalizar(context.Background(), 1200*time.Millisecond, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Começo do ditado, meio e o fim." {
		t.Fatalf("texto = %q", got)
	}
}

// Com generationComplete depois do fim do áudio, não espera o silêncio inteiro.
func TestFinalizarVoltaLogoQueOTurnoFecha(t *testing.T) {
	s := servidorLive(t, []string{"Frase curta"}, []string{"."}, 50*time.Millisecond, true)
	t0 := time.Now()
	got, err := s.Finalizar(context.Background(), 4*time.Second, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Frase curta." {
		t.Fatalf("texto = %q", got)
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("esperou %v mesmo com o turno fechado", d)
	}
}
