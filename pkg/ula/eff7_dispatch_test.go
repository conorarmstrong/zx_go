package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestEFF7PortDispatch pins the port $EFF7 address decode end to end
// through ULA.WritePort (not just Memory.SetEFF7 called directly): per
// zxnext.vhd:2604, the port is decoded on address bits 15:12="1110" and the
// low byte $F7 only -- bits 11:8 are don't-care, so any address of the form
// $x0F7-$xFF7 with the high nibble $E aliases it (a classic incompletely
// decoded Pentagon/Scorpion-style port carried through on the Next).
func TestEFF7PortDispatch(t *testing.T) {
	for _, addr := range []uint16{0xEFF7, 0xE0F7, 0xE1F7, 0xE8F7} {
		t.Run("", func(t *testing.T) {
			mem, err := memory.New("", roms.ModelNext)
			if err != nil {
				t.Fatalf("memory.New(ModelNext): %v", err)
			}
			u := New(mem, keyboard.New())

			if got := mem.GetMMU(0); got != 0xFF {
				t.Fatalf("precondition: slot 0 should default to ROM, got %#x", got)
			}

			u.WritePort(addr, 0x08) // bit 3 set

			if got := mem.GetMMU(0); got != 0x00 {
				t.Errorf("WritePort(%#04x, 0x08): slot 0 = %#x, want $00 (RAM bank 0 lo)", addr, got)
			}
			if got := mem.GetMMU(1); got != 0x01 {
				t.Errorf("WritePort(%#04x, 0x08): slot 1 = %#x, want $01 (RAM bank 0 hi)", addr, got)
			}
		})
	}
}

// TestEFF7PortDispatch_NotOn48K confirms $EFF7 is Next-only -- the same
// address pattern must not be misrouted into Memory.SetEFF7 on classic
// models (which don't have this port and would panic or corrupt state via
// the ModelNext-only fields it touches).
func TestEFF7PortDispatch_NotOn48K(t *testing.T) {
	testDir := "test_roms_eff7_48k"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, err := memory.New(testDir, roms.Model48K)
	if err != nil {
		t.Fatalf("memory.New(48K): %v", err)
	}
	u := New(mem, keyboard.New())

	// Must not panic, and must not affect slot 0 the way the Next's EFF7
	// bit 3 would.
	u.WritePort(0xEFF7, 0x08)

	if got := mem.GetMMU(0); got != 0xFF {
		t.Errorf("48K: WritePort(0xEFF7, 0x08) disturbed slot 0 = %#x, want $FF (48K has no $EFF7 port)", got)
	}
}
