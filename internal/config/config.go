// Package config lê uma única chave WISPR_API_KEY e decide o backend pelo
// prefixo `bk-`: Barroso Keys; demais chaves: Google AI Studio. Guarda também
// preferências sem segredos em ~/.barroso-wispr/config.json.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// Única credencial. Apenas `bk-` seleciona Barroso Keys; qualquer outra
	// chave é tratada como Google AI Studio.
	EnvAPIKey = "WISPR_API_KEY"

	// DefaultBaseURL é fixo; o usuário não configura URL.
	DefaultBaseURL   = "https://keys.barroso.tec.br/v1"
	BarrosoKeyPrefix = "bk-"
)

// Provider é um endpoint Chat Completions: base URL + chave.
type Provider struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key,omitempty"`
}

// Credenciais é a única chave informada pelo usuário. O backend é derivado
// do prefixo em Resolver, sem outra URL ou opção de provider.
type Credenciais struct {
	APIKey string
}

// Preferencias é o config do wispr (~/.barroso-wispr/config.json).
type Preferencias struct {
	Dicionario string `json:"dictionary"` // palavras-chave, uma por linha
	ModoTrava  bool   `json:"latch_mode"` // true depois de dois toques rápidos
}

// Modelos fixos por backend. O free tier e a qualidade do Live Google foram
// conferidos no pricing e na sonda de 2026-10-22.
const (
	ModeloVozBarroso   = "barroso-audio-stream"
	ModeloQuickBarroso = "barroso-quick"
	ModeloVozGoogle    = "gemini-3.5-transcribe-live"
	ModeloQuickGoogle  = "gemini-3.5-flash-lite"
	// Transcrição de reserva (áudio inteiro) quando a Live não devolve nada.
	ModeloSTTBarroso = "barroso-stt"
	ModeloSTTGoogle  = "gemini-3.5-flash-lite"

	ProviderBarroso = "barroso"
	ProviderGoogle  = "google"
)

// Home é o diretório de dados do wispr.
func Home() string {
	if h := os.Getenv("BARROSO_WISPR_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".barroso-wispr")
}

// DirHarness é o diretório de dados do Barroso Harness.
func DirHarness() string {
	if h := os.Getenv("BARROSO_HARNESS_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".barroso-harness")
}

// Chave lê a única variável de credencial do app. A chave não é salva em
// config.json; pode ser passada em runtime pelo ambiente do processo.
func Chave() (Credenciais, error) {
	key := strings.TrimSpace(os.Getenv(EnvAPIKey))
	if key == "" {
		return Credenciais{}, fmt.Errorf("defina %s com uma chave do Google AI Studio ou do Barroso Keys", EnvAPIKey)
	}
	return Credenciais{APIKey: key}, nil
}

// Carregar lê preferências sem segredos; as opções de modelo/provider não são
// configuráveis para manter a seleção determinística por formato de chave.
func Carregar() Preferencias {
	p := Preferencias{Dicionario: filepath.Join(Home(), "dicionario.txt")}
	b, err := os.ReadFile(filepath.Join(Home(), "config.json"))
	if err != nil {
		return p
	}
	var disk Preferencias
	if json.Unmarshal(b, &disk) == nil && disk.Dicionario != "" {
		p.Dicionario = disk.Dicionario
	}
	return p
}

// Destino é o backend e os modelos fixos selecionados pela chave.
type Destino struct {
	Provider   string
	BaseURL    string
	APIKey     string
	Modelo     string
	QuickModel string
	STTModel   string
}

// Resolver usa só o prefixo da chave: `bk-` é Barroso Keys; qualquer outro
// formato é uma chave Google AI Studio. Não há provider, URL ou modelo manual.
func Resolver(_ Preferencias) (Destino, error) {
	c, err := Chave()
	if err != nil {
		return Destino{}, err
	}
	if strings.HasPrefix(c.APIKey, BarrosoKeyPrefix) {
		return Destino{
			Provider: ProviderBarroso, BaseURL: DefaultBaseURL, APIKey: c.APIKey,
			Modelo: ModeloVozBarroso, QuickModel: ModeloQuickBarroso, STTModel: ModeloSTTBarroso,
		}, nil
	}
	return Destino{
		Provider: ProviderGoogle, APIKey: c.APIKey,
		Modelo: ModeloVozGoogle, QuickModel: ModeloQuickGoogle, STTModel: ModeloSTTGoogle,
	}, nil
}

// Salvar grava o config do wispr (best effort).
func Salvar(p Preferencias) {
	_ = os.MkdirAll(Home(), 0o700)
	b, _ := json.MarshalIndent(p, "", "  ")
	_ = os.WriteFile(filepath.Join(Home(), "config.json"), b, 0o600)
}

// Dicionario lê as palavras-chave, uma por linha, ignorando comentários (#).
func Dicionario(caminho string) []string {
	b, err := os.ReadFile(caminho)
	if err != nil {
		return nil
	}
	var out []string
	for _, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(strings.TrimSuffix(ln, "\r"))
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		out = append(out, ln)
	}
	return out
}
