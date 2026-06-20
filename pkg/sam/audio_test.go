package sam

import "testing"

// saaWrite drives the SAA1099 through the SAM port interface: address latch on
// port 0x01FF, data on port 0x00FF.
func saaWrite(m *Machine, reg, val byte) {
	m.WritePort(0x01FF, reg)
	m.WritePort(0x00FF, val)
}

func TestSAMDrivesSAA(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	saaWrite(m, 0x08, 0x80) // channel 0 frequency
	saaWrite(m, 0x10, 0x04) // channel 0 octave 4
	saaWrite(m, 0x00, 0xFF) // channel 0 amplitude L+R full
	saaWrite(m, 0x14, 0x01) // channel 0 tone enable
	saaWrite(m, 0x1C, 0x01) // master sound enable

	buf := make([]int16, SamplesPerFrame*2)
	m.GenerateAudio(buf)

	silent := true
	for _, s := range buf {
		if s != 0 {
			silent = false
			break
		}
	}
	if silent {
		t.Error("driving the SAA through the SAM ports should produce audio")
	}
}

// TestSAMSAAAddressDataDecode confirms 0x01FF latches the register and 0x00FF
// writes data (not the reverse).
func TestSAMSAAAddressDataDecode(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.WritePort(0x01FF, 0x03) // latch register 0x03
	m.WritePort(0x00FF, 0x5A) // write data
	if got := m.SAA.ReadRegister(0x03); got != 0x5A {
		t.Errorf("SAA reg 0x03 = %#02x, want 0x5A (0x01FF=addr, 0x00FF=data)", got)
	}
}
