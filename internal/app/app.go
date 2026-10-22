// Package app liga as peças do ditado: atalho → token Live → microfone em
// stream → texto cru → passe no barroso-quick com o dicionário → digitar.
package app

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ruanbarroso/barroso-wispr/internal/atalho"
	"github.com/ruanbarroso/barroso-wispr/internal/gateway"
	"github.com/ruanbarroso/barroso-wispr/internal/limpeza"
	"github.com/ruanbarroso/barroso-wispr/internal/transcribe"
)

// Microfone entrega PCM s16le 16 kHz mono em blocos (~100 ms) até Parar.
type Microfone interface {
	Iniciar(bloco func([]byte)) error
	Parar()
}

// Sessao é o que o app precisa de uma sessão Live (transcribe.Sessao).
type Sessao interface {
	EnviarAudio(ctx context.Context, pcm []byte) error
	Finalizar(ctx context.Context, quieto, teto time.Duration) (string, error)
	Uso() (entrada, saida int)
	Fechar()
}

// Deps são as dependências injetáveis (o teste troca todas).
type Deps struct {
	Gateway    *gateway.Cliente
	Chat       limpeza.Chat
	Abrir      func(ctx context.Context, token, modeloReal, instrucao string) (Sessao, error)
	Microfone  Microfone
	Digitar    func(string) error
	Dicionario func() []string
	ModeloVoz  string
	ModeloQuik string
	Log        io.Writer
}

// App mantém a máquina do atalho e a gravação em curso.
type App struct {
	d   Deps
	mu  sync.Mutex
	maq atalho.Maquina
	rec *gravacao
}

type gravacao struct {
	ctx      context.Context
	cancel   context.CancelFunc
	inicio   time.Time
	pronta   chan struct{} // fecha quando a sessão abriu (ou falhou)
	sessao   Sessao
	token    gateway.Token
	err      error
	mu       sync.Mutex
	pendente [][]byte // áudio capturado antes de a sessão abrir
}

func Novo(d Deps) *App { return &App{d: d} }

func (a *App) logf(format string, args ...any) {
	if a.d.Log != nil {
		fmt.Fprintf(a.d.Log, time.Now().Format("15:04:05.000")+" "+format+"\n", args...)
	}
}

// Pressionou / Soltou / OutraTecla / Tick recebem o teclado.
func (a *App) Pressionou() {
	a.tratar(a.comMaq(func(m *atalho.Maquina) atalho.Evento { return m.Pressionou(time.Now()) }))
}
func (a *App) Soltou() {
	a.tratar(a.comMaq(func(m *atalho.Maquina) atalho.Evento { return m.Soltou(time.Now()) }))
}
func (a *App) OutraTecla() {
	a.tratar(a.comMaq(func(m *atalho.Maquina) atalho.Evento { return m.OutraTecla() }))
}
func (a *App) Tick() {
	a.tratar(a.comMaq(func(m *atalho.Maquina) atalho.Evento { return m.Tick(time.Now()) }))
}

func (a *App) comMaq(f func(*atalho.Maquina) atalho.Evento) atalho.Evento {
	a.mu.Lock()
	defer a.mu.Unlock()
	return f(&a.maq)
}

func (a *App) tratar(e atalho.Evento) {
	switch e {
	case atalho.Comecar:
		a.comecar()
	case atalho.Parar:
		go a.parar(true)
	case atalho.Cancelar:
		go a.parar(false)
	}
}

// comecar liga o microfone NA HORA e abre a sessão em paralelo: o áudio do
// primeiro meio segundo fica em buffer e é enviado quando o setup completa,
// para não perder a primeira palavra.
func (a *App) comecar() {
	a.mu.Lock()
	if a.rec != nil {
		a.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	g := &gravacao{ctx: ctx, cancel: cancel, inicio: time.Now(), pronta: make(chan struct{})}
	a.rec = g
	a.mu.Unlock()
	a.logf("gravando")

	err := a.d.Microfone.Iniciar(func(pcm []byte) {
		b := append([]byte(nil), pcm...)
		g.mu.Lock()
		if g.sessao == nil {
			g.pendente = append(g.pendente, b)
			g.mu.Unlock()
			return
		}
		s := g.sessao
		g.mu.Unlock()
		_ = s.EnviarAudio(g.ctx, b)
	})
	if err != nil {
		a.logf("microfone: %v", err)
	}

	go func() {
		defer close(g.pronta)
		tok, err := a.d.Gateway.LiveToken(ctx, a.d.ModeloVoz)
		if err != nil {
			g.err = fmt.Errorf("live-token: %w", err)
			return
		}
		g.token = tok
		s, err := a.d.Abrir(ctx, tok.Token, tok.Servido, limpeza.DicaLive(a.d.Dicionario()))
		if err != nil {
			g.err = fmt.Errorf("sessão Live (%s): %w", tok.Servido, err)
			// O Google recusou depois do token emitido: avisa o gateway.
			go a.d.Gateway.LiveFailure(context.Background(), tok.RequestID)
			return
		}
		a.logf("sessão aberta em %v (%s)", time.Since(g.inicio).Round(time.Millisecond), tok.Servido)
		g.mu.Lock()
		pend := g.pendente
		g.pendente = nil
		g.sessao = s
		g.mu.Unlock()
		for _, b := range pend {
			_ = s.EnviarAudio(ctx, b)
		}
	}()
}

func (a *App) parar(entregar bool) {
	a.mu.Lock()
	g := a.rec
	a.rec = nil
	a.mu.Unlock()
	if g == nil {
		return
	}
	defer g.cancel()
	a.d.Microfone.Parar()
	solto := time.Now()
	<-g.pronta
	if g.err != nil {
		a.logf("%v", g.err)
		return
	}
	s := g.sessao
	defer s.Fechar()
	if !entregar {
		a.logf("cancelado")
		a.reportarUso(g)
		return
	}
	cru, err := s.Finalizar(g.ctx, 700*time.Millisecond, 8*time.Second)
	a.reportarUso(g)
	if err != nil && cru == "" {
		a.logf("transcrição: %v", err)
		return
	}
	tCru := time.Since(solto)
	if cru == "" {
		a.logf("nada transcrito")
		return
	}
	final, err := limpeza.Limpar(g.ctx, a.d.Chat, a.d.ModeloQuik, a.d.Dicionario(), cru)
	if err != nil {
		a.logf("limpeza falhou, digitando o cru: %v", err)
	}
	a.logf("cru em %v, final em %v: %q", tCru.Round(time.Millisecond), time.Since(solto).Round(time.Millisecond), final)
	if err := a.d.Digitar(final); err != nil {
		a.logf("digitar: %v", err)
	}
}

func (a *App) reportarUso(g *gravacao) {
	if g.sessao == nil || g.token.RequestID == "" {
		return
	}
	e, s := g.sessao.Uso()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = a.d.Gateway.LiveUsage(ctx, g.token.RequestID, gateway.Uso{Entrada: e, Saida: s})
	}()
}

// Abrir adapta transcribe.Abrir para a interface Sessao.
func AbrirLive(ctx context.Context, token, modeloReal, instrucao string) (Sessao, error) {
	return transcribe.Abrir(ctx, token, modeloReal, instrucao, nil)
}
