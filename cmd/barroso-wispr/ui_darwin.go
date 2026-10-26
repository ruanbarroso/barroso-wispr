//go:build darwin

package main

import (
	"os"
	"runtime"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"golang.org/x/sys/unix"
)

// O AppKit só aceita a run loop e as janelas na thread principal; main()
// roda nela enquanto esta trava estiver ativa.
func init() { runtime.LockOSThread() }

func nsString(s string) objc.ID {
	return objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("stringWithUTF8String:"), s)
}

func mostrarMensagem(titulo, mensagem string) {
	// No Terminal (ex.: --install) o erro já saiu no stderr; um alerta modal
	// só deixaria o comando parado esperando um clique.
	if _, err := unix.IoctlGetTermios(int(os.Stderr.Fd()), unix.TIOCGETA); err == nil {
		return
	}
	if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
		return
	}
	app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
	app.Send(objc.RegisterName("activateIgnoringOtherApps:"), true)
	alert := objc.ID(objc.GetClass("NSAlert")).Send(objc.RegisterName("new"))
	alert.Send(objc.RegisterName("setMessageText:"), nsString(titulo))
	alert.Send(objc.RegisterName("setInformativeText:"), nsString(mensagem))
	alert.Send(objc.RegisterName("runModal"))
	alert.Send(objc.RegisterName("release"))
}
