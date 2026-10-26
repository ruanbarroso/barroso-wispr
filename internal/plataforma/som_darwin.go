//go:build darwin

package plataforma

import (
	"encoding/binary"
	"errors"
	"time"

	"github.com/ebitengine/purego/objc"
)

// playPCM toca PCM s16le mono pelo NSSound (WAV em memória) e espera o fim,
// para os mesmos tons suaves do Windows soarem iguais no Mac.
func playPCM(pcm []byte, sampleRate int) error {
	if len(pcm) == 0 || len(pcm)%2 != 0 || sampleRate <= 0 {
		return errors.New("invalid PCM tone buffer")
	}
	if err := loadDarwinFrameworks(appKitPath); err != nil {
		return err
	}
	wav := wavMono16(pcm, sampleRate)
	duracao := time.Duration(len(pcm)/2) * time.Second / time.Duration(sampleRate)
	var err error
	comPool(func() {
		dados := objc.ID(objc.GetClass("NSData")).Send(objc.RegisterName("dataWithBytes:length:"), &wav[0], uint64(len(wav)))
		if dados == 0 {
			err = errors.New("NSData indisponível")
			return
		}
		som := objc.ID(objc.GetClass("NSSound")).Send(objc.RegisterName("alloc")).Send(objc.RegisterName("initWithData:"), dados)
		if som == 0 {
			err = errors.New("NSSound recusou o WAV")
			return
		}
		defer som.Send(objc.RegisterName("release"))
		if !objc.Send[bool](som, objc.RegisterName("play")) {
			err = errors.New("NSSound não tocou")
			return
		}
		// O NSSound toca de forma assíncrona; segura a referência até o fim.
		time.Sleep(duracao + 80*time.Millisecond)
	})
	return err
}

func wavMono16(pcm []byte, taxa int) []byte {
	out := make([]byte, 44, 44+len(pcm))
	copy(out[0:], "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(36+len(pcm)))
	copy(out[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)
	binary.LittleEndian.PutUint16(out[20:], 1)
	binary.LittleEndian.PutUint16(out[22:], 1)
	binary.LittleEndian.PutUint32(out[24:], uint32(taxa))
	binary.LittleEndian.PutUint32(out[28:], uint32(taxa*2))
	binary.LittleEndian.PutUint16(out[32:], 2)
	binary.LittleEndian.PutUint16(out[34:], 16)
	copy(out[36:], "data")
	binary.LittleEndian.PutUint32(out[40:], uint32(len(pcm)))
	return append(out, pcm...)
}
