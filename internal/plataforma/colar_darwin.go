//go:build darwin

package plataforma

import (
	"errors"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/ebitengine/purego/objc"
)

// Colar no macOS: guarda o texto do pasteboard, põe o ditado (marcado como
// transitório para gerenciadores de histórico), manda Command+V e devolve o
// texto anterior. Digitar (eventos Unicode do Quartz) é o plano B.

const (
	cgEventSourceUserData = 42
	cgPostEventToSession  = 1          // kCGSessionEventTap
	cgStateHID            = 1          // kCGEventSourceStateHIDSystemState
	cgFlagCommand         = 1 << 20    // kCGEventFlagMaskCommand
	macEventTag           = 0x42575750 // "BWWP": marca eventos que o próprio app injeta

	vkMacV          = 0x09
	vkMacReturn     = 0x24
	vkMacShift      = 0x38
	vkMacControl    = 0x3B
	vkMacOption     = 0x3A
	vkMacCommand    = 0x37
	vkMacRightShift = 0x3C
	vkMacRightOpt   = 0x3D
	vkMacRightCtrl  = 0x3E
	vkMacRightCmd   = 0x36

	// Quantos UTF-16 por evento: o Quartz trunca strings longas num só evento.
	maxUnicodePorEvento = 20
)

// Quanto esperar o app de destino ler o pasteboard antes de restaurar.
const esperaColarMac = 300 * time.Millisecond

var (
	eventosOnce sync.Once
	eventosErr  error

	cgCreateKeyboardEvent func(uintptr, uint16, bool) uintptr
	cgKeyboardSetUnicode  func(uintptr, uint64, *uint16)
	cgEventSetInt         func(uintptr, uint32, int64)
	cgEventSetFlags       func(uintptr, uint64)
	cgEventPost           func(uint32, uintptr)
	cgEventRelease        func(uintptr) // CFRelease serve para CGEventRef
	cgEventSourceKeyState func(int32, uint16) bool
	cgPreflightPost       func() bool
	cgRequestPost         func() bool
)

func initEventos() error {
	eventosOnce.Do(func() {
		for _, e := range []struct {
			framework, name string
			fn              any
		}{
			{coreGraphicsPath, "CGEventCreateKeyboardEvent", &cgCreateKeyboardEvent},
			{coreGraphicsPath, "CGEventKeyboardSetUnicodeString", &cgKeyboardSetUnicode},
			{coreGraphicsPath, "CGEventSetIntegerValueField", &cgEventSetInt},
			{coreGraphicsPath, "CGEventSetFlags", &cgEventSetFlags},
			{coreGraphicsPath, "CGEventPost", &cgEventPost},
			{coreGraphicsPath, "CGEventSourceKeyState", &cgEventSourceKeyState},
			{coreGraphicsPath, "CGPreflightPostEventAccess", &cgPreflightPost},
			{coreGraphicsPath, "CGRequestPostEventAccess", &cgRequestPost},
			{coreFoundationPath, "CFRelease", &cgEventRelease},
		} {
			if err := registerDarwinSymbol(e.framework, e.name, e.fn); err != nil {
				eventosErr = err
				return
			}
		}
	})
	return eventosErr
}

// permissaoPostar confere (e pede, na primeira vez) a permissão de
// Acessibilidade que o macOS exige para injetar teclas em outros apps.
func permissaoPostar() error {
	if err := initEventos(); err != nil {
		return err
	}
	if cgPreflightPost() || cgRequestPost() {
		return nil
	}
	return errors.New("o macOS não autorizou o envio de teclas: libere o Barroso Wispr em Ajustes do Sistema > Privacidade e Segurança > Acessibilidade")
}

// Colar cola o texto no campo com foco; se o pasteboard falhar, digita.
func Colar(texto string) error {
	if texto == "" {
		return nil
	}
	if err := permissaoPostar(); err != nil {
		return err // sem permissão, Digitar também falharia
	}
	if err := colarPasteboard(texto); err != nil {
		return Digitar(texto)
	}
	return nil
}

func colarPasteboard(texto string) error {
	if err := loadDarwinFrameworks(appKitPath); err != nil {
		return err
	}
	// Com Control/Option ainda físicos, o Command+V vira outro atalho. Na
	// trava, o toque de destravar pode estar seguro ainda.
	esperarModificadoresSoltosMac(2 * time.Second)

	var err error
	comPool(func() {
		pb := objc.ID(objc.GetClass("NSPasteboard")).Send(objc.RegisterName("generalPasteboard"))
		if pb == 0 {
			err = errors.New("NSPasteboard indisponível")
			return
		}
		tipoTexto := nsString("public.utf8-plain-text")
		anterior := pb.Send(objc.RegisterName("stringForType:"), tipoTexto)
		if anterior != 0 {
			// Copia: o objeto do pasteboard pode ser invalidado por clearContents.
			anterior = anterior.Send(objc.RegisterName("copy"))
			defer anterior.Send(objc.RegisterName("release"))
		}
		if err = gravarPasteboard(pb, texto, true); err != nil {
			return
		}
		enviarCommandV()
		time.Sleep(esperaColarMac)
		// Só o texto anterior é restaurado; imagem/arquivo não voltam.
		if anterior != 0 {
			pb.Send(objc.RegisterName("clearContents"))
			pb.Send(objc.RegisterName("setString:forType:"), anterior, tipoTexto)
		}
	})
	return err
}

