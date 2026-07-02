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

// TestLockUnlockSpeedSelect pins the user-facing CPU-speed override: while
// locked, guest NR$07 writes (SetSpeedSelect) are ignored; unlocking restores
// normal guest control. This is how the emulator forces e.g. 3.5 MHz for a
// game NextZXOS would otherwise run at 28 MHz.
func TestLockUnlockSpeedSelect(t *testing.T) {
	cpu, _ := createTestCPU()
	cpu.SetSpeedSelect(3)  // guest picks 28 MHz
	cpu.LockSpeedSelect(0) // user forces 3.5 MHz
	if !cpu.SpeedLocked() {
		t.Fatal("SpeedLocked() should be true after LockSpeedSelect")
	}
	cpu.SetSpeedSelect(3) // guest tries to go fast again — must be ignored
	if cpu.SpeedSelect() != 0 || cpu.SpeedMultiplier() != 1 {
		t.Errorf("locked to 3.5 MHz: sel=%d mult=%d, want 0/1 (guest write ignored)",
			cpu.SpeedSelect(), cpu.SpeedMultiplier())
	}
	cpu.UnlockSpeedSelect()
	if cpu.SpeedLocked() {
		t.Fatal("SpeedLocked() should be false after UnlockSpeedSelect")
	}
	cpu.SetSpeedSelect(3) // guest control restored
	if cpu.SpeedSelect() != 3 {
		t.Errorf("after unlock: guest NR$07=3 should take effect, got sel=%d", cpu.SpeedSelect())
	}
}
