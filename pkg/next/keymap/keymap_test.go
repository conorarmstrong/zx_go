package keymap

import "testing"

// TestSequentialDataWritesAutoIncrementAddress mirrors the FPGA
// behaviour at zxnext.vhd:6306 — a NextReg $2B write stores the
// byte at the current keymap address and then increments the
// address. The boot trace shows NextZXOS writing $2B 22 times
// in a row to populate the PS/2 keymap.
func TestSequentialDataWritesAutoIncrementAddress(t *testing.T) {
	m := New()

	// Reset address to 0 via $29 (low byte) with $28 sel=0,
	// addr[8]=0 already cleared by New().
	m.WriteAddrLow(0)

	m.WriteData(0xAA)
	m.WriteData(0xBB)
	m.WriteData(0xCC)

	if got := m.KeyMap(0); got != 0xAA {
		t.Errorf("keymap[0]=%02X want $AA", got)
	}
	if got := m.KeyMap(1); got != 0xBB {
		t.Errorf("keymap[1]=%02X want $BB", got)
	}
	if got := m.KeyMap(2); got != 0xCC {
		t.Errorf("keymap[2]=%02X want $CC", got)
	}
}

// TestAddrHighBitFromAddrH — $28 bit 0 is addr[8].
func TestAddrHighBitFromAddrH(t *testing.T) {
	m := New()
	m.WriteAddrHigh(0x01) // sel=0, addr[8]=1
	m.WriteAddrLow(0x05)  // addr[7:0]=05 -> addr=$105
	m.WriteData(0xDE)
	if got := m.KeyMap(0x105); got != 0xDE {
		t.Errorf("keymap[$105]=%02X want $DE", got)
	}
}

// TestJoymapSelectViaAddrHBit7 — $28 bit 7 routes $2B writes to joymap.
func TestJoymapSelectViaAddrHBit7(t *testing.T) {
	m := New()
	m.WriteAddrHigh(0x80) // sel=1
	m.WriteAddrLow(0x10)
	m.WriteData(0x42)
	if got := m.KeyMap(0x10); got != 0 {
		t.Errorf("keymap[$10]=%02X want $00 (write went to joymap)", got)
	}
	if got := m.JoyMap(0x10); got != 0x42 {
		t.Errorf("joymap[$10]=%02X want $42", got)
	}
}

// TestAddressWrapsAt9Bits verifies the auto-increment wraps from
// $1FF back to $000 — per VHDL the address is a 9-bit counter.
func TestAddressWrapsAt9Bits(t *testing.T) {
	m := New()
	m.WriteAddrHigh(0x01) // addr[8]=1
	m.WriteAddrLow(0xFF)  // addr = $1FF
	m.WriteData(0xA0)     // store at $1FF, then auto-inc wraps to $000
	m.WriteData(0xB0)     // store at $000

	if got := m.KeyMap(0x1FF); got != 0xA0 {
		t.Errorf("keymap[$1FF] = $%02X, want $A0", got)
	}
	if got := m.KeyMap(0x000); got != 0xB0 {
		t.Errorf("keymap[$000] = $%02X (post-wrap), want $B0", got)
	}
}

// TestFullKeymapPopulation verifies all 512 entries can be written
// sequentially without aliasing. Mirrors the NextZXOS boot pattern
// of populating the entire PS/2 keymap via $29 reset + $2B repeat.
func TestFullKeymapPopulation(t *testing.T) {
	m := New()
	m.WriteAddrHigh(0x00)
	m.WriteAddrLow(0x00)
	for i := 0; i < 512; i++ {
		m.WriteData(byte(i & 0xFF))
	}
	for i := 0; i < 512; i++ {
		want := byte(i & 0xFF)
		if got := m.KeyMap(uint16(i)); got != want {
			t.Errorf("keymap[%d] = $%02X, want $%02X", i, got, want)
			return // one failure is enough
		}
	}
}

// TestSelectDoesNotMixMaps verifies that flipping joymap-sel via $28
// while addr stays the same routes writes/reads to disjoint storage.
func TestSelectDoesNotMixMaps(t *testing.T) {
	m := New()
	// Write to keymap[$100].
	m.WriteAddrHigh(0x01) // sel=0, addr[8]=1
	m.WriteAddrLow(0x00)  // addr=$100
	m.WriteData(0x11)

	// Flip to joymap and write at the SAME address.
	m.WriteAddrHigh(0x81) // sel=1, addr[8]=1
	m.WriteAddrLow(0x00)
	m.WriteData(0x22)

	if got := m.KeyMap(0x100); got != 0x11 {
		t.Errorf("keymap[$100] = $%02X, want $11 (joymap write must not bleed)", got)
	}
	if got := m.JoyMap(0x100); got != 0x22 {
		t.Errorf("joymap[$100] = $%02X, want $22", got)
	}
}

// TestAddrHighBit_OnlyBit0Mapped verifies that bits 6:1 of NR$28 are
// ignored (only bits 7 = sel and 0 = addr[8] take effect per VHDL).
func TestAddrHighBit_OnlyBit0Mapped(t *testing.T) {
	m := New()
	m.WriteAddrHigh(0x7E) // bits 6:1 set; bits 7 and 0 clear
	m.WriteAddrLow(0x42)
	m.WriteData(0x55)

	// Should store in keymap[$042], NOT joymap and NOT addr[8]=1.
	if got := m.KeyMap(0x042); got != 0x55 {
		t.Errorf("keymap[$042] = $%02X, want $55 (bits 6:1 of NR$28 must be ignored)",
			got)
	}
	// Joymap untouched.
	if m.JoyMap(0x042) != 0 {
		t.Error("joymap[$042] dirty — bit 7 of NR$28 should not have triggered joymap")
	}
}
