//go:build windows

package plataforma

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Cópia completa do clipboard, para o ditado não apagar o que o usuário tinha
// copiado. Antes só o texto voltava: um print de tela (CF_DIB/CF_DIBV5 de
// centenas de KB) era trocado pelo ditado e perdido.
//
// Formatos em memória global (texto, imagem DIB, arquivos copiados no
// Explorer, HTML, RTF, formatos registrados por apps) são copiados byte a
// byte. O metafile vetorial (CF_ENHMETAFILE) vai pelos bytes do metafile.
// Ficam de fora handles GDI e privados, que não são bytes copiáveis
// (CF_BITMAP, CF_PALETTE, CF_METAFILEPICT, CF_DSP*, CF_PRIVATE*, CF_GDIOBJ*)
// — o Windows volta a sintetizar CF_BITMAP a partir do CF_DIB — e os formatos
// do OLE ("DataObject", "Ole Private Data"), que apontam para o objeto COM do
// app de origem e confundiriam o paste se voltassem sem ele.

const (
	cfMetafilePict = 3
	cfPalette      = 9
	cfEnhMetafile  = 14
	cfOwnerDisplay = 0x0080
	cfDspLast      = 0x008E
	cfPrivateFirst = 0x0200
	cfGdiObjLast   = 0x03FF

	// Teto da cópia: acima disso não se guarda nada e o clipboard fica com o
	// ditado, como antes.
	maxCopiaClipboard = 128 << 20
)

var (
	procEnumClipboardFormats       = user32.NewProc("EnumClipboardFormats")
	procGetClipboardFormatNameW    = user32.NewProc("GetClipboardFormatNameW")
	procGetClipboardSequenceNumber = user32.NewProc("GetClipboardSequenceNumber")
	procGlobalSize                 = kernel32.NewProc("GlobalSize")
	procGetEnhMetaFileBits         = gdi32.NewProc("GetEnhMetaFileBits")
	procSetEnhMetaFileBits         = gdi32.NewProc("SetEnhMetaFileBits")
)

// itemClipboard é um formato copiado.
type itemClipboard struct {
	formato uint32
	dados   []byte
}

// copiaClipboard é o conteúdo inteiro do clipboard num instante.
type copiaClipboard struct {
	itens []itemClipboard
}

var errCopiaGrande = errors.New("clipboard grande demais para copiar")

// copiarClipboard lê todos os formatos copiáveis do clipboard.
func copiarClipboard() (copiaClipboard, error) {
	if err := abrirClipboard(); err != nil {
		return copiaClipboard{}, err
	}
	defer procCloseClipboard.Call()
	var c copiaClipboard
	total := 0
	for f := uintptr(0); ; {
		r, _, _ := procEnumClipboardFormats.Call(f)
		if r == 0 {
			break
		}
		f = r
		formato := uint32(r)
		if !formatoCopiavel(formato) {
			continue
		}
		h, _, _ := procGetClipboardData.Call(uintptr(formato))
		if h == 0 {
			continue
		}
		var dados []byte
		if formato == cfEnhMetafile {
			dados = bytesEnhMetafile(h)
		} else {
			dados = bytesGlobal(h)
		}
		if dados == nil {
			continue
		}
		total += len(dados)
		if total > maxCopiaClipboard {
			return copiaClipboard{}, errCopiaGrande
		}
		c.itens = append(c.itens, itemClipboard{formato: formato, dados: dados})
	}
	return c, nil
}

func formatoCopiavel(f uint32) bool {
	switch {
	case f == cfBitmap, f == cfMetafilePict, f == cfPalette:
		return false
	case f >= cfOwnerDisplay && f <= cfDspLast:
		return false
	case f >= cfPrivateFirst && f <= cfGdiObjLast:
		return false
	}
	if f >= 0xC000 {
		switch nomeFormato(f) {
		case "DataObject", "Ole Private Data":
			return false
		}
	}
	return true
}

func nomeFormato(f uint32) string {
	buf := make([]uint16, 256)
	n, _, _ := procGetClipboardFormatNameW.Call(uintptr(f), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:n])
}

func bytesGlobal(h uintptr) []byte {
	tam, _, _ := procGlobalSize.Call(h)
	if tam == 0 || tam > maxCopiaClipboard {
		return nil
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return nil
	}
	defer procGlobalUnlock.Call(h)
	dados := make([]byte, tam)
	procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&dados[0])), p, tam)
	return dados
}

func bytesEnhMetafile(h uintptr) []byte {
	tam, _, _ := procGetEnhMetaFileBits.Call(h, 0, 0)
	if tam == 0 || tam > maxCopiaClipboard {
		return nil
	}
	dados := make([]byte, tam)
	if r, _, _ := procGetEnhMetaFileBits.Call(h, tam, uintptr(unsafe.Pointer(&dados[0]))); r == 0 {
		return nil
	}
	return dados
}

// restaurar devolve ao clipboard tudo o que estava na cópia. Uma cópia de um
// clipboard vazio deixa o clipboard vazio. O conteúdo devolvido é marcado
// para não entrar de novo no histórico do Win+V (ele já estava lá).
func (c copiaClipboard) restaurar() error {
	if err := abrirClipboard(); err != nil {
		return err
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	var falhas int
	for _, it := range c.itens {
		var h uintptr
		if it.formato == cfEnhMetafile {
			h, _, _ = procSetEnhMetaFileBits.Call(uintptr(len(it.dados)), uintptr(unsafe.Pointer(&it.dados[0])))
		} else {
			h = globalCom(it.dados)
		}
		if h == 0 {
			falhas++
			continue
		}
		if r, _, _ := procSetClipboardData.Call(uintptr(it.formato), h); r == 0 {
			if it.formato != cfEnhMetafile {
				procGlobalFree.Call(h)
			}
			falhas++
		}
	}
	if len(c.itens) > 0 {
		marcarForaDoHistorico()
	}
	if falhas > 0 {
		return errors.New("alguns formatos do clipboard não voltaram")
	}
	return nil
}

func globalCom(dados []byte) uintptr {
	if len(dados) == 0 {
		return 0
	}
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(len(dados)))
	if h == 0 {
		return 0
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return 0
	}
	procRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&dados[0])), uintptr(len(dados)))
	procGlobalUnlock.Call(h)
	return h
}

// marcarForaDoHistorico põe o marcador que o Windows lê para não guardar o
// conteúdo no histórico (Win+V) nem sincronizá-lo. Chamar com o clipboard
// aberto.
func marcarForaDoHistorico() {
	nome, _ := windows.UTF16PtrFromString("ExcludeClipboardContentFromMonitorProcessing")
	if f, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(nome))); f != 0 {
		if hv, _, _ := procGlobalAlloc.Call(gmemMoveable, 4); hv != 0 {
			if r, _, _ := procSetClipboardData.Call(f, hv); r == 0 {
				procGlobalFree.Call(hv)
			}
		}
	}
}

func sequenciaClipboard() uint32 {
	r, _, _ := procGetClipboardSequenceNumber.Call()
	return uint32(r)
}
