//go:build darwin

package plataforma

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// Indicador é a borda azul pulsante, sem clique, ao redor de cada tela
// enquanto grava. Dono do ciclo de vida: a thread principal. Start roda nela
// antes de Teclado.Rodar (que mantém o NSApplication rodando) e Close depois.
// SetActive vem de qualquer goroutine e só publica o estado; um timer no run
// loop principal aplica o estado mais recente (coalesce naturalmente).
type Indicador struct {
	mu      sync.Mutex
	state   indicatorLifecycle
	active  atomic.Bool
	id      uintptr
	windows []objc.ID // só na thread principal
	visible bool      // só na thread principal
	timer   uintptr   // CFRunLoopTimerRef; só na thread principal
	started time.Time
}

type indicatorLifecycle uint8

const (
	indicatorNew indicatorLifecycle = iota
	indicatorRunning
	indicatorClosed
)

const (
	macEdgeThickness     = 7
	macEdgeTimerInterval = 45 * time.Millisecond
	macWindowLevelStatus = 25 // NSStatusWindowLevel
	macBackingBuffered   = 2
	macPolicyAccessory   = 1
	// Em todos os Spaces, parado no Exposé, por cima de apps em tela cheia.
	macWindowBehavior = 1<<0 | 1<<4 | 1<<8
)

var (
	indicatorInitOnce sync.Once
	indicatorInitErr  error
	indicatorIDs      atomic.Uintptr
	indicadores       sync.Map // ID -> *Indicador; o timer recebe o ID, nunca ponteiro Go
	indicatorTimerCB  uintptr

	cfRunLoopGetMainInd      func() uintptr
	cfRunLoopAddTimer        func(uintptr, uintptr, uintptr)
	cfRunLoopTimerCreate     func(uintptr, float64, float64, uint64, int64, uintptr, *cfRunLoopTimerContext) uintptr
	cfRunLoopTimerInvalidate func(uintptr)
	cfRunLoopWakeUp          func(uintptr)
	cfAbsoluteTimeGetCurrent func() float64
	cfAllocatorDefault       uintptr
	cfRunLoopCommonModes     uintptr
)

// cfRunLoopTimerContext espelha CFRunLoopTimerContext; info leva o ID.
type cfRunLoopTimerContext struct {
	Version         int64
	Info            uintptr
	Retain          uintptr
	Release         uintptr
	CopyDescription uintptr
}

func initIndicatorRuntime() error {
	indicatorInitOnce.Do(func() {
		if err := loadDarwinFrameworks(appKitPath); err != nil {
			indicatorInitErr = err
			return
		}
		for _, e := range []struct {
			name string
			fn   any
		}{
			{"CFRunLoopGetMain", &cfRunLoopGetMainInd},
			{"CFRunLoopAddTimer", &cfRunLoopAddTimer},
			{"CFRunLoopTimerCreate", &cfRunLoopTimerCreate},
			{"CFRunLoopTimerInvalidate", &cfRunLoopTimerInvalidate},
			{"CFRunLoopWakeUp", &cfRunLoopWakeUp},
			{"CFAbsoluteTimeGetCurrent", &cfAbsoluteTimeGetCurrent},
		} {
			if err := registerDarwinSymbol(coreFoundationPath, e.name, e.fn); err != nil {
				indicatorInitErr = err
				return
			}
		}
		var err error
		if cfAllocatorDefault, err = darwinGlobalPointer(coreFoundationPath, "kCFAllocatorDefault"); err != nil {
			indicatorInitErr = err
			return
		}
		if cfRunLoopCommonModes, err = darwinGlobalPointer(coreFoundationPath, "kCFRunLoopCommonModes"); err != nil {
			indicatorInitErr = err
			return
		}
		indicatorTimerCB = purego.NewCallback(func(_ uintptr, info uintptr) {
			if v, ok := indicadores.Load(info); ok {
				v.(*Indicador).applyActive()
			}
		})
	})
	return indicatorInitErr
}

// NewIndicador constrói o indicador; chame Start antes de SetActive. Close
// pode ser chamado repetidamente.
func NewIndicador() *Indicador { return &Indicador{} }

