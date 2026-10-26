//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	mbOK            = 0x00000000
	mbIconError     = 0x00000010
	mbSetForeground = 0x00010000
)

var procMessageBoxW = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")

func mostrarMensagem(titulo, mensagem string) {
	t, _ := windows.UTF16PtrFromString(titulo)
	m, _ := windows.UTF16PtrFromString(mensagem)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbOK|mbIconError|mbSetForeground)
}
