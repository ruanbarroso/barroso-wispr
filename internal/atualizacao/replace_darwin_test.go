//go:build darwin

package atualizacao

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const infoPlistTeste = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleIdentifier</key><string>tec.barroso.wispr</string>
  <key>CFBundleExecutable</key><string>barroso-wispr</string>
  <key>CFBundlePackageType</key><string>APPL</string>
</dict></plist>
`

// isolarDarwin keeps tests away from the real LaunchAgent, keychain and Home.
func isolarDarwin(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BARROSO_WISPR_HOME", home)
	oldSvc, oldKC := servicoLaunchd, keychainLogin
	servicoLaunchd = func() string { return "gui/" + itoa(os.Getuid()) + "/tec.barroso.wispr.teste-inexistente" }
	keychainLogin = func() (string, error) { return filepath.Join(home, "inexistente.keychain-db"), nil }
	t.Cleanup(func() { servicoLaunchd, keychainLogin = oldSvc, oldKC })
	return home
}

func bundleTeste(t *testing.T, root string, exe []byte) string {
	t.Helper()
	macos := filepath.Join(root, "Barroso Wispr.app", "Contents", "MacOS")
	if err := os.MkdirAll(macos, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(macos), "Info.plist"), []byte(infoPlistTeste), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(macos, "barroso-wispr")
	if err := os.WriteFile(target, exe, 0o755); err != nil {
		t.Fatal(err)
	}
	return target
}

func TestValidarAlvoDarwin(t *testing.T) {
	root := t.TempDir()
	target := bundleTeste(t, root, []byte("x"))
	if err := validarAlvo(target); err != nil {
		t.Fatalf("valid bundle rejected: %v", err)
	}
	loose := filepath.Join(root, "barroso-wispr")
	if err := os.WriteFile(loose, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "Other.app", "Contents", "MacOS", "barroso-wispr")
	_ = os.MkdirAll(filepath.Dir(other), 0o755)
	_ = os.WriteFile(other, []byte("x"), 0o755)
	link := filepath.Join(root, "link", "Barroso Wispr.app", "Contents", "MacOS", "barroso-wispr")
	_ = os.MkdirAll(filepath.Dir(link), 0o755)
	_ = os.Symlink(target, link)
	for _, p := range []string{loose, other, link, "relative/Barroso Wispr.app/Contents/MacOS/barroso-wispr"} {
		if err := validarAlvo(p); err == nil {
			t.Errorf("validarAlvo(%q) unexpectedly succeeded", p)
		}
	}
	if got, want := backupDir(target), filepath.Join(root, ".barroso-wispr-backups"); got != want {
		t.Fatalf("backupDir = %q, want %q (outside the signed bundle)", got, want)
	}
}

func TestCaminhoProntoDarwin(t *testing.T) {
	home := isolarDarwin(t)
	update := filepath.Join(home, "update")
	if err := os.MkdirAll(update, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envPronto, "")
	ready := filepath.Join(update, ".wispr-ready-123")
	if err := os.WriteFile(arquivoPronto(), []byte(ready+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := CaminhoPronto(); got != ready {
		t.Fatalf("CaminhoPronto from file = %q, want %q", got, ready)
	}
	if _, err := os.Stat(arquivoPronto()); !os.IsNotExist(err) {
		t.Fatal("ready-path file was not consumed")
	}
	if got := CaminhoPronto(); got != "" {
		t.Fatalf("second CaminhoPronto = %q, want empty", got)
	}
	t.Setenv(envPronto, ready)
	if got := CaminhoPronto(); got != ready || os.Getenv(envPronto) != "" {
		t.Fatalf("CaminhoPronto from env = %q (env left %q)", got, os.Getenv(envPronto))
	}
	for _, bad := range []string{filepath.Join(t.TempDir(), ".wispr-ready-1"), filepath.Join(update, "evil"), "relative"} {
		if err := os.WriteFile(arquivoPronto(), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := CaminhoPronto(); got != "" {
			t.Errorf("CaminhoPronto accepted %q", bad)
		}
	}
}

func TestIdentidadeAssinatura(t *testing.T) {
	out := []byte(`  1) 0123456789ABCDEF0123456789abcdef01234567 "Barroso Wispr Local"
  2) 89ABCDEF0123456789ABCDEF0123456789ABCDEF "Other"
     2 valid identities found`)
	if got := identidadeAssinatura(out); got != "0123456789ABCDEF0123456789ABCDEF01234567" {
		t.Fatalf("identity = %q", got)
	}
	if got := identidadeAssinatura([]byte(`  1) 89ABCDEF0123456789ABCDEF0123456789ABCDEF "Other"`)); got != "" {
		t.Fatalf("foreign identity matched: %q", got)
	}
}

func TestWaitForProcessDarwin(t *testing.T) {
	ack := filepath.Join(t.TempDir(), "ack")
	if waitForProcess(0x7fffffff, ack) {
		t.Fatal("unknown PID unexpectedly signaled ready")
	}
	if _, err := os.Stat(ack); err == nil {
		t.Fatal("ack written for unknown PID")
	}
	cmd := exec.Command("/bin/sleep", "0.3")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	start := time.Now()
	if !waitForProcess(cmd.Process.Pid, ack) {
		t.Fatal("waitForProcess failed for a live child")
	}
	if _, err := os.Stat(ack); err != nil {
		t.Fatal("ack not written")
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Fatal("waitForProcess returned before the process exited")
	}
}

// runHandoffDarwin mirrors the Windows end-to-end flow with a real signed
// bundle; launchd is pointed at a missing label, so the helper falls back to
// exec with the readiness path in the environment.
func runHandoffDarwin(t *testing.T, newRole string) (target, marks string, code int) {
	t.Helper()
	home := isolarDarwin(t)
	build, root := t.TempDir(), t.TempDir()
	marks = t.TempDir()
	cache := filepath.Join(home, "update")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WISPR_E2E_DIR", marks)
	data, _ := os.ReadFile(buildFake(t, build, "old"))
	target = bundleTeste(t, root, data)
	if err := assinarBundle(bundlePath(target), filepath.Join(home, "inexistente.keychain-db")); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(cache, "wispr-update-1")
	data, _ = os.ReadFile(buildFake(t, build, newRole))
	if err := os.WriteFile(staged, data, 0o600); err != nil {
		t.Fatal(err)
	}

	old := exec.Command(target)
	old.Dir = root
	if err := old.Start(); err != nil {
		t.Fatal(err)
	}
	waitFile(t, filepath.Join(marks, "old-started"))
	_ = os.Remove(filepath.Join(marks, "old-started"))

	cfg := handoff{
		PID: old.Process.Pid, Target: target, Staged: staged, Dir: root,
		Args:  []string{"keep this arg"},
		Ready: filepath.Join(cache, ".wispr-ready-1"), Ack: filepath.Join(cache, ".wispr-helper-ack-1"),
	}
	raw, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(cache, ".wispr-handoff-1.json")
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { done <- RunHelper([]string{"-handoff", cfgPath}) }()
	waitFile(t, cfg.Ack)
	_ = old.Process.Kill()
	_, _ = old.Process.Wait()
	select {
	case code = <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("helper did not finish")
	}
	t.Cleanup(func() { _ = exec.Command("/usr/bin/pkill", "-f", target).Run() })
	return target, marks, code
}

func TestHelperInstallsAndRelaunchesNewVersionDarwin(t *testing.T) {
	target, marks, code := runHandoffDarwin(t, "new")
	if code != 0 {
		t.Fatalf("helper exit = %d", code)
	}
	waitFile(t, filepath.Join(marks, "new-started"))
	if _, err := os.Stat(filepath.Join(marks, "old-started")); err == nil {
		t.Fatal("old version was relaunched after a successful update")
	}
	backups, _ := filepath.Glob(filepath.Join(backupDir(target), "barroso-wispr.old-*"))
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want one", backups)
	}
	if out, err := exec.Command("/usr/bin/codesign", "--verify", "--strict", bundlePath(target)).CombinedOutput(); err != nil {
		t.Fatalf("updated bundle signature invalid: %v: %s", err, out)
	}
	LimparBackups(target)
	if backups, _ := filepath.Glob(filepath.Join(backupDir(target), "barroso-wispr.old-*")); len(backups) != 0 {
		t.Fatalf("LimparBackups left %v", backups)
	}
}

func TestHelperRollsBackWhenNewVersionFailsToStartDarwin(t *testing.T) {
	target, marks, code := runHandoffDarwin(t, "broken")
	if code == 0 {
		t.Fatal("helper reported success for a version that never became ready")
	}
	waitFile(t, filepath.Join(marks, "broken-started"))
	waitFile(t, filepath.Join(marks, "old-started"))
	if _, err := os.Stat(target + ".failed"); err == nil {
		t.Fatal("failed binary left behind")
	}
	if out, err := exec.Command("/usr/bin/codesign", "--verify", "--strict", bundlePath(target)).CombinedOutput(); err != nil {
		t.Fatalf("rolled-back bundle signature invalid: %v: %s", err, out)
	}
}
