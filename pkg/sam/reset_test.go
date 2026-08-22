package sam

import "testing"

// TestResetReturnsToPowerOn verifies a cold reset restarts the CPU at $0000 with
// ROM0 paged in and the ASIC paging registers back at power-on (all zero). The
// GUI reboot path depends on this for the SAM.
func TestResetReturnsToPowerOn(t *testing.T) {
	m, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	// Run the boot so paging, the PC and ASIC state diverge from power-on.
	runFrames(m, 600)
	if m.Mem.LMPR() == 0 && m.CPU.PC == 0 {
		t.Fatal("precondition: boot did not change paging/PC")
	}

	m.Reset()

	if m.CPU.PC != 0 {
		t.Errorf("after reset PC = %#04x, want 0x0000", m.CPU.PC)
	}
	if got := m.Mem.LMPR(); got != 0 {
		t.Errorf("after reset LMPR = %#02x, want 0x00 (ROM0 paged in)", got)
	}
	if got := m.Mem.HMPR(); got != 0 {
		t.Errorf("after reset HMPR = %#02x, want 0x00", got)
	}
	// ROM0 must be readable at $0000 again (reset vector).
	if m.Mem.Read(0x0000) == 0 {
		t.Error("after reset $0000 reads 0 — ROM0 not paged into section A")
	}
}

// TestResetThenBootsAgain is the behaviour that matters: after a reset the SAM
// boots back to its copyright screen exactly as from cold.
func TestResetThenBootsAgain(t *testing.T) {
	m, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	runFrames(m, 600)
	cold := screenHash(m)

	m.Reset()
	runFrames(m, 600)
	afterReset := screenHash(m)

	if afterReset != cold {
		t.Errorf("screen after reset+boot (%s) differs from cold boot (%s)", afterReset[:8], cold[:8])
	}
}

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
