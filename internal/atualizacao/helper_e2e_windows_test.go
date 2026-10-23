//go:build windows

package atualizacao

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// fakeApp builds a small executable that writes "<role>-started" in
// WISPR_E2E_DIR, signals the updater's readiness file when asked, and stays
// alive like the real app — except "broken", which exits at once.
const fakeAppSrc = `package main

import (
	"os"
	"path/filepath"
	"time"
)

var role = "old"

func main() {
	_ = os.WriteFile(filepath.Join(os.Getenv("WISPR_E2E_DIR"), role+"-started"), nil, 0o600)
	if role == "broken" {
		os.Exit(3)
	}
	if p := os.Getenv("BARROSO_WISPR_UPDATE_READY"); p != "" {
		_ = os.WriteFile(p, []byte("ready"), 0o600)
	}
	time.Sleep(20 * time.Second)
}
`

func buildFake(t *testing.T, dir, role string) string {
	t.Helper()
	src := filepath.Join(dir, "fake.go")
	if err := os.WriteFile(src, []byte(fakeAppSrc), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, role+".exe")
	cmd := exec.Command("go", "build", "-ldflags", "-X main.role="+role, "-o", out, src)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v: %s", role, err, b)
	}
	return out
}

func waitFile(t *testing.T, p string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(p); err == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", p)
}

// runHandoff starts the "old" app as the installed target, runs the helper
// against it with newRole staged, and stops the old app once the helper has
// acknowledged — the same order the real app follows.
func runHandoff(t *testing.T, newRole string) (installDir, marks string, code int) {
	t.Helper()
	build := t.TempDir()
	installDir, marks = t.TempDir(), t.TempDir()
	cache := t.TempDir()
	t.Setenv("WISPR_E2E_DIR", marks)

	target := filepath.Join(installDir, "wispr-e2e-target.exe")
	data, _ := os.ReadFile(buildFake(t, build, "old"))
	if err := os.WriteFile(target, data, 0o700); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(cache, "staged.exe")
	data, _ = os.ReadFile(buildFake(t, build, newRole))
	if err := os.WriteFile(staged, data, 0o600); err != nil {
		t.Fatal(err)
	}

	old := exec.Command(target)
	old.Dir = installDir
	if err := old.Start(); err != nil {
		t.Fatal(err)
	}
	waitFile(t, filepath.Join(marks, "old-started"))
	_ = os.Remove(filepath.Join(marks, "old-started"))

	cfg := handoff{
		PID: old.Process.Pid, Target: target, Staged: staged, Dir: installDir,
		Args:  []string{"keep this arg"},
		Ready: filepath.Join(cache, "ready"), Ack: filepath.Join(cache, "ack"),
	}
	raw, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(cache, "handoff.json")
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
	return installDir, marks, code
}

func killByImage(path string) {
	_ = exec.Command("taskkill", "/F", "/IM", filepath.Base(path)).Run()
}

func TestHelperInstallsAndRelaunchesNewVersion(t *testing.T) {
	installDir, marks, code := runHandoff(t, "new")
	t.Cleanup(func() { killByImage(filepath.Join(installDir, "wispr-e2e-target.exe")) })
	if code != 0 {
		t.Fatalf("helper exit = %d", code)
	}
	waitFile(t, filepath.Join(marks, "new-started"))
	if _, err := os.Stat(filepath.Join(marks, "old-started")); err == nil {
		t.Fatal("old version was relaunched after a successful update")
	}
	backups, _ := filepath.Glob(filepath.Join(installDir, "wispr-e2e-target.exe.old-*"))
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want one", backups)
	}
}

func TestHelperRollsBackWhenNewVersionFailsToStart(t *testing.T) {
	installDir, marks, code := runHandoff(t, "broken")
	t.Cleanup(func() { killByImage(filepath.Join(installDir, "wispr-e2e-target.exe")) })
	if code == 0 {
		t.Fatal("helper reported success for a version that never became ready")
	}
	waitFile(t, filepath.Join(marks, "broken-started"))
	waitFile(t, filepath.Join(marks, "old-started")) // rollback relaunched the old one
	if _, err := os.Stat(filepath.Join(installDir, "wispr-e2e-target.exe.failed")); err == nil {
		t.Fatal("failed binary left behind")
	}
}
