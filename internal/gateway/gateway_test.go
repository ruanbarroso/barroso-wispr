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

func TestLiveTokenGoogleUsaAuthTokensOficial(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "AIza-test" || r.Header.Get("Authorization") != "" {
			t.Errorf("auth headers: key=%q authorization=%q", r.Header.Get("x-goog-api-key"), r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["uses"] != float64(1) || body["expireTime"] == nil || body["newSessionExpireTime"] == nil {
			t.Errorf("body %v", body)
		}
		_, _ = w.Write([]byte(`{"name":"auth_tokens/abc","expireTime":"2026-10-22T21:00:00Z"}`))
	}))
	defer s.Close()

	c := NovoGoogle("AIza-test", "gemini-3.5-transcribe-live")
	c.AuthURL = s.URL
	got, err := c.LiveToken(context.Background(), "ignorado")
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "auth_tokens/abc" || got.Servido != "gemini-3.5-transcribe-live" || got.RequestID != "" {
		t.Fatalf("token %+v", got)
	}
	// Sem gateway, uso/falha não vão a lugar nenhum.
	if err := c.LiveUsage(context.Background(), "x", Uso{}); err != nil {
		t.Fatal(err)
	}
}

func TestChatGoogleUsaCamadaOpenAICompativel(t *testing.T) {
	c := NovoGoogle("k", "m")
	if c.BaseURL != "https://generativelanguage.googleapis.com/v1beta/openai" {
		t.Fatalf("base %s", c.BaseURL)
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
