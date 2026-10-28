// Package atalho é a máquina de estados do atalho de ditado, separada do
// hook do sistema para poder ser testada:
//
//   - segurar o atalho (sozinho, sem outra tecla) por Segurar = grava; soltar = para;
//   - dois toques rápidos = trava: grava sem segurar; pressionar de novo encerra imediatamente.
//
// "Sozinho" importa: no ABNT2, Ctrl+Alt é o AltGr (/, ?, °). Se qualquer
// outra tecla for pressionada com o atalho, ele é cancelado.
package atalho

import "time"

// Evento é o que a máquina devolve para o app.
type Evento int

const (
	Nada     Evento = iota
	Comecar         // abrir sessão e começar a gravar
	Parar           // encerrar a gravação e entregar o texto
	Cancelar        // descartar (outra tecla entrou no meio)
	Ignorar         // três toques: gesto descartado, nada é feito
)

const (
	Segurar    = 200 * time.Millisecond // tempo mínimo para virar gravação segurada
	ToqueDuplo = 350 * time.Millisecond // janela entre dois toques para travar
	ToqueCurto = 200 * time.Millisecond // um toque é soltar antes de Segurar
)

type estado int

const (
	ocioso          estado = iota
	apertado               // primeiro toque ou hold
	segundoApertado        // segundo toque pressionado, aguardando release/hold
	duploPendente          // segundo toque curto; aguarda a janela para excluir toque triplo
	gravando               // segurando e gravando
	travado                // gravando sem segurar (após toque duplo)
	soltandoTrava          // apertou de novo durante a trava; para ao soltar
	ignorado               // três toques: o gesto é descartado e não reage a nada
)

// Maquina recebe as transições do teclado já traduzidas.
type Maquina struct {
	st          estado
	desde       time.Time
	ultimoToque time.Time
	sujo        bool // outra tecla entrou com o atalho pressionado
}

// Pressionou: a combinação acabou de ficar completa.
func (m *Maquina) Pressionou(agora time.Time) Evento {
	switch m.st {
	case ocioso:
		m.st, m.desde, m.sujo = apertado, agora, false
		if !m.ultimoToque.IsZero() && agora.Sub(m.ultimoToque) <= ToqueDuplo {
			// Adia o início da trava até fechar a janela do toque triplo.
			m.st = segundoApertado
		}
	case duploPendente:
		if agora.Sub(m.desde) <= ToqueDuplo {
			m.st = ignorado
			m.ultimoToque = time.Time{}
			return Ignorar
		}
		// Se o tick atrasou, o duplo teria virado trava antes deste toque;
		// inicia e marca o press atual como o gesto que a encerra ao soltar.
		m.st = soltandoTrava
		m.ultimoToque = time.Time{}
		return Comecar
	case travado:
		// O segundo toque de destravar para imediatamente. Esperar `Soltou`
		// é frágil: o hook do Windows pode não entregar KeyUp quando a janela
		// que tem foco trata a combinação como atalho.
		m.st = ocioso
		m.ultimoToque = time.Time{}
		return Parar
	}
	return Nada
}

// Tick é chamado periodicamente (ou a cada evento) para promover
// apertado → gravando depois de Segurar, ou duploPendente → trava depois que
// a janela de toque triplo fecha sem um terceiro toque.
func (m *Maquina) Tick(agora time.Time) Evento {
	if (m.st == apertado || m.st == segundoApertado) && !m.sujo && agora.Sub(m.desde) >= Segurar {
		m.st = gravando
		m.ultimoToque = time.Time{}
		return Comecar
	}
	if m.st == duploPendente && agora.Sub(m.desde) >= ToqueDuplo {
		m.st = travado
		m.ultimoToque = time.Time{}
		return Comecar
	}
	return Nada
}

// OutraTecla: alguma tecla que não é do atalho foi pressionada.
func (m *Maquina) OutraTecla() Evento {
	switch m.st {
	case apertado, segundoApertado:
		m.sujo = true
		m.ultimoToque = time.Time{}
	case duploPendente:
		m.st = ocioso
		m.ultimoToque = time.Time{}
	case gravando:
		// Ctrl+Alt+X durante a gravação: era AltGr/atalho de outro app.
		m.st = ocioso
		return Cancelar
	case ignorado:
		// O gesto de três toques foi descartado; qualquer tecla extra volta
		// ao normal imediatamente, sem esperar o próximo toque curto.
		m.st = ocioso
		m.ultimoToque = time.Time{}
	}
	return Nada
}

// Soltou: a combinação deixou de estar completa.
func (m *Maquina) Soltou(agora time.Time) Evento {
	switch m.st {
	case apertado:
		m.st = ocioso
		if !m.sujo && agora.Sub(m.desde) < ToqueCurto {
			m.ultimoToque = agora
		}
	case segundoApertado:
		if !m.sujo && agora.Sub(m.desde) < ToqueCurto {
			m.st = duploPendente
			m.desde = agora
		} else {
			m.st = ocioso
			m.ultimoToque = time.Time{}
		}
	case gravando:
		m.st = ocioso
		return Parar
	case soltandoTrava:
		m.st = ocioso
		return Parar
	case ignorado:
		// O gesto descartado só termina quando a combinação é solta:
		// um quarto clique (sem soltar antes) não pode ser outro toque curto.
		m.st = ocioso
		m.ultimoToque = time.Time{}
	}
	return Nada
}

// Gravando informa se há gravação em curso.
func (m *Maquina) Gravando() bool {
	return m.st == gravando || m.st == travado || m.st == soltandoTrava
}

// Ignorado informa se a máquina descartou um gesto de três toques e está
// esperando o usuário soltar o atalho para voltar ao normal.
func (m *Maquina) Ignorado() bool { return m.st == ignorado }

// Apertado informa se a combinação acabou de ser pressionada (um toque que
// ainda não virou gravação segurada). O app usa para reconhecer um toque dado
// com o atalho ocioso e, se ainda assim houver gravação ligada, encerrá-la:
// é a rede de segurança do caso em que a máquina e a gravação discordavam e
// nenhum gesto desligava o microfone.
func (m *Maquina) Apertado() bool { return m.st == apertado || m.st == segundoApertado }

// Reset volta ao estado ocioso sem devolver eventos; o chamador já sabe o que
// fazer (encerrar um ditado em curso ou abortar uma troca de processo).
func (m *Maquina) Reset() {
	m.st = ocioso
	m.desde = time.Time{}
	m.ultimoToque = time.Time{}
	m.sujo = false
}