// gravarPasteboard põe o texto; transitorio marca o item com os tipos do
// nspasteboard.org para que gerenciadores de histórico o ignorem.
func gravarPasteboard(pb objc.ID, texto string, transitorio bool) error {
	pb.Send(objc.RegisterName("clearContents"))
	ok := objc.Send[bool](pb, objc.RegisterName("setString:forType:"), nsString(texto), nsString("public.utf8-plain-text"))
	if !ok {
		return errors.New("NSPasteboard recusou o texto")
	}
	if transitorio {
		vazio := nsString("")
		pb.Send(objc.RegisterName("setString:forType:"), vazio, nsString("org.nspasteboard.TransientType"))
		pb.Send(objc.RegisterName("setString:forType:"), vazio, nsString("org.nspasteboard.ConcealedType"))
	}
	return nil
}

func enviarCommandV() {
	postarTecla(vkMacCommand, true, cgFlagCommand)
	postarTecla(vkMacV, true, cgFlagCommand)
	postarTecla(vkMacV, false, cgFlagCommand)
	postarTecla(vkMacCommand, false, 0)
}

// postarTecla injeta uma tecla marcada (o tap do Teclado ignora a marca).
func postarTecla(tecla uint16, desce bool, flags uint64) {
	ev := cgCreateKeyboardEvent(0, tecla, desce)
	if ev == 0 {
		return
	}
	defer cgEventRelease(ev)
	cgEventSetFlags(ev, flags)
	cgEventSetInt(ev, cgEventSourceUserData, macEventTag)
	cgEventPost(cgPostEventToSession, ev)
}

// Digitar insere o texto no campo com foco como se fosse digitado: blocos de
// UTF-16 (acentos e emojis incluídos) e \n vira Return.
func Digitar(texto string) error {
	if texto == "" {
		return nil
	}
	if err := permissaoPostar(); err != nil {
		return err
	}
	for _, parte := range partesDigitacao(texto) {
		if parte == nil {
			postarTecla(vkMacReturn, true, 0)
			postarTecla(vkMacReturn, false, 0)
			continue
		}
		if err := postarUnicode(parte); err != nil {
			return err
		}
	}
	return nil
}

// partesDigitacao corta o texto em blocos de até maxUnicodePorEvento unidades
// UTF-16, sem partir pares substitutos; nil marca um Return. \r é ignorado.
func partesDigitacao(texto string) [][]uint16 {
	var partes [][]uint16
	for i, linha := range strings.Split(strings.ReplaceAll(texto, "\r", ""), "\n") {
		if i > 0 {
			partes = append(partes, nil)
		}
		u := utf16.Encode([]rune(linha))
		for len(u) > 0 {
			n := min(len(u), maxUnicodePorEvento)
			if n < len(u) && utf16.IsSurrogate(rune(u[n-1])) && u[n-1] < 0xDC00 {
				n-- // não separa um par substituto (emoji) entre dois eventos
			}
			partes = append(partes, u[:n])
			u = u[n:]
		}
	}
	return partes
}

func postarUnicode(u []uint16) error {
	for _, desce := range []bool{true, false} {
		ev := cgCreateKeyboardEvent(0, 0, desce)
		if ev == 0 {
			return errors.New("CGEventCreateKeyboardEvent falhou")
		}
		cgKeyboardSetUnicode(ev, uint64(len(u)), &u[0])
		cgEventSetFlags(ev, 0)
		cgEventSetInt(ev, cgEventSourceUserData, macEventTag)
		cgEventPost(cgPostEventToSession, ev)
		cgEventRelease(ev)
	}
	runtime.KeepAlive(u)
	return nil
}

func esperarModificadoresSoltosMac(teto time.Duration) {
	limite := time.Now().Add(teto)
	teclas := []uint16{vkMacControl, vkMacOption, vkMacShift, vkMacCommand,
		vkMacRightCtrl, vkMacRightOpt, vkMacRightShift, vkMacRightCmd}
	for time.Now().Before(limite) {
		apertada := false
		for _, k := range teclas {
			if cgEventSourceKeyState(cgStateHID, k) {
				apertada = true
				break
			}
		}
		if !apertada {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
}
