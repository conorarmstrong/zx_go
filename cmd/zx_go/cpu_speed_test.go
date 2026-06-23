package main

import "testing"

// TestForcedCPUSpeed pins the user-facing "CPU Speed" override (Machine ->
// CPU Speed). NextZXOS runs the Next at 28 MHz, which makes some games run too
// fast; the override lets the user pin a slower speed. Rules (applyForcedCPUSpeed):
//   - Auto (0): the CPU follows the guest's NextReg $07.
//   - Forced (1..4 -> 3.5/7/14/28 MHz): the CPU is pinned, ignoring guest writes.
//   - While a .nex is loading (macro active): never pinned, so NextZXOS boots at
//     full speed regardless of the override.
func TestForcedCPUSpeed(t *testing.T) {
	emu, err := newNextEmulator()
	if err != nil {
		t.Skipf("no ROMs: %v", err)
	}

	// Auto: a guest 28 MHz selection stands.
	emu.cpuSpeedForce = 0
	emu.nexloadMacro = nil
	emu.applyForcedCPUSpeed()
	emu.cpu.SetSpeedSelect(3)
	if emu.cpu.SpeedMultiplier() != 8 {
		t.Errorf("Auto: SpeedMultiplier=%d, want 8 (guest 28 MHz honoured)", emu.cpu.SpeedMultiplier())
	}

	// Force 3.5 MHz: guest writes to 28 MHz must be ignored.
	emu.cpuSpeedForce = 1
	emu.applyForcedCPUSpeed()
	emu.cpu.SetSpeedSelect(3)
	if emu.cpu.SpeedMultiplier() != 1 {
		t.Errorf("Forced 3.5 MHz: SpeedMultiplier=%d, want 1 (guest write ignored)", emu.cpu.SpeedMultiplier())
	}

	// While loading, the override is suspended so boot runs at full speed.
	emu.nexloadMacro = newNexloadMacro("/games/x.nex")
	emu.applyForcedCPUSpeed()
	emu.cpu.SetSpeedSelect(3)
	if emu.cpu.SpeedMultiplier() != 8 {
		t.Errorf("Loading: SpeedMultiplier=%d, want 8 (override suspended during load)", emu.cpu.SpeedMultiplier())
	}

	// Back to Auto releases the lock.
	emu.nexloadMacro = nil
	emu.cpuSpeedForce = 0
	emu.applyForcedCPUSpeed()
	if emu.cpu.SpeedLocked() {
		t.Error("Auto: CPU speed should be unlocked")
	}
}
