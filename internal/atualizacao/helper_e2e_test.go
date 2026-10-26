package atualizacao

import (
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
