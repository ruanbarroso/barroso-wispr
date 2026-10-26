//go:build darwin

package plataforma

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	TaxaAmostras = 16000
	BlocoBytes   = TaxaAmostras * 2 / 10 // bloco de 100 ms, PCM s16le mono

	audioQueueBuffers = 4
	pcmFormatLinear   = 0x6c70636d // 'lpcm'
	pcmSignedInteger  = 1 << 2
	pcmPacked         = 1 << 3
)

type audioStreamBasicDescription struct {
	SampleRate       float64
	FormatID         uint32
	FormatFlags      uint32
	BytesPerPacket   uint32
	FramesPerPacket  uint32
	BytesPerFrame    uint32
	ChannelsPerFrame uint32
	BitsPerChannel   uint32
	Reserved         uint32
}

type audioQueueBuffer struct {
	AudioDataBytesCapacity uint32
	AudioData              uintptr
	AudioDataByteSize      uint32
	UserData               uintptr
	PacketDescriptionCap   uint32
	PacketDescriptions     uintptr
	PacketDescriptionCount uint32
}

var (
	audioQueueInitOnce sync.Once
	audioQueueInitErr  error
	audioQueueID       atomic.Uintptr
	audioQueueMicros   sync.Map // ID numérico -> *capturaAudio; nunca passa ponteiro Go ao C

	audioQueueNewInput      func(*audioStreamBasicDescription, uintptr, uintptr, uintptr, uintptr, uint32, *uintptr) int32
	audioQueueAllocate      func(uintptr, uint32, *uintptr) int32
	audioQueueEnqueue       func(uintptr, uintptr, uint32, uintptr) int32
	audioQueueStart         func(uintptr, uintptr) int32
	audioQueueStop          func(uintptr, uint8) int32
	audioQueueDispose       func(uintptr, uint8) int32
	audioQueueInputCallback uintptr
)

func initAudioQueue() error {
	audioQueueInitOnce.Do(func() {
		if err := registerDarwinSymbol(audioToolboxPath, "AudioQueueNewInput", &audioQueueNewInput); err != nil {
			audioQueueInitErr = err
			return
		}
		if err := registerDarwinSymbol(audioToolboxPath, "AudioQueueAllocateBuffer", &audioQueueAllocate); err != nil {
			audioQueueInitErr = err
			return
		}
		if err := registerDarwinSymbol(audioToolboxPath, "AudioQueueEnqueueBuffer", &audioQueueEnqueue); err != nil {
			audioQueueInitErr = err
			return
		}
		if err := registerDarwinSymbol(audioToolboxPath, "AudioQueueStart", &audioQueueStart); err != nil {
			audioQueueInitErr = err
			return
		}
		if err := registerDarwinSymbol(audioToolboxPath, "AudioQueueStop", &audioQueueStop); err != nil {
			audioQueueInitErr = err
			return
		}
		if err := registerDarwinSymbol(audioToolboxPath, "AudioQueueDispose", &audioQueueDispose); err != nil {
			audioQueueInitErr = err
			return
		}
		audioQueueInputCallback = purego.NewCallback(func(context, queue, buffer, _ uintptr, _ uint32, _ uintptr) {
			value, ok := audioQueueMicros.Load(context)
			if !ok {
				return
			}
			value.(*capturaAudio).capturado(queue, buffer)
		})
	})
	return audioQueueInitErr
}

// Microfone captura áudio PCM s16le mono a 16 kHz com AudioQueue. O callback
// nativo copia cada buffer e o encaminha por uma fila para não bloquear o
// thread de áudio. O contexto nativo é um ID numérico, nunca um ponteiro Go.
type Microfone struct {
	mu     sync.Mutex
	sessao *capturaAudio
}

type capturaAudio struct {
	id      uintptr
	queue   uintptr
	active  atomic.Bool
	bloco   func([]byte)
	captura chan []byte
	parar   chan struct{}
	fim     chan struct{}
}

