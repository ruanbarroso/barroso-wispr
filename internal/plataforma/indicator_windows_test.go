//go:build windows

package plataforma

import (
	"testing"
	"time"
)

func TestEdgeSizeUsesVirtualDesktopDimensions(t *testing.T) {
	bounds := screenBounds{x: -1920, y: -120, width: 4480, height: 2520}
	for _, tc := range []struct {
		side       edgeSide
		wantWidth  int32
		wantHeight int32
	}{
		{edgeTop, 4480, edgeThickness},
		{edgeBottom, 4480, edgeThickness},
		{edgeLeft, edgeThickness, 2520},
		{edgeRight, edgeThickness, 2520},
	} {
		width, height := edgeSize(tc.side, bounds)
		if width != tc.wantWidth || height != tc.wantHeight {
			t.Errorf("edgeSize(%v) = (%d, %d), want (%d, %d)", tc.side, width, height, tc.wantWidth, tc.wantHeight)
		}
	}
}

func TestEdgeSizeClampsToSmallVirtualDesktop(t *testing.T) {
	bounds := screenBounds{width: 3, height: 5}
	for _, side := range []edgeSide{edgeTop, edgeBottom, edgeLeft, edgeRight} {
		width, height := edgeSize(side, bounds)
		if width <= 0 || height <= 0 || width > bounds.width || height > bounds.height {
			t.Errorf("edgeSize(%v) = (%d, %d), outside bounds %+v", side, width, height, bounds)
		}
	}
}

func TestFillEdgePixelsCreatesBlueFadeOnRequestedEdge(t *testing.T) {
	const width, height = 16, 16
	for _, side := range []edgeSide{edgeTop, edgeBottom, edgeLeft, edgeRight} {
		pixels := make([]byte, width*height*4)
		fillEdgePixels(pixels, width, height, side)
		outerX, outerY := 0, 0
		switch side {
		case edgeBottom:
			outerY = height - 1
		case edgeRight:
			outerX = width - 1
		}
		outer := (outerY*width + outerX) * 4
		if pixels[outer] != 180 || pixels[outer+1] != 98 || pixels[outer+2] != 24 || pixels[outer+3] != 180 {
			t.Errorf("side %v outer pixel BGRA = %v, want [180 98 24 180]", side, pixels[outer:outer+4])
		}
		innerX, innerY := width/2, height/2
		inner := (innerY*width + innerX) * 4
		if pixels[inner] != 0 || pixels[inner+1] != 0 || pixels[inner+2] != 0 || pixels[inner+3] != 0 {
			t.Errorf("side %v center pixel = %v, want transparent", side, pixels[inner:inner+4])
		}
	}
}

func TestPulseOpacityIsSubtleAndPeriodic(t *testing.T) {
	var minOpacity, maxOpacity byte = 255, 0
	for elapsed := time.Duration(0); elapsed < 2200*time.Millisecond; elapsed += 5 * time.Millisecond {
		opacity := pulseOpacity(elapsed)
		if opacity < minOpacity {
			minOpacity = opacity
		}
		if opacity > maxOpacity {
			maxOpacity = opacity
		}
	}
	if minOpacity < 32 || maxOpacity > 100 || maxOpacity-minOpacity < 40 {
		t.Fatalf("opacity range = [%d, %d], want a subtle, visible pulse", minOpacity, maxOpacity)
	}
	if got, want := pulseOpacity(150*time.Millisecond), pulseOpacity(2350*time.Millisecond); got != want {
		t.Fatalf("one cycle apart: got %d, want %d", got, want)
	}
}
