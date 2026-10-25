//go:build windows

// Package instalacao installs Barroso Wispr machine-wide and registers its
// per-user interactive logon task. Installation requires an already elevated
// process; this package never requests elevation itself.
package instalacao

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	TaskName       = "Barroso Wispr"
	ExecutableName = "barroso-wispr.exe"
	UACMessage     = "A instalação do Barroso Wispr precisa de permissões de administrador. Abra o PowerShell com ‘Executar como administrador’ e execute novamente: barroso-wispr.exe --install."
	maxExecutable  = 256 << 20
)

// ErrElevationRequired is returned before installation performs filesystem or
// Task Scheduler mutations when the current process is not elevated.
var ErrElevationRequired = errors.New(UACMessage)

// Deps provides the platform operations so tests can exercise the installer
// without administrator privileges or touching real machine state.
type Deps struct {
	Elevated     func() (bool, error)
	Source       func() (string, error)
	ProgramFiles func() (string, error)
	CurrentUser  func() (string, error)
	Ops          Operations
}

// Operations contains the filesystem, task, and launch boundaries used by
// Install. Any zero-valued operation uses the Windows implementation.
type Operations struct {
	MkdirAll       func(string, os.FileMode) error
	CreateTemp     func(string, string) (*os.File, error)
	Open           func(string) (io.ReadCloser, error)
	Rename         func(string, string) error
	Replace        func(source, target, backup string) error
	Remove         func(string) error
	Stat           func(string) (os.FileInfo, error)
	RegisterTask   func(name, xml string) error
	UnregisterTask func(name string) error
	Launch         func(exe string, args []string, dir string) error
	// StopRunning encerra as cópias do app que rodam a partir de installDir.
	// Sem isso a versão antiga continua aberta ao lado da nova (cada ditado
	// sai duas vezes) e o backup dela fica preso, travando a instalação seguinte.
	StopRunning func(installDir string) error
}

// Install checks elevation first, then copies the running executable to
// Program Files\Barroso Wispr using a same-directory temp file and an atomic
// Windows ReplaceFile operation when replacing an existing binary. It registers
// a current-user interactive logon task, rolling back the previously-owned
// executable if task registration or initial launch fails. No configuration or
// user data directory is read, moved, or modified.
func Install(deps Deps) error {
	elevated := deps.Elevated
	if elevated == nil {
		elevated = currentProcessElevated
	}
	ok, err := elevated()
	if err != nil {
		return fmt.Errorf("check administrator permissions: %w", err)
	}
	if !ok {
		return ErrElevationRequired
	}

	sourceFn := deps.Source
	if sourceFn == nil {
		sourceFn = os.Executable
	}
	source, err := sourceFn()
	if err != nil {
		return fmt.Errorf("locate installer executable: %w", err)
	}

	programFilesFn := deps.ProgramFiles
	if programFilesFn == nil {
		programFilesFn = func() (string, error) {
			return windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
		}
	}
	programFiles, err := programFilesFn()
	if err != nil {
		return fmt.Errorf("locate Program Files: %w", err)
	}
	userFn := deps.CurrentUser
	if userFn == nil {
		userFn = currentUserSID
	}
	user, err := userFn()
	if err != nil {
		return fmt.Errorf("identify current user: %w", err)
	}
	if strings.TrimSpace(user) == "" {
		return errors.New("identify current user: empty account identity")
	}

	ops := deps.Ops.withDefaults()
	installDir := filepath.Join(programFiles, "Barroso Wispr")
	target := filepath.Join(installDir, ExecutableName)
	if err := ops.MkdirAll(installDir, 0o755); err != nil {
		return fmt.Errorf("create installation directory: %w", err)
	}
	if err := ops.StopRunning(installDir); err != nil {
		return fmt.Errorf("stop running Barroso Wispr: %w", err)
	}

	backup, hadTarget, err := atomicInstall(source, target, ops)
	if err != nil {
		return fmt.Errorf("install executable: %w", err)
	}
	rollback := func(cause error) error {
		if rerr := rollbackExecutable(target, backup, hadTarget, ops); rerr != nil {
			return fmt.Errorf("%w; executable rollback failed: %v", cause, rerr)
		}
		return cause
	}

	taskXML, err := TaskXML(target, user)
	if err != nil {
		return rollback(fmt.Errorf("build logon task: %w", err))
	}
	if err := ops.RegisterTask(TaskName, taskXML); err != nil {
		return rollback(fmt.Errorf("register logon task: %w", err))
	}
	if err := ops.Launch(target, nil, installDir); err != nil {
		if !hadTarget {
			if taskErr := ops.UnregisterTask(TaskName); taskErr != nil {
				return rollback(fmt.Errorf("launch installed app: %v; remove newly registered task: %w", err, taskErr))
			}
		}
		return rollback(fmt.Errorf("launch installed app: %w", err))
	}
	if hadTarget {
		_ = ops.Remove(backup) // best effort cleanup after installation commits
	}
	// Sobras de instalações anteriores cujo backup estava em uso na hora.
	if stale, err := filepath.Glob(target + ".install-backup*"); err == nil {
		for _, f := range stale {
			_ = ops.Remove(f)
		}
	}
	return nil
}

