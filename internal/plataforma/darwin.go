//go:build darwin

// Package plataforma liga o Barroso Wispr aos serviços nativos do macOS usando
// chamadas dinâmicas puras em Go (sem CGO).
package plataforma

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

const (
	coreFoundationPath = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	coreGraphicsPath   = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"
	appKitPath         = "/System/Library/Frameworks/AppKit.framework/AppKit"
	audioToolboxPath   = "/System/Library/Frameworks/AudioToolbox.framework/AudioToolbox"
	libSystemPath      = "/usr/lib/libSystem.B.dylib"
)

var (
	frameworksMu sync.Mutex
	frameworks   = make(map[string]uintptr)
)

// openDarwinFramework opens a system framework once. Calls are lazy so the
// --arquivo diagnostic path does not initialize any Cocoa or audio services.
func openDarwinFramework(path string) (uintptr, error) {
	frameworksMu.Lock()
	defer frameworksMu.Unlock()
	if h := frameworks[path]; h != 0 {
		return h, nil
	}
	h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return 0, fmt.Errorf("abrir %s: %w", path, err)
	}
	frameworks[path] = h
	return h, nil
}

func registerDarwinSymbol(path, name string, fn any) error {
	h, err := openDarwinFramework(path)
	if err != nil {
		return err
	}
	p, err := purego.Dlsym(h, name)
	if err != nil {
		return fmt.Errorf("símbolo %s em %s: %w", name, path, err)
	}
	purego.RegisterFunc(fn, p)
	return nil
}

// darwinGlobalPointer returns the pointer stored in a framework's exported
// pointer-valued global (for example kCFAllocatorDefault).
func darwinGlobalPointer(path, name string) (uintptr, error) {
	h, err := openDarwinFramework(path)
	if err != nil {
		return 0, err
	}
	p, err := purego.Dlsym(h, name)
	if err != nil {
		return 0, fmt.Errorf("símbolo %s em %s: %w", name, path, err)
	}
	return *(*uintptr)(cPointer(p)), nil
}

var (
	mainThreadOnce sync.Once
	pthreadMainNP  func() int32
)

func isDarwinMainThread() bool {
	mainThreadOnce.Do(func() {
		_ = registerDarwinSymbol(libSystemPath, "pthread_main_np", &pthreadMainNP)
	})
	return pthreadMainNP != nil && pthreadMainNP() != 0
}

// cPointer converts an address owned by C (never Go memory) into a pointer
// without tripping go vet's uintptr check.
func cPointer(addr uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&addr)) }

func loadDarwinFrameworks(paths ...string) error {
	for _, path := range paths {
		if _, err := openDarwinFramework(path); err != nil {
			return err
		}
	}
	return nil
}

var (
	cfReleaseOnce sync.Once
	cfReleaseFn   func(uintptr)
)

// cfReleaseRef libera uma referência Core Foundation que o app possui (Create).
func cfReleaseRef(ref uintptr) {
	if ref == 0 {
		return
	}
	cfReleaseOnce.Do(func() { _ = registerDarwinSymbol(coreFoundationPath, "CFRelease", &cfReleaseFn) })
	if cfReleaseFn != nil {
		cfReleaseFn(ref)
	}
}

// comPool roda f dentro de um NSAutoreleasePool: goroutines não têm pool e
// objetos autoreleased (NSString etc.) vazariam a cada ditado.
func comPool(f func()) {
	// O pool precisa ser drenado na mesma thread em que foi criado; sem isso o
	// Go pode migrar a goroutine (ex.: no time.Sleep do colar) e o app cai.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if loadDarwinFrameworks(appKitPath) != nil {
		f()
		return
	}
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(objc.RegisterName("new"))
	defer pool.Send(objc.RegisterName("drain"))
	f()
}

func nsString(s string) objc.ID {
	return objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("stringWithUTF8String:"), s)
}

// Teclado recebe a combinação Ctrl esquerdo + Option esquerdo via Quartz.
// O tap observa as teclas globalmente e ignora eventos que o próprio app gera.
type Teclado struct {
	Pressionou func()
	Soltou     func()
	OutraTecla func()
	OnReady    func()

	ctrl   atomic.Bool
	alt    atomic.Bool
	ativo  atomic.Bool
	parar  atomic.Bool
	id     uintptr
	mu     sync.Mutex
	tap    uintptr
	source uintptr
	loop   uintptr
	fila   chan func()
	fim    chan struct{}
}

