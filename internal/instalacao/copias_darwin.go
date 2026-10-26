//go:build darwin

package instalacao

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// EncerrarOutrasCopias fecha as outras cópias do app que rodam do mesmo
// executável: a que subiu por último vale (mesma regra do Windows).
// Melhor esforço: erros ao sinalizar um processo são ignorados.
func EncerrarOutrasCopias() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	out, err := exec.Command("/bin/ps", "-axo", "pid=,comm=").Output()
	if err != nil {
		return err
	}
	self := os.Getpid()
	for _, linha := range strings.Split(string(out), "\n") {
		campos := strings.SplitN(strings.TrimSpace(linha), " ", 2)
		if len(campos) != 2 {
			continue
		}
		pid, err := strconv.Atoi(campos[0])
		if err != nil || pid == self {
			continue
		}
		caminho := strings.TrimSpace(campos[1])
		if r, err := filepath.EvalSymlinks(caminho); err == nil {
			caminho = r
		}
		if caminho == exe {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
	}
	return nil
}
