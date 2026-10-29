package app

import (
	"encoding/binary"
	"math"
	"math/rand"
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

func pcmRuido(duracao time.Duration, amplitude int32) []byte {
	amostras := int(duracao.Seconds() * 16000)
	pcm := make([]byte, amostras*2)
	r := rand.New(rand.NewSource(1))
	for i := 0; i < amostras; i++ {
		amostra := int16(r.Int31n(2*amplitude+1) - amplitude)
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(amostra))
	}
	return pcm
}

func pcmTom(duracao time.Duration, frequencia, amplitude float64) []byte {
	amostras := int(duracao.Seconds() * 16000)
	pcm := make([]byte, amostras*2)
	for i := 0; i < amostras; i++ {
		amostra := int16(math.Sin(2*math.Pi*frequencia*float64(i)/16000) * amplitude)
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(amostra))
	}
	return pcm
}

func TestDetectorVozFalaSilencioERuido(t *testing.T) {
	for _, tc := range []struct {
		nome string
		pcm  []byte
		voz  bool
	}{
		{nome: "silêncio", pcm: make([]byte, bytesPorSegundo)},
		{nome: "ruído contínuo", pcm: pcmRuido(time.Second, 5000)},
		{nome: "zumbido contínuo", pcm: pcmTom(time.Second, 60, 4000)},
		{nome: "fala", pcm: pcmSinteticoFalado(time.Second), voz: true},
	} {
		t.Run(tc.nome, func(t *testing.T) {
			detector := novoDetectorVoz()
			detector.processar(tc.pcm)
			if got := detector.falaConfirmada(); got != tc.voz {
				t.Fatalf("falaConfirmada() = %t, want %t", got, tc.voz)
			}
		})
	}
}