func (ops Operations) withDefaults() Operations {
	if ops.MkdirAll == nil {
		ops.MkdirAll = os.MkdirAll
	}
	if ops.CreateTemp == nil {
		ops.CreateTemp = os.CreateTemp
	}
	if ops.Open == nil {
		ops.Open = func(name string) (io.ReadCloser, error) { return os.Open(name) }
	}
	if ops.Rename == nil {
		ops.Rename = os.Rename
	}
	if ops.Replace == nil {
		ops.Replace = func(source, target, backup string) error {
			src, err := windows.UTF16PtrFromString(source)
			if err != nil {
				return err
			}
			dst, err := windows.UTF16PtrFromString(target)
			if err != nil {
				return err
			}
			bak, err := windows.UTF16PtrFromString(backup)
			if err != nil {
				return err
			}
			r, _, callErr := procReplaceFileW.Call(uintptr(unsafe.Pointer(dst)), uintptr(unsafe.Pointer(src)), uintptr(unsafe.Pointer(bak)), 0, 0, 0)
			if r == 0 {
				if callErr != nil {
					return callErr
				}
				return windows.GetLastError()
			}
			return nil
		}
	}
	if ops.Remove == nil {
		ops.Remove = os.Remove
	}
	if ops.Stat == nil {
		ops.Stat = os.Stat
	}
	if ops.RegisterTask == nil {
		ops.RegisterTask = registerTask
	}
	if ops.UnregisterTask == nil {
		ops.UnregisterTask = unregisterTask
	}
	if ops.Launch == nil {
		ops.Launch = launch
	}
	if ops.StopRunning == nil {
		ops.StopRunning = stopRunning
	}
	return ops
}

var procReplaceFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

func atomicInstall(source, target string, ops Operations) (backup string, hadTarget bool, err error) {
	src, err := ops.Open(source)
	if err != nil {
		return "", false, err
	}
	defer src.Close()

	tmp, err := ops.CreateTemp(filepath.Dir(target), ".barroso-wispr-install-*.exe")
	if err != nil {
		return "", false, err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = ops.Remove(tmpPath)
		}
	}()

	n, err := io.Copy(tmp, io.LimitReader(src, maxExecutable+1))
	if err != nil {
		return "", false, err
	}
	if n == 0 || n > maxExecutable {
		return "", false, fmt.Errorf("installer executable size invalid: %d", n)
	}
	if err := tmp.Sync(); err != nil {
		return "", false, err
	}
	if err := tmp.Close(); err != nil {
		return "", false, err
	}

	if _, err := ops.Stat(target); err == nil {
		hadTarget = true
		backup, err = freeBackup(target, ops)
		if err != nil {
			return "", false, err
		}
		if err := ops.Replace(tmpPath, target, backup); err != nil {
			// ReplaceFileW may have preserved the old target as backup before
			// failing. Restore it if the target disappeared; never report failure
			// while leaving the previously installed executable missing.
			if _, targetErr := ops.Stat(target); os.IsNotExist(targetErr) {
				if _, backupErr := ops.Stat(backup); backupErr == nil {
					if restoreErr := ops.Rename(backup, target); restoreErr != nil {
						return backup, true, fmt.Errorf("replace failed: %v; restore failed: %w", err, restoreErr)
					}
				}
			}
			return "", false, err
		}
	} else if os.IsNotExist(err) {
		if err := ops.Rename(tmpPath, target); err != nil {
			return "", false, err
		}
	} else {
		return "", false, err
	}
	committed = true
	return backup, hadTarget, nil
}

