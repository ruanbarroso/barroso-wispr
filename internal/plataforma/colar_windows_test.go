//go:build windows

package plataforma

import "testing"

// Ida e volta no clipboard real, com acentos e emoji (UTF-16 com surrogate).
// Restaura o que o usuário tinha no clipboard ao terminar.
func TestClipboardIdaEVolta(t *testing.T) {
	antes, tinha := lerTexto()
	defer func() {
		if tinha {
			_ = gravarTexto(antes, false)
		}
	}()
	want := "Olá, KIS! Ação concluída 🎤\nsegunda linha"
	if err := gravarTexto(want, true); err != nil {
		t.Skipf("clipboard indisponível neste ambiente: %v", err)
	}
	got, ok := lerTexto()
	if !ok || got != want {
		t.Fatalf("got %q ok=%v, want %q", got, ok, want)
	}
}
