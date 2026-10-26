//go:build darwin

package plataforma

import (
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"
)

// As structs passadas ao C precisam bater byte a byte com os headers da Apple.
func TestLayoutDasStructsNativas(t *testing.T) {
	if got := unsafe.Sizeof(audioStreamBasicDescription{}); got != 40 {
		t.Fatalf("sizeof(AudioStreamBasicDescription)=%d, want 40", got)
	}
	var b audioQueueBuffer
	for _, c := range []struct {
		nome      string
		got, want uintptr
	}{
		{"mAudioData", unsafe.Offsetof(b.AudioData), 8},
		{"mAudioDataByteSize", unsafe.Offsetof(b.AudioDataByteSize), 16},
		{"mUserData", unsafe.Offsetof(b.UserData), 24},
		{"mPacketDescriptionCount", unsafe.Offsetof(b.PacketDescriptionCount), 48},
	} {
		if c.got != c.want {
			t.Errorf("offset %s=%d, want %d", c.nome, c.got, c.want)
		}
	}
	if got := unsafe.Sizeof(cfRunLoopTimerContext{}); got != 40 {
		t.Fatalf("sizeof(CFRunLoopTimerContext)=%d, want 40", got)
	}
	if got := unsafe.Sizeof(macRect{}); got != 32 {
		t.Fatalf("sizeof(NSRect)=%d, want 32", got)
	}
}

func TestBlocoDe100ms(t *testing.T) {
	if TaxaAmostras != 16000 || BlocoBytes != 3200 {
		t.Fatalf("TaxaAmostras=%d BlocoBytes=%d", TaxaAmostras, BlocoBytes)
	}
}

func eventosTeclado(k *Teclado) *[]string {
	var ev []string
	k.Pressionou = func() { ev = append(ev, "down") }
	k.Soltou = func() { ev = append(ev, "up") }
	k.OutraTecla = func() { ev = append(ev, "outra") }
	return &ev
}

func TestCtrlOptionEsquerdosCompletamESoltam(t *testing.T) {
	k := &Teclado{}
	ev := eventosTeclado(k)
	k.tecla(macEventTypeFlags, macKeyLeftControl, macDeviceLeftCtrl)
	k.tecla(macEventTypeFlags, macKeyLeftOption, macDeviceLeftCtrl|macDeviceLeftAlt)
	k.tecla(macEventTypeFlags, macKeyLeftOption, macDeviceLeftCtrl)
	k.tecla(macEventTypeFlags, macKeyLeftControl, 0)
	if got := strings.Join(*ev, " "); got != "down up" {
		t.Fatalf("eventos=%q", got)
	}
}

// Option direito (AltGr de teclados europeus) nunca completa o atalho.
func TestOptionDireitoNaoCompletaOAtalho(t *testing.T) {
	k := &Teclado{}
	ev := eventosTeclado(k)
	k.tecla(macEventTypeFlags, macKeyLeftControl, macDeviceLeftCtrl)
	k.tecla(macEventTypeFlags, macKeyRightOption, macDeviceLeftCtrl|macDeviceRightAlt)
	if got := strings.Join(*ev, " "); got != "outra" {
		t.Fatalf("eventos=%q", got)
	}
}

// Soltar o Ctrl esquerdo com o direito apertado precisa soltar o atalho: a
// flag genérica de Control continuaria ligada, os bits por lado não.
func TestSoltarCtrlEsquerdoComDireitoApertado(t *testing.T) {
	k := &Teclado{}
	ev := eventosTeclado(k)
	k.tecla(macEventTypeFlags, macKeyLeftControl, macDeviceLeftCtrl)
	k.tecla(macEventTypeFlags, macKeyLeftOption, macDeviceLeftCtrl|macDeviceLeftAlt)
	k.tecla(macEventTypeFlags, macKeyRightControl, macDeviceLeftCtrl|macDeviceLeftAlt|macDeviceRightCtrl)
	k.tecla(macEventTypeFlags, macKeyLeftControl, macDeviceLeftAlt|macDeviceRightCtrl)
	if got := strings.Join(*ev, " "); got != "down outra up" {
		t.Fatalf("eventos=%q", got)
	}
}

func TestOutraTeclaComModificadorApertado(t *testing.T) {
	k := &Teclado{}
	ev := eventosTeclado(k)
	k.tecla(macEventTypeDown, 0x00, 0) // 'a' sem modificador: ignora
	k.tecla(macEventTypeFlags, macKeyLeftControl, macDeviceLeftCtrl)
	k.tecla(macEventTypeDown, 0x00, macDeviceLeftCtrl) // Ctrl+A
	k.tecla(macEventTypeFlags, vkMacShift, macDeviceLeftCtrl|0x2)
	if got := strings.Join(*ev, " "); got != "outra outra" {
		t.Fatalf("eventos=%q", got)
	}
}

