package atalho

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func at(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }

func TestSegurarGravaESoltarPara(t *testing.T) {
	var m Maquina
	if e := m.Pressionou(at(0)); e != Nada {
		t.Fatalf("press: %v", e)
	}
	if e := m.Tick(at(100)); e != Nada {
		t.Fatalf("antes de Segurar não grava: %v", e)
	}
	if e := m.Tick(at(210)); e != Comecar {
		t.Fatalf("depois de Segurar grava: %v", e)
	}
	if e := m.Soltou(at(3000)); e != Parar {
		t.Fatalf("soltar para: %v", e)
	}
}

func TestToqueRapidoSozinhoNaoGrava(t *testing.T) {
	var m Maquina
	m.Pressionou(at(0))
	if e := m.Soltou(at(80)); e != Nada {
		t.Fatalf("toque curto: %v", e)
	}
	if m.Gravando() {
		t.Fatal("um toque não grava")
	}
}

func TestToqueDuploTravaDepoisDaJanelaEToqueSeguinteEncerra(t *testing.T) {
	var m Maquina
	m.Pressionou(at(0))
	m.Soltou(at(80))
	if e := m.Pressionou(at(250)); e != Nada {
		t.Fatalf("segundo toque ainda não começa antes de excluir toque triplo: %v", e)
	}
	if e := m.Soltou(at(320)); e != Nada {
		t.Fatalf("soltar segundo toque aguarda janela: %v", e)
	}
	if e := m.Tick(at(320 + int(ToqueDuplo/time.Millisecond) - 1)); e != Nada || m.Gravando() {
		t.Fatalf("duplo começou antes de fechar janela: evento=%v gravando=%v", e, m.Gravando())
	}
	if e := m.Tick(at(320 + int(ToqueDuplo/time.Millisecond))); e != Comecar || !m.Gravando() {
		t.Fatalf("duplo não travou ao fechar janela: evento=%v gravando=%v", e, m.Gravando())
	}
	if e := m.Pressionou(at(900)); e != Parar {
		t.Fatalf("terceiro gesto após trava encerra imediatamente: %v", e)
	}
	if m.Gravando() {
		t.Fatal("depois do toque de destravar não deve continuar gravando")
	}
	if e := m.Soltou(at(908)); e != Nada {
		t.Fatalf("soltar depois de destravar não para novamente: %v", e)
	}
}

func TestToqueTriploRapidoIgnoradoSemIniciar(t *testing.T) {
	var m Maquina
	m.Pressionou(at(0))
	m.Soltou(at(60))
	m.Pressionou(at(140))
	m.Soltou(at(200))
	if m.Gravando() {
		t.Fatal("o segundo toque não deve iniciar antes da janela de toque triplo")
	}
	if e := m.Pressionou(at(260)); e != Ignorar {
		t.Fatalf("terceiro toque deve ser ignorado: %v", e)
	}
	if !m.Ignorado() || m.Gravando() {
		t.Fatalf("gesto triplo não foi descartado: ignorado=%v gravando=%v", m.Ignorado(), m.Gravando())
	}
	if e := m.Tick(at(1000)); e != Nada || m.Gravando() {
		t.Fatalf("tick iniciou gesto triplo ignorado: evento=%v gravando=%v", e, m.Gravando())
	}
	if e := m.Soltou(at(320)); e != Nada || m.Ignorado() {
		t.Fatalf("soltar deve limpar gesto descartado: evento=%v ignorado=%v", e, m.Ignorado())
	}
	if e := m.Pressionou(at(500)); e != Nada {
		t.Fatalf("próximo toque limpo foi confundido com triplo: %v", e)
	}
	if e := m.Tick(at(500 + int(Segurar/time.Millisecond))); e != Comecar {
		t.Fatalf("hold seguinte não começou: %v", e)
	}
}

func TestToqueTriploIgnoradoMesmoSeTickAtrasa(t *testing.T) {
	var m Maquina
	m.Pressionou(at(0))
	m.Soltou(at(50))
	m.Pressionou(at(100))
	m.Soltou(at(150))
	// O terceiro press ocorre antes do tick que normalmente converteria o
	// duplo em trava, e é consumido sem iniciar o microfone.
	if e := m.Pressionou(at(200)); e != Ignorar {
		t.Fatalf("terceiro toque = %v, want Ignorar", e)
	}
	m.Soltou(at(250))
	if e := m.Tick(at(1000)); e != Nada || m.Gravando() {
		t.Fatalf("tick atrasado iniciou o gesto descartado: %v", e)
	}
}

func TestTravaDestravaMesmoSeNaoChegarEventoSoltou(t *testing.T) {
	var m Maquina
	m.Pressionou(at(0))
	m.Soltou(at(80))
	if e := m.Pressionou(at(250)); e != Nada {
		t.Fatalf("segundo toque espera a janela do toque triplo: %v", e)
	}
	m.Soltou(at(300))
	// A janela fecha no tick: só então o duplo vira trava e começa a gravar.
	if e := m.Tick(at(300 + int(ToqueDuplo/time.Millisecond))); e != Comecar {
		t.Fatalf("duplo toque deve começar travado após a janela: %v", e)
	}
	if !m.Gravando() {
		t.Fatal("segundo toque solto deve continuar travado")
	}
	// A combinação vira e perde foco antes do key-up: pressionar deve bastar
	// para terminar; a máquina não pode depender do próximo KeyUp.
	if e := m.Pressionou(at(10_000)); e != Parar {
		t.Fatalf("pressionar novamente deve parar: %v", e)
	}
	if m.Gravando() {
		t.Fatal("trava não foi encerrada")
	}
}

func TestToquesLentosNaoTravam(t *testing.T) {
	var m Maquina
	m.Pressionou(at(0))
	m.Soltou(at(80))
	if e := m.Pressionou(at(900)); e != Nada {
		t.Fatalf("fora da janela não trava: %v", e)
	}
}

func TestAltGrComOutraTeclaNaoDispara(t *testing.T) {
	var m Maquina
	m.Pressionou(at(0))
	m.OutraTecla() // Ctrl+Alt+Q = "/" no ABNT2
	if e := m.Tick(at(500)); e != Nada {
		t.Fatalf("combinação suja não grava: %v", e)
	}
	m.Soltou(at(600))
	if e := m.Pressionou(at(700)); e != Nada {
		t.Fatalf("toque sujo não conta para o duplo: %v", e)
	}
}

func TestOutraTeclaDuranteGravacaoCancela(t *testing.T) {
	var m Maquina
	m.Pressionou(at(0))
	m.Tick(at(250))
	if e := m.OutraTecla(); e != Cancelar {
		t.Fatalf("got %v", e)
	}
	if m.Gravando() {
		t.Fatal("cancelado não grava")
	}
}
