//go:build windows

package atualizacao

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

type handoff struct {
	PID    int      `json:"pid"`
	Target string   `json:"target"`
	Staged string   `json:"staged"`
	Dir    string   `json:"dir"`
	Args   []string `json:"args"`
	Ready  string   `json:"ready"`
	Ack    string   `json:"ack"`
}

// PrepararTroca stages an out-of-process helper and leaves installation in the
// user's cache until the app has completed its last dictation and stopped.
func PrepararTroca(staged string, pid int, args []string, dir string, log io.Writer) bool {
	exe, err := os.Executable()
	if err != nil {
		logf(log, "atualização: localizar executável: %v", err)
		return false
	}
	if probe, err := os.CreateTemp(filepath.Dir(exe), ".wispr-write-test-*"); err != nil {
		logf(log, "atualização verificada ficou staged em %s; diretório sem escrita, atualize manualmente: %v", staged, err)
		return false
	} else {
		_ = probe.Close()
		_ = os.Remove(probe.Name())
	}
	readyFile, err := os.CreateTemp(filepath.Dir(staged), ".wispr-ready-*")
	if err != nil {
		logf(log, "atualização: criar sinal de prontidão: %v", err)
		return false
	}
	ready := readyFile.Name()
	_ = readyFile.Close()
	_ = os.Remove(ready)
	ackFile, err := os.CreateTemp(filepath.Dir(staged), ".wispr-helper-ack-*")
	if err != nil {
		logf(log, "atualização: criar confirmação do auxiliar: %v", err)
		return false
	}
	ack := ackFile.Name()
	_ = ackFile.Close()
	_ = os.Remove(ack)

	cfg := handoff{PID: pid, Target: exe, Staged: staged, Dir: dir, Args: args, Ready: ready, Ack: ack}
	data, err := json.Marshal(cfg)
	if err != nil {
		return false
	}
	cfgFile, err := os.CreateTemp(filepath.Dir(staged), ".wispr-handoff-*.json")
	if err != nil {
		logf(log, "atualização: criar arquivo de handoff: %v", err)
		return false
	}
	cfgPath := cfgFile.Name()
	if _, err := cfgFile.Write(data); err != nil {
		_ = cfgFile.Close()
		_ = os.Remove(cfgPath)
		return false
	}
	if err := cfgFile.Close(); err != nil {
		_ = os.Remove(cfgPath)
		return false
	}
	binary, err := os.ReadFile(exe)
	if err != nil {
		_ = os.Remove(cfgPath)
		logf(log, "atualização: ler auxiliar: %v", err)
		return false
	}
	helper, err := os.CreateTemp(filepath.Dir(staged), "wispr-update-helper-*.exe")
	if err != nil {
		_ = os.Remove(cfgPath)
		logf(log, "atualização: criar auxiliar: %v", err)
		return false
	}
	helperPath := helper.Name()
	if _, err := helper.Write(binary); err != nil {
		_ = helper.Close()
		_ = os.Remove(helperPath)
		_ = os.Remove(cfgPath)
		return false
	}
	if err := helper.Close(); err != nil {
		_ = os.Remove(helperPath)
		_ = os.Remove(cfgPath)
		return false
	}
	cmd := exec.Command(helperPath, "--wispr-update-helper", "-handoff", cfgPath)
	cmd.Dir = dir
	cmd.SysProcAttr = &windows.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(helperPath)
		_ = os.Remove(cfgPath)
		_ = os.Remove(ack)
		logf(log, "atualização: iniciar auxiliar: %v", err)
		return false
	}
	_ = cmd.Process.Release()
	deadline := time.NewTimer(10 * time.Second)
	tick := time.NewTicker(25 * time.Millisecond)
	defer deadline.Stop()
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if _, err := os.Stat(ack); err == nil {
				_ = os.Remove(ack)
				logf(log, "atualização verificada; auxiliar pronto para handoff")
				return true
			}
		case <-deadline.C:
			_ = os.Remove(helperPath)
			_ = os.Remove(cfgPath)
			_ = os.Remove(ack)
			logf(log, "atualização: auxiliar não confirmou inicialização; adiando")
			return false
		}
	}
}

