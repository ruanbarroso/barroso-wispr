package app

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/rolandhe/go-vad"
)

const (
	bytesPorSegundo = 16000 * 2 // PCM s16le, 16 kHz, mono
	minDitadoBytes  = bytesPorSegundo
	minVozBytes     = bytesPorSegundo / 5 // 200 ms de quadros classificados como voz
	quadroVADBytes  = 320 * 2             // 20 ms a 16 kHz
	preRollMaxBytes = bytesPorSegundo * 2
)

type quadroAudio struct {
	rms float64
	voz bool
}

type detectorVoz struct {
	vad     *vad.VAD
	resto   []byte
	quadros []quadroAudio // janela de 1 s para distinguir fala de ruído contínuo

	// Estatística da gravação inteira, só para o log de descarte.
	totalQuadros int
	totalVoz     int
	rmsMax       float64
}

// resumo descreve o que o microfone entregou, para diagnosticar descartes.
func (d *detectorVoz) resumo() string {
	return fmt.Sprintf("%.1fs de áudio, %d quadros de voz do VAD em %d, RMS máximo %.0f (de 32767)",
		float64(d.totalQuadros)*0.02, d.totalVoz, d.totalQuadros, d.rmsMax)
}

func novoDetectorVoz() detectorVoz {
	detector := vad.New()
	_ = detector.SetSampleRate(vad.SampleRate16k)
	_ = detector.SetMode(vad.ModeVeryAggressive)
	return detectorVoz{vad: detector}
}

// processar classifica quadros completos de 20 ms e conserva o resto para a
// próxima chamada. O VAD sozinho aceita ruído contínuo em alguns microfones;
// falaConfirmada também exige modulação do envelope característica da voz.
func (d *detectorVoz) processar(pcm []byte) {
	buf := append(d.resto, pcm...)
	completo := len(buf) / quadroVADBytes * quadroVADBytes
	amostras := make([]int16, quadroVADBytes/2)
	for off := 0; off < completo; off += quadroVADBytes {
		var energia float64
		for i := range amostras {
			amostras[i] = int16(binary.LittleEndian.Uint16(buf[off+i*2:]))
			energia += float64(amostras[i]) * float64(amostras[i])
		}
		resultado, err := d.vad.Process(amostras)
		q := quadroAudio{
			rms: math.Sqrt(energia / float64(len(amostras))),
			voz: err == nil && resultado == vad.ResultVoice,
		}
		d.totalQuadros++
		if q.voz {
			d.totalVoz++
		}
		if q.rms > d.rmsMax {
			d.rmsMax = q.rms
		}
		d.quadros = append(d.quadros, q)
		if len(d.quadros) > 1000/(20) {
			d.quadros = d.quadros[1:]
		}
	}
	d.resto = append(d.resto[:0], buf[completo:]...)
}

func (d *detectorVoz) falaConfirmada() bool {
	var n int
	var soma, quadrados float64
	for _, q := range d.quadros {
		if !q.voz {
			continue
		}
		n++
		soma += q.rms
		quadrados += q.rms * q.rms
	}
	if n*quadroVADBytes < minVozBytes || n == 0 {
		return false
	}
	media := soma / float64(n)
	if media == 0 {
		return false
	}
	cv2 := quadrados/float64(n)/(media*media) - 1
	// Exige pelo menos 25% de variação no RMS entre quadros de voz. Ruído
	// contínuo é praticamente estacionário; fala alterna fonemas e pausas.
	return cv2 >= 0.0625
}
