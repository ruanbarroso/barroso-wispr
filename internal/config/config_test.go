package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResolverGoogleComChaveSemPrefixoBK(t *testing.T) {
	t.Setenv(EnvAPIKey, "AIza-test")
	d, err := Resolver(Preferencias{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Provider != ProviderGoogle || d.APIKey != "AIza-test" || d.BaseURL != "" {
		t.Fatalf("got %+v", d)
	}
	if d.Modelo != ModeloVozGoogle || d.Modelo == "" {
		t.Fatalf("voz não fixada no melhor modelo live free-tier: %q", d.Modelo)
	}
	if d.QuickModel != ModeloQuickGoogle {
		t.Fatalf("quick model %q", d.QuickModel)
	}
}

func TestResolverBarrosoPeloPrefixoDash(t *testing.T) {
	t.Setenv(EnvAPIKey, "bk-livekey")
	d, err := Resolver(Preferencias{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Provider != ProviderBarroso || d.APIKey != "bk-livekey" || d.BaseURL != DefaultBaseURL {
		t.Fatalf("got %+v", d)
	}
	if d.Modelo != ModeloVozBarroso || d.QuickModel != ModeloQuickBarroso {
		t.Fatalf("models %+v", d)
	}
}

func TestResolverBKUnderscoreIsNotBarroso(t *testing.T) {
	t.Setenv(EnvAPIKey, "bk_legacy")
	d, err := Resolver(Preferencias{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Provider != ProviderGoogle || d.BaseURL != "" {
		t.Fatalf("`bk_` must not select Barroso Keys: %+v", d)
	}
}

func TestResolverBarrosoPeloPrefixoDashAtual(t *testing.T) {
	t.Setenv(EnvAPIKey, "bk-livekey")
	d, err := Resolver(Preferencias{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Provider != ProviderBarroso || d.APIKey != "bk-livekey" || d.BaseURL != DefaultBaseURL {
		t.Fatalf("chave Barroso Keys não roteada pelo prefixo emitido: %+v", d)
	}
}

func TestResolverExigeUmaChave(t *testing.T) {
	t.Setenv(EnvAPIKey, "")
	if _, err := Resolver(Preferencias{}); err == nil {
		t.Fatal("esperava erro quando WISPR_API_KEY está ausente")
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
