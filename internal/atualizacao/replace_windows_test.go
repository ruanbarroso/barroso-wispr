//go:build windows

package atualizacao

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

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
