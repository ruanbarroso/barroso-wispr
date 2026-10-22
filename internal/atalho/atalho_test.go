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

func TestToqueDuploTravaEToqueSeguinteEncerra(t *testing.T) {
	var m Maquina
	m.Pressionou(at(0))
	m.Soltou(at(80))
	if e := m.Pressionou(at(250)); e != Comecar {
		t.Fatalf("segundo toque trava e começa: %v", e)
	}
	if e := m.Soltou(at(320)); e != Nada {
		t.Fatalf("soltar na trava continua gravando: %v", e)
	}
	if e := m.Tick(at(5000)); e != Nada || !m.Gravando() {
		t.Fatalf("trava segue gravando: %v", e)
	}
	m.Pressionou(at(9000))
	if e := m.Soltou(at(9080)); e != Parar {
		t.Fatalf("toque na trava encerra: %v", e)
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