const (
	macKeyLeftControl  = 0x3B
	macKeyRightControl = 0x3E
	macKeyLeftOption   = 0x3A
	macKeyRightOption  = 0x3D
	macDeviceLeftCtrl  = 0x00000001
	macDeviceLeftAlt   = 0x00000020
	macDeviceRightAlt  = 0x00000040
	macDeviceRightCtrl = 0x00002000
	macEventTypeDown   = 10
	macEventTypeUp     = 11
	macEventTypeFlags  = 12
	macTapDisabledTime = 0xFFFFFFFE
	macTapDisabledUser = 0xFFFFFFFF
	macTapSession      = 1
	macTapHeadInsert   = 0
	macTapListenOnly   = 1
	macKeyCodeField    = 9
	macUserDataField   = 42
)

var (
	tecladoInitOnce sync.Once
	tecladoInitErr  error
	tecladoIDs      atomic.Uintptr
	teclados        sync.Map // uintptr ID -> *Teclado; IDs são inteiros, não ponteiros Go
	mainQueue       uintptr

	cgEventTapCreate          func(uint32, uint32, uint32, uint64, uintptr, uintptr) uintptr
	cgEventTapEnable          func(uintptr, bool)
	cgEventGetInt             func(uintptr, uint32) int64
	cgEventGetFlags           func(uintptr) uint64
	cgPreflightListen         func() bool
	cgRequestListen           func() bool
	cfMachPortCreateRunSource func(uintptr, uintptr, int64) uintptr
	cfMachPortInvalidate      func(uintptr)
	cfRunLoopAddSourceKey     func(uintptr, uintptr, uintptr)
	cfRunLoopRemoveSourceKey  func(uintptr, uintptr, uintptr)
	cfRunLoopGetCurrentKey    func() uintptr
	cfRunLoopStop             func(uintptr)
	cfRunLoopWakeKey          func(uintptr)
	cfReleaseKey              func(uintptr)
	dispatchAsyncF            func(uintptr, uintptr, uintptr)
)

func initTecladoRuntime() error {
	tecladoInitOnce.Do(func() {
		for _, entry := range []struct {
			framework, name string
			fn              any
		}{
			{coreGraphicsPath, "CGEventTapCreate", &cgEventTapCreate},
			{coreGraphicsPath, "CGEventTapEnable", &cgEventTapEnable},
			{coreGraphicsPath, "CGEventGetIntegerValueField", &cgEventGetInt},
			{coreGraphicsPath, "CGEventGetFlags", &cgEventGetFlags},
			{coreGraphicsPath, "CGPreflightListenEventAccess", &cgPreflightListen},
			{coreGraphicsPath, "CGRequestListenEventAccess", &cgRequestListen},
			{coreFoundationPath, "CFMachPortCreateRunLoopSource", &cfMachPortCreateRunSource},
			{coreFoundationPath, "CFMachPortInvalidate", &cfMachPortInvalidate},
			{coreFoundationPath, "CFRunLoopAddSource", &cfRunLoopAddSourceKey},
			{coreFoundationPath, "CFRunLoopRemoveSource", &cfRunLoopRemoveSourceKey},
			{coreFoundationPath, "CFRunLoopGetCurrent", &cfRunLoopGetCurrentKey},
			{coreFoundationPath, "CFRunLoopStop", &cfRunLoopStop},
			{coreFoundationPath, "CFRunLoopWakeUp", &cfRunLoopWakeKey},
			{coreFoundationPath, "CFRelease", &cfReleaseKey},
			{libSystemPath, "dispatch_async_f", &dispatchAsyncF},
		} {
			if err := registerDarwinSymbol(entry.framework, entry.name, entry.fn); err != nil {
				tecladoInitErr = err
				return
			}
		}
		h, err := openDarwinFramework(libSystemPath)
		if err != nil {
			tecladoInitErr = err
			return
		}
		p, err := purego.Dlsym(h, "_dispatch_main_q")
		if err != nil {
			tecladoInitErr = fmt.Errorf("dispatch main queue: %w", err)
			return
		}
		mainQueue = p
	})
	return tecladoInitErr
}

func (t *Teclado) iniciarFila() {
	t.fila, t.fim = make(chan func(), 64), make(chan struct{})
	go func() {
		defer close(t.fim)
		for fn := range t.fila {
			fn()
		}
	}()
}

