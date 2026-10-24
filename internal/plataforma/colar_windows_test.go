//go:build windows

package plataforma

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// Um print de tela (CF_DIB) sobrevive ao ditado: copia, põe o texto do
// ditado, restaura e confere a imagem byte a byte. Antes só texto voltava.
func TestImagemDoClipboardVoltaDepoisDoDitado(t *testing.T) {
	usuario, err := copiarClipboard()
	if err != nil {
		t.Skipf("clipboard indisponível: %v", err)
	}
	defer func() { _ = usuario.restaurar() }()

	// DIB 32x16, 32 bpp, com um padrão que não se repete.
	const w, h = 32, 16
	dib := make([]byte, 40+w*h*4)
	binary.LittleEndian.PutUint32(dib[0:], 40)
	binary.LittleEndian.PutUint32(dib[4:], w)
	binary.LittleEndian.PutUint32(dib[8:], h)
	binary.LittleEndian.PutUint16(dib[12:], 1)
	binary.LittleEndian.PutUint16(dib[14:], 32)
	for i := 40; i < len(dib); i++ {
		dib[i] = byte(i * 7)
	}
	print := copiaClipboard{itens: []itemClipboard{{formato: cfDIB, dados: dib}}}
	if err := print.restaurar(); err != nil {
		t.Fatalf("pôr a imagem no clipboard: %v", err)
	}

	antes, err := copiarClipboard()
	if err != nil {
		t.Fatal(err)
	}
	if err := gravarTexto("texto do ditado", true); err != nil {
		t.Fatal(err)
	}
	if got, _ := lerTexto(); got != "texto do ditado" {
		t.Fatalf("ditado no clipboard = %q", got)
	}
	if err := antes.restaurar(); err != nil {
		t.Fatalf("restaurar: %v", err)
	}

	depois, err := copiarClipboard()
	if err != nil {
		t.Fatal(err)
	}
	var volta []byte
	for _, it := range depois.itens {
		if it.formato == cfDIB {
			volta = it.dados
		}
	}
	if !bytes.Equal(volta[:len(dib)], dib) {
		t.Fatalf("imagem não voltou igual (len %d, want %d)", len(volta), len(dib))
	}
	if _, temTexto := lerTexto(); temTexto {
		t.Fatal("o texto do ditado ficou no clipboard junto da imagem")
	}
}

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
