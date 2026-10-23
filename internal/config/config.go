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
	// Provider: "barroso" (barroso-keys), "google" (AI Studio direto) ou
	// vazio = automático (barroso se houver credencial dele, senão google).
	Provider     string `json:"provider,omitempty"`
	GoogleAPIKey string `json:"google_api_key,omitempty"`
	Modelo       string `json:"model"`       // voz: virtual no barroso, real no google
	QuickModel   string `json:"quick_model"` // passe de limpeza
	Dicionario   string `json:"dictionary"`  // palavras-chave, uma por linha
	ModoTrava    bool   `json:"latch_mode"`  // true depois de dois toques rápidos
}

// Defaults
const (
	ModeloVoz   = "barroso-audio-stream"
	ModeloQuick = "barroso-quick"

	// Sem gateway não há chain: modelo real. O transcribe-live foi o melhor
	// da sonda de 2026-10-22 (texto final ~460 ms após o fim da fala).
	ModeloVozGoogle   = "gemini-3.5-transcribe-live"
	ModeloQuickGoogle = "gemini-3.5-flash-lite"

	ProviderBarroso = "barroso"
	ProviderGoogle  = "google"

	EnvGeminiKey = "GEMINI_API_KEY"
	EnvGoogleKey = "GOOGLE_API_KEY"
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
// Modelos vazios ficam vazios aqui: o default depende do provider (Resolver).
func Carregar() Preferencias {
	p := Preferencias{Dicionario: filepath.Join(Home(), "dicionario.txt")}
	b, err := os.ReadFile(filepath.Join(Home(), "config.json"))
	if err != nil {
		return p
	}
	var disk Preferencias
	if json.Unmarshal(b, &disk) == nil {
		p.Provider = strings.ToLower(strings.TrimSpace(disk.Provider))
		p.GoogleAPIKey = strings.TrimSpace(disk.GoogleAPIKey)
		p.Modelo, p.QuickModel = disk.Modelo, disk.QuickModel
		if disk.Dicionario != "" {
			p.Dicionario = disk.Dicionario
		}
		p.ModoTrava = disk.ModoTrava
	}
	return p
}

// Destino é o provider escolhido, com credencial e modelos resolvidos.
type Destino struct {
	Provider   string // ProviderBarroso | ProviderGoogle
	BaseURL    string // só barroso
	APIKey     string
	Modelo     string
	QuickModel string
}

// chaveGoogle: config do wispr, depois GEMINI_API_KEY, depois GOOGLE_API_KEY.
func chaveGoogle(p Preferencias) string {
	for _, k := range []string{p.GoogleAPIKey, os.Getenv(EnvGeminiKey), os.Getenv(EnvGoogleKey)} {
		if k = strings.TrimSpace(k); k != "" {
			return k
		}
	}
	return ""
}

// Resolver escolhe o provider. Automático: barroso-keys se houver credencial
// dele (ambiente BARROSO_* ou config do Barroso Harness); senão Google AI
// Studio se houver chave; senão erro explicando as duas opções.
func Resolver(p Preferencias) (Destino, error) {
	google := func() (Destino, error) {
		k := chaveGoogle(p)
		if k == "" {
			return Destino{}, fmt.Errorf("provider google sem chave: defina %s ou google_api_key em %s",
				EnvGeminiKey, filepath.Join(Home(), "config.json"))
		}
		d := Destino{Provider: ProviderGoogle, APIKey: k, Modelo: p.Modelo, QuickModel: p.QuickModel}
		if d.Modelo == "" {
			d.Modelo = ModeloVozGoogle
		}
		if d.QuickModel == "" {
			d.QuickModel = ModeloQuickGoogle
		}
		return d, nil
	}
	barroso := func() (Destino, error) {
		c, err := Cred()
		if err != nil {
			return Destino{}, err
		}
		d := Destino{Provider: ProviderBarroso, BaseURL: c.BaseURL, APIKey: c.APIKey, Modelo: p.Modelo, QuickModel: p.QuickModel}
		if d.Modelo == "" {
			d.Modelo = ModeloVoz
		}
		if d.QuickModel == "" {
			d.QuickModel = ModeloQuick
		}
		return d, nil
	}
	switch p.Provider {
	case ProviderGoogle:
		return google()
	case ProviderBarroso:
		return barroso()
	case "":
		d, errB := barroso()
		if errB == nil && d.APIKey != "" {
			return d, nil
		}
		if chaveGoogle(p) != "" {
			return google()
		}
		return Destino{}, fmt.Errorf("nenhuma credencial: use o barroso-keys (BARROSO_AUTH_TOKEN ou `barroso-harness -init`) ou o Google AI Studio (%s)", EnvGeminiKey)
	default:
		return Destino{}, fmt.Errorf("provider %q desconhecido (use %q ou %q)", p.Provider, ProviderBarroso, ProviderGoogle)
	}
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
