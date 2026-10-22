//go:build windows

// Package plataforma liga o wispr ao sistema operacional: hook global de
// teclado (WH_KEYBOARD_LL) para o atalho e SendInput com KEYEVENTF_UNICODE
// para digitar o texto no campo com foco.
package plataforma

import (
	"errors"
	"runtime"
	"sync/atomic"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procSetWindowsHookExW   = user32.NewProc("SetWindowsHookExW")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procPostThreadMessageW  = user32.NewProc("PostThreadMessageW")
	procSendInput           = user32.NewProc("SendInput")
	procGetCurrentThreadId  = kernel32.NewProc("GetCurrentThreadId")
)

const (
	whKeyboardLL = 13

	wmKeyDown    = 0x0100
	wmKeyUp      = 0x0101
	wmSysKeyDown = 0x0104
	wmSysKeyUp   = 0x0105
	wmQuit       = 0x0012

	llkhfInjected = 0x10

	vkControl  = 0x11
	vkMenu     = 0x12 // Alt
	vkLControl = 0xA2
	vkRControl = 0xA3
	vkLMenu    = 0xA4
	vkRMenu    = 0xA5 // AltGr

	inputKeyboard    = 1
	keyeventfKeyUp   = 0x0002
	keyeventfUnicode = 0x0004
)

type kbdllhookstruct struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

// keybdInput + padding: INPUT tem 40 bytes no amd64 (união com MOUSEINPUT).
type input struct {
	Type uint32
	_    uint32
	Ki   keybdinput
	_    [8]byte
}

type keybdinput struct {
	WVk         uint16
	WScan       uint16
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

// Teclado entrega as transições do atalho Ctrl(esquerdo)+Alt(esquerdo).
// O AltGr (Alt direito, que o Windows reporta como LCtrl+RAlt) nunca conta.
type Teclado struct {
	Pressionou func()
	Soltou     func()
	OutraTecla func()

	thread uint32
	ctrl   atomic.Bool
	alt    atomic.Bool
	ativo  atomic.Bool
}

// Rodar instala o hook e bloqueia no laço de mensagens até Parar.
func (t *Teclado) Rodar() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	tid, _, _ := procGetCurrentThreadId.Call()
	t.thread = uint32(tid)

	// lParam chega como ponteiro tipado: evita a conversão uintptr→Pointer que
	// o vet (corretamente) reprova.
	cb := windows.NewCallback(func(nCode int, wParam uintptr, k *kbdllhookstruct) uintptr {
		if nCode >= 0 && k != nil {
			// Teclas que o próprio wispr injeta (SendInput) não são do usuário.
			if k.Flags&llkhfInjected == 0 {
				t.tecla(k.VkCode, wParam == wmKeyDown || wParam == wmSysKeyDown, wParam == wmKeyUp || wParam == wmSysKeyUp)
			}
		}
		r, _, _ := procCallNextHookEx.Call(0, uintptr(nCode), wParam, uintptr(unsafe.Pointer(k)))
		return r
	})
	h, _, err := procSetWindowsHookExW.Call(whKeyboardLL, cb, 0, 0)
	if h == 0 {
		return err
	}
	defer procUnhookWindowsHookEx.Call(h)

	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return nil
		}
	}
}

// Parar encerra o laço de mensagens.
func (t *Teclado) Parar() {
	if t.thread != 0 {
		procPostThreadMessageW.Call(uintptr(t.thread), wmQuit, 0, 0)
	}
}

func (t *Teclado) tecla(vk uint32, desce, sobe bool) {
	switch vk {
	case vkLControl, vkControl:
		t.mudou(&t.ctrl, desce, sobe)
	case vkLMenu, vkMenu:
		t.mudou(&t.alt, desce, sobe)
	case vkRControl, vkRMenu:
		// AltGr e Ctrl direito: tratar como "outra tecla" para não disparar.
		if desce && t.OutraTecla != nil {
			t.OutraTecla()
		}
	default:
		if desce && (t.ctrl.Load() || t.alt.Load()) && t.OutraTecla != nil {
			t.OutraTecla()
		}
	}
}

func (t *Teclado) mudou(b *atomic.Bool, desce, sobe bool) {
	if desce {
		b.Store(true)
	} else if sobe {
		b.Store(false)
	}
	completo := t.ctrl.Load() && t.alt.Load()
	if completo && !t.ativo.Load() {
		t.ativo.Store(true)
		if t.Pressionou != nil {
			t.Pressionou()
		}
	} else if !completo && t.ativo.Load() {
		t.ativo.Store(false)
		if t.Soltou != nil {
			t.Soltou()
		}
	}
}

// Digitar insere o texto no campo com foco como se fosse digitado, em UTF-16
// (acentos e emojis incluídos). Quebras de linha viram Shift+Enter? Não: \n
// vira Enter, que é o que um usuário digitando faria.
func Digitar(texto string) error {
	if texto == "" {
		return nil
	}
	var ins []input
	for _, u := range utf16.Encode([]rune(texto)) {
		if u == '\r' {
			continue
		}
		if u == '\n' {
			ins = append(ins,
				input{Type: inputKeyboard, Ki: keybdinput{WVk: 0x0D}},
				input{Type: inputKeyboard, Ki: keybdinput{WVk: 0x0D, DwFlags: keyeventfKeyUp}})
			continue
		}
		ins = append(ins,
			input{Type: inputKeyboard, Ki: keybdinput{WScan: u, DwFlags: keyeventfUnicode}},
			input{Type: inputKeyboard, Ki: keybdinput{WScan: u, DwFlags: keyeventfUnicode | keyeventfKeyUp}})
	}
	n, _, err := procSendInput.Call(uintptr(len(ins)), uintptr(unsafe.Pointer(&ins[0])), unsafe.Sizeof(ins[0]))
	if int(n) != len(ins) {
		if err == nil || errors.Is(err, windows.ERROR_SUCCESS) {
			err = errors.New("SendInput bloqueado (janela com privilégio maior, ex.: app como administrador)")
		}
		return err
	}
	return nil
}
