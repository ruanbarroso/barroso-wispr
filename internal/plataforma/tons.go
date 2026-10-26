package plataforma

// Sons de aviso (iguais em Windows e macOS): tons senoidais suaves com fade,
// gerados em PCM s16le mono e tocados por playPCM de cada plataforma.

import (
	"encoding/binary"
	"math"
	"sync"
	"time"
)

// BeepReady plays a soft two-note chime when capture starts.
func BeepReady() {
	playTones([]tone{{frequency: 523, duration: 105 * time.Millisecond, level: 0.05}, {frequency: 659, duration: 125 * time.Millisecond, level: 0.05}})
}

// BeepDone plays a soft descending chime when capture ends.
func BeepDone() {
	playTones([]tone{{frequency: 587, duration: 95 * time.Millisecond, level: 0.046}, {frequency: 494, duration: 120 * time.Millisecond, level: 0.046}})
}

// BeepErro é o aviso de que a gravação parou sozinha (a conexão Live caiu):
// três notas descendo, mais graves que o tom de fim normal.
func BeepErro() {
	playTones([]tone{{frequency: 440, duration: 110 * time.Millisecond, level: 0.05}, {frequency: 349, duration: 130 * time.Millisecond, level: 0.05}, {frequency: 262, duration: 170 * time.Millisecond, level: 0.05}})
}

// EstadoProcessando liga ou desliga um pulso discreto durante a transcrição,
// a limpeza e a colagem.
func EstadoProcessando(ativo bool) { processando.definir(ativo) }

type pulso struct {
	mu      sync.Mutex
	ativo   bool
	geracao uint64
}

var processando pulso

func (p *pulso) definir(ativo bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ativo == p.ativo {
		return
	}
	p.ativo = ativo
	p.geracao++
	if ativo {
		go p.tocar(p.geracao)
	}
}

func (p *pulso) vigente(g uint64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ativo && p.geracao == g
}

func (p *pulso) tocar(g uint64) {
	// Deixa o tom de fim terminar antes do primeiro pulso.
	for n := 0; n < 7; n++ {
		if !p.vigente(g) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	for p.vigente(g) {
		playTones([]tone{{frequency: 440, duration: 75 * time.Millisecond, level: 0.03}})
		for n := 0; n < 10 && p.vigente(g); n++ {
			time.Sleep(100 * time.Millisecond)
		}
	}
}

type tone struct {
	frequency uint32
	duration  time.Duration
	level     float64
}

const (
	toneSampleRate = 22050
	toneFade       = 18 * time.Millisecond
)

func playTones(tones []tone) {
	go func() {
		pcm := renderTones(tones, toneSampleRate)
		if len(pcm) == 0 {
			return
		}
		_ = playPCM(pcm, toneSampleRate)
	}()
}

func renderTones(tones []tone, sampleRate int) []byte {
	var pcm []byte
	for _, note := range tones {
		if note.frequency < 80 || note.frequency > 4000 || note.duration <= 0 || note.level <= 0 {
			continue
		}
		pcm = append(pcm, renderTone(note, sampleRate)...)
		pcm = append(pcm, make([]byte, sampleRate/100)...)
	}
	return pcm
}

func renderTone(note tone, sampleRate int) []byte {
	if sampleRate <= 0 || note.frequency == 0 || note.duration <= 0 || note.level <= 0 {
		return nil
	}
	count := int(time.Duration(sampleRate) * note.duration / time.Second)
	if count <= 0 {
		return nil
	}
	out := make([]byte, count*2)
	fade := int(time.Duration(sampleRate) * toneFade / time.Second)
	if fade > count/2 {
		fade = count / 2
	}
	for i := 0; i < count; i++ {
		envelope := 1.0
		if fade > 0 && i < fade {
			envelope = 0.5 - 0.5*math.Cos(math.Pi*float64(i)/float64(fade))
		}
		remaining := count - 1 - i
		if fade > 0 && remaining < fade {
			release := 0.5 - 0.5*math.Cos(math.Pi*float64(remaining)/float64(fade))
			if release < envelope {
				envelope = release
			}
		}
		phase := 2 * math.Pi * float64(note.frequency) * float64(i) / float64(sampleRate)
		sample := int16(math.Round(math.Sin(phase) * note.level * envelope * math.MaxInt16))
		binary.LittleEndian.PutUint16(out[i*2:], uint16(sample))
	}
	return out
}
