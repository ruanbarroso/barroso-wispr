// Package app liga as peças do ditado: atalho → token Live → microfone em
// stream → texto cru → passe no barroso-quick com o dicionário → digitar.
package app

import (
	"context"
	"fmt"
	"io"
	"strings"
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
	// Caiu diz se a conexão terminou sem o app pedir, e por quê.
	Caiu() (bool, string)
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
	BeepErro          func() // no lugar do BeepDone quando a gravação parou sozinha
	EstadoGravacao    func(bool)
	EstadoProcessando func(bool)
	// Transcrever é a reserva: transcreve o áudio inteiro do ditado de uma vez
	// quando a Live não devolve texto. Opcional.
	Transcrever func(ctx context.Context, pcm []byte) (string, error)
}

// diagnosticavel é a sessão que sabe resumir o que o servidor mandou.
type diagnosticavel interface{ Diagnostico() string }

// App mantém a máquina do atalho e a gravação em curso.
type App struct {
	d   Deps
	mu  sync.Mutex
	maq atalho.Maquina
	rec *gravacao

	ativos   int // inclui finalização, limpeza e colagem após rec ser limpo
	lote     *lote
	proc     int // ditados processando; o pulso toca enquanto > 0
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

// lote junta os ditados que se sobrepõem: um ditado começado enquanto o
// anterior ainda não foi colado entra no mesmo lote, e o lote só é colado,
// de uma vez e na ordem de fala, quando todos os ditados dele terminaram.
type lote struct {
	partes []string
	feitos int
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
		tok, err := a.buscarToken(ctx)
		a.tokMu.Lock()
		a.tokBusca = nil
		if err == nil {
			a.tokPrev, a.tokEm = &tok, time.Now()
		}
		a.tokMu.Unlock()
		close(feito)
	}()
}

const (
	tetoToken          = 8 * time.Second
	maxTentativasToken = 3
)

// esperaHedgeToken é variável só para o teste não esperar 2,5 s.
var esperaHedgeToken = 2500 * time.Millisecond