// freeBackup devolve um nome livre para o backup do executável atual. Um
// backup que sobrou de uma instalação anterior é de uma versão já substituída:
// é apagado; se ainda estiver em uso e não puder ser apagado, usa o próximo nome.
func freeBackup(target string, ops Operations) (string, error) {
	for i := 0; i < 100; i++ {
		name := target + ".install-backup"
		if i > 0 {
			name = fmt.Sprintf("%s-%d", name, i)
		}
		_, err := ops.Stat(name)
		if os.IsNotExist(err) {
			return name, nil
		}
		if err != nil {
			return "", err
		}
		if ops.Remove(name) == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("no free rollback file name next to %s", target)
}

func rollbackExecutable(target, backup string, hadTarget bool, ops Operations) error {
	if hadTarget {
		failed := target + ".install-failed"
		_ = ops.Remove(failed)
		if err := ops.Rename(target, failed); err != nil {
			return err
		}
		if err := ops.Rename(backup, target); err != nil {
			_ = ops.Rename(failed, target)
			return err
		}
		_ = ops.Remove(failed)
		return nil
	}
	if err := ops.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// TaskXML returns the Task Scheduler 1.2 XML for an interactive per-user logon
// task running the installed executable with highest available privileges.
func TaskXML(executable, userSID string) (string, error) {
	if strings.TrimSpace(executable) == "" || strings.TrimSpace(userSID) == "" {
		return "", errors.New("executable and user SID are required")
	}
	escape := func(s string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>Barroso Wispr interactive logon startup</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>true</Enabled><UserId>%s</UserId></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>%s</UserId><LogonType>InteractiveToken</LogonType><RunLevel>HighestAvailable</RunLevel></Principal></Principals>
  <Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><AllowHardTerminate>true</AllowHardTerminate><StartWhenAvailable>true</StartWhenAvailable><Enabled>true</Enabled><Hidden>false</Hidden><ExecutionTimeLimit>PT0S</ExecutionTimeLimit><Priority>7</Priority></Settings>
  <Actions Context="Author"><Exec><Command>%s</Command><WorkingDirectory>%s</WorkingDirectory></Exec></Actions>
</Task>`, escape(userSID), escape(userSID), escape(executable), escape(filepath.Dir(executable))), nil
}

func currentProcessElevated() (bool, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return false, err
	}
	defer token.Close()
	return token.IsElevated(), nil
}

func currentUserSID() (string, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

func registerTask(name, taskXML string) error {
	// schtasks consumes XML on stdin from a private temporary file. The XML is
	// UTF-16 as required by Task Scheduler's /XML input format.
	utf16Data := utf16.Encode([]rune(taskXML))
	data := []byte{0xFF, 0xFE}
	for _, unit := range utf16Data {
		data = append(data, byte(unit), byte(unit>>8))
	}
	f, err := os.CreateTemp("", "barroso-wispr-task-*.xml")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	cmd := exec.Command("schtasks.exe", "/Create", "/TN", name, "/XML", path, "/F")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /Create: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func unregisterTask(name string) error {
	cmd := exec.Command("schtasks.exe", "/Delete", "/TN", name, "/F")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /Delete: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// stopRunning encerra os processos (exceto este) cujo executável está em
// installDir e se chama barroso-wispr.exe ou um backup dele — a cópia antiga em
// execução continua aberta mesmo depois de renomeada para backup.
func stopRunning(installDir string) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)
	self := uint32(os.Getpid())
	dir := strings.ToLower(filepath.Clean(installDir)) + `\`
	prefix := strings.ToLower(ExecutableName)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	var errs []error
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if entry.ProcessID == self || entry.ProcessID == 0 {
			continue
		}
		name := strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, entry.ProcessID)
		if err != nil {
			continue // processo de outro lugar ou que já saiu
		}
		buf := make([]uint16, windows.MAX_LONG_PATH)
		size := uint32(len(buf))
		if windows.QueryFullProcessImageName(h, 0, &buf[0], &size) == nil {
			image := strings.ToLower(windows.UTF16ToString(buf[:size]))
			if strings.HasPrefix(image, dir) && !strings.Contains(image[len(dir):], `\`) {
				if err := windows.TerminateProcess(h, 0); err != nil {
					errs = append(errs, fmt.Errorf("pid %d: %w", entry.ProcessID, err))
				} else {
					_, _ = windows.WaitForSingleObject(h, 5000)
				}
			}
		}
		windows.CloseHandle(h)
	}
	return errors.Join(errs...)
}

// EncerrarOutrasCopias fecha as outras cópias do app que rodam da mesma pasta
// deste executável: a que subiu por último vale. Sem isso, um Stop/Start da
// tarefa de logon deixava a cópia lançada pelo auxiliar de atualização viva
// (2026-10-25: bw.13 e bw.15 rodando juntas, o atalho disparando nas duas).
// Melhor esforço: uma cópia sem elevação não consegue fechar uma elevada.
func EncerrarOutrasCopias() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return stopRunning(filepath.Dir(exe))
}

func launch(exe string, args []string, dir string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	return cmd.Start()
}
