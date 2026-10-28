//go:build darwin

package main

import (
	"os/exec"
	"time"

	"github.com/ruanbarroso/barroso-wispr/internal/plataforma"
)

func simularAtalho(fala string) error {
	return plataforma.SimularAtalho(func() {
		time.Sleep(2500 * time.Millisecond)
		_ = exec.Command("say", "-v", "Luciana", fala).Run()
		time.Sleep(500 * time.Millisecond)
	})
}
