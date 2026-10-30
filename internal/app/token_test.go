package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ruanbarroso/barroso-wispr/internal/gateway"
)

// A primeira chamada do live-token trava (como os 31 s de 2026-10-29): a
// segunda, disparada depois de esperaHedgeToken, responde e vale.
func TestBuscarTokenDisparaOutraTentativaQuandoAPrimeiraDemora(t *testing.T) {
	old := esperaHedgeToken
	esperaHedgeToken = 50 * time.Millisecond
	t.Cleanup(func() { esperaHedgeToken = old })

	var chamadas atomic.Int32
	libera := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if chamadas.Add(1) == 1 {
			<-libera
			return
		}
		w.Header().Set("x-served-model", "gemini-3.5-transcribe-live")
		_ = json.NewEncoder(w).Encode(gateway.Token{Token: "ephemeral", Model: "barroso-audio-stream"})
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(libera) })
	a := Novo(Deps{Gateway: gateway.Novo(server.URL+"/v1", "test"), ModeloVoz: "barroso-audio-stream"})

	t0 := time.Now()
	tok, err := a.buscarToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.Token != "ephemeral" {
		t.Fatalf("token = %q", tok.Token)
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("levou %v: não usou a segunda tentativa", d)
	}
	if chamadas.Load() < 2 {
		t.Fatalf("chamadas = %d, want >= 2", chamadas.Load())
	}
}