// Iniciar abre o dispositivo padrão. Os blocos entregues ao callback têm
// aproximadamente 100 ms; Parar pode ser chamado mesmo após erro de abertura.
func (m *Microfone) Iniciar(bloco func([]byte)) error {
	if bloco == nil {
		return errors.New("callback de áudio nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessao != nil {
		return errors.New("microfone já está gravando")
	}
	if err := initAudioQueue(); err != nil {
		return err
	}

	format := audioStreamBasicDescription{
		SampleRate:       TaxaAmostras,
		FormatID:         pcmFormatLinear,
		FormatFlags:      pcmSignedInteger | pcmPacked,
		BytesPerPacket:   2,
		FramesPerPacket:  1,
		BytesPerFrame:    2,
		ChannelsPerFrame: 1,
		BitsPerChannel:   16,
	}
	s := &capturaAudio{
		id:      audioQueueID.Add(1),
		bloco:   bloco,
		captura: make(chan []byte, 16),
		parar:   make(chan struct{}),
		fim:     make(chan struct{}),
	}
	s.active.Store(true)
	var queue uintptr
	status := audioQueueNewInput(&format, audioQueueInputCallback, s.id, 0, 0, 0, &queue)
	runtime.KeepAlive(&format)
	if status != 0 || queue == 0 {
		s.active.Store(false)
		audioQueueMicros.Delete(s.id)
		if status == 0 {
			return errors.New("AudioQueueNewInput retornou uma fila nula")
		}
		return fmt.Errorf("AudioQueueNewInput (confira Privacidade e Segurança > Microfone): OSStatus %d", status)
	}
	s.queue = queue
	audioQueueMicros.Store(s.id, s)

	for n := 0; n < audioQueueBuffers; n++ {
		var buffer uintptr
		if status = audioQueueAllocate(queue, BlocoBytes, &buffer); status != 0 || buffer == 0 {
			s.descartar()
			if status == 0 {
				return fmt.Errorf("AudioQueueAllocateBuffer retornou buffer nulo (%d)", n)
			}
			return fmt.Errorf("AudioQueueAllocateBuffer: OSStatus %d", status)
		}
		if status = audioQueueEnqueue(queue, buffer, 0, 0); status != 0 {
			s.descartar()
			return fmt.Errorf("AudioQueueEnqueueBuffer: OSStatus %d", status)
		}
	}
	go s.entregar()
	if status = audioQueueStart(queue, 0); status != 0 {
		s.encerrar()
		return fmt.Errorf("AudioQueueStart (confira o dispositivo de entrada e a permissão de Microfone): OSStatus %d", status)
	}
	m.sessao = s
	return nil
}

func (s *capturaAudio) capturado(queue, buffer uintptr) {
	if buffer == 0 || !s.active.Load() {
		return
	}
	native := (*audioQueueBuffer)(cPointer(buffer))
	length := int(native.AudioDataByteSize)
	if length > int(native.AudioDataBytesCapacity) {
		length = int(native.AudioDataBytesCapacity)
	}
	var pcm []byte
	if length > 0 && native.AudioData != 0 {
		pcm = append([]byte(nil), unsafe.Slice((*byte)(cPointer(native.AudioData)), length)...)
	}
	// Devolve o buffer antes de qualquer trabalho do app: o callback roda no
	// thread de áudio e nunca pode esperar rede ou locks do app.
	if !s.active.Load() {
		return
	}
	_ = audioQueueEnqueue(queue, buffer, 0, 0)
	if len(pcm) == 0 {
		return
	}
	select {
	case s.captura <- pcm:
	default: // backpressure: perder 100 ms é melhor que travar o áudio
	}
}

func (s *capturaAudio) entregar() {
	defer close(s.fim)
	for {
		select {
		case <-s.parar:
			return
		case pcm := <-s.captura:
			select {
			case <-s.parar:
				return
			default:
			}
			s.bloco(pcm)
		}
	}
}

// encerrar para a fila, espera a goroutine de entrega (até 1 s) e libera a
// fila. AudioQueueDispose síncrono garante que nenhum callback roda depois.
func (s *capturaAudio) encerrar() {
	s.active.Store(false)
	close(s.parar)
	_ = audioQueueStop(s.queue, 1)
	select {
	case <-s.fim:
	case <-time.After(time.Second):
	}
	s.descartar()
}

func (s *capturaAudio) descartar() {
	s.active.Store(false)
	if s.queue != 0 {
		_ = audioQueueStop(s.queue, 1)
		_ = audioQueueDispose(s.queue, 1) // também libera os buffers da fila
		s.queue = 0
	}
	audioQueueMicros.Delete(s.id)
}

// Parar encerra a captura e pode ser chamado repetidamente. O último bloco
// parcial (<100 ms) é descartado, como no Windows.
func (m *Microfone) Parar() {
	m.mu.Lock()
	s := m.sessao
	m.sessao = nil
	m.mu.Unlock()
	if s != nil {
		s.encerrar()
	}
}
