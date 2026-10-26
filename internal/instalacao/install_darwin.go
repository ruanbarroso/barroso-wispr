//go:build darwin

// Package instalacao installs Barroso Wispr as a per-user macOS app and
// LaunchAgent. Installation never requests administrator privileges.
package instalacao

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	BundleName       = "Barroso Wispr.app"
	ExecutableName   = "barroso-wispr"
	BundleIdentifier = "tec.barroso.wispr"
	LaunchAgentLabel = "tec.barroso.wispr"
	APIKeyEnv        = "WISPR_API_KEY"
	maxExecutable    = 256 << 20
)

var (
	// ErrSudoNotAllowed prevents installation in the wrong home directory when
	// the installer is run through sudo or directly as root.
	ErrSudoNotAllowed = errors.New("não execute o instalador do Barroso Wispr com sudo ou como root")
	ErrAPIKeyRequired = errors.New("defina WISPR_API_KEY antes de instalar o Barroso Wispr")
)

// Deps provides user/environment lookups so tests do not need to use the real
// home directory, environment, or process identity.
type Deps struct {
	Source        func() (string, error)
	Home          func() (string, error)
	CurrentUID    func() (string, error)
	APIKey        func() string
	RunningAsRoot func() bool
	Ops           Operations
}

// Operations contains every filesystem and external-process boundary used by
// Install. Zero-valued operations use the macOS implementation. Tests can
// replace signing and launchctl independently from filesystem work.
type Operations struct {
	MkdirAll   func(string, os.FileMode) error
	MkdirTemp  func(string, string) (string, error)
	CreateTemp func(string, string) (*os.File, error)
	Open       func(string) (io.ReadCloser, error)
	WriteFile  func(string, []byte, os.FileMode) error
	Chmod      func(string, os.FileMode) error
	Stat       func(string) (os.FileInfo, error)
	Rename     func(string, string) error
	Swap       func(string, string) error
	Remove     func(string) error
	RemoveAll  func(string) error

	// RunCommand is used for openssl, security, codesign and launchctl.
	// Each argument is passed directly to exec.Command (no shell is involved).
	RunCommand func(name string, args ...string) ([]byte, error)
	// SignBundle signs with the local stable identity, falling back to ad-hoc.
	SignBundle func(bundlePath, loginKeychain string) error
	// Bootout ignores the expected "not loaded" result; Bootstrap returns
	// errors to the installer.
	Bootout   func(uid, label string) error
	Bootstrap func(uid, plistPath string) error
	Logf      func(format string, args ...any)
}

