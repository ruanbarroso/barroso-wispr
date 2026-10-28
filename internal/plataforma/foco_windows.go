//go:build windows

package plataforma

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// FocoAtual identifica o processo dono da janela ativa sem ler seu título nem
// o conteúdo dos controles. O Windows pode negar consulta ao processo.
func FocoAtual() FocoSnapshot {
	f := FocoSnapshot{}
	hwnd := windows.GetForegroundWindow()
	if hwnd == 0 {
		f.MetadataError = "GetForegroundWindow retornou HWND nulo"
		return f
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err != nil {
		f.MetadataError = err.Error()
		return f
	}
	f.PID = int32(pid)
	if pid == 0 {
		f.MetadataError = "janela sem PID"
		return f
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		f.MetadataError = err.Error()
		return f
	}
	defer windows.CloseHandle(process)
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(process, 0, &buf[0], &n); err != nil {
		f.MetadataError = err.Error()
		return f
	}
	f.App = filepath.Base(windows.UTF16ToString(buf[:n]))
	return f
}
