package app

import (
	"math"
	"testing"
	"time"
)

func pcmSinteticoFalado(duracao time.Duration) []byte {
	amostras := int(duracao.Seconds() * 16000)
	pcm := make([]byte, amostras*2)
	for i := 0; i < amostras; i++ {
		t := float64(i) / 16000
		envelope := 0.6 + 0.4*math.Sin(2*math.Pi*3*t)
		voz := envelope * (math.Sin(2*math.Pi*130*t) + 0.5*math.Sin(2*math.Pi*260*t) + 0.25*math.Sin(2*math.Pi*520*t) + 0.15*math.Sin(2*math.Pi*1040*t))
		amostra := int16(voz * 5000)
		pcm[i*2] = byte(amostra)
		pcm[i*2+1] = byte(uint16(amostra) >> 8)
	}
	return pcm
}

func TestDetectorVozFalaSilencio(t *testing.T) {
	detector := novoDetectorVoz()
	if got := detector.processar(make([]byte, bytesPorSegundo)); got != 0 {
		t.Fatalf("silêncio classificado como voz: %d bytes", got)
	}
	detector = novoDetectorVoz()
	if got := detector.processar(pcmSinteticoFalado(time.Second)); got < minVozBytes {
		t.Fatalf("fala sintética classificada como silêncio: %d bytes de voz", got)
	}
}
