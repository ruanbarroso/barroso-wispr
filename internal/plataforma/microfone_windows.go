//go:build windows

package plataforma

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Microfone captura pelo WinMM (waveIn): o Windows já converte para PCM
// s16le 16 kHz mono, que é o formato da Live API — sem CGO, sem resample.
// WASAPI teria latência menor, mas 100 ms por bloco já basta para ditado.

var (
	winmm                   = windows.NewLazySystemDLL("winmm.dll")
	procWaveInOpen          = winmm.NewProc("waveInOpen")
	procWaveInClose         = winmm.NewProc("waveInClose")
	procWaveInPrepareHeader = winmm.NewProc("waveInPrepareHeader")
	procWaveInUnprepareHdr  = winmm.NewProc("waveInUnprepareHeader")
	procWaveInAddBuffer     = winmm.NewProc("waveInAddBuffer")
	procWaveInStart         = winmm.NewProc("waveInStart")
	procWaveInReset         = winmm.NewProc("waveInReset")
)

const (
	waveMapper    = ^uintptr(0) // WAVE_MAPPER (-1): dispositivo padrão
	callbackEvent = 0x00050000
	whdrDone      = 0x00000001
	waveFormatPCM = 1

	TaxaAmostras = 16000
	BlocoBytes   = TaxaAmostras * 2 / 10 // 100 ms de s16 mono
	nBuffers     = 4
)

type waveformatex struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	CbSize         uint16
}

type wavehdr struct {
	Data          *byte
	BufferLength  uint32
	BytesRecorded uint32
	User          uintptr
	Flags         uint32
	Loops         uint32
	Next          uintptr
	Reserved      uintptr
}

// Microfone implementa app.Microfone.
type Microfone struct {
	mu     sync.Mutex
	hwi    uintptr
	evento windows.Handle
	hdrs   [nBuffers]wavehdr
	bufs   [nBuffers][]byte
	parar  chan struct{}
	fim    chan struct{}
}

func mmErr(nome string, r uintptr) error {
	if r == 0 {
		return nil
	}
	return fmt.Errorf("%s: MMRESULT %d", nome, r)
}

// Iniciar abre o dispositivo padrão e entrega blocos de ~100 ms a `bloco`,
// numa goroutine própria, até Parar.
func (m *Microfone) Iniciar(bloco func([]byte)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hwi != 0 {
		return errors.New("microfone já está gravando")
	}
	ev, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return err
	}
	wfx := waveformatex{FormatTag: waveFormatPCM, Channels: 1, SamplesPerSec: TaxaAmostras,
		AvgBytesPerSec: TaxaAmostras * 2, BlockAlign: 2, BitsPerSample: 16}
	var hwi uintptr
	r, _, _ := procWaveInOpen.Call(uintptr(unsafe.Pointer(&hwi)), waveMapper,
		uintptr(unsafe.Pointer(&wfx)), uintptr(ev), 0, callbackEvent)
	if err := mmErr("waveInOpen (sem microfone ou acesso negado em Privacidade > Microfone?)", r); err != nil {
		windows.CloseHandle(ev)
		return err
	}
	m.hwi, m.evento = hwi, ev
	for i := range m.hdrs {
		if m.bufs[i] == nil {
			m.bufs[i] = make([]byte, BlocoBytes)
		}
		m.hdrs[i] = wavehdr{Data: &m.bufs[i][0], BufferLength: BlocoBytes}
		h := &m.hdrs[i]
		if r, _, _ := procWaveInPrepareHeader.Call(hwi, uintptr(unsafe.Pointer(h)), unsafe.Sizeof(*h)); r != 0 {
			m.fecharLocked()
			return mmErr("waveInPrepareHeader", r)
		}
		if r, _, _ := procWaveInAddBuffer.Call(hwi, uintptr(unsafe.Pointer(h)), unsafe.Sizeof(*h)); r != 0 {
			m.fecharLocked()
			return mmErr("waveInAddBuffer", r)
		}
	}
	if r, _, _ := procWaveInStart.Call(hwi); r != 0 {
		m.fecharLocked()
		return mmErr("waveInStart", r)
	}
	m.parar, m.fim = make(chan struct{}), make(chan struct{})
	go m.laco(bloco, m.parar, m.fim)
	return nil
}

func (m *Microfone) laco(bloco func([]byte), parar, fim chan struct{}) {
	defer close(fim)
	prox := 0 // os buffers voltam na ordem em que foram adicionados
	for {
		select {
		case <-parar:
			return
		default:
		}
		windows.WaitForSingleObject(m.evento, 50)
		for {
			h := &m.hdrs[prox]
			if h.Flags&whdrDone == 0 {
				break
			}
			if n := h.BytesRecorded; n > 0 {
				bloco(append([]byte(nil), m.bufs[prox][:n]...))
			}
			select {
			case <-parar:
				return
			default:
			}
			h.Flags &^= whdrDone
			h.BytesRecorded = 0
			procWaveInAddBuffer.Call(m.hwi, uintptr(unsafe.Pointer(h)), unsafe.Sizeof(*h))
			prox = (prox + 1) % nBuffers
		}
	}
}

// Parar para a captura e libera o dispositivo. O áudio parcial do último
// bloco é descartado (<100 ms).
func (m *Microfone) Parar() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hwi == 0 {
		return
	}
	close(m.parar)
	windows.SetEvent(m.evento)
	select {
	case <-m.fim:
	case <-time.After(time.Second):
	}
	m.fecharLocked()
}

func (m *Microfone) fecharLocked() {
	procWaveInReset.Call(m.hwi)
	for i := range m.hdrs {
		h := &m.hdrs[i]
		procWaveInUnprepareHdr.Call(m.hwi, uintptr(unsafe.Pointer(h)), unsafe.Sizeof(*h))
	}
	procWaveInClose.Call(m.hwi)
	windows.CloseHandle(m.evento)
	m.hwi, m.evento = 0, 0
}