func (t *Teclado) pararFila() {
	if t.fila != nil {
		close(t.fila)
		<-t.fim
		t.fila = nil
	}
}

func (t *Teclado) emitir(fn func()) {
	if fn == nil {
		return
	}
	if t.fila == nil {
		fn()
		return
	}
	select {
	case t.fila <- fn:
	default: // fila cheia: descarta callback, não bloqueia o evento do sistema
	}
}

// Rodar instala o event tap no main CFRunLoop, chama OnReady e executa
// NSApplication.run até Parar. Deve ser chamado na thread principal bloqueada.
func (t *Teclado) Rodar() error {
	if !isDarwinMainThread() {
		return errors.New("Teclado.Rodar deve ser chamado na thread principal do macOS")
	}
	if err := initTecladoRuntime(); err != nil {
		return err
	}
	if !cgPreflightListen() && !cgRequestListen() {
		return errors.New("o macOS não autorizou o monitoramento do teclado; conceda acesso em Ajustes do Sistema > Privacidade e Segurança > Monitoramento de Entrada")
	}
	id := tecladoIDs.Add(1)
	t.mu.Lock()
	t.id = id
	t.mu.Unlock()
	teclados.Store(id, t)
	mask := uint64(1)<<macEventTypeDown | uint64(1)<<macEventTypeUp | uint64(1)<<macEventTypeFlags
	tap := cgEventTapCreate(macTapSession, macTapHeadInsert, macTapListenOnly, mask, tecladoTapCallback(), id)
	if tap == 0 {
		t.cleanupTap()
		return errors.New("não foi possível instalar o monitor global do teclado (confira Ajustes do Sistema > Privacidade e Segurança > Monitoramento de Entrada)")
	}
	t.mu.Lock()
	t.tap, t.loop = tap, cfRunLoopGetCurrentKey()
	t.mu.Unlock()
	if t.loop == 0 {
		t.cleanupTap()
		return errors.New("CFRunLoopGetCurrent retornou nil")
	}
	t.source = cfMachPortCreateRunSource(cfAllocatorDefaultForTeclado(), t.tap, 0)
	if t.source == 0 {
		t.cleanupTap()
		return errors.New("CFMachPortCreateRunLoopSource falhou")
	}
	cfRunLoopAddSourceKey(t.loop, t.source, cfRunLoopCommonModesForTeclado())
	cgEventTapEnable(t.tap, true)
	t.iniciarFila()
	if t.OnReady != nil {
		t.OnReady()
	}
	if !t.parar.Load() {
		comPool(func() {
			app := objcIDSharedApplication()
			app.Send(objc.RegisterName("run"))
		})
	}
	t.cleanupTap()
	t.pararFila()
	return nil
}

var (
	cfDefaultInitOnce sync.Once
	cfDefaultAlloc    uintptr
	cfDefaultModes    uintptr
)

func cfAllocatorDefaultForTeclado() uintptr {
	cfDefaultInitOnce.Do(func() {
		cfDefaultAlloc, _ = darwinGlobalPointer(coreFoundationPath, "kCFAllocatorDefault")
		cfDefaultModes, _ = darwinGlobalPointer(coreFoundationPath, "kCFRunLoopCommonModes")
	})
	return cfDefaultAlloc
}

func cfRunLoopCommonModesForTeclado() uintptr {
	cfAllocatorDefaultForTeclado()
	return cfDefaultModes
}

func objcIDSharedApplication() objc.ID {
	return objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
}

// Parar encerra Rodar. Pode ser chamado de qualquer goroutine (sinal, troca de
// versão): a parada é agendada na fila principal, nunca espera por ela.
func (t *Teclado) Parar() {
	t.parar.Store(true)
	t.mu.Lock()
	id := t.id
	t.mu.Unlock()
	if id == 0 || initTecladoRuntime() != nil {
		return // Rodar ainda não começou (ou já terminou): o flag basta
	}
	if isDarwinMainThread() {
		t.stopMainLoop()
		return
	}
	dispatchAsyncF(mainQueue, id, stopKeyboardOnMainCallback)
}

var stopKeyboardOnMainCallback = purego.NewCallback(func(id uintptr) {
	if v, ok := teclados.Load(id); ok {
		v.(*Teclado).stopMainLoop()
	}
})

