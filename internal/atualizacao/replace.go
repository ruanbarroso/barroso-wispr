package atualizacao

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// envPronto leva ao app relançado por exec o arquivo que ele cria quando o
// atalho global está pronto.
const envPronto = "BARROSO_WISPR_UPDATE_READY"

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
	if err := validarAlvo(exe); err != nil {
		logf(log, "atualização verificada ficou staged em %s; instalação fora do formato esperado, atualize manualmente: %v", staged, err)
		return false
	}
	if probe, err := os.CreateTemp(filepath.Dir(exe), ".wispr-write-test-*"); err != nil {
		logf(log, "atualização verificada ficou staged em %s; diretório sem escrita, atualize manualmente: %v", staged, err)
		return false
	} else {
		_ = probe.Close()
		_ = os.Remove(probe.Name())
	}
	cache := filepath.Dir(staged)
	ready, err := nomeLivre(cache, ".wispr-ready-*")
	if err != nil {
		logf(log, "atualização: criar sinal de prontidão: %v", err)
		return false
	}
	ack, err := nomeLivre(cache, ".wispr-helper-ack-*")
	if err != nil {
		logf(log, "atualização: criar confirmação do auxiliar: %v", err)
		return false
	}

	cfg := handoff{PID: pid, Target: exe, Staged: staged, Dir: dir, Args: args, Ready: ready, Ack: ack}
	data, err := json.Marshal(cfg)
	if err != nil {
		return false
	}
	cfgFile, err := os.CreateTemp(cache, ".wispr-handoff-*.json")
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
	helperPath, removeHelper, err := prepararAuxiliar(exe, cache)
	if err != nil {
		_ = os.Remove(cfgPath)
		logf(log, "atualização: criar auxiliar: %v", err)
		return false
	}
	cmd := exec.Command(helperPath, "--wispr-update-helper", "-handoff", cfgPath)
	cmd.Dir = dir
	desacoplar(cmd)
	if err := cmd.Start(); err != nil {
		removeHelper()
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
			removeHelper()
			_ = os.Remove(cfgPath)
			_ = os.Remove(ack)
			logf(log, "atualização: auxiliar não confirmou inicialização; adiando")
			return false
		}
	}
}

// nomeLivre reserva um nome único no diretório e o deixa livre para quem
// for sinalizar com ele.
func nomeLivre(dir, pattern string) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return name, nil
}

// RunHelper runs from the app executable (or a copy of it) in its private
// helper mode.
func RunHelper(args []string) int {
	fs := flag.NewFlagSet("wispr-update-helper", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("handoff", "", "")
	if fs.Parse(args) != nil || *cfgPath == "" {
		return 2
	}
	if !filepath.IsAbs(*cfgPath) || filepath.Clean(*cfgPath) != *cfgPath {
		return 2
	}
	info, err := os.Lstat(*cfgPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return 2
	}
	data, err := os.ReadFile(*cfgPath)
	if err != nil {
		return 2
	}
	var cfg handoff
	if err := json.Unmarshal(data, &cfg); err != nil || validarHandoff(cfg, *cfgPath) != nil {
		return 2
	}
	if err := os.Remove(*cfgPath); err != nil {
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
		_ = posInstalar(cfg.Target)
		relancarAntigo(cfg)
		return 1
	}
	if err := posInstalar(cfg.Target); err != nil {
		if rollbackErr := rollback(cfg.Target, backup); rollbackErr == nil {
			_ = posInstalar(cfg.Target)
			relancarAntigo(cfg)
		}
		return 1
	}
	novo, err := relancar(cfg)
	if err == nil {
		err = awaitReady(novo.saiu, cfg.Ready, 30*time.Second)
		if err != nil && novo.parar != nil {
			novo.parar()
		}
	}
	limparPronto(cfg)
	if err == nil {
		return 0 // keep the backup for cleanup at the next startup
	}
	if rollbackErr := rollback(cfg.Target, backup); rollbackErr != nil {
		return 1
	}
	if err := posInstalar(cfg.Target); err != nil {
		return 1
	}
	relancarAntigo(cfg)
	return 1
}

// validarHandoff recusa handoff incompleto ou que aponte para fora do cache
// onde o próprio app o criou: staged, prontidão e ack nascem ao lado dele.
func validarHandoff(cfg handoff, cfgPath string) error {
	if cfg.PID < 1 || cfg.Target == "" || cfg.Staged == "" || cfg.Dir == "" || cfg.Ready == "" || cfg.Ack == "" {
		return errors.New("handoff incompleto")
	}
	for _, p := range []string{cfg.Target, cfg.Staged, cfg.Ready, cfg.Ack} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return fmt.Errorf("caminho de handoff inválido: %q", p)
		}
	}
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		return err
	}
	cache := filepath.Dir(abs)
	for _, p := range []string{cfg.Staged, cfg.Ready, cfg.Ack} {
		if filepath.Dir(p) != cache {
			return fmt.Errorf("caminho de handoff fora do cache: %q", p)
		}
	}
	return validarAlvo(cfg.Target)
}

// instancia é o app relançado pelo auxiliar. saiu é nil quando o processo
// não é filho do auxiliar (LaunchAgent) e só a prontidão pode ser observada.
type instancia struct {
	saiu  <-chan error
	parar func()
}

func instanciaDe(cmd *exec.Cmd) instancia {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return instancia{saiu: done, parar: func() {
		// The wait result may already have been consumed by awaitReady; the
		// goroutine always reaps the process, so stop must not receive again.
		_ = cmd.Process.Kill()
	}}
}

func installAt(target, staged string) (string, error) {
	local, err := os.CreateTemp(filepath.Dir(target), ".wispr-new-*"+filepath.Ext(target))
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
	if err := local.Chmod(0o755); err != nil {
		return "", err
	}
	if err := local.Sync(); err != nil {
		return "", err
	}
	if err := local.Close(); err != nil {
		return "", err
	}
	dir := backupDir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	var backup string
	for i := 1; i < 1000; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s.old-%d", filepath.Base(target), i))
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
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

// awaitReady espera o app novo criar o arquivo de prontidão. exited pode ser
// nil quando o processo não é filho do auxiliar.
func awaitReady(exited <-chan error, ready string, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-exited:
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

// LimparBackups remove os backups numerados do executável (chamado em um
// startup posterior, quando o exe antigo já não está mais em execução).
func LimparBackups(exe string) {
	matches, _ := filepath.Glob(filepath.Join(backupDir(exe), filepath.Base(exe)+".old-*"))
	for _, p := range matches {
		_ = os.Remove(p)
	}
}
