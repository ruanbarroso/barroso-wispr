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

// Colar mantém a API simples para callers sem logging detalhado.
func Colar(texto string) error {
	_, err := ColarDiagnostico(texto)
	return err
}

// ColarDiagnostico registra foco, estado do clipboard e eventos de teclado
// aceitos pelo sistema. O Windows não confirma inserção no controle de destino.
func ColarDiagnostico(texto string) (d DiagnosticoColagem, err error) {
	t0 := time.Now()
	defer func() { d.Duracao = time.Since(t0) }()
	d.Etapa = "início"
	d.FocoAntes = FocoAtual()
	if texto == "" {
		d.Etapa = "texto vazio"
		return d, nil
	}
	if err = colarClipboard(texto, &d); err == nil {
		return d, nil
	}
	causa := err
	d.Fallback = "Digitar: " + causa.Error()
	d.FocoNoEnvio = FocoAtual()
	d.EventosDigitacao, err = digitarEventos(texto)
	d.FocoDepois = FocoAtual()
	if err != nil {
		d.Etapa = "fallback Digitar falhou"
		return d, err
	}
	d.Etapa = "fallback Digitar enviado; inserção não confirmada"
	return d, nil
}

func colarClipboard(texto string, d *DiagnosticoColagem) error {
	// Com Ctrl/Alt/Shift/Win ainda físicos, o Ctrl+V vira outro atalho.
	d.EsperaModificadores, d.ModificadoresPendentes = esperarModificadoresSoltosDiagnostico(2 * time.Second)
	d.FocoDepoisDaEspera = FocoAtual()

	anterior, errCopia := copiarClipboard()
	if errCopia != nil {
		d.ClipboardReadError = errCopia.Error()
	}
	d.ClipboardSeqAntes = int64(sequenciaClipboard())
	if err := gravarTexto(texto, true); err != nil {
		d.Etapa = "gravar texto no clipboard"
		return err
	}
	d.ClipboardSeqDepois = int64(sequenciaClipboard())
	if copiado, ok := lerTexto(); !ok || copiado != texto {
		d.Etapa = "verificar texto no clipboard"
		d.ClipboardReadError = "não foi possível ler de volta o texto gravado"
		return errors.New("texto não foi confirmado no clipboard")
	}
	// Sequência antes do envio: se continuar igual após a espera, o ditado
	// ainda é o dono do clipboard e pode ser devolvido.
	seqDitado := sequenciaClipboard()

	d.ClipboardVerificado = true
	d.FocoNoEnvio = FocoAtual()
	d.Etapa = "enviar Ctrl+V"
	var err error
	d.EventosPaste, err = enviarCtrlVDiagnostico()
	if err != nil {
		if errCopia == nil {
			if restoreErr := anterior.restaurar(); restoreErr != nil {
				d.ClipboardRestoreError = restoreErr.Error()
			}
		}
		return err
	}
	time.Sleep(esperaColar)
	d.FocoDepois = FocoAtual()
	d.ClipboardSeqDepois = int64(sequenciaClipboard())
	// Se o usuário copiou algo novo nesse intervalo, o clipboard já é dele.
	if errCopia == nil && sequenciaClipboard() == seqDitado {
		if restoreErr := anterior.restaurar(); restoreErr != nil {
			d.ClipboardRestoreError = restoreErr.Error()
		}
	}
	d.Etapa = "Ctrl+V enviado; inserção não confirmada pelo sistema"
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
	_, err := enviarCtrlVDiagnostico()
	return err
}

func enviarCtrlVDiagnostico() (int, error) {
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
		return int(n), err
	}
	return int(n), nil
}

func teclaFisicaApertada(vk uintptr) bool {
	r, _, _ := procGetAsyncKeyState.Call(vk)
	return r&0x8000 != 0
}

func esperarModificadoresSoltosDiagnostico(teto time.Duration) (time.Duration, []string) {
	inicio := time.Now()
	limite := inicio.Add(teto)
	teclas := []struct {
		vk   uintptr
		nome string
	}{
		{vkControl, "control"}, {vkMenu, "alt"}, {vkShift, "shift"},
		{vkLControl, "left-control"}, {vkRControl, "right-control"},
		{vkLMenu, "left-alt"}, {vkRMenu, "right-alt"},
		{vkLWin, "left-win"}, {vkRWin, "right-win"},
	}
	var pendentes []string
	for {
		pendentes = pendentes[:0]
		for _, k := range teclas {
			if teclaFisicaApertada(k.vk) {
				pendentes = append(pendentes, k.nome)
			}
		}
		if len(pendentes) == 0 || !time.Now().Before(limite) {
			return time.Since(inicio), append([]string(nil), pendentes...)
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func esperarModificadoresSoltos(teto time.Duration) {
	_, _ = esperarModificadoresSoltosDiagnostico(teto)
}
