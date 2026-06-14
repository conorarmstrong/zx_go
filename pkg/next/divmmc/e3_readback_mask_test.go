package divmmc

import "testing"

// TestE3ReadbackMasksReservedBitsAndStickyMapram verifies port-$E3 read-back
// matches the FBLabs core / reference (`port_e3_reg & ~0x30`):
// reserved bits 4-5 read 0, and bit 6 reflects the sticky MAPRAM latch.
func TestE3ReadbackMasksReservedBitsAndStickyMapram(t *testing.T) {
	p := New(nil)

	// reserved bits 4-5 must read back as 0.
	p.WritePort(0xE3, 0x3A) // 0011_1010: bits 5,4,3,1
	if v, _ := p.ReadPort(0xE3); v != 0x0A {
		t.Errorf("after OUT $E3,$3A: read-back=%#02x, want $0A (bits 4-5 masked)", v)
	}

	// MAPRAM (bit 6) is sticky: once set it stays set across a write of 0.
	p.WritePort(0xE3, 0x40) // set MAPRAM
	if v, _ := p.ReadPort(0xE3); v&0x40 == 0 {
		t.Errorf("after OUT $E3,$40: MAPRAM bit not set, read-back=%#02x", v)
	}
	p.WritePort(0xE3, 0x05) // bank 5, no MAPRAM in this write
	if v, _ := p.ReadPort(0xE3); v != 0x45 {
		t.Errorf("after OUT $E3,$05 (MAPRAM still latched): read-back=%#02x, want $45", v)
	}

	// CONMEM (bit 7) and low bank bits pass through unchanged.
	p.WritePort(0xE3, 0x86) // 1000_0110: CONMEM + bank 6 (MAPRAM already sticky)
	if v, _ := p.ReadPort(0xE3); v != 0xC6 {
		t.Errorf("after OUT $E3,$86: read-back=%#02x, want $C6 (CONMEM|MAPRAM|bank6)", v)
	}
}