// RunHelper runs from the copied app executable in its private helper mode.
func RunHelper(args []string) int {
	fs := flag.NewFlagSet("wispr-update-helper", flag.ContinueOnError)
	fs.SetOutput(discardWriter{})
	cfgPath := fs.String("handoff", "", "")
	if fs.Parse(args) != nil || *cfgPath == "" {
		return 2
	}
	data, err := os.ReadFile(*cfgPath)
	_ = os.Remove(*cfgPath)
	if err != nil {
		return 2
	}
	var cfg handoff
	if err := json.Unmarshal(data, &cfg); err != nil || cfg.PID < 1 || cfg.Target == "" || cfg.Staged == "" || cfg.Dir == "" || cfg.Ready == "" || cfg.Ack == "" {
		return 2
	}
	if !waitForProcess(cfg.PID, cfg.Ack) {
		return 1
	}
	backup, err := installAt(cfg.Target, cfg.Staged)
	if err != nil {
		if backup != "" {
			if _, statErr := os.Stat(cfg.Target); os.IsNotExist(statErr) {
				_ = os.Rename(backup, cfg.Target)
			}
		}
		_, _ = launch(cfg.Target, cfg.Args, cfg.Dir, os.Environ())
		return 1
	}
	// O app relançado herda o ambiente do auxiliar (que por sua vez herdou o
	// do app): nada de segredo vai parar em arquivo de handoff.
	childEnv := withEnv(os.Environ(), "BARROSO_WISPR_UPDATE_READY", cfg.Ready)
	child, err := launch(cfg.Target, cfg.Args, cfg.Dir, childEnv)
	if err == nil {
		err = awaitReady(child, cfg.Ready, 30*time.Second)
		if err != nil && child.Process != nil {
			_ = child.Process.Kill()
			_, _ = child.Process.Wait()
		}
	}
	if err == nil {
		return 0 // keep the backup for cleanup at the next startup
	}
	if rollbackErr := rollback(cfg.Target, backup); rollbackErr != nil {
		return 1
	}
	_, _ = launch(cfg.Target, cfg.Args, cfg.Dir, os.Environ())
	return 1
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

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

func installAt(target, staged string) (string, error) {
	local, err := os.CreateTemp(filepath.Dir(target), ".wispr-new-*.exe")
	if err != nil {
		return "", err
	}
	localPath := local.Name()
	ok := false
	defer func() {
		_ = local.Close()
		if !ok {
			_ = os.Remove(localPath)
		}
	}()
	src, err := os.Open(staged)
	if err != nil {
		return "", err
	}
	n, copyErr := io.Copy(local, io.LimitReader(src, maxBinary+1))
	closeErr := src.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if n == 0 || n > maxBinary {
		return "", fmt.Errorf("staged binary size invalid: %d", n)
	}
	if err := local.Sync(); err != nil {
		return "", err
	}
	if err := local.Close(); err != nil {
		return "", err
	}
	var backup string
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("%s.old-%d", target, i)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			backup = candidate
			break
		}
	}
	if backup == "" {
		return "", fmt.Errorf("no free backup name for %s", target)
	}
	if err := os.Rename(target, backup); err != nil {
		return "", err
	}
	if err := os.Rename(localPath, target); err != nil {
		if rollbackErr := os.Rename(backup, target); rollbackErr != nil {
			return backup, fmt.Errorf("replace failed: %v; rollback failed: %w", err, rollbackErr)
		}
		return "", err
	}
	ok = true
	_ = os.Remove(staged)
	return backup, nil
}

func rollback(target, backup string) error {
	failed := target + ".failed"
	_ = os.Remove(failed)
	if err := os.Rename(target, failed); err != nil {
		return err
	}
	if err := os.Rename(backup, target); err != nil {
		_ = os.Rename(failed, target)
		return err
	}
	_ = os.Remove(failed)
	return nil
}

func awaitReady(cmd *exec.Cmd, ready string, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			if err == nil {
				return fmt.Errorf("new app exited before readiness signal")
			}
			return fmt.Errorf("new app failed before readiness signal: %w", err)
		case <-tick.C:
			if _, err := os.Stat(ready); err == nil {
				_ = os.Remove(ready)
				return nil
			}
		case <-timer.C:
			return fmt.Errorf("new app did not signal readiness")
		}
	}
}

func launch(exe string, args []string, dir string, env []string) (*exec.Cmd, error) {
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func withEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return append(out, prefix+value)
}
