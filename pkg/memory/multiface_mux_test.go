package memory

import "testing"

// While the Multiface memory is paged in, sram_pre_override is "000"
// (zxnext.vhd:3030-3036), and the divMMC ROM and RAM arms of the SRAM mux
// both need override bit 2 (:3084, :3092). So a divMMC that is already paged
// in, held or by CONMEM, does not show through: $0000-$1FFF reads the
// Multiface ROM and $2000-$3FFF reads and writes the Multiface RAM.
func TestTheMultifaceOutranksAPagedDivMMC(t *testing.T) {
	m := newNextMem(t)
	rom := make([]byte, 0x2000)
	rom[0x0066] = 0xA5
	m.SetMultifaceROM(rom)
	m.PeripheralRead = func(uint16) (byte, bool) { return 0x11, true }
	divWrites := 0
	m.PeripheralWrite = func(uint16, byte) bool { divWrites++; return true }
	m.SetMultifaceActive(true)

	if got := m.Read(0x0066); got != 0xA5 {
		t.Errorf("$0066 = %#02x, want the Multiface ROM's $A5", got)
	}
	m.Write(0x2100, 0x5A)
	if divWrites != 0 {
		t.Errorf("a write at $2100 went to the divMMC")
	}
	if got := m.Read(0x2100); got != 0x5A {
		t.Errorf("$2100 = %#02x, want the Multiface RAM's $5A", got)
	}
}

// A reset pages the Multiface out (multiface.vhd:175, mf_enable <= '0').
func TestResetPagesTheMultifaceOut(t *testing.T) {
	m := newNextMem(t)
	m.SetMultifaceROM(make([]byte, 0x2000))
	m.SetMultifaceActive(true)
	if err := m.Reset(); err != nil {
		t.Fatal(err)
	}
	if m.MultifaceActive() {
		t.Error("the Multiface is still paged in after a reset")
	}
}
