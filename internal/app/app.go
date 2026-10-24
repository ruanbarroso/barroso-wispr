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
	Gateway           *gateway.Cliente
	Chat              limpeza.Chat
	Abrir             func(ctx context.Context, token, modeloReal, instrucao string) (Sessao, error)
	Microfone         Microfone
	Digitar           func(string) error
	Dicionario        func() []string
	ModeloVoz         string
	ModeloQuik        string
	Log               io.Writer
	BeepReady         func()
	BeepDone          func()
	EstadoGravacao    func(bool)
	EstadoProcessando func(bool)
}

// App mantém a máquina do atalho e a gravação em curso.
type App struct {
	d   Deps
	mu  sync.Mutex
	maq atalho.Maquina
	rec *gravacao

	ativos   int // inclui finalização, limpeza e colagem após rec ser limpo
	trocaPed bool
	trocando bool
	troca    func() bool
	parada   func()

	// Token Live pedido com antecedência: o live-token do gateway variou de
	// 0,4 s a 13 s, e era quase todo o tempo de abertura da sessão.
	tokMu    sync.Mutex
	tokPrev  *gateway.Token
	tokEm    time.Time
	tokBusca chan struct{} // fecha quando o pedido em andamento termina
}

// validadeToken fica abaixo dos 60 s que o token tem para abrir uma sessão
// (newSessionExpireTime, no gateway e no auth_tokens do Google).
const validadeToken = 45 * time.Second

// Aquecer pede um token em segundo plano para a próxima gravação.
func (a *App) Aquecer() {
	a.tokMu.Lock()
	if a.tokBusca != nil || (a.tokPrev != nil && time.Since(a.tokEm) < validadeToken) {
		a.tokMu.Unlock()
		return
	}
	feito := make(chan struct{})
	a.tokBusca = feito
	a.tokMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		tok, err := a.d.Gateway.LiveToken(ctx, a.d.ModeloVoz)
		a.tokMu.Lock()
		a.tokBusca = nil
		if err == nil {
			a.tokPrev, a.tokEm = &tok, time.Now()
		}
		a.tokMu.Unlock()
		close(feito)
	}()
}

// tokenPronto devolve o token aquecido, se ainda válido; ele é de uso único.
// Se um pedido de aquecimento está em andamento, espera por ele em vez de
// abrir um segundo pedido, que só somaria mais uma espera no gateway.
func (a *App) tokenPronto(ctx context.Context) (gateway.Token, bool) {
	a.tokMu.Lock()
	busca := a.tokBusca
	a.tokMu.Unlock()
	if busca != nil {
		select {
		case <-busca:
		case <-ctx.Done():
			return gateway.Token{}, false
		}
	}
	a.tokMu.Lock()
	defer a.tokMu.Unlock()
	if a.tokPrev == nil || time.Since(a.tokEm) >= validadeToken {
		a.tokPrev = nil
		return gateway.Token{}, false
	}
	tok := *a.tokPrev
	a.tokPrev = nil
	return tok, true
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
	// captureMu/capturando controlam o callback de estado da captura, isolado
	// do beep: o microfone já desligou, mas a finalização/colagem ainda segue.
	captureMu              sync.Mutex
	capturando             bool
	capturaEncerrada       bool
	capturaFalsaNotificada bool
}

func Novo(d Deps) *App { return &App{d: d} }

// notificarEstadoGravacao acompanha apenas a captura, não a transcrição nem
// a colagem. O callback é serializado com o estado para impedir que um início
// tardio reative uma gravação já encerrada.
func (a *App) notificarEstadoGravacao(g *gravacao, ativo bool) bool {
	g.captureMu.Lock()
	defer g.captureMu.Unlock()
	if ativo {
		if g.capturaEncerrada || g.capturando {
			return false
		}
		g.capturando = true
	} else {
		if g.capturaEncerrada {
			return false
		}
		g.capturaEncerrada = true
		g.capturando = false
	}
	if a.d.EstadoGravacao != nil {
		a.d.EstadoGravacao(ativo)
	}
	return true
}

