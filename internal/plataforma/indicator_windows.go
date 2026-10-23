//go:build windows

package plataforma

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Indicador is a non-interactive, pulsing border around the virtual desktop.
// Its windows and message loop are owned by a dedicated locked OS thread.
type Indicador struct {
	mu             sync.Mutex
	state          indicatorState
	threadID       atomic.Uint32
	done           chan struct{}
	ready          chan error
	closeRequested bool
	active         atomic.Bool
	notifyPending  atomic.Bool
	visible        bool // only accessed by the window thread
	startedAt      time.Time
	bounds         screenBounds    // only accessed by the window thread
	surfaces       [4]*edgeSurface // only accessed by the window thread
}

type indicatorState uint8

const (
	indicatorNew indicatorState = iota
	indicatorStarting
	indicatorRunning
	indicatorClosed
)

// NewIndicador constructs a desktop-edge recording indicator. Call Start before
// SetActive; Close is safe to call repeatedly.
func NewIndicador() *Indicador { return &Indicador{} }

// Start creates the layered windows and starts their message loop. It returns
// after the dedicated window thread is ready, or with a Win32 initialization
// error. Starting an already-running indicator is harmless; a closed instance
// cannot be restarted.
func (i *Indicador) Start() error {
	i.mu.Lock()
	switch i.state {
	case indicatorRunning:
		i.mu.Unlock()
		return nil
	case indicatorStarting:
		i.mu.Unlock()
		return errors.New("indicador já está iniciando")
	case indicatorClosed:
		i.mu.Unlock()
		return errors.New("indicador já foi fechado")
	}
	i.state = indicatorStarting
	i.done = make(chan struct{})
	i.ready = make(chan error, 1)
	done, ready := i.done, i.ready
	i.mu.Unlock()

	go i.run(done, ready)
	if err := <-ready; err != nil {
		return err
	}
	return nil
}

// SetActive shows and animates the indicator while recording. It is
// non-blocking and safe to call from capture callbacks; repeated calls coalesce
// naturally because the window thread reads the latest state.
func (i *Indicador) SetActive(active bool) {
	i.active.Store(active)
	if !i.notifyPending.CompareAndSwap(false, true) {
		return // A wake-up is already queued; the thread will read this store.
	}
	tid := i.threadID.Load()
	if tid != 0 {
		if posted, _, _ := procPostThreadMessageW.Call(uintptr(tid), wmAppIndicator, 0, 0); posted == 0 {
			i.notifyPending.Store(false)
		}
	} else {
		i.notifyPending.Store(false)
	}
}

