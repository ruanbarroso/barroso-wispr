package app

import (
	"encoding/binary"

	"github.com/rolandhe/go-vad"
)

const (
	bytesPorSegundo = 16000 * 2 // PCM s16le, 16 kHz, mono
	minDitadoBytes  = bytesPorSegundo
	minVozBytes     = bytesPorSegundo / 10 // 100 ms de quadros classificados como voz
	quadroVADBytes  = 320 * 2              // 20 ms a 16 kHz
	preRollMaxBytes = bytesPorSegundo * 2
)

type detectorVoz struct {
	vad   *vad.VAD
	resto []byte
}

func novoDetectorVoz() detectorVoz {
	detector := vad.New()
	_ = detector.SetSampleRate(vad.SampleRate16k)
	_ = detector.SetMode(vad.ModeQuality)
	return detectorVoz{vad: detector}
}

// processar classifica quadros completos de 20 ms e conserva o resto para a
// próxima chamada. O retorno conta apenas o PCM classificado como voz.
func (d *detectorVoz) processar(pcm []byte) (voz int) {
	buf := append(d.resto, pcm...)
	completo := len(buf) / quadroVADBytes * quadroVADBytes
	amostras := make([]int16, quadroVADBytes/2)
	for off := 0; off < completo; off += quadroVADBytes {
		for i := range amostras {
			amostras[i] = int16(binary.LittleEndian.Uint16(buf[off+i*2:]))
		}
		resultado, err := d.vad.Process(amostras)
		if err == nil && resultado == vad.ResultVoice {
			voz += quadroVADBytes
		}
	}
	d.resto = append(d.resto[:0], buf[completo:]...)
	return voz
}
