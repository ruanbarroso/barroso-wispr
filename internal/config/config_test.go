package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCredLêProviderAtivoDoHarness(t *testing.T) {
	t.Setenv("BARROSO_HARNESS_HOME", t.TempDir())
	t.Setenv(EnvBaseURL, "")
	t.Setenv(EnvAuthToken, "")
	p := filepath.Join(os.Getenv("BARROSO_HARNESS_HOME"), "config.json")
	data := `{"current_provider":"barroso","providers":{"barroso":{"base_url":"https://keys.example/v1","api_key":"bk-test"},"other":{"base_url":"https://other/v1","api_key":"wrong"}}}`
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Cred()
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseURL != "https://keys.example/v1" || got.APIKey != "bk-test" {
		t.Fatalf("got %+v", got)
	}
}

func TestCredPrefereOGatewayQuandoOAtivoEOutro(t *testing.T) {
	t.Setenv("BARROSO_HARNESS_HOME", t.TempDir())
	t.Setenv(EnvBaseURL, "")
	t.Setenv(EnvAuthToken, "")
	p := filepath.Join(os.Getenv("BARROSO_HARNESS_HOME"), "config.json")
	data := `{"current_provider":"zen","providers":{"zen":{"base_url":"https://opencode.ai/inference/openai/v1","api_key":"public"},"bk":{"base_url":"https://keys.barroso.tec.br/v1","api_key":"bk-real"}}}`
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Cred()
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "bk-real" || got.BaseURL != "https://keys.barroso.tec.br/v1" {
		t.Fatalf("got %+v", got)
	}
}

func TestCredEnvSobrepoeConfig(t *testing.T) {
	t.Setenv("BARROSO_HARNESS_HOME", t.TempDir())
	t.Setenv(EnvBaseURL, "https://override.example/v1")
	t.Setenv(EnvAuthToken, "env-token")
	got, err := Cred()
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseURL != "https://override.example/v1" || got.APIKey != "env-token" || !got.DeEnv {
		t.Fatalf("got %+v", got)
	}
}

func TestDicionarioIgnoraBrancosEComentarios(t *testing.T) {
	p := filepath.Join(t.TempDir(), "dicionario.txt")
	if err := os.WriteFile(p, []byte("# empresa\r\nBarroso\r\n\nMariana\n KIS \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Dicionario(p)
	want := []string{"Barroso", "Mariana", "KIS"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