// Close hides and destroys the overlay and waits for its window thread to exit.
func (i *Indicador) Close() {
	i.mu.Lock()
	if i.state == indicatorNew {
		i.state = indicatorClosed
		i.mu.Unlock()
		return
	}
	if i.state == indicatorClosed {
		done := i.done
		i.mu.Unlock()
		if done != nil {
			<-done
		}
		return
	}
	i.closeRequested = true
	done, tid := i.done, i.threadID.Load()
	if tid != 0 {
		procPostThreadMessageW.Call(uintptr(tid), wmQuit, 0, 0)
	}
	i.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (i *Indicador) run(done chan struct{}, ready chan error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(done)

	tid, _, _ := procGetCurrentThreadId.Call()
	// A message queue is created lazily by Windows. Force its creation before
	// publishing the thread ID, so PostThreadMessage cannot race queue setup.
	var m msg
	procPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, pmNoRemove)
	i.mu.Lock()
	i.threadID.Store(uint32(tid))
	i.mu.Unlock()

	if err := i.initialize(); err != nil {
		i.mu.Lock()
		i.threadID.Store(0)
		i.state = indicatorClosed
		i.mu.Unlock()
		ready <- err
		return
	}
	i.startedAt = time.Now()
	i.mu.Lock()
	closing := i.closeRequested
	if !closing {
		i.state = indicatorRunning
	}
	i.mu.Unlock()
	ready <- nil
	if closing {
		i.destroy()
		i.markClosed()
		return
	}
	// Honor SetActive calls made before Start completed.
	i.applyActive()

	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		if uint32(m.Message) == wmAppIndicator {
			i.notifyPending.Store(false)
			i.applyActive()
			continue
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	i.destroy()
	i.markClosed()
}

func (i *Indicador) markClosed() {
	i.mu.Lock()
	i.threadID.Store(0)
	i.state = indicatorClosed
	i.mu.Unlock()
}

func (i *Indicador) initialize() error {
	if err := registerIndicatorWindowClass(); err != nil {
		return err
	}
	bounds := virtualScreenBounds()
	if bounds.width <= 0 || bounds.height <= 0 {
		return errors.New("no active desktop bounds available")
	}
	i.bounds = bounds
	for side := edgeTop; side <= edgeRight; side++ {
		s := &edgeSurface{owner: i, side: side}
		w, h := edgeSize(side, bounds)
		hwnd, err := createIndicatorWindow(s, bounds, w, h)
		if err != nil {
			i.destroy()
			return err
		}
		s.hwnd = hwnd
		indicatorWindows.Store(hwnd, s)
		i.surfaces[side] = s
		if err := s.resize(w, h); err != nil {
			i.destroy()
			return err
		}
		i.positionSurface(s, bounds)
		s.update(72)
	}
	return nil
}

func (i *Indicador) applyActive() {
	active := i.active.Load()
	if active == i.visible && (!active || i.surfaces[edgeTop] != nil) {
		return
	}
	if active {
		if i.surfaces[edgeTop] == nil {
			return
		}
		i.refresh()
		for _, s := range i.surfaces {
			if s != nil {
				procSetWindowPos.Call(s.hwnd, hwndTopmost,
					winLongParam(s.x), winLongParam(s.y), winLongParam(s.width), winLongParam(s.height),
					swpNoActivate|swpShowWindow)
			}
		}
		procSetTimer.Call(i.surfaces[edgeTop].hwnd, edgeTimerID, edgeTimerMilliseconds, 0)
		i.visible = true
		return
	}
	if i.surfaces[edgeTop] != nil {
		procKillTimer.Call(i.surfaces[edgeTop].hwnd, edgeTimerID)
	}
	for _, s := range i.surfaces {
		if s != nil {
			procShowWindow.Call(s.hwnd, swHide)
		}
	}
	i.visible = false
}

func (i *Indicador) tick() {
	if !i.active.Load() {
		i.applyActive()
		return
	}
	alpha := pulseOpacity(time.Since(i.startedAt))
	for _, s := range i.surfaces {
		if s != nil {
			s.update(alpha)
		}
	}
}

func (i *Indicador) refresh() {
	bounds := virtualScreenBounds()
	if bounds.width <= 0 || bounds.height <= 0 {
		return
	}
	i.bounds = bounds
	for side, s := range i.surfaces {
		if s == nil {
			continue
		}
		w, h := edgeSize(edgeSide(side), bounds)
		if w <= 0 || h <= 0 {
			procShowWindow.Call(s.hwnd, swHide)
			continue
		}
		if err := s.resize(w, h); err != nil {
			continue // Keep the last valid surface if a display mode is transient.
		}
		i.positionSurface(s, bounds)
		s.update(pulseOpacity(time.Since(i.startedAt)))
		if i.visible {
			procSetWindowPos.Call(s.hwnd, hwndTopmost,
				winLongParam(s.x), winLongParam(s.y), winLongParam(s.width), winLongParam(s.height),
				swpNoActivate|swpShowWindow)
		}
	}
}

func (i *Indicador) positionSurface(s *edgeSurface, bounds screenBounds) {
	s.updatePosition(bounds)
	procSetWindowPos.Call(s.hwnd, hwndTopmost,
		winLongParam(s.x), winLongParam(s.y), winLongParam(s.width), winLongParam(s.height),
		swpNoActivate|swpNoownerzorder)
}

func (i *Indicador) destroy() {
	if i.surfaces[edgeTop] != nil {
		procKillTimer.Call(i.surfaces[edgeTop].hwnd, edgeTimerID)
	}
	for n, s := range i.surfaces {
		if s == nil {
			continue
		}
		indicatorWindows.Delete(s.hwnd)
		s.close()
		procDestroyWindow.Call(s.hwnd)
		i.surfaces[n] = nil
	}
	i.visible = false
}

type edgeSide uint8

const (
	edgeTop edgeSide = iota
	edgeBottom
	edgeLeft
	edgeRight
	edgeThickness         = 7
	edgeTimerID           = 1
	edgeTimerMilliseconds = 45
	wmDisplayChange       = 0x007E
	wmTimer               = 0x0113
	wmNcHitTest           = 0x0084
	wmMouseActivate       = 0x0021
	wmAppIndicator        = 0x8001
	htTransparent         = ^uintptr(0)
	maNoActivate          = 3
)

const (
	wsPopup         = 0x80000000
	wsExTopmost     = 0x00000008
	wsExToolwindow  = 0x00000080
	wsExTransparent = 0x00000020
	wsExLayered     = 0x00080000
	wsExNoactivate  = 0x08000000

	swpNoownerzorder = 0x0200
	swpNoActivate    = 0x0010
	swpShowWindow    = 0x0040
	swHide           = 0

	hwndTopmost = ^uintptr(0)
	pmNoRemove  = 0

	ulwAlpha     = 0x00000002
	acSrcOver    = 0
	acSrcAlpha   = 1
	dibRgbColors = 0
	dibRgb       = 0
	biRgb        = 0
)

type screenBounds struct{ x, y, width, height int32 }

type edgeSurface struct {
	owner       *Indicador
	side        edgeSide
	hwnd        uintptr
	dc          uintptr
	bitmap      uintptr
	stockBitmap uintptr
	bits        unsafe.Pointer
	width       int32
	height      int32
	x           int32
	y           int32
}

var indicatorWindows sync.Map // HWND -> *edgeSurface; WndProc never calls user code.

func windowProc(hwnd, message, wParam, lParam uintptr) uintptr {
	if value, ok := indicatorWindows.Load(hwnd); ok {
		s := value.(*edgeSurface)
		switch uint32(message) {
		case wmNcHitTest:
			return htTransparent
		case wmMouseActivate:
			return maNoActivate
		case wmDisplayChange:
			s.owner.refresh()
		case wmTimer:
			if wParam == edgeTimerID && s.side == edgeTop {
				s.owner.tick()
			}
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return r
}

var (
	gdi32                       = windows.NewLazySystemDLL("gdi32.dll")
	procRegisterClassExW        = user32.NewProc("RegisterClassExW")
	procCreateWindowExW         = user32.NewProc("CreateWindowExW")
	procDefWindowProcW          = user32.NewProc("DefWindowProcW")
	procDestroyWindow           = user32.NewProc("DestroyWindow")
	procSetWindowPos            = user32.NewProc("SetWindowPos")
	procShowWindow              = user32.NewProc("ShowWindow")
	procSetTimer                = user32.NewProc("SetTimer")
	procKillTimer               = user32.NewProc("KillTimer")
	procTranslateMessage        = user32.NewProc("TranslateMessage")
	procDispatchMessageW        = user32.NewProc("DispatchMessageW")
	procPeekMessageW            = user32.NewProc("PeekMessageW")
	procGetSystemMetrics        = user32.NewProc("GetSystemMetrics")
	procGetModuleHandleW        = kernel32.NewProc("GetModuleHandleW")
	procUpdateLayeredWindow     = user32.NewProc("UpdateLayeredWindow")
	procCreateCompatibleDC      = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection        = gdi32.NewProc("CreateDIBSection")
	procSelectObject            = gdi32.NewProc("SelectObject")
	procDeleteObject            = gdi32.NewProc("DeleteObject")
	procDeleteDC                = gdi32.NewProc("DeleteDC")
	indicatorWindowProcCallback = windows.NewCallback(windowProc)
)

const indicatorWindowClass = "BarrosoWisprDesktopEdgeIndicatorWindow"

func registerIndicatorWindowClass() error {
	name, _ := windows.UTF16PtrFromString(indicatorWindowClass)
	hinstance, _, _ := procGetModuleHandleW.Call(0)
	wc := wndClassEx{Size: uint32(unsafe.Sizeof(wndClassEx{})), WndProc: indicatorWindowProcCallback,
		Instance: hinstance, ClassName: name}
	r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	runtime.KeepAlive(name)
	if r == 0 && !errors.Is(err, windows.ERROR_CLASS_ALREADY_EXISTS) {
		return fmt.Errorf("RegisterClassExW: %w", win32Error(err))
	}
	return nil
}

func createIndicatorWindow(s *edgeSurface, bounds screenBounds, width, height int32) (uintptr, error) {
	name, _ := windows.UTF16PtrFromString(indicatorWindowClass)
	exStyle := uintptr(wsExTopmost | wsExToolwindow | wsExTransparent | wsExLayered | wsExNoactivate)
	hwnd, _, err := procCreateWindowExW.Call(exStyle,
		uintptr(unsafe.Pointer(name)), 0, wsPopup,
		winLongParam(bounds.x), winLongParam(bounds.y), winLongParam(width), winLongParam(height),
		0, 0, moduleHandle(), 0)
	runtime.KeepAlive(name)
	if hwnd == 0 {
		return 0, fmt.Errorf("CreateWindowExW: %w", win32Error(err))
	}
	return hwnd, nil
}

func moduleHandle() uintptr {
	h, _, _ := procGetModuleHandleW.Call(0)
	return h
}

func win32Error(err error) error {
	if err == nil || errors.Is(err, windows.ERROR_SUCCESS) {
		return errors.New("Win32 call failed")
	}
	return err
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSmall  uintptr
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

type point struct{ X, Y int32 }
type size struct{ CX, CY int32 }
type blendFunction struct {
	Op             byte
	Flags          byte
	SourceConstant byte
	AlphaFormat    byte
}

func (s *edgeSurface) resize(width, height int32) error {
	if width <= 0 || height <= 0 {
		return errors.New("invalid edge dimensions")
	}
	if s.width == width && s.height == height && s.bitmap != 0 {
		return nil
	}
	if s.dc == 0 {
		dc, _, err := procCreateCompatibleDC.Call(0)
		if dc == 0 {
			return fmt.Errorf("CreateCompatibleDC: %w", win32Error(err))
		}
		s.dc = dc
	}
	bi := bitmapInfo{Header: bitmapInfoHeader{Size: uint32(unsafe.Sizeof(bitmapInfoHeader{})), Width: width, Height: -height,
		Planes: 1, BitCount: 32, Compression: biRgb, SizeImage: uint32(width * height * 4)}}
	var bits unsafe.Pointer
	bitmap, _, err := procCreateDIBSection.Call(s.dc, uintptr(unsafe.Pointer(&bi)), dibRgbColors,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	runtime.KeepAlive(&bi)
	if bitmap == 0 || bits == nil {
		return fmt.Errorf("CreateDIBSection: %w", win32Error(err))
	}
	old, _, selectErr := procSelectObject.Call(s.dc, bitmap)
	if old == 0 || old == ^uintptr(0) {
		procDeleteObject.Call(bitmap)
		return fmt.Errorf("SelectObject: %w", win32Error(selectErr))
	}
	if s.bitmap == 0 {
		s.stockBitmap = old
	} else {
		procDeleteObject.Call(old)
	}
	if bits == nil {
		procDeleteObject.Call(bitmap)
		return errors.New("CreateDIBSection returned a nil pixel buffer")
	}
	s.bitmap, s.bits, s.width, s.height = bitmap, bits, width, height
	pixelCount := int(width) * int(height)
	pixels := unsafe.Slice((*byte)(s.bits), pixelCount*4)
	fillEdgePixels(pixels, int(width), int(height), s.side)
	return nil
}

func (s *edgeSurface) update(opacity byte) {
	if s.hwnd == 0 || s.dc == 0 || s.bitmap == 0 {
		return
	}
	dest, source := point{s.x, s.y}, point{}
	wh := size{s.width, s.height}
	blend := blendFunction{Op: acSrcOver, SourceConstant: opacity, AlphaFormat: acSrcAlpha}
	procUpdateLayeredWindow.Call(s.hwnd, 0, uintptr(unsafe.Pointer(&dest)), uintptr(unsafe.Pointer(&wh)),
		s.dc, uintptr(unsafe.Pointer(&source)), 0, uintptr(unsafe.Pointer(&blend)), ulwAlpha)
}

func (s *edgeSurface) close() {
	if s.dc != 0 {
		if s.stockBitmap != 0 {
			procSelectObject.Call(s.dc, s.stockBitmap)
		}
		if s.bitmap != 0 {
			procDeleteObject.Call(s.bitmap)
		}
		procDeleteDC.Call(s.dc)
	}
	s.dc, s.bitmap, s.stockBitmap, s.bits = 0, 0, 0, nil
}

func edgeSize(side edgeSide, bounds screenBounds) (int32, int32) {
	switch side {
	case edgeTop, edgeBottom:
		return bounds.width, min32(edgeThickness, bounds.height)
	case edgeLeft, edgeRight:
		return min32(edgeThickness, bounds.width), bounds.height
	default:
		return 0, 0
	}
}

func (s *edgeSurface) updatePosition(bounds screenBounds) {
	s.x, s.y = bounds.x, bounds.y
	s.width, s.height = edgeSize(s.side, bounds)
	switch s.side {
	case edgeBottom:
		s.y += bounds.height - s.height
	case edgeRight:
		s.x += bounds.width - s.width
	}
}

func virtualScreenBounds() screenBounds {
	return screenBounds{
		x:      int32(getSystemMetric(76)),
		y:      int32(getSystemMetric(77)),
		width:  int32(getSystemMetric(78)),
		height: int32(getSystemMetric(79)),
	}
}

func getSystemMetric(index uintptr) uintptr {
	r, _, _ := procGetSystemMetrics.Call(index)
	return r
}

func min32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}

func winLongParam(value int32) uintptr { return uintptr(uint32(value)) }

func fillEdgePixels(pixels []byte, width, height int, side edgeSide) {
	if width <= 0 || height <= 0 || len(pixels) < width*height*4 {
		return
	}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			distance := x
			switch side {
			case edgeTop:
				distance = y
			case edgeBottom:
				distance = height - 1 - y
			case edgeLeft:
				distance = x
			case edgeRight:
				distance = width - 1 - x
			}
			if distance >= edgeThickness {
				continue
			}
			alpha := uint8((edgeThickness - distance) * 180 / edgeThickness)
			index := (y*width + x) * 4
			// Premultiplied BGRA for the #238BFF blue edge.
			pixels[index+0] = uint8(255 * int(alpha) / 255)
			pixels[index+1] = uint8(139 * int(alpha) / 255)
			pixels[index+2] = uint8(35 * int(alpha) / 255)
			pixels[index+3] = alpha
		}
	}
}

func pulseOpacity(elapsed time.Duration) byte {
	const cycle = 2200 * time.Millisecond
	phase := 2 * math.Pi * float64(elapsed%cycle) / float64(cycle)
	return byte(58 + 25*math.Sin(phase))
}