// buscarToken pede o token Live com tentativas em paralelo: o live-token do
// gateway costuma responder em 0,5 s, mas em 2026-10-29 algumas chamadas
// levaram 10 s e 31 s, e o tom e a borda de "pronto" só saem depois dele. Se a
// primeira não voltar em esperaHedgeToken (ou falhar), dispara outra; vale a
// primeira que responder.
func (a *App) buscarToken(ctx context.Context) (gateway.Token, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type resultado struct {
		tok gateway.Token
		err error
	}
	ch := make(chan resultado, maxTentativasToken)
	lancar := func() {
		go func() {
			c, cc := context.WithTimeout(ctx, tetoToken)
			defer cc()
			tok, err := a.d.Gateway.LiveToken(c, a.d.ModeloVoz)
			ch <- resultado{tok, err}
		}()
	}
	tentativas, voando := 1, 1
	lancar()
	timer := time.NewTimer(esperaHedgeToken)
	defer timer.Stop()
	for {
		select {
		case r := <-ch:
			voando--
			if r.err == nil {
				return r.tok, nil
			}
			if voando == 0 {
				if tentativas >= maxTentativasToken {
					return gateway.Token{}, r.err
				}
				tentativas++
				voando++
				lancar()
			}
		case <-timer.C:
			if tentativas < maxTentativasToken {
				tentativas++
				voando++
				lancar()
				timer.Reset(esperaHedgeToken)
			}
		case <-ctx.Done():
			return gateway.Token{}, ctx.Err()
		}
	}
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
	ctx            context.Context
	cancel         context.CancelFunc
	inicio         time.Time
	pronta         chan struct{} // fecha quando abre a sessão, falha ou o áudio é descartado
	sessao         Sessao
	token          gateway.Token
	err            error
	caiu           bool
	lote           *lote
	parte          int // posição deste ditado no lote
	mu             sync.Mutex
	audioMu        sync.Mutex // ordena captura com a abertura e o envio à Live
	prontaUma      sync.Once
	pendente       [][]byte // buffer local até a validação e abertura da Live
	audio          []byte   // áudio válido para a transcrição de reserva
	detector       detectorVoz
	bytesAud       int
	sessaoIniciada bool
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

// acao é o que o atalho decidiu, já aplicado ao estado do app: a gravação
// que começou (reservada em a.rec) ou a que foi retirada de a.rec para parar.
type acao struct {
	comecar  *gravacao
	parar    *gravacao
	entregar bool
}

// comMaq decide e aplica o evento do atalho sob o MESMO lock: a máquina do
// atalho e a.rec nunca discordam. Antes, Tick (outra goroutine) e Soltou
// decidiam sob o lock mas só registravam a gravação depois de soltá-lo; com
// toques rápidos o Parar corria antes de o Comecar registrar a gravação, não
// achava nada, e a gravação ficava ligada com o atalho achando que estava
// ocioso — nenhum toque a desligava.
func (a *App) comMaq(f func(*atalho.Maquina) atalho.Evento) acao {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.trocando {
		return acao{}
	}
	gravandoAntes := a.maq.Gravando()
	e := f(&a.maq)
	switch e {
	case atalho.Comecar:
		if a.trocaPed {
			// Com troca pedida, deixa terminar o ditado em curso, mas não começa outro.
			a.maq.Reset()
			return acao{}
		}
		if a.rec != nil {
			// Rede de segurança: o atalho achava que não havia gravação, mas
			// havia. O gesto de começar encerra a que está ligada.
			a.maq.Reset()
			return a.retirarRec(true)
		}
		return acao{comecar: a.reservar()}
	case atalho.Parar, atalho.Cancelar:
		return a.retirarRec(e == atalho.Parar)
	}
	// Rede de segurança: um toque com o atalho ocioso e uma gravação ligada
	// (não deveria acontecer) encerra a gravação em vez de ser ignorado.
	if !gravandoAntes && !a.maq.Gravando() && a.rec != nil && e == atalho.Nada && a.maq.Apertado() {
		a.maq.Reset()
		return a.retirarRec(true)
	}
	return acao{}
}

// retirarRec tira a gravação em curso de a.rec; a.mu deve estar travado.
func (a *App) retirarRec(entregar bool) acao {
	g := a.rec
	a.rec = nil
	if g == nil {
		return acao{}
	}
	return acao{parar: g, entregar: entregar}
}

func (a *App) tratar(x acao) {
	if x.parar != nil {
		go a.encerrar(x.parar, x.entregar)
	}
	if x.comecar != nil {
		a.iniciar(x.comecar)
	}
}

// reservar cria a gravação e a registra em a.rec; a.mu deve estar travado.
func (a *App) reservar() *gravacao {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	g := &gravacao{ctx: ctx, cancel: cancel, inicio: time.Now(), pronta: make(chan struct{}), detector: novoDetectorVoz()}
	if a.lote == nil {
		a.lote = &lote{}
	}
	g.lote, g.parte = a.lote, len(a.lote.partes)
	a.lote.partes = append(a.lote.partes, "")
	a.rec = g
	a.ativos++
	return g
}

// comecar reserva e inicia uma gravação fora do atalho (diagnóstico e testes).
func (a *App) comecar() {
	a.mu.Lock()
	if a.rec != nil || a.trocaPed || a.trocando {
		a.mu.Unlock()
		return
	}
	g := a.reservar()
	a.mu.Unlock()
	a.iniciar(g)
}

// iniciar captura localmente e só abre a Live depois de 1 s de áudio com voz.
func (a *App) iniciar(g *gravacao) {
	// O token leva de 0,5 s a 30 s: pede já, enquanto o VAD confirma a fala,
	// para a sessão abrir sem esperar por ele (tokenPronto espera este pedido).
	a.Aquecer()
	err := a.d.Microfone.Iniciar(func(pcm []byte) {
		b := append([]byte(nil), pcm...)
		g.audioMu.Lock()
		g.mu.Lock()
		if !g.sessaoIniciada {
			g.bytesAud += len(b)
			g.detector.processar(b)
		}
		if g.sessao == nil {
			g.pendente = append(g.pendente, b)
			abrir := !g.sessaoIniciada && g.bytesAud >= minDitadoBytes && g.detector.falaConfirmada()
			if abrir {
				g.sessaoIniciada = true
				if a.d.Transcrever != nil {
					for _, bloco := range g.pendente {
						g.audio = append(g.audio, bloco...)
					}
				}
			} else if !g.sessaoIniciada {
				g.pendente = limitarPreRoll(g.pendente, preRollMaxBytes)
			} else if a.d.Transcrever != nil {
				g.audio = append(g.audio, b...)
			}
			g.mu.Unlock()
			g.audioMu.Unlock()
			if abrir {
				go a.abrirSessao(g)
			}
			return
		}
		s := g.sessao
		if a.d.Transcrever != nil {
			g.audio = append(g.audio, b...)
		}
		g.mu.Unlock()
		_ = s.EnviarAudio(g.ctx, b)
		g.audioMu.Unlock()
	})
	if err != nil {
		g.mu.Lock()
		g.err = fmt.Errorf("microfone: %w", err)
		g.mu.Unlock()
		a.logf("microfone: %v", err)
		a.notificarEstadoGravacao(g, false)
		g.prontaUma.Do(func() { close(g.pronta) })
	}
}

func limitarPreRoll(blocos [][]byte, maxBytes int) [][]byte {
	total := 0
	for _, bloco := range blocos {
		total += len(bloco)
	}
	remover := total - maxBytes
	for remover > 0 && len(blocos) > 0 {
		if remover >= len(blocos[0]) {
			remover -= len(blocos[0])
			blocos = blocos[1:]
			continue
		}
		if remover%2 != 0 {
			remover++
		}
		if remover >= len(blocos[0]) {
			blocos = blocos[1:]
		} else {
			blocos[0] = blocos[0][remover:]
		}
		break
	}
	return blocos
}

func (a *App) abrirSessao(g *gravacao) {
	defer g.prontaUma.Do(func() { close(g.pronta) })
	ctx := g.ctx
	tok, aquecido := a.tokenPronto(ctx)
	if !aquecido {
		var err error
		tok, err = a.buscarToken(ctx)
		if err != nil {
			g.mu.Lock()
			g.err = fmt.Errorf("live-token: %w", err)
			g.mu.Unlock()
			return
		}
	}
	tToken := time.Since(g.inicio)
	g.mu.Lock()
	g.token = tok
	g.mu.Unlock()
	s, err := a.d.Abrir(ctx, tok.Token, tok.Servido, limpeza.DicaLive(a.d.Dicionario()))
	if err != nil {
		g.mu.Lock()
		g.err = fmt.Errorf("sessão Live (%s): %w", tok.Servido, err)
		g.mu.Unlock()
		go a.d.Gateway.LiveFailure(context.Background(), tok.RequestID)
		return
	}
	origem := "pedido agora"
	if aquecido {
		origem = "aquecido"
	}
	a.logf("sessão aberta em %v (token %v %s + conexão %v, %s)", time.Since(g.inicio).Round(time.Millisecond),
		tToken.Round(time.Millisecond), origem, (time.Since(g.inicio) - tToken).Round(time.Millisecond), tok.Servido)
	g.audioMu.Lock()
	g.mu.Lock()
	pend := g.pendente
	g.pendente = nil
	g.sessao = s
	g.mu.Unlock()
	for _, bloco := range pend {
		_ = s.EnviarAudio(ctx, bloco)
	}
	g.audioMu.Unlock()
	if a.notificarEstadoGravacao(g, true) {
		a.logf("pronto para falar")
		if a.d.BeepReady != nil {
			a.d.BeepReady()
		}
	}
	go a.vigiarConexao(g)
}

// vigiarConexao encerra o ditado no instante em que a sessão Live falha ou
// cai no meio da gravação. Antes o usuário continuava falando sem nada ser
// transcrito e só descobria no fim, com o texto cortado: agora toca o som de
// erro, a borda some e o que já foi transcrito é colado no cursor.
func (a *App) vigiarConexao(g *gravacao) {
	select {
	case <-g.pronta:
	case <-g.ctx.Done():
		return
	}
	motivo := ""
	if g.err != nil {
		motivo = g.err.Error()
	} else {
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for motivo == "" {
			select {
			case <-g.ctx.Done():
				return
			case <-tick.C:
				if caiu, m := g.sessao.Caiu(); caiu {
					motivo = m
				}
			}
		}
	}
	a.mu.Lock()
	if a.rec != g {
		a.mu.Unlock()
		return // o usuário já tinha encerrado
	}
	a.rec = nil
	a.maq.Reset() // um próximo Ctrl+Alt começa ditado novo, não "encerra" este
	a.mu.Unlock()
	g.mu.Lock()
	g.caiu = true
	g.mu.Unlock()
	a.logf("a gravação parou sozinha: %s", motivo)
	a.encerrar(g, true)
}

func (a *App) parar(entregar bool) {
	a.mu.Lock()
	g := a.rec
	a.rec = nil
	a.mu.Unlock()
	if g == nil {
		return
	}
	a.encerrar(g, entregar)
}

// encerrar desliga a captura de g e, se entregar, transcreve e cola.
func (a *App) encerrar(g *gravacao, entregar bool) {
	defer g.cancel()
	defer a.terminouDitado() // roda por último: depois da colagem e do Fechar
	a.d.Microfone.Parar()
	a.notificarEstadoGravacao(g, false)
	g.audioMu.Lock()
	g.mu.Lock()
	iniciada := g.sessaoIniciada
	erro := g.err
	if !iniciada {
		g.pendente = nil
	}
	g.mu.Unlock()
	g.audioMu.Unlock()
	if !iniciada {
		g.prontaUma.Do(func() { close(g.pronta) })
		if !entregar {
			a.logf("gravação cancelada")
		} else if erro != nil {
			a.logf("%v", erro)
		} else {
			a.logf("gravação ignorada: menos de 1 s ou sem fala detectada")
		}
		a.entregar(g, "")
		return
	}
	// Depois de um ditado válido costuma vir outro: deixa o próximo token pronto.
	defer a.Aquecer()
	if entregar {
		a.processando(1)
		defer a.processando(-1)
	}
	// Roda antes dos defers acima: o texto (ou nada, se falhou ou foi
	// cancelado) sempre libera a vaga deste ditado no lote.
	texto := ""
	defer func() { a.entregar(g, texto) }()
	beepFim := a.d.BeepDone
	g.mu.Lock()
	caiu := g.caiu
	g.mu.Unlock()
	if caiu && a.d.BeepErro != nil {
		beepFim = a.d.BeepErro
	}
	if beepFim != nil {
		beepFim()
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
	quieto, teto := esperaFinal(solto.Sub(g.inicio))
	cru, err := s.Finalizar(g.ctx, quieto, teto)
	a.reportarUso(g)
	if err != nil && cru == "" {
		a.logf("transcrição: %v", err)
	}
	if cru == "" {
		cru = a.reserva(g, s)
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
	texto = final
}

// minReserva é o mínimo de áudio (0,5 s a 16 kHz s16le) para valer a reserva.
const minReserva = 16000 * 2 / 2

// reserva transcreve o áudio inteiro do ditado quando a Live terminou sem
// texto. Em 2026-10-25 19:49 a Live abriu, recebeu 3 s de fala e não devolveu
// nada: com a borda acesa o usuário falou, e o texto tem de sair.
func (a *App) reserva(g *gravacao, s Sessao) string {
	diag := ""
	if d, ok := s.(diagnosticavel); ok {
		diag = " (" + d.Diagnostico() + ")"
	}
	g.mu.Lock()
	pcm := g.audio
	g.audio = nil
	g.mu.Unlock()
	if a.d.Transcrever == nil || len(pcm) < minReserva {
		if diag != "" {
			a.logf("Live sem texto%s", diag)
		}
		return ""
	}
	a.logf("Live sem texto%s; transcrevendo %v de áudio pela reserva", diag,
		(time.Duration(len(pcm)) * time.Second / 32000).Round(100*time.Millisecond))
	ctx, cancel := context.WithTimeout(g.ctx, 60*time.Second)
	defer cancel()
	t0 := time.Now()
	texto, err := a.d.Transcrever(ctx, pcm)
	if err != nil {
		a.logf("reserva falhou: %v", err)
		return ""
	}
	a.logf("reserva em %v", time.Since(t0).Round(time.Millisecond))
	return strings.TrimSpace(texto)
}

// entregar guarda o texto do ditado no lote e, se ele era o último pendente,
// cola o lote inteiro de uma vez. Enquanto houver ditado gravando ou
// processando no lote, nada é colado.
func (a *App) entregar(g *gravacao, texto string) {
	a.mu.Lock()
	l := g.lote
	l.partes[g.parte] = strings.TrimSpace(texto)
	l.feitos++
	if l.feitos < len(l.partes) {
		a.mu.Unlock()
		if texto != "" {
			a.logf("texto guardado; esperando %d ditado(s) do lote", len(l.partes)-l.feitos)
		}
		return
	}
	if a.lote == l {
		a.lote = nil
	}
	a.mu.Unlock()
	var partes []string
	for _, p := range l.partes {
		if p != "" {
			partes = append(partes, p)
		}
	}
	if len(partes) == 0 {
		return
	}
	if len(partes) > 1 {
		a.logf("colando %d ditados juntos", len(partes))
	}
	if err := a.d.Digitar(strings.Join(partes, " ")); err != nil {
		a.logf("digitar: %v", err)
	}
}

// processando conta os ditados em transcrição/limpeza: o pulso liga no
// primeiro e só desliga quando o último termina.
func (a *App) processando(delta int) {
	a.mu.Lock()
	antes := a.proc
	a.proc += delta
	depois := a.proc
	a.mu.Unlock()
	if a.d.EstadoProcessando == nil {
		return
	}
	if antes == 0 && depois > 0 {
		a.d.EstadoProcessando(true)
	} else if antes > 0 && depois == 0 {
		a.d.EstadoProcessando(false)
	}
}

// esperaFinal dá o silêncio e o teto da espera pela transcrição final,
// crescendo com a duração da gravação. Com 0,7 s fixo, um ditado de 33 s ou
// 1min35s voltava cortado: o modelo ainda transcrevia o fim quando o app
// considerava o texto pronto.
func esperaFinal(gravado time.Duration) (quieto, teto time.Duration) {
	quieto = 1200*time.Millisecond + gravado/20
	if quieto > 4*time.Second {
		quieto = 4 * time.Second
	}
	teto = 8*time.Second + gravado/4
	if teto > 30*time.Second {
		teto = 30 * time.Second
	}
	return quieto, teto
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
