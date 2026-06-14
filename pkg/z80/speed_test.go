package z80

import "testing"

func TestSpeedMultiplierDefaultsToOne(t *testing.T) {
	cpu, _ := createTestCPU()
	if cpu.SpeedMultiplier() != 1 {
		t.Errorf("SpeedMultiplier on fresh CPU = %d, want 1", cpu.SpeedMultiplier())
	}
}

func TestSpeedSelectMaps(t *testing.T) {
	cases := []struct {
		sel  byte
		want int
	}{
		{0, 1},
		{1, 2},
		{2, 4},
		{3, 8},
	}
	for _, c := range cases {
		cpu, _ := createTestCPU()
		cpu.SetSpeedSelect(c.sel)
		if got := cpu.SpeedMultiplier(); got != c.want {
			t.Errorf("SetSpeedSelect(%d): SpeedMultiplier = %d, want %d", c.sel, got, c.want)
		}
		if cpu.SpeedSelect() != c.sel {
			t.Errorf("SpeedSelect readback = %d, want %d", cpu.SpeedSelect(), c.sel)
		}
	}
}

func TestSpeedSelectMasksHighBits(t *testing.T) {
	cpu, _ := createTestCPU()
	cpu.SetSpeedSelect(0xFF) // only low 2 bits should survive
	if cpu.SpeedSelect() != 0x03 {
		t.Errorf("SetSpeedSelect(0xFF): SpeedSelect = %#x, want 0x03", cpu.SpeedSelect())
	}
	if cpu.SpeedMultiplier() != 8 {
		t.Errorf("SetSpeedSelect(0xFF): SpeedMultiplier = %d, want 8 (28 MHz)", cpu.SpeedMultiplier())
	}
}
