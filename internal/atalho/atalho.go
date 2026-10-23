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
)

const (
	Segurar    = 200 * time.Millisecond // tempo mínimo para virar gravação segurada
	ToqueDuplo = 350 * time.Millisecond // janela entre dois toques para travar
	ToqueCurto = 200 * time.Millisecond // um toque é soltar antes de Segurar
)

type estado int

const (
	ocioso        estado = iota
	apertado             // combinação pressionada, ainda não decidiu
	gravando             // segurando e gravando
	travado              // gravando sem segurar (após toque duplo)
	soltandoTrava        // apertou de novo durante a trava; para ao soltar
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
		// O segundo toque dentro da janela trava: começa a gravar já.
		if !m.ultimoToque.IsZero() && agora.Sub(m.ultimoToque) <= ToqueDuplo {
			m.st = travado
			m.ultimoToque = time.Time{}
			return Comecar
		}
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
// apertado → gravando depois de Segurar.
func (m *Maquina) Tick(agora time.Time) Evento {
	if m.st == apertado && !m.sujo && agora.Sub(m.desde) >= Segurar {
		m.st = gravando
		return Comecar
	}
	return Nada
}

// OutraTecla: alguma tecla que não é do atalho foi pressionada.
func (m *Maquina) OutraTecla() Evento {
	switch m.st {
	case apertado:
		m.sujo = true
		m.ultimoToque = time.Time{}
	case gravando:
		// Ctrl+Alt+X durante a gravação: era AltGr/atalho de outro app.
		m.st = ocioso
		return Cancelar
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
	case gravando:
		m.st = ocioso
		return Parar
	case soltandoTrava:
		m.st = ocioso
		return Parar
	}
	return Nada
}

// Gravando informa se há gravação em curso.
func (m *Maquina) Gravando() bool {
	return m.st == gravando || m.st == travado || m.st == soltandoTrava
}
