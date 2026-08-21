package sam

import "testing"

// Machine.Reset cleared the CPU, paging, border, line interrupt and the
// beeper, but not the SAA1099. Music from the previous program went on
// playing over the copyright screen after Machine → Reboot.
func TestResetSilencesTheSAA(t *testing.T) {
	m := newTestMachine(t)

	// Start a tone: amplitude on channel 0, frequency set, tone enabled.
	m.SAA.WriteRegister(0x00, 0xFF) // amplitude 0: full left and right
	m.SAA.WriteRegister(0x08, 0x80) // frequency 0
	m.SAA.WriteRegister(0x10, 0x02) // octave 0
	m.SAA.WriteRegister(0x14, 0x01) // frequency enable, channel 0
	m.SAA.WriteRegister(0x1C, 0x01) // sound enable

	m.Reset()

	for reg := byte(0x00); reg <= 0x1C; reg++ {
		if got := m.SAA.ReadRegister(reg); got != 0 {
			t.Errorf("SAA register %#02x = %#02x after Reset, want 0", reg, got)
		}
	}
}
