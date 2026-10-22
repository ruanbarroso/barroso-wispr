// Package config lê as credenciais do mesmo lugar do Barroso Harness
// (~/.barroso-harness/config.json), com override por ambiente
// (BARROSO_BASE_URL / BARROSO_AUTH_TOKEN), e guarda as preferências do
// wispr em ~/.barroso-wispr/config.json.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	EnvBaseURL   = "BARROSO_BASE_URL"
	EnvAuthToken = "BARROSO_AUTH_TOKEN"

	// DefaultBaseURL é o gateway da casa (padrão OpenAI, com /v1 no fim).
	DefaultBaseURL = "https://keys.barroso.tec.br/v1"
)

// Provider é um endpoint Chat Completions: base URL + chave.
type Provider struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key,omitempty"`
}

// Credenciais é o par ativo resolvido na inicialização.
type Credenciais struct {
	BaseURL string
	APIKey  string
	DeEnv   bool // veio de BARROSO_BASE_URL/AUTH_TOKEN, não do config do bh
}

// Preferencias é o config do wispr (~/.barroso-wispr/config.json).
type Preferencias struct {
	Modelo     string `json:"model"`       // virtual de voz (live-token)
	QuickModel string `json:"quick_model"` // passe de limpeza
	Dicionario string `json:"dictionary"`  // palavras-chave, uma por linha
	ModoTrava  bool   `json:"latch_mode"`  // true depois de dois toques rápidos
}

// Defaults
const (
	ModeloVoz   = "barroso-audio-stream"
	ModeloQuick = "barroso-quick"
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

// carregarProviderLê lê o config.json do bh e devolve o provider ativo.
func carregarProvider() (*Provider, error) {
	path := filepath.Join(DirHarness(), "config.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		CurrentProvider string               `json:"current_provider"`
		Providers       map[string]*Provider `json:"providers"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("config inválido %s: %w", path, err)
	}
	// O live-token só existe no barroso-keys: o provider ATIVO do bh pode ser
	// outro (o usuário troca com /provider), então vale o do gateway da casa.
	if p := raw.Providers[raw.CurrentProvider]; p != nil && ehGateway(p.BaseURL) {
		return p, nil
	}
	for _, p := range raw.Providers {
		if p != nil && ehGateway(p.BaseURL) && p.APIKey != "" {
			return p, nil
		}
	}
	if raw.CurrentProvider == "" {
		return nil, errors.New("config do bh sem current_provider")
	}
	p := raw.Providers[raw.CurrentProvider]
	if p == nil {
		return nil, fmt.Errorf("provider %q não existe no config do bh", raw.CurrentProvider)
	}
	return p, nil
}

// ehGateway diz se a base URL é do barroso-keys.
func ehGateway(base string) bool {
	return strings.Contains(strings.ToLower(base), "keys.barroso.tec.br")
}

// Cred resolve as credenciais: ambiente primeiro, depois o config do bh.
func Cred() (Credenciais, error) {
	base := strings.TrimSpace(os.Getenv(EnvBaseURL))
	token := strings.TrimSpace(os.Getenv(EnvAuthToken))
	if base != "" || token != "" {
		c := Credenciais{BaseURL: base, APIKey: token, DeEnv: true}
		if c.BaseURL == "" {
			c.BaseURL = DefaultBaseURL
		}
		if p, err := carregarProvider(); err == nil && c.APIKey == "" {
			c.APIKey = p.APIKey
		}
		return c, nil
	}
	p, err := carregarProvider()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Credenciais{}, fmt.Errorf("sem %s no ambiente e sem %s: rode `barroso-harness -init` ou exporte BARROSO_AUTH_TOKEN",
				EnvAuthToken, filepath.Join(DirHarness(), "config.json"))
		}
		return Credenciais{}, err
	}
	c := Credenciais{BaseURL: p.BaseURL, APIKey: p.APIKey}
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	return c, nil
}

// Carregar lê o config do wispr; arquivo ausente devolve os defaults.
func Carregar() Preferencias {
	p := Preferencias{Modelo: ModeloVoz, QuickModel: ModeloQuick, Dicionario: filepath.Join(Home(), "dicionario.txt")}
	b, err := os.ReadFile(filepath.Join(Home(), "config.json"))
	if err != nil {
		return p
	}
	var disk Preferencias
	if json.Unmarshal(b, &disk) == nil {
		if disk.Modelo != "" {
			p.Modelo = disk.Modelo
		}
		if disk.QuickModel != "" {
			p.QuickModel = disk.QuickModel
		}
		if disk.Dicionario != "" {
			p.Dicionario = disk.Dicionario
		}
		p.ModoTrava = disk.ModoTrava
	}
	return p
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
