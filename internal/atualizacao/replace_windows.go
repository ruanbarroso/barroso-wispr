//go:build windows

package atualizacao

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// CaminhoPronto devolve o arquivo que o app relançado pelo auxiliar cria ao
// ficar pronto, e limpa a variável para não vazar para processos filhos.
func CaminhoPronto() string {
	p := os.Getenv(envPronto)
	if p != "" {
		_ = os.Unsetenv(envPronto)
	}
	return p
}

// validarAlvo exige um .exe absoluto e existente.
func validarAlvo(target string) error {
	if !filepath.IsAbs(target) || !strings.EqualFold(filepath.Ext(target), ".exe") {
		return errors.New("executável alvo inválido")
	}
	fi, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New("executável alvo não é arquivo regular")
	}
	return nil
}

// backupDir: no Windows os backups ficam ao lado do exe.
func backupDir(target string) string { return filepath.Dir(target) }

// prepararAuxiliar copia o exe para o cache: o Windows não deixa renomear o
// arquivo de um processo em execução se o auxiliar rodar dele.
func prepararAuxiliar(exe, cache string) (string, func(), error) {
	binary, err := os.ReadFile(exe)
	if err != nil {
		return "", nil, err
	}
	helper, err := os.CreateTemp(cache, "wispr-update-helper-*.exe")
	if err != nil {
		return "", nil, err
	}
	helperPath := helper.Name()
	remove := func() { _ = os.Remove(helperPath) }
	if _, err := helper.Write(binary); err != nil {
		_ = helper.Close()
		remove()
		return "", nil, err
	}
	if err := helper.Close(); err != nil {
		remove()
		return "", nil, err
	}
	return helperPath, remove, nil
}

func desacoplar(cmd *exec.Cmd) {
	cmd.SysProcAttr = &windows.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

func waitForProcess(pid int, ack string) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	if err := os.WriteFile(ack, []byte("ready"), 0o600); err != nil {
		return false
	}
	_, err = windows.WaitForSingleObject(h, windows.INFINITE)
	return err == nil
}

func posInstalar(string) error { return nil }

// relancar sobe a versão nova como filho do auxiliar. O app relançado herda o
// ambiente do auxiliar (que por sua vez herdou o do app): nada de segredo vai
// parar em arquivo de handoff.
func relancar(cfg handoff) (instancia, error) {
	cmd, err := launch(cfg.Target, cfg.Args, cfg.Dir, withEnv(os.Environ(), envPronto, cfg.Ready))
	if err != nil {
		return instancia{}, err
	}
	return instanciaDe(cmd), nil
}

func relancarAntigo(cfg handoff) {
	_, _ = launch(cfg.Target, cfg.Args, cfg.Dir, os.Environ())
}

func limparPronto(cfg handoff) { _ = os.Remove(cfg.Ready) }
