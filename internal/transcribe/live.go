// Package transcribe abre uma sessão da Gemini Live API com o token efêmero
// emitido pelo barroso-keys, manda o microfone em PCM 16 kHz enquanto o
// atalho está pressionado e junta a transcrição de entrada.
package transcribe

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// URLLive é o protocolo oficial da Gemini Live API para token efêmero:
// BidiGenerateContentConstrained com access_token= (BidiGenerateContent com
// key= recusa o token). v1beta é a versão documentada para tokens efêmeros —
// vale igual para o token do barroso-keys e o do AI Studio, porque nos dois
// casos quem emite é o mesmo /v1beta/auth_tokens do Google.
const URLLive = "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContentConstrained"

// MimePCM é o formato que o microfone entrega.
const MimePCM = "audio/pcm;rate=16000"

// Modalidade escolhe responseModalities pelo modelo REAL (x-served-model):
// o gemini-3.5-transcribe-live só aceita TEXT; os outros modelos Live, AUDIO.
func Modalidade(modeloReal string) string {
	if strings.Contains(modeloReal, "transcribe") {
		return "TEXT"
	}
	return "AUDIO"
}

// URLDaSessao monta a URL com o token em access_token (nunca em key).
func URLDaSessao(token string) string {
	return URLLive + "?access_token=" + url.QueryEscape(token)
}

// MensagemSetup é o primeiro quadro da sessão.
func MensagemSetup(modeloReal, instrucao string) map[string]any {
	setup := map[string]any{
		"model":                   "models/" + strings.TrimPrefix(modeloReal, "models/"),
		"generationConfig":        map[string]any{"responseModalities": []string{Modalidade(modeloReal)}},
		"inputAudioTranscription": map[string]any{},
	}
	if instrucao != "" {
		setup["systemInstruction"] = map[string]any{"parts": []map[string]string{{"text": instrucao}}}
	}
	return map[string]any{"setup": setup}
}

