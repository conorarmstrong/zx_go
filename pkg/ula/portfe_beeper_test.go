package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Port $FE write/read behaviour — beeper (bit 4), MIC (bit 3), border
// (bits 2:0) on write; EAR (bit 6) + reserved bits (5,7 read as 1) on
// read. Plus the BRIGHT attribute (bit 6) render path.

func newULAForPortTest(t *testing.T) *ULA {
	t.Helper()
	dir := "test_roms_ula"
	createTestROMs(t, dir)
	t.Cleanup(func() { cleanupTestROMs(dir) })
	mem, _ := memory.New(dir, roms.Model48K)
	return New(mem, keyboard.New())
}

func TestULA_PortFE_BeeperMicBorder(t *testing.T) {
	u := newULAForPortTest(t)

	// Beeper bit 4 high → Speaker true.
	u.WritePort(0xFE, 0x10)
	if !u.Speaker {
		t.Error("FE bit4=1: Speaker should be true")
	}
	// Beeper bit 4 low → Speaker false.
	u.WritePort(0xFE, 0x00)
	if u.Speaker {
		t.Error("FE bit4=0: Speaker should be false")
	}
	// MIC bit 3 high → Mic true.
	u.WritePort(0xFE, 0x08)
	if !u.Mic {
		t.Error("FE bit3=1: Mic should be true")
	}
	// Border = bits 2:0; bits 3-7 must not bleed into it.
	u.WritePort(0xFE, 0xFF)
	if u.BorderColour != 0x07 {
		t.Errorf("FE border = %d, want 7 (bits 2:0 only)", u.BorderColour)
	}
	u.WritePort(0xFE, 0x02)
	if u.BorderColour != 0x02 {
		t.Errorf("FE border = %d, want 2", u.BorderColour)
	}
}

func TestULA_PortFE_EARAndReservedBits(t *testing.T) {
	u := newULAForPortTest(t)

	// No tape: bit 6 (EAR) = 0, bits 5 and 7 = 1 (reserved read-high).
	u.TapeIn = false
	v, ok := u.ReadPort(0xFE)
	if !ok {
		t.Fatal("ReadPort(0xFE) not handled")
	}
	if v&0x40 != 0 {
		t.Errorf("EAR bit6 = 1 with no tape; want 0 (val=%#02x)", v)
	}
	if v&0x20 == 0 || v&0x80 == 0 {
		t.Errorf("reserved bits 5,7 must read 1 (val=%#02x)", v)
	}

	// Tape driving EAR high → bit 6 = 1.
	u.TapeIn = true
	v, _ = u.ReadPort(0xFE)
	if v&0x40 == 0 {
		t.Errorf("EAR bit6 = 0 with TapeIn; want 1 (val=%#02x)", v)
	}
}

func TestULA_AttributeBright(t *testing.T) {
	u := newULAForPortTest(t)
	screen := u.mem.GetPage(u.mem.ScreenPage)
	screen[0] = 0xFF // all 8 pixels = ink

	// Normal: ink=1 (blue), paper=0, bright off.
	screen[0x1800] = 0x01
	pNormal := u.Render().RGBAAt(BorderLeft, BorderTop)

	// Bright: same ink=1 but bright bit (0x40) set → palette index +8.
	screen[0x1800] = 0x41
	pBright := u.Render().RGBAAt(BorderLeft, BorderTop)

	if pNormal == pBright {
		t.Errorf("BRIGHT attr did not change ink colour: normal=%v bright=%v", pNormal, pBright)
	}
	// Bright blue must match palette[1+8]=palette[9].
	want := u.palette[9]
	if pBright != want {
		t.Errorf("bright ink = %v, want palette[9]=%v", pBright, want)
	}
}
