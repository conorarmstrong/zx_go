// Package keymap models the Spectrum Next PS/2 keymap RAM exposed
// via NextRegs $28 (KEYMAP_ADDR_H + select), $29 (KEYMAP_ADDR_L),
// $2A (KEYMAP_DATA_H — vestigial), $2B (KEYMAP_DATA_L, auto-inc).
//
// Behaviour is taken from the TBBlue FPGA verilog
// (cores/zxnext/src/zxnext.vhd:6294-6324). Reads of these NextRegs
// return zero in the FPGA, so this model is write-only.
//
// Two backing tables exist:
//   - keymap (PS/2 scancode -> Spectrum key index), addressed
//     0..511 via the 9-bit nr_keymap_addr.
//   - joymap (key joystick mapping), same address space, selected
//     by NextReg $28 bit 7 (nr_keymap_sel).
package keymap

// Map holds the 512-entry keymap and joymap selected via $28 bit 7.
type Map struct {
	keymap [512]byte
	joymap [512]byte

	addr      uint16 // 9-bit
	joymapSel bool
}

// New returns a zero-initialised keymap.
func New() *Map { return &Map{} }

// WriteAddrHigh handles a write to NextReg $28. Bit 7 selects
// joymap, bit 0 is the high bit (addr[8]) of the address.
func (m *Map) WriteAddrHigh(val byte) {
	m.joymapSel = val&0x80 != 0
	m.addr = (m.addr & 0xFF) | (uint16(val&0x01) << 8)
}

// WriteAddrLow handles a write to NextReg $29 — addr[7:0].
func (m *Map) WriteAddrLow(val byte) {
	m.addr = (m.addr & 0x100) | uint16(val)
}

// WriteData handles a write to NextReg $2B. Stores val at the
// current address in the active map (keymap or joymap) and
// increments the address (wrapping at 9 bits).
func (m *Map) WriteData(val byte) {
	idx := m.addr & 0x1FF
	if m.joymapSel {
		m.joymap[idx] = val
	} else {
		m.keymap[idx] = val
	}
	m.addr = (m.addr + 1) & 0x1FF
}

// KeyMap returns a stored PS/2 keymap entry (read-side; for the
// keyboard subsystem to consume).
func (m *Map) KeyMap(idx uint16) byte { return m.keymap[idx&0x1FF] }

// JoyMap returns a stored joymap entry.
func (m *Map) JoyMap(idx uint16) byte { return m.joymap[idx&0x1FF] }
