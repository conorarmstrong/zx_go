package sam

import "testing"

// TestMachineStartsInROM0 confirms a freshly built SAM machine resets with
// PC=$0000 reading ROM0's first byte (the DI reset entry).
func TestMachineStartsInROM0(t *testing.T) {
	m, err := NewFromROM(synthROM())
	if err != nil {
		t.Fatalf("NewFromROM: %v", err)
	}
	if m.CPU.PC != 0x0000 {
		t.Errorf("reset PC = %#04x, want $0000", m.CPU.PC)
	}
	if got := m.Mem.Read(m.CPU.PC); got != 0xF3 {
		t.Errorf("byte at reset PC = %#02x, want 0xF3 (DI / ROM0)", got)
	}
}

// TestMachineRunsAFrame confirms the CPU executes against ROM0 without
// faulting; this is a liveness check, not a boot check.
func TestMachineRunsAFrame(t *testing.T) {
	m, err := NewFromROM(synthROM())
	if err != nil {
		t.Fatalf("NewFromROM: %v", err)
	}
	before := m.CPU.InstructionCount()
	m.RunFrame()
	if m.CPU.InstructionCount() <= before {
		t.Errorf("CPU did not execute a frame: instruction count %d -> %d", before, m.CPU.InstructionCount())
	}
}
