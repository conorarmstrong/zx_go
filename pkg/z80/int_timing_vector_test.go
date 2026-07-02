package z80

import "testing"

// Interrupt + NMI T-state counts, IM 0/1/2 vector handling, and NMI
// semantics (vector $0066, PC push, IFF/IM effects): IM1=13T,
// IM2=19T, NMI=11T.

// intCPU returns a test CPU primed for an interrupt: IFF1 set, SP in
// high RAM, a known PC, the requested IM.
func intCPU(t *testing.T, im byte) (*CPU, interface {
	Read(uint16) byte
}) {
	t.Helper()
	cpu, mem := createTestCPU()
	t.Cleanup(func() { cleanupTestROMs("test_roms_z80") })
	cpu.IFF1 = true
	cpu.IFF2 = true
	cpu.IM = im
	cpu.SP = 0x9000
	cpu.PC = 0x1234
	return cpu, mem
}

func TestINT_IM0_VectorAndTiming(t *testing.T) {
	cpu, mem := intCPU(t, 0)
	before := cpu.tstates
	cpu.interrupt()
	if got := cpu.tstates - before; got != 13 {
		t.Errorf("IM0 INT = %d T, want 13", got)
	}
	if cpu.PC != 0x0038 {
		t.Errorf("IM0 PC = $%04X, want $0038", cpu.PC)
	}
	// Return address $1234 pushed at SP (SP decremented by 2).
	if cpu.SP != 0x8FFE {
		t.Errorf("IM0 SP = $%04X, want $8FFE", cpu.SP)
	}
	lo := mem.Read(0x8FFE)
	hi := mem.Read(0x8FFF)
	if uint16(hi)<<8|uint16(lo) != 0x1234 {
		t.Errorf("IM0 pushed PC = $%02X%02X, want $1234", hi, lo)
	}
	if cpu.IFF1 {
		t.Error("IM0 INT should clear IFF1")
	}
}

func TestINT_IM1_VectorAndTiming(t *testing.T) {
	cpu, _ := intCPU(t, 1)
	before := cpu.tstates
	cpu.interrupt()
	if got := cpu.tstates - before; got != 13 {
		t.Errorf("IM1 INT = %d T, want 13", got)
	}
	if cpu.PC != 0x0038 {
		t.Errorf("IM1 PC = $%04X, want $0038", cpu.PC)
	}
	if cpu.WZ != 0x0038 {
		t.Errorf("IM1 WZ = $%04X, want $0038 (CALL-like)", cpu.WZ)
	}
}

func TestINT_IM2_VectorAndTiming(t *testing.T) {
	cpu, mem := intCPU(t, 2)
	cpu.I = 0x80
	cpu.IM2Vector = 0xFF // ULA data-bus value → vector table addr $80FF
	// Vector table entry at $80FF/$8100 → handler $C0DE.
	wmem := mem.(interface {
		Read(uint16) byte
		Write(uint16, byte)
	})
	wmem.Write(0x80FF, 0xDE)
	wmem.Write(0x8100, 0xC0)
	before := cpu.tstates
	cpu.interrupt()
	if got := cpu.tstates - before; got != 19 {
		t.Errorf("IM2 INT = %d T, want 19", got)
	}
	if cpu.PC != 0xC0DE {
		t.Errorf("IM2 PC = $%04X, want $C0DE (indirect via I:vector)", cpu.PC)
	}
	if cpu.WZ != 0xC0DE {
		t.Errorf("IM2 WZ = $%04X, want $C0DE", cpu.WZ)
	}
}

func TestINT_IFF1Off_NoVector(t *testing.T) {
	cpu, _ := intCPU(t, 1)
	cpu.IFF1 = false
	before := cpu.tstates
	cpu.interrupt()
	if cpu.PC != 0x1234 || cpu.tstates != before {
		t.Errorf("INT with IFF1=0 should be a no-op; PC=$%04X dT=%d", cpu.PC, cpu.tstates-before)
	}
}

func TestNMI_VectorTimingAndState(t *testing.T) {
	cpu, mem := intCPU(t, 2)
	cpu.IM = 2 // IM must be unchanged by NMI
	before := cpu.tstates
	cpu.NMI()
	if got := cpu.tstates - before; got != 11 {
		t.Errorf("NMI = %d T, want 11", got)
	}
	if cpu.PC != 0x0066 {
		t.Errorf("NMI PC = $%04X, want $0066", cpu.PC)
	}
	if cpu.IM != 2 {
		t.Errorf("NMI changed IM to %d, want 2 (unchanged)", cpu.IM)
	}
	if cpu.IFF1 {
		t.Error("NMI should clear IFF1")
	}
	// Return address $1234 pushed.
	lo := mem.Read(0x8FFE)
	hi := mem.Read(0x8FFF)
	if uint16(hi)<<8|uint16(lo) != 0x1234 {
		t.Errorf("NMI pushed PC = $%02X%02X, want $1234", hi, lo)
	}
	if cpu.SP != 0x8FFE {
		t.Errorf("NMI SP = $%04X, want $8FFE", cpu.SP)
	}
}
