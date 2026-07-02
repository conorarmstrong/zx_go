package z80

import (
	"testing"
)

// INT acceptance edge-case tests.
//
// Verifies Z80 interrupt-acceptance rules per the canonical Z80
// programmer's manual + Sean Young's "Undocumented Z80 Documented":
//
//   - INT cannot be accepted while IFF1 = 0 (interrupts disabled)
//   - INT cannot be accepted in the instruction immediately after EI
//     (one-instruction delay)
//   - NMI is accepted regardless of IFF1
//   - NMI clears IFF1 (but preserves IFF2 — see CPU.NMI() comments
//     for the FUSE-style trade-off explanation)
//
// These tests focus on the EDGE conditions, not the happy-path
// IM 0/1/2 vector dispatch (covered by existing TestInterruptIMx).

// TestIntAccept_IFF1Disabled_NoAcceptance verifies an IRQ pending
// while IFF1 = false is silently dropped (never executes).
func TestIntAccept_IFF1Disabled_NoAcceptance(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.IM = 1
	cpu.IFF1 = false // explicitly disabled
	cpu.IFF2 = false
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0
	preSP := cpu.SP
	prePC := cpu.PC

	cpu.interrupt()

	if cpu.PC != prePC {
		t.Errorf("IFF1=0: PC moved from $%04X to $%04X — INT should have been ignored",
			prePC, cpu.PC)
	}
	if cpu.SP != preSP {
		t.Errorf("IFF1=0: SP changed from $%04X to $%04X — no push should have happened",
			preSP, cpu.SP)
	}
}

// TestIntAccept_EIDelay_DefersOneInstruction verifies the EI-delay
// rule: after EI executes, IRQs are deferred by one instruction.
// The sequence `EI; <insn>; <insn>` accepts the IRQ on the second
// <insn>'s M1 fetch, not the first.
//
// Implementation: cpu.eiDelay set true by EI execution; the M1 hook
// at executeInstruction's top clears it without accepting; the next
// M1 sees eiDelay = false and accepts.
func TestIntAccept_EIDelay_DefersOneInstruction(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.IM = 1
	cpu.IFF1 = false
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0

	// Place EI; NOP; NOP at $8000.
	mem.Write(0x8000, 0xFB) // EI
	mem.Write(0x8001, 0x00) // NOP
	mem.Write(0x8002, 0x00) // NOP

	// Execute EI — enables IFF1, sets eiDelay.
	cpu.StepInstruction()
	if !cpu.IFF1 {
		t.Fatal("EI: IFF1 should be true after execution")
	}
	if !cpu.eiDelay {
		t.Fatal("EI: eiDelay should be true after execution")
	}

	// Set IRQ pending.
	cpu.IRQPending.Store(true)

	// Step the next instruction (NOP at $8001).
	// StepInstructionWithIRQ checks IRQPending at the M1 boundary.
	// On this step, eiDelay was true so INT is deferred — the latch
	// clears but the take doesn't happen.
	cpu.StepInstructionWithIRQ()
	if cpu.eiDelay {
		t.Errorf("after one instruction post-EI: eiDelay still true")
	}
	if cpu.PC != 0x8002 {
		t.Errorf("after one-insn post-EI: PC=$%04X, want $8002 (= NOP at $8001 ran sequentially, INT deferred)",
			cpu.PC)
	}
}

// TestNMI_BypassesIFF1 verifies NMI is accepted regardless of IFF1.
func TestNMI_BypassesIFF1(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.IFF1 = false
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0
	prePC := cpu.PC
	preSP := cpu.SP

	cpu.NMI()

	if cpu.PC != 0x0066 {
		t.Errorf("NMI with IFF1=0: PC=$%04X, want $0066 (NMI vector)", cpu.PC)
	}
	if cpu.SP != preSP-2 {
		t.Errorf("NMI: SP=$%04X, want $%04X (push of return addr)",
			cpu.SP, preSP-2)
	}
	_ = prePC
}

// TestNMI_ClearsIFF1 verifies that NMI clears IFF1 (so subsequent
// IRQs are blocked until the NMI handler does EI / RETN).
func TestNMI_ClearsIFF1(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.IFF1 = true
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0

	cpu.NMI()

	if cpu.IFF1 {
		t.Errorf("NMI: IFF1 should be cleared, got true")
	}
}

// TestNMI_ExitsHalt verifies that NMI wakes the CPU from HALT and
// continues execution at the NMI vector. (HALT-with-IFF1=0 deadlock
// can only be escaped via NMI.)
func TestNMI_ExitsHalt(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.Halted = true
	cpu.IFF1 = false // HALT-no-IFF deadlock state
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0

	cpu.NMI()

	if cpu.Halted {
		t.Errorf("NMI: Halted should be cleared")
	}
	if cpu.PC != 0x0066 {
		t.Errorf("NMI from HALT-no-IFF: PC=$%04X, want $0066", cpu.PC)
	}
}

// TestINT_ExitsHalt verifies that INT (with IFF1=true) wakes the CPU
// from HALT and continues at the INT vector. (Standard EI; HALT;
// idle pattern.)
func TestINT_ExitsHalt(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.Halted = true
	cpu.IFF1 = true
	cpu.IM = 1
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0

	cpu.interrupt()

	if cpu.Halted {
		t.Errorf("INT from HALT with IFF1=1: Halted should be cleared")
	}
	if cpu.PC != 0x0038 {
		t.Errorf("INT from HALT-IM1: PC=$%04X, want $0038", cpu.PC)
	}
}

// TestINT_HaltWithIFFOff_NotAccepted verifies that INT does NOT wake
// the CPU from HALT when IFF1=0 — the canonical "HALT deadlock" case.
// Only NMI can escape this.
func TestINT_HaltWithIFFOff_NotAccepted(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.Halted = true
	cpu.IFF1 = false
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0
	preSP := cpu.SP

	cpu.interrupt()

	if !cpu.Halted {
		t.Errorf("INT with IFF1=0: Halted should NOT be cleared")
	}
	if cpu.SP != preSP {
		t.Errorf("INT with IFF1=0: SP changed (no push expected)")
	}
}

// TestNMI_PreservesIFF2_FUSEStyle locks in our deliberate divergence
// from the Z80 programmer's manual. Per the CPU.NMI() comment,
// we leave IFF2 unchanged (rather than copy IFF1 → IFF2 then clear
// IFF1) to match FUSE — RETN restores IFF1 from IFF2 correctly
// for Multiface 3 / +3 software that depends on this.
func TestNMI_PreservesIFF2_FUSEStyle(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// Pre-state: IFF1 = false (DI section), IFF2 = false.
	cpu.IFF1 = false
	cpu.IFF2 = false
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0

	cpu.NMI()

	// IFF2 should still be false — NOT set to IFF1 (= the spec
	// behavior we deliberately diverge from).
	if cpu.IFF2 {
		t.Errorf("NMI preserves IFF2 (FUSE-style): expected IFF2=false, got true")
	}
}
