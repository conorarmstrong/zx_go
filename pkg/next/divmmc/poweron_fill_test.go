package divmmc

import "testing"

// divMMC RAM power-on content must read $00, matching real hardware:
// unwritten divMMC RAM banks read as zero, not $FF.
//
// This is load-bearing for the 128K-BASIC launch: the editor-machine
// directory scan reads candidate entries straight out of freshly
// allocated divMMC RAM banks. A $FF fill makes those empty entries
// read as "end-of-directory / no entry", so the scan counts zero
// available editors and falls back to the no-reveal built-in 128
// editor (a black screen). With a $00 fill the scan behaves as on
// hardware.
//
// The esxDOS-mount per-partition state byte at window $2301 does not
// depend on the power-on fill: esxDOS writes that byte to $FF before
// the mount decision reads it, so the fill value there is moot.
func TestDivMMCRAM_PowersOnAsZero(t *testing.T) {
	p := New(make([]byte, ROMSize))
	p.WritePort(0x00E3, 0x80) // CONMEM in, bank 0
	for _, addr := range []uint16{0x2000, 0x2301, 0x3FFF} {
		got, ok := p.HandleRead(addr)
		if !ok || got != 0x00 {
			t.Fatalf("unwritten divMMC RAM at $%04X = $%02X ok=%v, want $00", addr, got, ok)
		}
	}
	// every bank, not just bank 0
	for bank := byte(0); bank < NumBanks; bank++ {
		p.WritePort(0x00E3, 0x80|bank)
		got, _ := p.HandleRead(0x2123)
		if got != 0x00 {
			t.Fatalf("unwritten divMMC RAM bank %d = $%02X, want $00", bank, got)
		}
	}
}
