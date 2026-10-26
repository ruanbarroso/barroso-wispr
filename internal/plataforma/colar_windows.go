//go:build windows

package plataforma

import (
	"errors"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Colar insere o texto de uma vez pelo clipboard: copia tudo o que estava lá
// (clipboard_copia_windows.go), põe o ditado, manda Ctrl+V e devolve o
// conteúdo anterior. Digitar caractere por caractere (SendInput Unicode) fica
// como plano B.

var (
	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procEmptyClipboard             = user32.NewProc("EmptyClipboard")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procSetClipboardData           = user32.NewProc("SetClipboardData")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procRegisterClipboardFormatW   = user32.NewProc("RegisterClipboardFormatW")
	procGetAsyncKeyState           = user32.NewProc("GetAsyncKeyState")
	procGlobalAlloc                = kernel32.NewProc("GlobalAlloc")
	procGlobalFree                 = kernel32.NewProc("GlobalFree")
	procGlobalLock                 = kernel32.NewProc("GlobalLock")
	procGlobalUnlock               = kernel32.NewProc("GlobalUnlock")
	procRtlMoveMemory              = windows.NewLazySystemDLL("kernel32.dll").NewProc("RtlMoveMemory")
)

const (
	cfBitmap      = 2
	cfDIB         = 8
	cfUnicodeText = 13
	gmemMoveable  = 0x0002

	vkShift = 0x10
	vkLWin  = 0x5B
	vkRWin  = 0x5C
	vkV     = 0x56
)

// Quanto esperar o app de destino ler o clipboard antes de restaurar. O
// Ctrl+V é assíncrono: restaurar cedo demais cola o conteúdo ANTIGO.
const esperaColar = 300 * time.Millisecond

// Colar cola o texto no campo com foco; se o clipboard falhar, digita.
func Colar(texto string) error {
	if texto == "" {
		return nil
	}
	if err := colarClipboard(texto); err != nil {
		return Digitar(texto)
	}
	return nil
}

func colarClipboard(texto string) error {
	// Com Ctrl/Alt/Shift/Win ainda físicos, o Ctrl+V vira Ctrl+Alt+V (atalho
	// de outro app). Na trava, o toque de destravar pode estar seguro ainda.
	esperarModificadoresSoltos(2 * time.Second)

	// Copia TUDO o que o usuário tinha (texto, print, arquivos...) para
	// devolver depois do Ctrl+V. Sem cópia, o ditado fica no clipboard.
	anterior, errCopia := copiarClipboard()
	if err := gravarTexto(texto, true); err != nil {
		return err
	}
	nossa := sequenciaClipboard()
	if err := enviarCtrlV(); err != nil {
		if errCopia == nil {
			_ = anterior.restaurar()
		}
		return err
	}
	time.Sleep(esperaColar)
	// Se o usuário copiou algo novo nesse intervalo, o clipboard já é dele.
	if errCopia == nil && sequenciaClipboard() == nossa {
		_ = anterior.restaurar()
	}
	return nil
}

func abrirClipboard() error {
	// Outro processo pode estar com o clipboard aberto por alguns ms.
	for i := 0; i < 20; i++ {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("clipboard ocupado por outro processo")
}

func lerTexto() (string, bool) {
	if r, _, _ := procIsClipboardFormatAvailable.Call(cfUnicodeText); r == 0 {
		return "", false
	}
	if abrirClipboard() != nil {
		return "", false
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", false
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return "", false
	}
	defer procGlobalUnlock.Call(h)
	var u []uint16
	for off := uintptr(0); ; off += 2 {
		var word uint16
		procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&word)), p+off, 2)
		if word == 0 {
			break
		}
		u = append(u, word)
	}
	return string(utf16.Decode(u)), true
}

// gravarTexto põe o texto no clipboard. foraDoHistorico marca o conteúdo para
// não entrar no histórico do Win+V nem na sincronização entre dispositivos —
// o ditado é transitório.
func gravarTexto(texto string, foraDoHistorico bool) error {
	u := append(utf16.Encode([]rune(texto)), 0)
	tam := uintptr(len(u) * 2)
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, tam)
	if h == 0 {
		return errors.New("GlobalAlloc falhou")
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return errors.New("GlobalLock falhou")
	}
	procRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&u[0])), tam)
	procGlobalUnlock.Call(h)

	if err := abrirClipboard(); err != nil {
		procGlobalFree.Call(h)
		return err
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	if r, _, _ := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		procGlobalFree.Call(h) // sem sucesso, a memória continua nossa
		return errors.New("SetClipboardData falhou")
	}
	if foraDoHistorico {
		marcarForaDoHistorico()
	}
	return nil
}

func enviarCtrlV() error {
	ins := []input{
		{Type: inputKeyboard, Ki: keybdinput{WVk: vkControl}},
		{Type: inputKeyboard, Ki: keybdinput{WVk: vkV}},
		{Type: inputKeyboard, Ki: keybdinput{WVk: vkV, DwFlags: keyeventfKeyUp}},
		{Type: inputKeyboard, Ki: keybdinput{WVk: vkControl, DwFlags: keyeventfKeyUp}},
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

func teclaFisicaApertada(vk uintptr) bool {
	r, _, _ := procGetAsyncKeyState.Call(vk)
	return r&0x8000 != 0
}

func esperarModificadoresSoltos(teto time.Duration) {
	limite := time.Now().Add(teto)
	for time.Now().Before(limite) {
		if !teclaFisicaApertada(vkControl) && !teclaFisicaApertada(vkMenu) &&
			!teclaFisicaApertada(vkShift) && !teclaFisicaApertada(vkLWin) && !teclaFisicaApertada(vkRWin) {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
}