// Install builds a complete app bundle beside ~/Applications/Barroso Wispr.app,
// signs it, and atomically swaps it into place. It then atomically writes a
// private LaunchAgent plist and reloads that agent, which starts the app.
// Any failure after replacing existing files attempts to restore both the old
// bundle and old plist.
func Install(deps Deps) error {
	rootFn := deps.RunningAsRoot
	if rootFn == nil {
		rootFn = func() bool { return os.Geteuid() == 0 || os.Geteuid() != os.Getuid() }
	}
	if rootFn() {
		return ErrSudoNotAllowed
	}

	sourceFn := deps.Source
	if sourceFn == nil {
		sourceFn = os.Executable
	}
	source, err := sourceFn()
	if err != nil {
		return fmt.Errorf("localizar o executável do instalador: %w", err)
	}

	homeFn := deps.Home
	if homeFn == nil {
		homeFn = os.UserHomeDir
	}
	home, err := homeFn()
	if err != nil {
		return fmt.Errorf("localizar a pasta pessoal: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return errors.New("localizar a pasta pessoal: caminho vazio")
	}

	uidFn := deps.CurrentUID
	if uidFn == nil {
		uidFn = func() (string, error) { return strconv.Itoa(os.Getuid()), nil }
	}
	uid, err := uidFn()
	if err != nil {
		return fmt.Errorf("identificar o usuário atual: %w", err)
	}
	if !validUID(uid) {
		return fmt.Errorf("identificar o usuário atual: UID inválido %q", uid)
	}

	keyFn := deps.APIKey
	if keyFn == nil {
		keyFn = func() string { return os.Getenv(APIKeyEnv) }
	}
	apiKey := strings.TrimSpace(keyFn())
	if apiKey == "" {
		return ErrAPIKeyRequired
	}

	ops := deps.Ops.withDefaults()
	applications := filepath.Join(home, "Applications")
	if err := ops.MkdirAll(applications, 0o755); err != nil {
		return fmt.Errorf("criar a pasta Applications: %w", err)
	}

	targetBundle := filepath.Join(applications, BundleName)
	stageBundle, err := ops.MkdirTemp(applications, ".barroso-wispr-install-*.app")
	if err != nil {
		return fmt.Errorf("preparar o app temporário: %w", err)
	}
	bundleStageOwned := true
	defer func() {
		if bundleStageOwned {
			_ = ops.RemoveAll(stageBundle)
		}
	}()
	if err := ops.Chmod(stageBundle, 0o755); err != nil {
		return fmt.Errorf("ajustar permissões do app temporário: %w", err)
	}
	if err := buildBundle(source, stageBundle, ops); err != nil {
		return fmt.Errorf("montar o app: %w", err)
	}

	keychain := filepath.Join(home, "Library", "Keychains", "login.keychain-db")
	if err := ops.SignBundle(stageBundle, keychain); err != nil {
		return fmt.Errorf("assinar o app: %w", err)
	}

	tx := &installTx{ops: ops, uid: uid, bundlePath: targetBundle}
	tx.bundleBackup, tx.hadBundle, err = swapIntoPlace(stageBundle, targetBundle, ops)
	if err != nil {
		return fmt.Errorf("instalar o app: %w", err)
	}
	// With an existing installation the staging path now holds the previous
	// bundle (the rollback copy); on a first install it was renamed away.
	bundleStageOwned = false
	tx.bundleSwapped = true

	launchAgents := filepath.Join(home, "Library", "LaunchAgents")
	if err := ops.MkdirAll(launchAgents, 0o700); err != nil {
		return tx.rollback(fmt.Errorf("criar a pasta LaunchAgents: %w", err))
	}
	tx.plistPath = filepath.Join(launchAgents, LaunchAgentLabel+".plist")
	plistData, err := LaunchAgentPlist(filepath.Join(targetBundle, "Contents", "MacOS", ExecutableName), apiKey)
	if err != nil {
		return tx.rollback(fmt.Errorf("montar o LaunchAgent: %w", err))
	}
	stagePlist, err := stagePrivateFile(launchAgents, ".tec.barroso.wispr-install-*.plist", []byte(plistData), ops)
	if err != nil {
		return tx.rollback(fmt.Errorf("preparar o LaunchAgent: %w", err))
	}
	tx.plistBackup, tx.hadPlist, err = swapIntoPlace(stagePlist, tx.plistPath, ops)
	if err != nil {
		_ = ops.Remove(stagePlist)
		return tx.rollback(fmt.Errorf("instalar o LaunchAgent: %w", err))
	}
	tx.plistSwapped = true

	// bootout runs even on a first install; the default operation treats
	// launchctl's "not loaded" answer as success. With RunAtLoad, bootstrap
	// starts the app; opening the bundle again could launch a second process.
	tx.agentTouched = true
	if err := ops.Bootout(uid, LaunchAgentLabel); err != nil {
		return tx.rollback(fmt.Errorf("descarregar o LaunchAgent anterior: %w", err))
	}
	if err := ops.Bootstrap(uid, tx.plistPath); err != nil {
		return tx.rollback(fmt.Errorf("registrar o LaunchAgent: %w", err))
	}

	// Commit: previous versions are no longer needed. Cleanup is best effort.
	if tx.hadBundle {
		_ = ops.RemoveAll(tx.bundleBackup)
	}
	if tx.hadPlist {
		_ = ops.Remove(tx.plistBackup)
	}
	return nil
}

// installTx records what Install has replaced so rollback restores exactly
// those changes, in reverse order.
type installTx struct {
	ops Operations
	uid string

	bundlePath, bundleBackup string
	hadBundle, bundleSwapped bool

	plistPath, plistBackup string
	hadPlist, plistSwapped bool

	agentTouched bool
}

func (tx *installTx) rollback(cause error) error {
	ops := tx.ops
	var problems []error
	if tx.agentTouched {
		if err := ops.Bootout(tx.uid, LaunchAgentLabel); err != nil {
			problems = append(problems, fmt.Errorf("descarregar o LaunchAgent novo: %w", err))
		}
	}
	plistRestored := false
	if tx.plistSwapped {
		if tx.hadPlist {
			if err := ops.Swap(tx.plistBackup, tx.plistPath); err != nil {
				problems = append(problems, fmt.Errorf("restaurar o LaunchAgent anterior: %w", err))
			} else {
				plistRestored = true
				_ = ops.Remove(tx.plistBackup) // now holds the failed new plist
			}
		} else if err := ops.Remove(tx.plistPath); err != nil && !os.IsNotExist(err) {
			problems = append(problems, fmt.Errorf("remover o LaunchAgent novo: %w", err))
		}
	}
	bundleRestored := !tx.hadBundle
	if tx.bundleSwapped {
		if tx.hadBundle {
			if err := ops.Swap(tx.bundleBackup, tx.bundlePath); err != nil {
				problems = append(problems, fmt.Errorf("restaurar o app anterior: %w", err))
			} else {
				bundleRestored = true
				_ = ops.RemoveAll(tx.bundleBackup) // now holds the failed new bundle
			}
		} else if err := ops.RemoveAll(tx.bundlePath); err != nil {
			problems = append(problems, fmt.Errorf("remover o app novo: %w", err))
		}
	}
	// Reload the previous agent only when both its plist and the bundle it
	// points at were restored; otherwise it could start the failed version.
	if tx.agentTouched && plistRestored && tx.hadBundle && bundleRestored {
		if err := ops.Bootstrap(tx.uid, tx.plistPath); err != nil {
			problems = append(problems, fmt.Errorf("recarregar o LaunchAgent anterior: %w", err))
		}
	}
	if len(problems) != 0 {
		return fmt.Errorf("%w; rollback incompleto: %w", cause, errors.Join(problems...))
	}
	return cause
}

func validUID(uid string) bool {
	if uid == "" {
		return false
	}
	for _, r := range uid {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (ops Operations) withDefaults() Operations {
	if ops.MkdirAll == nil {
		ops.MkdirAll = os.MkdirAll
	}
	if ops.MkdirTemp == nil {
		ops.MkdirTemp = os.MkdirTemp
	}
	if ops.CreateTemp == nil {
		ops.CreateTemp = os.CreateTemp
	}
	if ops.Open == nil {
		ops.Open = func(path string) (io.ReadCloser, error) { return os.Open(path) }
	}
	if ops.WriteFile == nil {
		ops.WriteFile = os.WriteFile
	}
	if ops.Chmod == nil {
		ops.Chmod = os.Chmod
	}
	if ops.Stat == nil {
		ops.Stat = os.Stat
	}
	if ops.Rename == nil {
		ops.Rename = os.Rename
	}
	if ops.Swap == nil {
		ops.Swap = func(a, b string) error { return unix.RenamexNp(a, b, unix.RENAME_SWAP) }
	}
	if ops.Remove == nil {
		ops.Remove = os.Remove
	}
	if ops.RemoveAll == nil {
		ops.RemoveAll = os.RemoveAll
	}
	if ops.RunCommand == nil {
		ops.RunCommand = runCommand
	}
	if ops.Logf == nil {
		ops.Logf = log.Printf
	}
	if ops.Bootout == nil {
		ops.Bootout = func(uid, label string) error {
			domain := "gui/" + uid + "/" + label
			out, err := ops.RunCommand("/bin/launchctl", "bootout", domain)
			if err != nil && launchctlNotLoaded(out) {
				return nil
			}
			return commandError("launchctl bootout", out, err)
		}
	}
	if ops.Bootstrap == nil {
		ops.Bootstrap = func(uid, plistPath string) error {
			out, err := ops.RunCommand("/bin/launchctl", "bootstrap", "gui/"+uid, plistPath)
			return commandError("launchctl bootstrap", out, err)
		}
	}
	if ops.SignBundle == nil {
		base := ops
		ops.SignBundle = func(bundlePath, keychain string) error { return signBundle(bundlePath, keychain, base) }
	}
	return ops
}

func runCommand(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

func commandError(action string, output []byte, err error) error {
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w: %s", action, err, message)
}

func launchctlNotLoaded(output []byte) bool {
	text := strings.ToLower(string(output))
	return strings.Contains(text, "no such process") || strings.Contains(text, "could not find service") || strings.Contains(text, "service not found")
}

func buildBundle(source, bundlePath string, ops Operations) error {
	contents := filepath.Join(bundlePath, "Contents")
	macOS := filepath.Join(contents, "MacOS")
	if err := ops.MkdirAll(macOS, 0o755); err != nil {
		return err
	}

	input, err := ops.Open(source)
	if err != nil {
		return err
	}
	binary, readErr := io.ReadAll(io.LimitReader(input, maxExecutable+1))
	closeErr := input.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(binary) == 0 || len(binary) > maxExecutable {
		return fmt.Errorf("tamanho inválido do executável do instalador: %d bytes", len(binary))
	}
	executable := filepath.Join(macOS, ExecutableName)
	if err := ops.WriteFile(executable, binary, 0o755); err != nil {
		return err
	}
	if err := ops.Chmod(executable, 0o755); err != nil {
		return err
	}
	if err := ops.WriteFile(filepath.Join(contents, "Info.plist"), []byte(bundleInfoPlist()), 0o644); err != nil {
		return err
	}
	return nil
}

func bundleInfoPlist() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleIdentifier</key><string>` + BundleIdentifier + `</string>
  <key>CFBundleName</key><string>Barroso Wispr</string>
  <key>CFBundleExecutable</key><string>` + ExecutableName + `</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>LSUIElement</key><true/>
  <key>NSMicrophoneUsageDescription</key><string>O Barroso Wispr precisa do microfone para transcrever sua fala.</string>
</dict></plist>
`
}

// LaunchAgentPlist returns the per-user launchd definition. The environment
// variable contains the installing user's current key and is kept private by
// the installer's 0600 atomic-file write.
func LaunchAgentPlist(executable, apiKey string) (string, error) {
	if strings.TrimSpace(executable) == "" || strings.TrimSpace(apiKey) == "" {
		return "", errors.New("executável e WISPR_API_KEY são obrigatórios")
	}
	escape := func(value string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(value))
		return b.String()
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>` + LaunchAgentLabel + `</string>
  <key>ProgramArguments</key><array><string>` + escape(executable) + `</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><false/>
  <key>EnvironmentVariables</key><dict>
    <key>` + APIKeyEnv + `</key><string>` + escape(apiKey) + `</string>
  </dict>
</dict></plist>
`, nil
}

func stagePrivateFile(dir, pattern string, data []byte, ops Operations) (string, error) {
	file, err := ops.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	path := file.Name()
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = ops.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Chmod(0o600); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	committed = true
	return path, nil
}

// swapIntoPlace atomically replaces target. stage and target must be siblings;
// after a successful exchange stage contains the former target as rollback.
func swapIntoPlace(stage, target string, ops Operations) (backup string, hadTarget bool, err error) {
	if filepath.Clean(filepath.Dir(stage)) != filepath.Clean(filepath.Dir(target)) {
		return "", false, errors.New("staging e destino precisam estar na mesma pasta")
	}
	_, err = ops.Stat(target)
	if err == nil {
		if err := ops.Swap(stage, target); err != nil {
			return "", false, err
		}
		return stage, true, nil
	}
	if !os.IsNotExist(err) {
		return "", false, err
	}
	if err := ops.Rename(stage, target); err != nil {
		return "", false, err
	}
	return "", false, nil
}

var identityLine = regexp.MustCompile(`(?i)\b([0-9a-f]{40})\s+"Barroso Wispr Local"`)

// ensureSigningIdentity reuses or creates the stable local code-signing
// certificate in the user's login keychain. The private key and certificate
// are created only in a temporary directory, imported with codesign access,
// then removed. The self-signed identity can be untrusted by Gatekeeper; it is
// used to keep the bundle's designated requirement stable for TCC permissions.
func ensureSigningIdentity(keychain string, ops Operations) (string, error) {
	out, err := ops.RunCommand("/usr/bin/security", "find-identity", "-p", "codesigning", keychain)
	if err == nil {
		if identity := matchingIdentity(out); identity != "" {
			return identity, nil
		}
	} else {
		return "", commandError("security find-identity", out, err)
	}

	tempDir, err := ops.MkdirTemp("", "barroso-wispr-signing-*")
	if err != nil {
		return "", fmt.Errorf("criar pasta temporária para identidade: %w", err)
	}
	defer func() { _ = ops.RemoveAll(tempDir) }()
	if err := ops.Chmod(tempDir, 0o700); err != nil {
		return "", err
	}

	config := `[req]
distinguished_name=dn
x509_extensions=code_signing
prompt=no
[dn]
CN=Barroso Wispr Local
[code_signing]
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=critical,codeSigning
subjectKeyIdentifier=hash
`
	configPath := filepath.Join(tempDir, "identity.cnf")
	keyPath := filepath.Join(tempDir, "private-key.pem")
	certPath := filepath.Join(tempDir, "certificate.pem")
	p12Path := filepath.Join(tempDir, "identity.p12")
	passwordPath := filepath.Join(tempDir, "password.txt")
	if err := ops.WriteFile(configPath, []byte(config), 0o600); err != nil {
		return "", err
	}
	passwordBytes := make([]byte, 24)
	if _, err := rand.Read(passwordBytes); err != nil {
		return "", err
	}
	password := hex.EncodeToString(passwordBytes)
	if err := ops.WriteFile(passwordPath, []byte(password), 0o600); err != nil {
		return "", err
	}

	if output, err := ops.RunCommand("/usr/bin/openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", keyPath, "-out", certPath, "-days", "3650", "-config", configPath, "-sha256"); err != nil {
		return "", commandError("openssl criar identidade", output, err)
	}
	if output, err := ops.RunCommand("/usr/bin/openssl", "pkcs12", "-export", "-inkey", keyPath, "-in", certPath, "-out", p12Path, "-passout", "file:"+passwordPath, "-name", "Barroso Wispr Local", "-keypbe", "PBE-SHA1-3DES", "-certpbe", "PBE-SHA1-3DES", "-macalg", "sha1"); err != nil {
		return "", commandError("openssl empacotar identidade", output, err)
	}
	if output, err := ops.RunCommand("/usr/bin/security", "import", p12Path, "-k", keychain, "-f", "pkcs12", "-P", password, "-T", "/usr/bin/codesign"); err != nil {
		return "", commandError("security importar identidade", output, err)
	}
	out, err = ops.RunCommand("/usr/bin/security", "find-identity", "-p", "codesigning", keychain)
	if err != nil {
		return "", commandError("security confirmar identidade", out, err)
	}
	identity := matchingIdentity(out)
	if identity == "" {
		return "", errors.New("security importou o certificado, mas não encontrou a identidade de assinatura")
	}
	ops.Logf("Identidade local de assinatura Barroso Wispr Local criada no login keychain.")
	return identity, nil
}

func matchingIdentity(output []byte) string {
	match := identityLine.FindSubmatch(output)
	if len(match) != 2 {
		return ""
	}
	return strings.ToUpper(string(match[1]))
}

func signBundle(bundlePath, keychain string, ops Operations) error {
	identity, identityErr := ensureSigningIdentity(keychain, ops)
	if identityErr != nil {
		ops.Logf("Aviso: não foi possível criar/reutilizar a identidade local (%v); usando assinatura ad-hoc. As permissões do macOS podem precisar ser concedidas novamente após cada atualização.", identityErr)
	} else {
		out, err := ops.RunCommand("/usr/bin/codesign", "--force", "--sign", identity, "--keychain", keychain, "--identifier", BundleIdentifier, "--timestamp=none", bundlePath)
		if err == nil {
			return nil
		}
		ops.Logf("Aviso: a assinatura com identidade local falhou (%v); tentando assinatura ad-hoc. As permissões do macOS podem precisar ser concedidas novamente após cada atualização.", commandError("codesign identidade local", out, err))
	}
	out, err := ops.RunCommand("/usr/bin/codesign", "--force", "--sign", "-", "--identifier", BundleIdentifier, "--timestamp=none", bundlePath)
	if err != nil {
		return commandError("codesign ad-hoc", out, err)
	}
	return nil
}