// PedirTroca agenda f (que encerra o processo para a versão nova) para o
// primeiro momento em que nenhum ditado estiver em curso — inclusive a
// finalização, a limpeza e a colagem. Até lá, o atalho não começa gravação
// nova. Se f devolver false, a troca é abandonada e o atalho volta ao normal.
func (a *App) DefinirParada(f func()) {
	a.mu.Lock()
	a.parada = f
	a.mu.Unlock()
	a.talvezTrocar()
}

func (a *App) PedirTroca(f func() bool) {
	a.mu.Lock()
	a.troca, a.trocaPed = f, true
	a.mu.Unlock()
	a.talvezTrocar()
}

// Ocioso informa se não há ditado em curso nem troca de processo começada.
func (a *App) Ocioso() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ativos == 0 && !a.trocando
}

func (a *App) talvezTrocar() {
	a.mu.Lock()
	if !a.trocaPed || a.trocando || a.ativos > 0 || a.troca == nil || a.parada == nil {
		a.mu.Unlock()
		return
	}
	f := a.troca
	a.trocando = true
	a.mu.Unlock()
	if f() {
		a.mu.Lock()
		parar := a.parada
		a.mu.Unlock()
		if parar != nil {
			parar()
		}
		return
	}
	a.mu.Lock()
	a.trocando, a.trocaPed, a.troca = false, false, nil
	a.maq.Reset()
	a.mu.Unlock()
}

func (a *App) terminouDitado() {
	a.mu.Lock()
	a.ativos--
	a.mu.Unlock()
	a.talvezTrocar()
}

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
	if a.trocando {
		return atalho.Nada
	}
	e := f(&a.maq)
	// Com troca pedida, deixa terminar o ditado em curso (Parar/Cancelar
	// passam), mas não começa outro.
	if e == atalho.Comecar && a.trocaPed {
		a.maq.Reset()
		return atalho.Nada
	}
	return e
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
	if a.rec != nil || a.trocaPed || a.trocando {
		a.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	g := &gravacao{ctx: ctx, cancel: cancel, inicio: time.Now(), pronta: make(chan struct{})}
	a.rec = g
	a.ativos++
	a.mu.Unlock()
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
		a.notificarEstadoGravacao(g, false)
	} else {
		if !a.notificarEstadoGravacao(g, true) {
			// A gravação pode ter sido encerrada enquanto Iniciar ainda rodava.
			a.d.Microfone.Parar()
		} else if a.d.BeepReady != nil {
			// O microfone já grava desde aqui; o áudio fica em buffer até a sessão
			// abrir. Então já dá para falar — não precisa esperar a conexão.
			a.d.BeepReady()
		}
	}

	go func() {
		defer close(g.pronta)
		tok, aquecido := a.tokenPronto(ctx)
		if !aquecido {
			var err error
			tok, err = a.d.Gateway.LiveToken(ctx, a.d.ModeloVoz)
			if err != nil {
				g.err = fmt.Errorf("live-token: %w", err)
				return
			}
		}
		tToken := time.Since(g.inicio)
		g.token = tok
		s, err := a.d.Abrir(ctx, tok.Token, tok.Servido, limpeza.DicaLive(a.d.Dicionario()))
		if err != nil {
			g.err = fmt.Errorf("sessão Live (%s): %w", tok.Servido, err)
			// O Google recusou depois do token emitido: avisa o gateway.
			go a.d.Gateway.LiveFailure(context.Background(), tok.RequestID)
			return
		}
		origem := "pedido agora"
		if aquecido {
			origem = "aquecido"
		}
		a.logf("sessão aberta em %v (token %v %s + conexão %v, %s)", time.Since(g.inicio).Round(time.Millisecond),
			tToken.Round(time.Millisecond), origem, (time.Since(g.inicio) - tToken).Round(time.Millisecond), tok.Servido)
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
	defer a.terminouDitado() // roda por último: depois da colagem e do Fechar
	// Depois de um ditado costuma vir outro: deixa o próximo token pronto.
	defer a.Aquecer()
	a.d.Microfone.Parar()
	a.notificarEstadoGravacao(g, false)
	if entregar && a.d.EstadoProcessando != nil {
		a.d.EstadoProcessando(true)
		defer a.d.EstadoProcessando(false)
	}
	if a.d.BeepDone != nil {
		a.d.BeepDone()
	}
	if !entregar {
		a.logf("gravação cancelada")
	} else {
		a.logf("gravação encerrada")
	}
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
