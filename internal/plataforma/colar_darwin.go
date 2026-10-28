//go:build darwin

package plataforma

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/ebitengine/purego/objc"
)

// Colar no macOS: põe o ditado no pasteboard (marcado como transitório para
// gerenciadores de histórico) e manda Command+V. Digitar (eventos Unicode do
// Quartz) é o plano B.

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

// Colar preserva a API simples para callers sem logging detalhado.
func Colar(texto string) error {
	_, err := ColarDiagnostico(texto)
	return err
}

// ColarDiagnostico registra foco, modificadores, pasteboard e eventos enviados.
// O macOS não confirma que o aplicativo de destino inseriu o texto.
func ColarDiagnostico(texto string) (d DiagnosticoColagem, err error) {
	t0 := time.Now()
	defer func() { d.Duracao = time.Since(t0) }()
	d.Etapa = "início"
	d.FocoAntes = FocoAtual()
	if texto == "" {
		d.Etapa = "texto vazio"
		return d, nil
	}
	if err = permissaoPostar(); err != nil {
		d.Etapa = "permissão para eventos de teclado"
		return d, err
	}
	if err = colarPasteboard(texto, &d); err == nil {
		return d, nil
	}
	causa := err
	d.Fallback = "Digitar: " + causa.Error()
	if permErr := permissaoPostar(); permErr != nil {
		d.Etapa = "fallback bloqueado pela permissão"
		return d, permErr
	}
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

func colarPasteboard(texto string, d *DiagnosticoColagem) error {
	if err := loadDarwinFrameworks(appKitPath); err != nil {
		d.Etapa = "abrir AppKit"
		return err
	}
	// Com modificadores ainda pressionados, Command+V pode virar outro atalho.
	d.EsperaModificadores, d.ModificadoresPendentes = esperarModificadoresSoltosMacDiagnostico(2 * time.Second)
	d.FocoDepoisDaEspera = FocoAtual()

	var err error
	comPool(func() {
		pb := objc.ID(objc.GetClass("NSPasteboard")).Send(objc.RegisterName("generalPasteboard"))
		if pb == 0 {
			d.Etapa = "obter NSPasteboard"
			err = errors.New("NSPasteboard indisponível")
			return
		}
		d.ClipboardSeqAntes = objc.Send[int64](pb, objc.RegisterName("changeCount"))
		// Não restaurar o pasteboard: os testes mostraram que isso fazia ditados
		// subsequentes serem colados vazios no Chrome.
		if err = gravarPasteboard(pb, texto, true); err != nil {
			d.Etapa = "gravar texto no pasteboard"
			return
		}
		d.ClipboardSeqDepois = objc.Send[int64](pb, objc.RegisterName("changeCount"))
		conteudo := pb.Send(objc.RegisterName("stringForType:"), nsString("public.utf8-plain-text"))
		if conteudo == 0 || !objc.Send[bool](conteudo, objc.RegisterName("isEqualToString:"), nsString(texto)) {
			d.Etapa = "verificar texto no pasteboard"
			err = errors.New("texto não foi confirmado no pasteboard")
			return
		}
		d.ClipboardVerificado = true
		d.FocoNoEnvio = FocoAtual()
		d.Etapa = "enviar Command+V"
		d.EventosPaste = enviarCommandVDiagnostico()
		if d.EventosPaste != 4 {
			err = fmt.Errorf("apenas %d/4 eventos Command+V foram criados", d.EventosPaste)
			return
		}
		time.Sleep(esperaColarMac)
		d.FocoDepois = FocoAtual()
		d.ClipboardSeqDepois = objc.Send[int64](pb, objc.RegisterName("changeCount"))
		d.Etapa = "Command+V enviado; inserção não confirmada pelo sistema"
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

func enviarCommandV() { _ = enviarCommandVDiagnostico() }

func enviarCommandVDiagnostico() int {
	eventos := 0
	for _, e := range []struct {
		tecla uint16
		desce bool
		flags uint64
	}{
		{vkMacCommand, true, cgFlagCommand},
		{vkMacV, true, cgFlagCommand},
		{vkMacV, false, cgFlagCommand},
		{vkMacCommand, false, 0},
	} {
		if postarTecla(e.tecla, e.desce, e.flags) {
			eventos++
		}
	}
	return eventos
}

// postarTecla injeta uma tecla marcada (o tap do Teclado ignora a marca).
// true significa que o evento foi criado e enviado ao Quartz, não que o app o aceitou.
func postarTecla(tecla uint16, desce bool, flags uint64) bool {
	ev := cgCreateKeyboardEvent(0, tecla, desce)
	if ev == 0 {
		return false
	}
	defer cgEventRelease(ev)
	cgEventSetFlags(ev, flags)
	cgEventSetInt(ev, cgEventSourceUserData, macEventTag)
	cgEventPost(cgPostEventToSession, ev)
	return true
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
	_, err := digitarEventos(texto)
	return err
}

func digitarEventos(texto string) (int, error) {
	eventos := 0
	for _, parte := range partesDigitacao(texto) {
		if parte == nil {
			if !postarTecla(vkMacReturn, true, 0) || !postarTecla(vkMacReturn, false, 0) {
				return eventos, errors.New("CGEventCreateKeyboardEvent falhou ao enviar Return")
			}
			eventos += 2
			continue
		}
		n, err := postarUnicode(parte)
		eventos += n
		if err != nil {
			return eventos, err
		}
	}
	return eventos, nil
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

func postarUnicode(u []uint16) (int, error) {
	eventos := 0
	for _, desce := range []bool{true, false} {
		ev := cgCreateKeyboardEvent(0, 0, desce)
		if ev == 0 {
			return eventos, errors.New("CGEventCreateKeyboardEvent falhou")
		}
		cgKeyboardSetUnicode(ev, uint64(len(u)), &u[0])
		cgEventSetFlags(ev, 0)
		cgEventSetInt(ev, cgEventSourceUserData, macEventTag)
		cgEventPost(cgPostEventToSession, ev)
		cgEventRelease(ev)
		eventos++
	}
	runtime.KeepAlive(u)
	return eventos, nil
}

func esperarModificadoresSoltosMacDiagnostico(teto time.Duration) (time.Duration, []string) {
	inicio := time.Now()
	limite := inicio.Add(teto)
	teclas := []struct {
		codigo uint16
		nome   string
	}{
		{vkMacControl, "control"}, {vkMacOption, "option"}, {vkMacShift, "shift"}, {vkMacCommand, "command"},
		{vkMacRightCtrl, "right-control"}, {vkMacRightOpt, "right-option"},
		{vkMacRightShift, "right-shift"}, {vkMacRightCmd, "right-command"},
	}
	var pendentes []string
	for {
		pendentes = pendentes[:0]
		for _, k := range teclas {
			if cgEventSourceKeyState(cgStateHID, k.codigo) {
				pendentes = append(pendentes, k.nome)
			}
		}
		if len(pendentes) == 0 || !time.Now().Before(limite) {
			return time.Since(inicio), append([]string(nil), pendentes...)
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func esperarModificadoresSoltosMac(teto time.Duration) {
	_, _ = esperarModificadoresSoltosMacDiagnostico(teto)
}
