// Package gateway fala com o barroso-keys: emite o token efêmero da Live API
// (/v1/audio/live-token) e faz o passe de limpeza no Chat Completions.
package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// UserAgent nomeado: WAF de provider bloqueia Go-http-client/1.1 antes de
// olhar a chave (lição do barroso-harness).
const UserAgent = "barroso-wispr"

type Cliente struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client

	// Google = falar direto com o Google AI Studio (chave do AI Studio), sem
	// o barroso-keys: o token efêmero sai do /v1beta/auth_tokens oficial e o
	// passe de limpeza vai pela camada OpenAI-compatível do Gemini.
	Google bool
	// ModeloLive é o modelo REAL da sessão no modo Google (não há chain nem
	// x-served-model fora do gateway).
	ModeloLive string
	// AuthURL do auth_tokens (trocável no teste).
	AuthURL string
}

// Endpoints oficiais do Google AI Studio.
const (
	GoogleBaseURL    = "https://generativelanguage.googleapis.com/v1beta/openai"
	GoogleAuthTokens = "https://generativelanguage.googleapis.com/v1beta/auth_tokens"
)

func Novo(baseURL, apiKey string) *Cliente {
	return &Cliente{BaseURL: strings.TrimSuffix(baseURL, "/"), APIKey: apiKey, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// NovoGoogle fala direto com o Google AI Studio usando a chave do AI Studio.
func NovoGoogle(apiKey, modeloLive string) *Cliente {
	c := Novo(GoogleBaseURL, apiKey)
	c.Google, c.ModeloLive, c.AuthURL = true, modeloLive, GoogleAuthTokens
	return c
}

// Token é o que o live-token devolve. Servido vem do header x-served-model:
// o modelo REAL que atendeu, necessário para montar o setup da sessão.
type Token struct {
	Token     string `json:"token"`
	ExpiraEm  string `json:"expira_em"`
	Model     string `json:"model"`
	RequestID string `json:"request_id"`
	Servido   string `json:"-"`
}

func (c *Cliente) post(ctx context.Context, path string, body any) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	return c.HTTP.Do(req)
}

// resumoErro tira uma linha legível de um corpo de erro.
func resumoErro(status int, b []byte) error {
	var j struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := strings.TrimSpace(string(b))
	if json.Unmarshal(b, &j) == nil && j.Error.Message != "" {
		msg = j.Error.Message
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return fmt.Errorf("HTTP %d: %s", status, msg)
}

// liveTokenGoogle emite o token pelo endpoint oficial: POST
// /v1beta/auth_tokens com x-goog-api-key; o token é o campo `name`.
func (c *Cliente) liveTokenGoogle(ctx context.Context) (Token, error) {
	agora := time.Now().UTC()
	body, _ := json.Marshal(map[string]any{
		"uses":                 1,
		"expireTime":           agora.Add(30 * time.Minute).Format(time.RFC3339),
		"newSessionExpireTime": agora.Add(time.Minute).Format(time.RFC3339),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.AuthURL, bytes.NewReader(body))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("x-goog-api-key", c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Token{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return Token{}, resumoErro(resp.StatusCode, b)
	}
	var j struct {
		Name       string `json:"name"`
		ExpireTime string `json:"expireTime"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		return Token{}, err
	}
	if j.Name == "" {
		return Token{}, errors.New("auth_tokens sem name")
	}
	return Token{Token: j.Name, ExpiraEm: j.ExpireTime, Model: c.ModeloLive, Servido: c.ModeloLive}, nil
}

// LiveToken pede o token efêmero para o modelo (virtual ou real).
func (c *Cliente) LiveToken(ctx context.Context, model string) (Token, error) {
	if c.Google {
		return c.liveTokenGoogle(ctx)
	}
	resp, err := c.post(ctx, "/audio/live-token", map[string]any{"model": model})
	if err != nil {
		return Token{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return Token{}, resumoErro(resp.StatusCode, b)
	}
	var t Token
	if err := json.Unmarshal(b, &t); err != nil {
		return Token{}, err
	}
	if t.Token == "" {
		return Token{}, errors.New("live-token sem token")
	}
	t.Servido = resp.Header.Get("x-served-model")
	if t.Servido == "" {
		t.Servido = t.Model
	}
	return t, nil
}

// Uso é o consumo somado da sessão, devolvido ao gateway para fechar o log.
type Uso struct {
	Entrada int
	Saida   int
}

// LiveUsage reporta o uso da sessão (melhor esforço).
func (c *Cliente) LiveUsage(ctx context.Context, requestID string, u Uso) error {
	// Fora do gateway não há log para fechar.
	if c.Google || requestID == "" {
		return nil
	}
	// Campos no nível de cima, como o liveUsage do gateway lê.
	resp, err := c.post(ctx, "/audio/live-usage", map[string]any{
		"request_id":        requestID,
		"prompt_tokens":     u.Entrada,
		"completion_tokens": u.Saida,
		"total_tokens":      u.Entrada + u.Saida,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return resumoErro(resp.StatusCode, b)
	}
	return nil
}

// LiveFailure avisa que o Google recusou a sessão depois do token emitido
// (ex.: teto de gasto do projeto), para a conta sair do rodízio. Melhor esforço.
func (c *Cliente) LiveFailure(ctx context.Context, requestID string) error {
	if c.Google || requestID == "" {
		return nil
	}
	resp, err := c.post(ctx, "/audio/live-failure", map[string]any{"request_id": requestID})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return resumoErro(resp.StatusCode, b)
	}
	return nil
}

// promptTranscricao é o pedido da transcrição de reserva no modo Google.
const promptTranscricao = "Transcreva exatamente o que é falado neste áudio, no idioma falado. Responda só com a transcrição, sem comentários. Se não houver fala, responda vazio."

// Transcrever transcreve um WAV inteiro de uma vez (reserva para quando a
// Live não devolve nada). No barroso-keys vai pelo /audio/transcriptions
// (contrato OpenAI); no Google, pelo Chat Completions com input_audio.
func (c *Cliente) Transcrever(ctx context.Context, model string, wav []byte) (string, error) {
	if c.Google {
		return c.transcreverChat(ctx, model, wav)
	}
	var corpo bytes.Buffer
	mw := multipart.NewWriter(&corpo)
	_ = mw.WriteField("model", model)
	_ = mw.WriteField("response_format", "json")
	fw, err := mw.CreateFormFile("file", "ditado.wav")
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(wav); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/audio/transcriptions", &corpo)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return "", resumoErro(resp.StatusCode, b)
	}
	var j struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		return "", err
	}
	return strings.TrimSpace(j.Text), nil
}

func (c *Cliente) transcreverChat(ctx context.Context, model string, wav []byte) (string, error) {
	resp, err := c.post(ctx, "/chat/completions", map[string]any{
		"model": model,
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": promptTranscricao},
				{"type": "input_audio", "input_audio": map[string]string{
					"data": base64.StdEncoding.EncodeToString(wav), "format": "wav",
				}},
			},
		}},
		"temperature": 0,
		"stream":      false,
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return "", resumoErro(resp.StatusCode, b)
	}
	var j struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		return "", err
	}
	if len(j.Choices) == 0 {
		return "", errors.New("resposta sem choices")
	}
	return strings.TrimSpace(j.Choices[0].Message.Content), nil
}

// WAV embrulha PCM s16le mono num cabeçalho WAV.
func WAV(pcm []byte, taxa int) []byte {
	var b bytes.Buffer
	b.Grow(44 + len(pcm))
	le := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	le(uint32(36 + len(pcm)))
	b.WriteString("WAVEfmt ")
	le(uint32(16))
	le(uint16(1)) // PCM
	le(uint16(1)) // mono
	le(uint32(taxa))
	le(uint32(taxa * 2))
	le(uint16(2))
	le(uint16(16))
	b.WriteString("data")
	le(uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}

// Chat faz uma chamada Chat Completions sem stream e devolve o texto.
func (c *Cliente) Chat(ctx context.Context, model, system, user string) (string, error) {
	resp, err := c.post(ctx, "/chat/completions", map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": 0,
		"stream":      false,
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return "", resumoErro(resp.StatusCode, b)
	}
	var j struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		return "", err
	}
	if len(j.Choices) == 0 {
		return "", errors.New("resposta sem choices")
	}
	return j.Choices[0].Message.Content, nil
}