func (t *Teclado) stopMainLoop() {
	if !isDarwinMainThread() {
		return
	}
	comPool(func() {
		app := objcIDSharedApplication()
		app.Send(objc.RegisterName("stop:"), objc.ID(0))
		// stop: só vale depois do próximo evento; um evento vazio acorda o run.
		ev := objc.ID(objc.GetClass("NSEvent")).Send(
			objc.RegisterName("otherEventWithType:location:modifierFlags:timestamp:windowNumber:context:subtype:data1:data2:"),
			uint64(15), macPoint{}, uint64(0), float64(0), int64(0), objc.ID(0), int16(0), int64(0), int64(0))
		if ev != 0 {
			app.Send(objc.RegisterName("postEvent:atStart:"), ev, true)
		}
	})
}

func (t *Teclado) cleanupTap() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.loop != 0 && t.source != 0 {
		cfRunLoopRemoveSourceKey(t.loop, t.source, cfRunLoopCommonModesForTeclado())
	}
	if t.source != 0 {
		cfReleaseKey(t.source)
		t.source = 0
	}
	if t.tap != 0 {
		cfMachPortInvalidate(t.tap)
		cfReleaseKey(t.tap)
		t.tap = 0
	}
	if t.id != 0 {
		teclados.Delete(t.id)
		t.id = 0
	}
}

func (t *Teclado) processEvent(eventType uint32, event uintptr) {
	t.tecla(eventType, uint16(cgEventGetInt(event, macKeyCodeField)), cgEventGetFlags(event))
}

// tecla é a máquina de estados pura (testável sem o Quartz). Usa os bits
// por dispositivo das flags para distinguir Ctrl/Option esquerdos dos direitos.
func (t *Teclado) tecla(eventType uint32, keyCode uint16, flags uint64) {
	if eventType == macEventTypeFlags {
		switch keyCode {
		case macKeyLeftControl:
			t.mudou(&t.ctrl, flags&macDeviceLeftCtrl != 0)
		case macKeyLeftOption:
			t.mudou(&t.alt, flags&macDeviceLeftAlt != 0)
		case macKeyRightControl:
			if flags&macDeviceRightCtrl != 0 {
				t.emitir(t.OutraTecla)
			}
		case macKeyRightOption:
			if flags&macDeviceRightAlt != 0 {
				t.emitir(t.OutraTecla)
			}
		default:
			// Outro modificador (Shift, Command, Fn) apertado junto do atalho.
			if t.ctrl.Load() || t.alt.Load() {
				t.emitir(t.OutraTecla)
			}
		}
		return
	}
	if eventType == macEventTypeDown && (t.ctrl.Load() || t.alt.Load()) {
		t.emitir(t.OutraTecla)
	}
}

func (t *Teclado) mudou(value *atomic.Bool, down bool) {
	value.Store(down)
	complete := t.ctrl.Load() && t.alt.Load()
	if complete && !t.ativo.Load() {
		t.ativo.Store(true)
		t.emitir(t.Pressionou)
	} else if !complete && t.ativo.Load() {
		t.ativo.Store(false)
		t.emitir(t.Soltou)
	}
}

var (
	tecladoCallbackOnce sync.Once
	tecladoCallbackPtr  uintptr
)

// tecladoTapCallback é criado uma vez (purego limita o número de callbacks).
// userInfo é o ID inteiro do Teclado; nenhum ponteiro Go chega ao Quartz.
func tecladoTapCallback() uintptr {
	tecladoCallbackOnce.Do(func() {
		tecladoCallbackPtr = purego.NewCallback(func(_ uintptr, eventType uintptr, event uintptr, userInfo uintptr) uintptr {
			v, ok := teclados.Load(userInfo)
			if !ok {
				return event
			}
			k := v.(*Teclado)
			typeValue := uint32(eventType)
			if typeValue == macTapDisabledTime || typeValue == macTapDisabledUser {
				// O sistema desliga taps lentos; religa (o callback já é rápido).
				k.mu.Lock()
				tap := k.tap
				k.mu.Unlock()
				if tap != 0 {
					cgEventTapEnable(tap, true)
				}
				return event
			}
			if event == 0 || cgEventGetInt(event, macUserDataField) == macEventTag {
				return event // teclas que o próprio wispr injeta não são do usuário
			}
			k.processEvent(typeValue, event)
			return event
		})
	})
	return tecladoCallbackPtr
}
