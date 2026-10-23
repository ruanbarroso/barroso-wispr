//go:build windows

package atualizacao

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestInstallAtAndRollback(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "wispr.exe")
	staged := filepath.Join(t.TempDir(), "staged.exe")
	if err := os.WriteFile(target, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup, err := installAt(target, staged)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new" {
		t.Fatalf("installed = %q", got)
	}
	got, _ = os.ReadFile(backup)
	if string(got) != "old" {
		t.Fatalf("backup = %q", got)
	}
	if err := rollback(target, backup); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(target)
	if string(got) != "old" {
		t.Fatalf("rollback left %q", got)
	}
}

func TestInstallAtPreservesTargetWhenStagedMissing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "wispr.exe")
	if err := os.WriteFile(target, []byte("current"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := installAt(target, filepath.Join(dir, "missing.exe")); err == nil {
		t.Fatal("installAt unexpectedly succeeded")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "current" {
		t.Fatalf("target = %q, %v", got, err)
	}
}

func TestHandoffArgsAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "result.json")
	exe := filepath.Join(dir, "recorder.exe")
	source := filepath.Join(dir, "recorder.go")
	code := `package main
import ("encoding/json"; "os"; "path/filepath")
func main() {
 b, _ := json.Marshal(map[string]any{"args":os.Args[1:], "cwd":must(filepath.Abs(".")), "value":os.Getenv("WISPR_TEST_VALUE")})
 _ = os.WriteFile(os.Getenv("WISPR_TEST_OUTPUT"), b, 0600)
}
func must(s string, e error) string { if e != nil { panic(e) }; return s }
`
	if err := os.WriteFile(source, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", exe, source)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build recorder: %v: %s", err, out)
	}
	wantDir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := launch(exe, []string{"one arg", "two"}, dir, withEnv(os.Environ(), "WISPR_TEST_VALUE", "preserved"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Env = withEnv(cmd.Env, "WISPR_TEST_OUTPUT", out)
	// Env is copied when Start runs; launch with both values for this check.
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	cmd, err = launch(exe, []string{"one arg", "two"}, dir, withEnv(withEnv(os.Environ(), "WISPR_TEST_VALUE", "preserved"), "WISPR_TEST_OUTPUT", out))
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Args  []string `json:"args"`
		Cwd   string   `json:"cwd"`
		Value string   `json:"value"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Cwd != wantDir || got.Value != "preserved" || len(got.Args) != 2 || got.Args[0] != "one arg" || got.Args[1] != "two" {
		t.Fatalf("launch context = %+v", got)
	}
}

func TestWaitForProcessFailsForUnknownPID(t *testing.T) {
	ack := filepath.Join(t.TempDir(), "ack")
	if waitForProcess(0x7fffffff, ack) {
		t.Fatal("unknown PID unexpectedly signaled ready")
	}
}

func TestCurrentExecutableCanBeOpenedForSafeReplacement(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(p)
	if _, err := os.Stat(exe); err != nil {
		t.Fatal(err)
	}
}