// quadro é o subconjunto das mensagens do servidor que interessa aqui.
type quadro struct {
	SetupComplete *struct{} `json:"setupComplete"`
	ServerContent *struct {
		InputTranscription *struct {
			Text string `json:"text"`
		} `json:"inputTranscription"`
		TurnComplete bool `json:"turnComplete"`
		// O gemini-3.5-transcribe-live não manda turnComplete: depois do
		// audioStreamEnd ele entrega a última parcial e, junto, generationComplete
		// (medido em 2026-10-24, ~0,7 s após o fim de 47 s de fala).
		GenerationComplete bool `json:"generationComplete"`
	} `json:"serverContent"`
	UsageMetadata *struct {
		PromptTokenCount   int `json:"promptTokenCount"`
		ResponseTokenCount int `json:"responseTokenCount"`
		ThoughtsTokenCount int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
	GoAway *struct{} `json:"goAway"`
}

// Sessao é uma conexão Live aberta.
type Sessao struct {
	conn *websocket.Conn

	mu       sync.Mutex
	texto    strings.Builder
	ultima   time.Time // hora da última parcial recebida
	turno    time.Time // hora do último fim de geração/turno do servidor
	entrada  int
	saida    int
	erroLeit error
	terminou chan struct{}
	// encerrando marca que o fim da conexão foi pedido pelo app (Finalizar ou
	// Fechar). Qualquer fim sem isso — inclusive um fechamento "normal" vindo
	// do servidor — é queda no meio da gravação.
	encerrando bool
	caiu       bool
	motivo     string
	onParcial  func(string)
}

// Abrir conecta, manda o setup e espera o setupComplete.
func Abrir(ctx context.Context, token, modeloReal, instrucao string, onParcial func(string)) (*Sessao, error) {
	conn, _, err := websocket.Dial(ctx, URLDaSessao(token), nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(4 << 20)
	s := &Sessao{conn: conn, terminou: make(chan struct{}), onParcial: onParcial}
	b, _ := json.Marshal(MensagemSetup(modeloReal, instrucao))
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		conn.CloseNow()
		return nil, err
	}
	// O primeiro quadro tem de ser o setupComplete; qualquer outra coisa é erro.
	_, raw, err := conn.Read(ctx)
	if err != nil {
		conn.CloseNow()
		return nil, err
	}
	var q quadro
	if json.Unmarshal(raw, &q) != nil || q.SetupComplete == nil {
		conn.CloseNow()
		return nil, errors.New("sessão Live sem setupComplete: " + resumo(raw))
	}
	go s.ler()
	return s, nil
}

func resumo(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func (s *Sessao) ler() {
	defer close(s.terminou)
	for {
		_, raw, err := s.conn.Read(context.Background())
		if err != nil {
			s.mu.Lock()
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				s.erroLeit = err
			}
			if !s.encerrando {
				s.caiu = true
				s.motivo = err.Error()
			}
			s.mu.Unlock()
			return
		}
		var q quadro
		if json.Unmarshal(raw, &q) != nil {
			continue
		}
		s.mu.Lock()
		if q.ServerContent != nil && q.ServerContent.InputTranscription != nil && q.ServerContent.InputTranscription.Text != "" {
			s.texto.WriteString(q.ServerContent.InputTranscription.Text)
			s.ultima = time.Now()
			parcial := s.texto.String()
			cb := s.onParcial
			s.mu.Unlock()
			if cb != nil {
				cb(parcial)
			}
			s.mu.Lock()
		}
		if q.ServerContent != nil && (q.ServerContent.TurnComplete || q.ServerContent.GenerationComplete) {
			s.turno = time.Now()
		}
		if u := q.UsageMetadata; u != nil {
			// Uso é POR TURNO: soma-se. Pensamento é cobrado como saída.
			s.entrada += u.PromptTokenCount
			s.saida += u.ResponseTokenCount + u.ThoughtsTokenCount
		}
		s.mu.Unlock()
	}
}

// EnviarAudio manda um bloco de PCM s16le 16 kHz mono.
func (s *Sessao) EnviarAudio(ctx context.Context, pcm []byte) error {
	msg := map[string]any{"realtimeInput": map[string]any{"audio": map[string]string{
		"data": base64.StdEncoding.EncodeToString(pcm), "mimeType": MimePCM,
	}}}
	b, _ := json.Marshal(msg)
	return s.conn.Write(ctx, websocket.MessageText, b)
}

// folgaTurno é quanto esperar, depois do sinal de fim que chega após o fim do
// áudio, por alguma parcial atrasada antes de devolver o texto.
const folgaTurno = 300 * time.Millisecond

// Finalizar avisa o fim do áudio e espera a transcrição assentar. O caminho
// normal é o sinal do servidor: generationComplete (ou turnComplete) depois
// do audioStreamEnd, mais uma folga curta. Sem esse sinal, devolve quando passa
// `quieto` sem parcial nova depois do fim, ou no `teto`.
//
// O silêncio conta a partir do mais recente entre o fim do áudio e a última
// parcial, e quem chama aumenta `quieto` com a duração da gravação: em fala
// longa o modelo ainda transcreve os últimos segundos depois do fim, e um
// `quieto` curto devolvia só o começo do ditado.
func (s *Sessao) Finalizar(ctx context.Context, quieto, teto time.Duration) (string, error) {
	b, _ := json.Marshal(map[string]any{"realtimeInput": map[string]any{"audioStreamEnd": true}})
	s.mu.Lock()
	s.encerrando = true
	s.mu.Unlock()
	fim := time.Now()
	if err := s.conn.Write(ctx, websocket.MessageText, b); err != nil {
		return s.Texto(), err
	}
	limite := time.NewTimer(teto)
	defer limite.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return s.Texto(), ctx.Err()
		case <-s.terminou:
			return s.Texto(), s.erro()
		case <-limite.C:
			return s.Texto(), nil
		case <-tick.C:
			s.mu.Lock()
			ultima, turno, temTexto := s.ultima, s.turno, s.texto.Len() > 0
			s.mu.Unlock()
			ref := fim
			if ultima.After(ref) {
				ref = ultima
			}
			if !temTexto {
				continue
			}
			if turno.After(fim) && time.Since(ref) >= folgaTurno && time.Since(turno) >= folgaTurno {
				return s.Texto(), nil
			}
			if time.Since(ref) >= quieto {
				return s.Texto(), nil
			}
		}
	}
}

func (s *Sessao) erro() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.erroLeit
}

// Texto é a transcrição acumulada até agora.
func (s *Sessao) Texto() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(s.texto.String())
}

// Uso devolve os tokens somados da sessão.
func (s *Sessao) Uso() (entrada, saida int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entrada, s.saida
}

// Fechar encerra a conexão.
func (s *Sessao) Fechar() {
	s.mu.Lock()
	s.encerrando = true
	s.mu.Unlock()
	_ = s.conn.Close(websocket.StatusNormalClosure, "")
}

// Caiu informa se a conexão terminou sem o app pedir — rede, cota, limite de
// sessão ou fechamento do servidor — e o motivo, para o log.
func (s *Sessao) Caiu() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.caiu, s.motivo
}
