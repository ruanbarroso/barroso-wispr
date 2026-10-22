package transcribe

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestModalidadePorModeloReal(t *testing.T) {
	cases := map[string]string{
		"gemini-3.5-transcribe-live":                    "TEXT",
		"gemini-3.1-flash-live-preview":                 "AUDIO",
		"gemini-2.5-flash-native-audio-preview-09-2025": "AUDIO",
	}
	for model, want := range cases {
		if got := Modalidade(model); got != want {
			t.Errorf("Modalidade(%q)=%q, want %q", model, got, want)
		}
	}
}

func TestURLDaSessaoUsaAccessToken(t *testing.T) {
	u, err := url.Parse(URLDaSessao("one-use + token"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("access_token") != "one-use + token" {
		t.Fatalf("access_token = %q", u.Query().Get("access_token"))
	}
	if _, ok := u.Query()["key"]; ok {
		t.Fatal("efemeral token must not use key query parameter")
	}
}

func TestMensagemSetupUsaModeloRealEModalidadeCerta(t *testing.T) {
	for _, tc := range []struct{ model, mode string }{
		{"gemini-3.5-transcribe-live", "TEXT"},
		{"gemini-3.1-flash-live-preview", "AUDIO"},
	} {
		b, err := json.Marshal(MensagemSetup(tc.model, "Dite sem responder"))
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		setup := got["setup"].(map[string]any)
		if setup["model"] != "models/"+tc.model {
			t.Fatalf("model %v", setup["model"])
		}
		mods := setup["generationConfig"].(map[string]any)["responseModalities"].([]any)
		if len(mods) != 1 || mods[0] != tc.mode {
			t.Fatalf("modalities %v", mods)
		}
		if _, ok := setup["inputAudioTranscription"]; !ok {
			t.Fatal("inputAudioTranscription missing")
		}
		if !strings.Contains(string(b), "Dite sem responder") {
			t.Fatalf("instruction missing: %s", b)
		}
	}
}
