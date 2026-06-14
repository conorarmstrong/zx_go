package divmmc

import "testing"

// divMMC RAM power-on content must read $00, matching the FBLabs FPGA
// core and the reference emulator.
//
// The reference models the divMMC RAM with a default value of 0,
// and a direct pre-ENTER measurement of the reference at the NextZXOS
// 128K-menu confirms every unwritten divMMC RAM bank reads $00 (bank
// sum 0x000000), NOT $FF. An earlier revision of this file filled the
// banks with $FF on the theory that "real SRAM reads effectively
// all-ones"; that was wrong — the core zeroes the RAM and NextZXOS is
// written against $00.
//
// The $FF fill actively broke the 128K-BASIC launch: the editor-machine
// directory scan reads candidate entries straight out of freshly
// allocated divMMC RAM banks. With $FF those empty entries read as
// "end-of-directory / no entry", so the scan counted zero available
// editors and fell back to the no-reveal built-in 128 editor (a black
// screen). With $00 the scan behaves as on hardware.
//
// The old the development log-D27 esxDOS-mount concern (the per-partition state
// byte at window $2301) does NOT depend on the power-on fill: esxDOS
// WRITES that byte to $FF before the mount decision reads it (measured
// bank-0 $2301 = $FF at pre-ENTER under the $00 power-on fill), and the
// full boot is bit-for-bit identical with $00 vs $FF (90%→54% non-black
// at the same frames, no reset loop).
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
