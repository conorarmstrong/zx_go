package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/ay"
	"github.com/conorarmstrong/zx_go/pkg/next/divmmc"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
)

// Covers WireDivMMCEntryPoints — installs NR$B8 / $BB
// OnWrite/OnRead handlers that forward to the pager's
// EntryPoints0/1 accessors.

func TestWireDivMMCEntryPoints_RoutesB8(t *testing.T) {
	disp := nextregs.New()
	pager := divmmc.New(nil)
	WireDivMMCEntryPoints(disp, pager)

	// Write to NR$B8 → pager.SetEntryPoints0 called.
	disp.WriteReg(0xB8, 0xAA)
	if pager.EntryPoints0() != 0xAA {
		t.Errorf("pager.EntryPoints0 = $%02X, want $AA", pager.EntryPoints0())
	}
	// Read NR$B8 should reflect the pager value.
	if got := disp.ReadReg(0xB8); got != 0xAA {
		t.Errorf("ReadReg($B8) = $%02X, want $AA", got)
	}
}

func TestWireDivMMCEntryPoints_RoutesBB(t *testing.T) {
	disp := nextregs.New()
	pager := divmmc.New(nil)
	WireDivMMCEntryPoints(disp, pager)

	disp.WriteReg(0xBB, 0x55)
	if pager.EntryPoints1() != 0x55 {
		t.Errorf("pager.EntryPoints1 = $%02X, want $55", pager.EntryPoints1())
	}
	if got := disp.ReadReg(0xBB); got != 0x55 {
		t.Errorf("ReadReg($BB) = $%02X, want $55", got)
	}
}

// Covers WireMultiAY (legacy alias for the AY-only path
// of WirePeripheral2).
func TestWireMultiAY_DelegatesToPeripheral2(t *testing.T) {
	disp := nextregs.New()
	engine := ay.NewEngine()
	WireMultiAY(disp, engine)
	// Write to NR$06 — exact bit semantics aren't asserted here;
	// the goal is just to exercise the legacy-alias wiring.
	disp.WriteReg(0x06, 0x20)
	_ = engine.Selected()
}
