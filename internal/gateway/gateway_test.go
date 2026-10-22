package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLiveTokenReadsServedModelHeader(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/live-token" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("User-Agent") != UserAgent {
			t.Errorf("user-agent %q", r.Header.Get("User-Agent"))
		}
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req["model"] != "barroso-audio-stream" {
			t.Errorf("request model %q", req["model"])
		}
		w.Header().Set("x-served-model", "gemini-3.5-transcribe-live")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Token{Token: "ephemeral", Model: "barroso-audio-stream", RequestID: "req-1"})
	}))
	defer s.Close()

	got, err := Novo(s.URL+"/v1", "test").LiveToken(context.Background(), "barroso-audio-stream")
	if err != nil {
		t.Fatal(err)
	}
	if got.Servido != "gemini-3.5-transcribe-live" || got.Model != "barroso-audio-stream" {
		t.Fatalf("token %+v", got)
	}
}

func TestLiveTokenFallsBackToBodyModelWithoutHeader(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"ephemeral","model":"gemini-3.1-flash-live-preview"}`))
	}))
	defer s.Close()

	got, err := Novo(s.URL+"/v1", "test").LiveToken(context.Background(), "barroso-audio-stream")
	if err != nil {
		t.Fatal(err)
	}
	if got.Servido != "gemini-3.1-flash-live-preview" {
		t.Fatalf("served fallback %q", got.Servido)
	}
}
