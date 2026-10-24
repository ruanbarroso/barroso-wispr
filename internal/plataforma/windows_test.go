//go:build windows

package plataforma

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"
	"time"
	"unsafe"
)

// SendInput recusa (retorna 0) se cbSize não for o sizeof(INPUT) do Win32.
func TestRenderToneHasLowVolumeAndSoftFades(t *testing.T) {
	const rate = 22050
	note := tone{frequency: 523, duration: 120 * time.Millisecond, level: 0.035}
	pcm := renderTone(note, rate)
	if want := rate * 120 / 1000 * 2; len(pcm) != want {
		t.Fatalf("PCM length = %d bytes, want %d", len(pcm), want)
	}
	peak := 0
	for i := 0; i < len(pcm); i += 2 {
		v := int(math.Abs(float64(int16(binary.LittleEndian.Uint16(pcm[i:])))))
		if v > peak {
			peak = v
		}
	}
	if peak > math.MaxInt16*4/100 {
		t.Fatalf("peak = %d, want a low-level soft tone", peak)
	}
	if got := int16(binary.LittleEndian.Uint16(pcm[:2])); got != 0 {
		t.Fatalf("first sample = %d, want a zero-crossing fade-in", got)
	}
	if got := int16(binary.LittleEndian.Uint16(pcm[len(pcm)-2:])); got != 0 {
		t.Fatalf("last sample = %d, want a zero-crossing fade-out", got)
	}
}

func TestTamanhoDoINPUT(t *testing.T) {
	want := uintptr(40)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		want = 28
	}
	if got := unsafe.Sizeof(input{}); got != want {
		t.Fatalf("sizeof(INPUT)=%d, want %d", got, want)
	}
}

func TestAltGrNaoCompletaOAtalho(t *testing.T) {
	var press, outra int
	k := &Teclado{Pressionou: func() { press++ }, OutraTecla: func() { outra++ }}
	k.tecla(vkLControl, true, false) // o Windows manda LCtrl falso antes do AltGr
	k.tecla(vkRMenu, true, false)
	if press != 0 || outra != 1 {
		t.Fatalf("press=%d outra=%d", press, outra)
	}
}

func TestCtrlAltEsquerdosCompletamESoltam(t *testing.T) {
	var press, solt int
	k := &Teclado{Pressionou: func() { press++ }, Soltou: func() { solt++ }}
	k.tecla(vkLControl, true, false)
	k.tecla(vkLMenu, true, false)
	k.tecla(vkLMenu, true, false) // auto-repeat não dispara de novo
	k.tecla(vkLMenu, false, true)
	if press != 1 || solt != 1 {
		t.Fatalf("press=%d solt=%d", press, solt)
	}
}

// O callback do hook não pode executar o trabalho do app: se ele demora
// (abrir microfone, rede), o Windows remove o hook em silêncio e o toque de
// destravar nunca chega. Com a fila ligada, tecla() volta na hora.
func TestHookNaoBloqueiaComCallbackLento(t *testing.T) {
	feito := make(chan struct{})
	k := &Teclado{Pressionou: func() { time.Sleep(500 * time.Millisecond); close(feito) }}
	k.iniciarFila()
	defer k.pararFila()
	t0 := time.Now()
	k.tecla(vkLControl, true, false)
	k.tecla(vkLMenu, true, false)
	if d := time.Since(t0); d > 50*time.Millisecond {
		t.Fatalf("tecla() bloqueou %v no callback do hook", d)
	}
	select {
	case <-feito:
	case <-time.After(2 * time.Second):
		t.Fatal("callback nunca rodou")
	}
}

func TestTravaDestravaNoHookComNovoPressionamento(t *testing.T) {
	var eventos []string
	k := &Teclado{
		Pressionou: func() { eventos = append(eventos, "down") },
		Soltou:     func() { eventos = append(eventos, "up") },
	}
	// primeiro toque, solta; segundo toque trava e solta; terceiro toque
	// deve emitir Pressionou mesmo que o aplicativo foque/consuma o key-up.
	k.tecla(vkLControl, true, false)
	k.tecla(vkLMenu, true, false)
	k.tecla(vkLMenu, false, true)
	k.tecla(vkLControl, false, true)
	k.tecla(vkLControl, true, false)
	k.tecla(vkLMenu, true, false)
	k.tecla(vkLMenu, false, true)
	k.tecla(vkLControl, false, true)
	k.tecla(vkLControl, true, false)
	k.tecla(vkLMenu, true, false)
	if got := strings.Join(eventos, " "); got != "down up down up down" {
		t.Fatalf("eventos de começo, trava e destrava: %v", eventos)
	}
}
