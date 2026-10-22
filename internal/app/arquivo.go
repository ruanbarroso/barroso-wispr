package app

import (
	"sync"
	"time"
)

// MicArquivo é um microfone que toca um PCM em tempo real (diagnóstico e
// testes): um bloco a cada 100 ms, como o waveIn entregaria.
type MicArquivo struct {
	PCM   []byte
	Bloco int

	mu    sync.Mutex
	parar chan struct{}
}

func (m *MicArquivo) Iniciar(bloco func([]byte)) error {
	m.mu.Lock()
	m.parar = make(chan struct{})
	parar := m.parar
	m.mu.Unlock()
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for off := 0; off < len(m.PCM); off += m.Bloco {
			select {
			case <-parar:
				return
			case <-t.C:
			}
			fim := min(off+m.Bloco, len(m.PCM))
			bloco(m.PCM[off:fim])
		}
	}()
	return nil
}

func (m *MicArquivo) Parar() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.parar != nil {
		close(m.parar)
		m.parar = nil
	}
}

// Simular faz o que segurar o atalho por `dur` e soltar faria.
func (a *App) Simular(dur time.Duration) {
	a.comecar()
	time.Sleep(dur)
	a.parar(true)
}