// Start cria as janelas de forma síncrona na thread principal, antes de o
// run loop começar. Não bloqueia esperando o run loop nem usa dispatch_sync.
func (i *Indicador) Start() error {
	if !isDarwinMainThread() {
		return errors.New("Indicador.Start deve rodar na thread principal do macOS")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	switch i.state {
	case indicatorRunning:
		return nil
	case indicatorClosed:
		return errors.New("indicador já foi fechado")
	}
	if err := initIndicatorRuntime(); err != nil {
		return err
	}
	var err error
	comPool(func() {
		app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
		// Accessory: sem ícone no Dock e sem roubar o foco do app em uso.
		app.Send(objc.RegisterName("setActivationPolicy:"), int64(macPolicyAccessory))
		err = i.createWindows()
	})
	if err != nil {
		i.destroyWindows()
		return err
	}
	i.id = indicatorIDs.Add(1)
	indicadores.Store(i.id, i)
	ctx := cfRunLoopTimerContext{Info: i.id}
	interval := macEdgeTimerInterval.Seconds()
	i.timer = cfRunLoopTimerCreate(cfAllocatorDefault, cfAbsoluteTimeGetCurrent()+interval, interval, 0, 0, indicatorTimerCB, &ctx)
	if i.timer == 0 {
		indicadores.Delete(i.id)
		i.destroyWindows()
		return errors.New("CFRunLoopTimerCreate falhou")
	}
	cfRunLoopAddTimer(cfRunLoopGetMainInd(), i.timer, cfRunLoopCommonModes)
	i.started = time.Now()
	i.state = indicatorRunning
	return nil
}

// SetActive mostra/esconde a borda. Não bloqueia: só publica o estado e
// acorda o run loop principal; o timer aplica em até ~45 ms.
func (i *Indicador) SetActive(active bool) {
	i.active.Store(active)
	i.mu.Lock()
	running := i.state == indicatorRunning
	i.mu.Unlock()
	if running {
		cfRunLoopWakeUp(cfRunLoopGetMainInd())
	}
}

// Close esconde e destrói as janelas. Deve rodar na thread principal (main.go
// o chama com defer, depois que Teclado.Rodar volta).
func (i *Indicador) Close() {
	i.mu.Lock()
	defer i.mu.Unlock()
	switch i.state {
	case indicatorClosed:
		return
	case indicatorNew:
		i.state = indicatorClosed
		return
	}
	if !isDarwinMainThread() {
		return // AppKit só na thread principal; o processo está saindo de todo modo
	}
	i.state = indicatorClosed
	indicadores.Delete(i.id)
	if i.timer != 0 {
		cfRunLoopTimerInvalidate(i.timer)
		cfReleaseRef(i.timer)
		i.timer = 0
	}
	comPool(i.destroyWindows)
}

func (i *Indicador) createWindows() error {
	screens := objc.ID(objc.GetClass("NSScreen")).Send(objc.RegisterName("screens"))
	count := objc.Send[uint64](screens, objc.RegisterName("count"))
	if count == 0 {
		return errors.New("o macOS não informou nenhuma tela ativa")
	}
	azul := objc.ID(objc.GetClass("NSColor")).Send(objc.RegisterName("colorWithSRGBRed:green:blue:alpha:"),
		35.0/255, 139.0/255, 1.0, 0.75)
	for n := uint64(0); n < count; n++ {
		screen := screens.Send(objc.RegisterName("objectAtIndex:"), n)
		frame := objc.Send[macRect](screen, objc.RegisterName("frame"))
		for _, rect := range edgeRectsMac(frame) {
			// Um alloc por janela: init em objeto já iniciado é comportamento indefinido.
			w := objc.ID(objc.GetClass("NSWindow")).Send(objc.RegisterName("alloc"))
			w = w.Send(objc.RegisterName("initWithContentRect:styleMask:backing:defer:"),
				rect, uint64(0), uint64(macBackingBuffered), false)
			if w == 0 {
				return errors.New("NSWindow initWithContentRect falhou")
			}
			w.Send(objc.RegisterName("setReleasedWhenClosed:"), false)
			w.Send(objc.RegisterName("setOpaque:"), false)
			w.Send(objc.RegisterName("setBackgroundColor:"), azul)
			w.Send(objc.RegisterName("setHasShadow:"), false)
			w.Send(objc.RegisterName("setIgnoresMouseEvents:"), true)
			w.Send(objc.RegisterName("setLevel:"), int64(macWindowLevelStatus))
			w.Send(objc.RegisterName("setCollectionBehavior:"), uint64(macWindowBehavior))
			w.Send(objc.RegisterName("setAlphaValue:"), 0.0)
			i.windows = append(i.windows, w)
		}
	}
	if len(i.windows) == 0 {
		return errors.New("nenhuma tela tinha dimensões utilizáveis")
	}
	return nil
}

func (i *Indicador) destroyWindows() {
	for _, w := range i.windows {
		w.Send(objc.RegisterName("orderOut:"), objc.ID(0))
		w.Send(objc.RegisterName("close"))
		w.Send(objc.RegisterName("release"))
	}
	i.windows, i.visible = nil, false
}

// applyActive roda no timer da thread principal, sempre — inclusive para
// esconder quando SetActive(false) veio de outra goroutine.
func (i *Indicador) applyActive() {
	active := i.active.Load()
	if !active && !i.visible {
		return
	}
	comPool(func() {
		if !active {
			for _, w := range i.windows {
				w.Send(objc.RegisterName("orderOut:"), objc.ID(0))
			}
			i.visible = false
			return
		}
		alpha := float64(pulseOpacityMac(time.Since(i.started))) / 255
		for _, w := range i.windows {
			w.Send(objc.RegisterName("setAlphaValue:"), alpha)
			if !i.visible {
				w.Send(objc.RegisterName("orderFrontRegardless"))
			}
		}
		i.visible = true
	})
}

type macPoint struct{ X, Y float64 }
type macSize struct{ Width, Height float64 }
type macRect struct {
	Origin macPoint
	Size   macSize
}

// edgeRectsMac devolve as quatro faixas (topo, base, esquerda, direita) de uma
// tela, em coordenadas Cocoa (origem embaixo à esquerda), com a espessura
// limitada ao tamanho da tela. Tela vazia não gera faixas.
func edgeRectsMac(f macRect) []macRect {
	w, h := f.Size.Width, f.Size.Height
	if w <= 0 || h <= 0 {
		return nil
	}
	tx, ty := math.Min(macEdgeThickness, w), math.Min(macEdgeThickness, h)
	x, y := f.Origin.X, f.Origin.Y
	return []macRect{
		{macPoint{x, y + h - ty}, macSize{w, ty}}, // topo
		{macPoint{x, y}, macSize{w, ty}},          // base
		{macPoint{x, y}, macSize{tx, h}},          // esquerda
		{macPoint{x + w - tx, y}, macSize{tx, h}}, // direita
	}
}

// pulseOpacityMac é o mesmo pulso do Windows (pulseOpacity), duplicado até
// a função ir para um arquivo comum.
func pulseOpacityMac(elapsed time.Duration) byte {
	const cycle = 2200 * time.Millisecond
	phase := 2 * math.Pi * float64(elapsed%cycle) / float64(cycle)
	return byte(58 + 25*math.Sin(phase))
}
