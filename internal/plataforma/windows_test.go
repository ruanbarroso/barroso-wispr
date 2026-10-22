//go:build windows

package plataforma

import (
	"testing"
	"unsafe"
)

// SendInput recusa (retorna 0) se cbSize não for o sizeof(INPUT) do Win32.
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
