package z80

import (
	"testing"
)

// TestInstructionCountIncrementsOnFetch verifies the new free-running
// instruction counter ticks once per opcode fetch — including for
// prefixed instructions, which fetch twice (prefix byte + suffix byte).
func TestInstructionCountIncrementsOnFetch(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// Three NOPs at 0x8000.
	mem.Write(0x8000, 0x00)
	mem.Write(0x8001, 0x00)
	mem.Write(0x8002, 0x00)
	cpu.PC = 0x8000
	cpu.ResetInstructionCount()

	if got := cpu.InstructionCount(); got != 0 {
		t.Fatalf("after Reset: count = %d, want 0", got)
	}

	cpu.executeInstruction()
	if got := cpu.InstructionCount(); got != 1 {
		t.Errorf("after 1 NOP: count = %d, want 1", got)
	}
	cpu.executeInstruction()
	cpu.executeInstruction()
	if got := cpu.InstructionCount(); got != 3 {
		t.Errorf("after 3 NOPs: count = %d, want 3", got)
	}
}

// TestInstructionCountPrefixedInstruction verifies that DD-, FD-, ED-,
// and CB-prefixed instructions each tick the counter twice — once for
// the prefix fetch, once for the suffix. Matches FUSE's R++ at every
// fetch site (opcodes_base.c:837 et al.).
func TestInstructionCountPrefixedInstruction(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// CB 00 = RLC B (a single CB-prefixed instruction).
	mem.Write(0x8000, 0xCB)
	mem.Write(0x8001, 0x00)
	cpu.PC = 0x8000
	cpu.ResetInstructionCount()
	cpu.executeInstruction()
	if got := cpu.InstructionCount(); got != 2 {
		t.Errorf("CB-prefixed instruction: count = %d, want 2", got)
	}
}

// TestExecuteRZXFrameRunsExactly checks that ExecuteRZXFrame stops
// precisely when the requested instruction count is reached.
func TestExecuteRZXFrameRunsExactly(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// Fill 0x8000..0x80FF with NOPs (255 of them) followed by HALT.
	for i := 0; i < 255; i++ {
		mem.Write(uint16(0x8000+i), 0x00)
	}
	mem.Write(0x80FF, 0x76) // HALT
	cpu.PC = 0x8000
	cpu.IFF1 = false // No interrupt at end so we can inspect cleanly
	cpu.ResetInstructionCount()

	cpu.ExecuteRZXFrame(10)
	if got := cpu.InstructionCount(); got != 10 {
		t.Errorf("after ExecuteRZXFrame(10): count = %d, want 10", got)
	}
	if cpu.PC != 0x800A {
		t.Errorf("after 10 NOPs from 0x8000: PC = %04X, want 0x800A", cpu.PC)
	}
}

// TestExecuteRZXFrameAcrossHalt verifies that HALT iterations advance
// the instruction counter (matching FUSE's PC--/PC++ trick) — without
// this, an RZX recording that captured a HALT-then-interrupt sequence
// would hang at the HALT during playback.
func TestExecuteRZXFrameAcrossHalt(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	mem.Write(0x8000, 0x76) // HALT at 0x8000
	cpu.PC = 0x8000
	cpu.IFF1 = false
	cpu.ResetInstructionCount()

	// One real fetch (the HALT itself) + 9 spinning iterations.
	cpu.ExecuteRZXFrame(10)
	if got := cpu.InstructionCount(); got != 10 {
		t.Errorf("HALT spin: count = %d, want 10", got)
	}
	if !cpu.Halted {
		t.Error("expected Halted=true after HALT")
	}
}

// TestExecuteRZXFrameInterruptAtEnd verifies the end-of-frame interrupt
// fires after the instruction count target is reached, matching what
// libspectrum_rzx_playback expects.
func TestExecuteRZXFrameInterruptAtEnd(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// 5 NOPs at 0x8000.
	for i := 0; i < 5; i++ {
		mem.Write(uint16(0x8000+i), 0x00)
	}
	cpu.PC = 0x8000
	cpu.SP = 0xFF00
	cpu.IFF1 = true
	cpu.IM = 1
	cpu.ResetInstructionCount()

	cpu.ExecuteRZXFrame(5)
	// IM 1 vectors to 0x0038.
	if cpu.PC != 0x0038 {
		t.Errorf("after IM1 interrupt: PC = %04X, want 0x0038", cpu.PC)
	}
	if cpu.IFF1 {
		t.Error("after interrupt acceptance: IFF1 should be cleared")
	}
}
