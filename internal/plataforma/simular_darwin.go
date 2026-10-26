//go:build darwin

package plataforma

import (
	"sync"
	"time"

	"github.com/ebitengine/purego/objc"
)

var (
	setTypeOnce    sync.Once
	cgEventSetType func(uintptr, uint32)
)

// SimularAtalho (diagnóstico) aperta Ctrl+Option esquerdos sem a marca do
// app, para o tap do Teclado tratar como se fosse o usuário; roda durante e
// solta.
func SimularAtalho(durante func()) error {
	if err := permissaoPostar(); err != nil {
		return err
	}
	setTypeOnce.Do(func() { _ = registerDarwinSymbol(coreGraphicsPath, "CGEventSetType", &cgEventSetType) })
	if cgEventSetType == nil {
		return errSemSetType
	}
	tecla := func(k uint16, desce bool, flags uint64) {
		ev := cgCreateKeyboardEvent(0, k, desce)
		if ev == 0 {
			return
		}
		cgEventSetType(ev, macEventTypeFlags)
		cgEventSetFlags(ev, flags)
		cgEventPost(cgPostEventToSession, ev)
		cgEventRelease(ev)
	}
	const ctrl, opt = 1<<18 | macDeviceLeftCtrl, 1<<19 | macDeviceLeftAlt
	tecla(vkMacControl, true, ctrl)
	tecla(vkMacOption, true, ctrl|opt)
	durante()
	tecla(vkMacOption, false, ctrl)
	tecla(vkMacControl, false, 0)
	time.Sleep(50 * time.Millisecond)
	return nil
}

var errSemSetType = errorString("CGEventSetType indisponível")

type errorString string

func (e errorString) Error() string { return string(e) }

// AppEmFoco devolve o nome do app que recebe a colagem (diagnóstico no log).
func AppEmFoco() string {
	if loadDarwinFrameworks(appKitPath) != nil {
		return ""
	}
	nome := ""
	comPool(func() {
		ws := objc.ID(objc.GetClass("NSWorkspace")).Send(objc.RegisterName("sharedWorkspace"))
		if ws == 0 {
			return
		}
		app := ws.Send(objc.RegisterName("frontmostApplication"))
		if app == 0 {
			return
		}
		if s := app.Send(objc.RegisterName("localizedName")); s != 0 {
			if p := s.Send(objc.RegisterName("UTF8String")); p != 0 {
				nome = goString(uintptr(p))
			}
		}
	})
	return nome
}

func goString(p uintptr) string {
	var b []byte
	for i := uintptr(0); i < 256; i++ {
		c := *(*byte)(cPointer(p + i))
		if c == 0 {
			break
		}
		b = append(b, c)
	}
	return string(b)
}