func TestTravaDestravaComNovoPressionamento(t *testing.T) {
	k := &Teclado{}
	ev := eventosTeclado(k)
	toque := func() {
		k.tecla(macEventTypeFlags, macKeyLeftControl, macDeviceLeftCtrl)
		k.tecla(macEventTypeFlags, macKeyLeftOption, macDeviceLeftCtrl|macDeviceLeftAlt)
		k.tecla(macEventTypeFlags, macKeyLeftOption, macDeviceLeftCtrl)
		k.tecla(macEventTypeFlags, macKeyLeftControl, 0)
	}
	toque()
	toque()
	k.tecla(macEventTypeFlags, macKeyLeftControl, macDeviceLeftCtrl)
	k.tecla(macEventTypeFlags, macKeyLeftOption, macDeviceLeftCtrl|macDeviceLeftAlt)
	if got := strings.Join(*ev, " "); got != "down up down up down" {
		t.Fatalf("eventos=%q", got)
	}
}

// O callback do tap nunca pode rodar o trabalho do app.
func TestTapNaoBloqueiaComCallbackLento(t *testing.T) {
	feito := make(chan struct{})
	k := &Teclado{Pressionou: func() { time.Sleep(300 * time.Millisecond); close(feito) }}
	k.iniciarFila()
	defer k.pararFila()
	t0 := time.Now()
	k.tecla(macEventTypeFlags, macKeyLeftControl, macDeviceLeftCtrl)
	k.tecla(macEventTypeFlags, macKeyLeftOption, macDeviceLeftCtrl|macDeviceLeftAlt)
	if d := time.Since(t0); d > 50*time.Millisecond {
		t.Fatalf("tecla() bloqueou %v", d)
	}
	select {
	case <-feito:
	case <-time.After(2 * time.Second):
		t.Fatal("callback nunca rodou")
	}
}

func TestPartesDigitacao(t *testing.T) {
	texto := "Olá\r\nmundo 🎤" + strings.Repeat("x", 25)
	partes := partesDigitacao(texto)
	var recomposto []string
	for _, p := range partes {
		if p == nil {
			recomposto = append(recomposto, "\n")
			continue
		}
		if len(p) > maxUnicodePorEvento {
			t.Fatalf("parte com %d unidades", len(p))
		}
		if last := rune(p[len(p)-1]); utf16.IsSurrogate(last) && last < 0xDC00 {
			t.Fatalf("parte termina no meio de um par substituto: %v", p)
		}
		recomposto = append(recomposto, string(utf16.Decode(p)))
	}
	if got, want := strings.Join(recomposto, ""), strings.ReplaceAll(texto, "\r", ""); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// Emoji exatamente na fronteira de 20 unidades vai inteiro para o bloco seguinte.
	borda := partesDigitacao(strings.Repeat("a", 19) + "🎤")
	if len(borda) != 2 || len(borda[0]) != 19 || len(borda[1]) != 2 {
		t.Fatalf("fronteira: %v", borda)
	}
}

func TestEdgeRectsMac(t *testing.T) {
	tela := macRect{macPoint{-1440, 100}, macSize{1440, 900}}
	r := edgeRectsMac(tela)
	want := []macRect{
		{macPoint{-1440, 993}, macSize{1440, 7}},
		{macPoint{-1440, 100}, macSize{1440, 7}},
		{macPoint{-1440, 100}, macSize{7, 900}},
		{macPoint{-7, 100}, macSize{7, 900}},
	}
	if len(r) != 4 {
		t.Fatalf("len=%d", len(r))
	}
	for n := range want {
		if r[n] != want[n] {
			t.Errorf("faixa %d=%+v, want %+v", n, r[n], want[n])
		}
	}
	for _, f := range edgeRectsMac(macRect{Size: macSize{3, 5}}) {
		if f.Size.Width > 3 || f.Size.Height > 5 || f.Size.Width <= 0 || f.Size.Height <= 0 {
			t.Errorf("faixa fora da tela minúscula: %+v", f)
		}
	}
	if edgeRectsMac(macRect{}) != nil {
		t.Error("tela vazia gerou faixas")
	}
}

func TestPulseOpacityMac(t *testing.T) {
	var lo, hi byte = 255, 0
	for e := time.Duration(0); e < 2200*time.Millisecond; e += 5 * time.Millisecond {
		o := pulseOpacityMac(e)
		lo, hi = min(lo, o), max(hi, o)
	}
	if lo < 32 || hi > 100 || hi-lo < 40 {
		t.Fatalf("faixa [%d, %d]", lo, hi)
	}
}

// Sem Rodar, Parar só marca o flag e não toca em AppKit; Close sem Start é inócuo.
func TestPararECloseSemIniciar(t *testing.T) {
	k := &Teclado{}
	k.Parar()
	if !k.parar.Load() {
		t.Fatal("Parar não marcou o flag")
	}
	i := NewIndicador()
	i.SetActive(true)
	i.Close()
	i.Close()
	if err := i.Start(); err == nil {
		t.Fatal("Start depois de Close deveria falhar")
	}
	var m Microfone
	m.Parar()
	m.Parar()
}
