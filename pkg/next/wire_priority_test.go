package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/compositor"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
)

// LayerPriority unit tests (iter 221) for the small storage type
// that mediates between NR$15 writes and compositor.PriorityMode
// decoding. The compositor's existing tests cover the rendering
// effects; these tests pin the byte → mode decoding in isolation.

// TestLayerPriority_NewIsZero verifies a fresh priority store is at
// raw byte 0 (= ModeSLU, the reset default).
func TestLayerPriority_NewIsZero(t *testing.T) {
	p := NewLayerPriority()
	if p.Get() != 0 {
		t.Errorf("new LayerPriority.Get() = $%02X, want 0", p.Get())
	}
	if p.Mode() != compositor.ModeSLU {
		t.Errorf("new LayerPriority.Mode() = %v, want ModeSLU", p.Mode())
	}
}

// TestLayerPriority_ModeDecodes — each NR$15 low-2-bit pattern
// maps to the matching PriorityMode.
func TestLayerPriority_ModeDecodes(t *testing.T) {
	cases := []struct {
		raw  byte
		want compositor.PriorityMode
	}{
		{0x00, compositor.ModeSLU},
		{0x01, compositor.ModeLSU},
		{0x02, compositor.ModeSUL},
		{0x03, compositor.ModeLUS},
		// High bits ignored.
		{0xFC, compositor.ModeSLU},
		{0xFD, compositor.ModeLSU},
	}
	for _, c := range cases {
		p := NewLayerPriority()
		p.Set(c.raw)
		if p.Mode() != c.want {
			t.Errorf("Set($%02X): Mode() = %v, want %v", c.raw, p.Mode(), c.want)
		}
	}
}

// TestLayerPriority_GetRoundTrips — Get returns the exact byte
// last set, including high bits we don't decode.
func TestLayerPriority_GetRoundTrips(t *testing.T) {
	p := NewLayerPriority()
	p.Set(0xCD)
	if p.Get() != 0xCD {
		t.Errorf("Get after Set($CD) = $%02X, want $CD", p.Get())
	}
}

// TestWireLayerPriority_Routes verifies the wire-layer plumbing
// installs the OnWrite handler so NR$15 writes update the priority
// store.
func TestWireLayerPriority_Routes(t *testing.T) {
	disp := nextregs.New()
	p := NewLayerPriority()
	WireLayerPriority(disp, p)

	disp.WriteReg(0x15, 0x03)
	if p.Mode() != compositor.ModeLUS {
		t.Errorf("after NR$15=$03: Mode = %v, want ModeLUS", p.Mode())
	}
	if got := disp.ReadReg(0x15); got != 0x03 {
		t.Errorf("dispatcher storage: ReadReg($15) = $%02X, want $03", got)
	}
}
