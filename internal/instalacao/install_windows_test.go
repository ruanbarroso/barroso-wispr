//go:build windows

package instalacao

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testDeps(t *testing.T) (Deps, string, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "installer.exe")
	if err := os.WriteFile(source, []byte("installer-binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	programFiles := filepath.Join(root, "Program Files")
	return Deps{
		Elevated:     func() (bool, error) { return true, nil },
		Source:       func() (string, error) { return source, nil },
		ProgramFiles: func() (string, error) { return programFiles, nil },
		CurrentUser:  func() (string, error) { return "S-1-5-21-123-456-789-1001", nil },
	}, source, programFiles
}

func TestInstallFailsFastWithoutElevation(t *testing.T) {
	deps, _, _ := testDeps(t)
	mutations := 0
	lookups := 0
	deps.Elevated = func() (bool, error) { return false, nil }
	deps.Source = func() (string, error) { lookups++; return "", nil }
	deps.ProgramFiles = func() (string, error) { lookups++; return "", nil }
	deps.CurrentUser = func() (string, error) { lookups++; return "", nil }
	deps.Ops = Operations{
		MkdirAll:     func(string, os.FileMode) error { mutations++; return nil },
		RegisterTask: func(string, string) error { mutations++; return nil },
		Launch:       func(string, []string, string) error { mutations++; return nil },
	}
	if err := Install(deps); !errors.Is(err, ErrElevationRequired) {
		t.Fatalf("Install error = %v, want elevation required", err)
	}
	if mutations != 0 || lookups != 0 {
		t.Fatalf("unelevated install did work: mutations=%d lookups=%d", mutations, lookups)
	}
	if !strings.Contains(ErrElevationRequired.Error(), "administrador") {
		t.Fatalf("elevation message is not suitable for a Portuguese MessageBox: %q", ErrElevationRequired)
	}
}

func TestInstallElevatedRegistersInteractiveTaskAndLaunches(t *testing.T) {
	deps, source, programFiles := testDeps(t)
	taskCalls, launchCalls := 0, 0
	var registered string
	deps.Ops.RegisterTask = func(name, taskXML string) error {
		taskCalls++
		if name != TaskName {
			t.Fatalf("task name = %q", name)
		}
		registered = taskXML
		return nil
	}
	deps.Ops.Launch = func(exe string, args []string, dir string) error {
		launchCalls++
		want := filepath.Join(programFiles, "Barroso Wispr", ExecutableName)
		if exe != want || dir != filepath.Dir(want) || len(args) != 0 {
			t.Fatalf("launch = (%q, %v, %q), want (%q, [], %q)", exe, args, dir, want, filepath.Dir(want))
		}
		return nil
	}
	userData := filepath.Join(t.TempDir(), "existing-user-data")
	if err := os.MkdirAll(userData, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(userData, "config.json")
	if err := os.WriteFile(config, []byte(`{"dictionary":"custom.txt"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Install(deps); err != nil {
		t.Fatal(err)
	}
	if taskCalls != 1 || launchCalls != 1 {
		t.Fatalf("task calls=%d, launch calls=%d", taskCalls, launchCalls)
	}
	target := filepath.Join(programFiles, "Barroso Wispr", ExecutableName)
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "installer-binary" {
		t.Fatalf("installed executable = %q, %v", got, err)
	}
	original, err := os.ReadFile(source)
	if err != nil || string(original) != "installer-binary" {
		t.Fatalf("source changed: %q, %v", original, err)
	}
	keptConfig, err := os.ReadFile(config)
	if err != nil || string(keptConfig) != `{"dictionary":"custom.txt"}` {
		t.Fatalf("user config changed: %q, %v", keptConfig, err)
	}
	for _, required := range []string{"<LogonTrigger>", "<UserId>S-1-5-21-123-456-789-1001</UserId>", "<LogonType>InteractiveToken</LogonType>", "<RunLevel>HighestAvailable</RunLevel>", "<Command>" + target + "</Command>", "<WorkingDirectory>" + filepath.Dir(target) + "</WorkingDirectory>"} {
		if !strings.Contains(registered, required) {
			t.Errorf("task XML missing %q", required)
		}
	}
	if strings.Contains(registered, "<Password>") || strings.Contains(registered, "<LogonType>Password") {
		t.Fatal("task XML requests a password")
	}
}

func TestInstallRollsBackOwnedExecutableWhenTaskRegistrationFails(t *testing.T) {
	deps, _, programFiles := testDeps(t)
	target := filepath.Join(programFiles, "Barroso Wispr", ExecutableName)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("previous-owned-executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	deps.Ops.RegisterTask = func(string, string) error { return errors.New("scheduler unavailable") }
	launchCalls := 0
	deps.Ops.Launch = func(string, []string, string) error { launchCalls++; return nil }
	if err := Install(deps); err == nil || !strings.Contains(err.Error(), "register logon task") {
		t.Fatalf("Install error = %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "previous-owned-executable" {
		t.Fatalf("rollback executable = %q, %v", got, err)
	}
	if launchCalls != 0 {
		t.Fatalf("launch called %d times after task registration failure", launchCalls)
	}
	if _, err := os.Stat(target + ".install-backup"); !os.IsNotExist(err) {
		t.Fatalf("backup remains after rollback: %v", err)
	}
}

func TestInstallRollsBackNewExecutableWhenInitialLaunchFails(t *testing.T) {
	deps, _, programFiles := testDeps(t)
	deps.Ops.RegisterTask = func(string, string) error { return nil }
	deps.Ops.Launch = func(string, []string, string) error { return errors.New("launch failed") }
	if err := Install(deps); err == nil || !strings.Contains(err.Error(), "launch installed app") {
		t.Fatalf("Install error = %v", err)
	}
	target := filepath.Join(programFiles, "Barroso Wispr", ExecutableName)
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("new executable remains after failed launch: %v", err)
	}
}

func TestAtomicInstallPreservesExistingTargetIfReplacementFails(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.exe")
	target := filepath.Join(dir, "target.exe")
	if err := os.WriteFile(source, []byte("new"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	ops := Operations{}
	ops.Replace = func(source, target, backup string) error {
		return errors.New("simulated atomic-swap failure")
	}
	if _, _, err := atomicInstall(source, target, ops.withDefaults()); err == nil {
		t.Fatal("atomicInstall unexpectedly succeeded")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "old" {
		t.Fatalf("target after failed replacement = %q, %v", got, err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".barroso-wispr-install-*.exe"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v, %v", matches, err)
	}
}

func TestTaskXMLRejectsEmptyIdentityAndEscapesValues(t *testing.T) {
	if _, err := TaskXML("", "S-1-5-21"); err == nil {
		t.Fatal("empty executable accepted")
	}
	xml, err := TaskXML(`C:\Program Files\Wispr & Tools\wispr.exe`, `S-1-5-21<&`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(xml, `S-1-5-21&lt;&amp;`) || !strings.Contains(xml, `Wispr &amp; Tools`) {
		t.Fatalf("XML values were not escaped: %s", xml)
	}
}
