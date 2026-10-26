//go:build darwin

package atualizacao

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ruanbarroso/barroso-wispr/internal/config"
)

// Convenções do instalador (internal/instalacao/install_darwin.go). Duplicadas
// aqui para o atualizador não depender do pacote do instalador.
const (
	bundleName       = "Barroso Wispr.app"
	bundleExecutable = "barroso-wispr"
	bundleIdentifier = "tec.barroso.wispr"
	launchAgentLabel = "tec.barroso.wispr"
	identidadeLocal  = "Barroso Wispr Local"
)

// arquivoPronto guarda, em Home/update, o caminho de prontidão para o app que
// o launchd sobe: kickstart não deixa o auxiliar passar ambiente ao processo.
func arquivoPronto() string { return filepath.Join(config.Home(), "update", "ready-path") }

// CaminhoPronto devolve o arquivo que este processo deve criar quando estiver
// pronto, se ele foi iniciado pelo auxiliar de atualização. Vem do ambiente
// (relançamento direto) ou de Home/update/ready-path (LaunchAgent); o arquivo
// é consumido na leitura e só vale um caminho .wispr-ready-* em Home/update.
func CaminhoPronto() string {
	p := os.Getenv(envPronto)
	if p != "" {
		_ = os.Unsetenv(envPronto)
	}
	if p == "" {
		f := arquivoPronto()
		if fi, err := os.Lstat(f); err == nil && fi.Mode().IsRegular() && fi.Size() <= 4096 {
			if b, err := os.ReadFile(f); err == nil {
				p = strings.TrimSpace(string(b))
			}
		}
		_ = os.Remove(f)
	}
	if p == "" || !caminhoProntoValido(p) {
		return ""
	}
	return p
}

func caminhoProntoValido(p string) bool {
	dir := filepath.Join(config.Home(), "update")
	return filepath.IsAbs(p) && filepath.Clean(p) == p && filepath.Dir(p) == filepath.Clean(dir) &&
		strings.HasPrefix(filepath.Base(p), ".wispr-ready-")
}

// validarAlvo exige .../Barroso Wispr.app/Contents/MacOS/barroso-wispr com
// bundle e executável reais (sem symlink).
func validarAlvo(target string) error {
	if !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return errors.New("executável alvo inválido")
	}
	bundle := bundlePath(target)
	if filepath.Base(bundle) != bundleName || filepath.Join(bundle, "Contents", "MacOS", bundleExecutable) != target {
		return errors.New("executável alvo fora de " + bundleName + "/Contents/MacOS/" + bundleExecutable)
	}
	fi, err := os.Lstat(bundle)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errors.New("bundle alvo inválido")
	}
	fi, err = os.Lstat(target)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New("executável alvo não é arquivo regular")
	}
	return nil
}

func bundlePath(target string) string {
	return filepath.Dir(filepath.Dir(filepath.Dir(target)))
}

// backupDir fica ao lado do bundle (mesmo volume, rename atômico) e fora dele:
// qualquer arquivo extra em Contents/MacOS invalida a assinatura do app.
func backupDir(target string) string {
	bundle := bundlePath(target)
	if filepath.Base(bundle) != bundleName {
		return filepath.Dir(target)
	}
	return filepath.Join(filepath.Dir(bundle), ".barroso-wispr-backups")
}

// prepararAuxiliar: no macOS o auxiliar roda do próprio executável instalado;
// renomear o arquivo de um processo em execução é permitido.
func prepararAuxiliar(exe, _ string) (string, func(), error) {
	return exe, func() {}, nil
}

func desacoplar(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// waitForProcess confirma que o PID existe, sinaliza o ack e espera o app
// antigo sair, sondando com kill(pid, 0).
func waitForProcess(pid int, ack string) bool {
	if pid < 1 || pid == os.Getpid() || !processoVivo(pid) {
		return false
	}
	if err := os.WriteFile(ack, []byte("ready"), 0o600); err != nil {
		return false
	}
	for processoVivo(pid) {
		time.Sleep(50 * time.Millisecond)
	}
	return true
}

func processoVivo(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// posInstalar reassina o bundle: o executável novo invalida a assinatura do
// bundle, e o macOS recusaria abrir o app.
func posInstalar(target string) error {
	if filepath.Base(bundlePath(target)) != bundleName {
		return nil
	}
	keychain, err := keychainLogin()
	if err != nil {
		return err
	}
	return assinarBundle(bundlePath(target), keychain)
}

// keychainLogin é variável para os testes não consultarem o keychain real.
var keychainLogin = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Keychains", "login.keychain-db"), nil
}

var identidadeRE = regexp.MustCompile(`(?i)\b([0-9a-f]{40})\s+"` + regexp.QuoteMeta(identidadeLocal) + `"`)

// assinarBundle usa a identidade local estável criada pelo instalador (para o
// macOS manter as permissões entre versões) e cai para ad-hoc sem ela.
func assinarBundle(bundle, keychain string) error {
	if out, err := exec.Command("/usr/bin/security", "find-identity", "-p", "codesigning", keychain).CombinedOutput(); err == nil {
		if id := identidadeAssinatura(out); id != "" {
			if exec.Command("/usr/bin/codesign", "--force", "--sign", id, "--keychain", keychain, "--identifier", bundleIdentifier, "--timestamp=none", bundle).Run() == nil {
				return nil
			}
		}
	}
	if out, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", "--identifier", bundleIdentifier, "--timestamp=none", bundle).CombinedOutput(); err != nil {
		return fmt.Errorf("codesign ad-hoc: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func identidadeAssinatura(out []byte) string {
	m := identidadeRE.FindSubmatch(out)
	if len(m) != 2 {
		return ""
	}
	return strings.ToUpper(string(m[1]))
}

// servicoLaunchd é variável para os testes não tocarem o agente real.
var servicoLaunchd = func() string {
	return "gui/" + strconv.Itoa(os.Getuid()) + "/" + launchAgentLabel
}

// relancar sobe a versão nova pelo LaunchAgent (que carrega a chave do plist);
// sem agente carregado, executa o binário direto como filho do auxiliar.
func relancar(cfg handoff) (instancia, error) {
	if err := os.MkdirAll(filepath.Dir(arquivoPronto()), 0o700); err == nil &&
		os.WriteFile(arquivoPronto(), []byte(cfg.Ready), 0o600) == nil {
		if exec.Command("/bin/launchctl", "kickstart", servicoLaunchd()).Run() == nil {
			return instancia{parar: func() {
				_ = exec.Command("/bin/launchctl", "kill", "SIGKILL", servicoLaunchd()).Run()
			}}, nil
		}
	}
	_ = os.Remove(arquivoPronto())
	cmd, err := launch(cfg.Target, cfg.Args, cfg.Dir, withEnv(os.Environ(), envPronto, cfg.Ready))
	if err != nil {
		return instancia{}, err
	}
	return instanciaDe(cmd), nil
}

func relancarAntigo(cfg handoff) {
	_ = os.Remove(arquivoPronto())
	if exec.Command("/bin/launchctl", "kickstart", servicoLaunchd()).Run() == nil {
		return
	}
	_, _ = launch(cfg.Target, cfg.Args, cfg.Dir, os.Environ())
}

func limparPronto(cfg handoff) {
	_ = os.Remove(cfg.Ready)
	_ = os.Remove(arquivoPronto())
}
