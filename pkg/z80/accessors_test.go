package z80

import "testing"

// Z80 register accessor + SoftReset coverage.
//
// Establishes contract guarantees for the public 16-bit pair accessors
// (HL/BC/DE + setters), the soft-reset semantics (Z80 /RESET-faithful:
// clears PC/I/R/IFF1/IFF2/IM/Halted; leaves SP/IX/IY/A/F/BC/DE/HL/shadows
// intact), the T-state setter, and the RemovePostFetchHook lifecycle.

func TestHLBCDERoundTrip(t *testing.T) {
	cpu, _ := createTestCPU()
	cpu.SetHL(0x1234)
	if got := cpu.HL(); got != 0x1234 {
		t.Errorf("HL round trip: got %04X, want 1234", got)
	}
	if cpu.H != 0x12 || cpu.L != 0x34 {
		t.Errorf("HL split: H=%02X L=%02X, want 12 34", cpu.H, cpu.L)
	}
	cpu.SetBC(0xABCD)
	if got := cpu.BC(); got != 0xABCD {
		t.Errorf("BC round trip: got %04X, want ABCD", got)
	}
	cpu.SetDE(0x00FF)
	if got := cpu.DE(); got != 0x00FF {
		t.Errorf("DE round trip: got %04X, want 00FF", got)
	}
	// Boundaries.
	cpu.SetHL(0x0000)
	if got := cpu.HL(); got != 0x0000 {
		t.Errorf("HL zero: got %04X", got)
	}
	cpu.SetBC(0xFFFF)
	if got := cpu.BC(); got != 0xFFFF {
		t.Errorf("BC max: got %04X", got)
	}
}

func TestSetTstates(t *testing.T) {
	cpu, _ := createTestCPU()
	if cpu.Tstates() != 0 {
		t.Errorf("initial Tstates = %d, want 0", cpu.Tstates())
	}
	cpu.SetTstates(123456789)
	if got := cpu.Tstates(); got != 123456789 {
		t.Errorf("SetTstates round trip: got %d, want 123456789", got)
	}
	// Overflow is fine — uint64 doesn't wrap meaningfully here.
	cpu.SetTstates(0xFFFFFFFFFFFFFFFF)
	if got := cpu.Tstates(); got != 0xFFFFFFFFFFFFFFFF {
		t.Errorf("SetTstates max: got %d", got)
	}
}

// TestSoftResetClearsZ80FaithfulSubset verifies SoftReset matches the
// Z80 /RESET pin spec: PC=0, I=0, R=0, IFF1=IFF2=false, IM=0,
// Halted=false. SP, IX, IY, GP regs, and shadow set are LEFT INTACT —
// this is what differentiates SoftReset from the full Reset (power-up)
// path.
func TestSoftResetClearsZ80FaithfulSubset(t *testing.T) {
	cpu, _ := createTestCPU()
	// Pre-state — everything dirty.
	cpu.A = 0xAB
	cpu.F = 0xCD
	cpu.B = 0x11
	cpu.C = 0x22
	cpu.D = 0x33
	cpu.E = 0x44
	cpu.H = 0x55
	cpu.L = 0x66
	cpu.IX = 0x1234
	cpu.IY = 0x5678
	cpu.SP = 0x9ABC
	cpu.PC = 0xDEAD
	cpu.I = 0xFF
	cpu.R = 0x7F
	cpu.IFF1 = true
	cpu.IFF2 = true
	cpu.IM = 2
	cpu.Halted = true
	cpu.A_ = 0xCC
	cpu.F_ = 0xDD

	cpu.SoftReset()

	// Cleared by /RESET.
	if cpu.PC != 0 {
		t.Errorf("SoftReset PC = %04X, want 0000", cpu.PC)
	}
	if cpu.I != 0 {
		t.Errorf("SoftReset I = %02X, want 00", cpu.I)
	}
	if cpu.R != 0 {
		t.Errorf("SoftReset R = %02X, want 00", cpu.R)
	}
	if cpu.IFF1 || cpu.IFF2 {
		t.Errorf("SoftReset IFF1/IFF2 = %v/%v, want false/false", cpu.IFF1, cpu.IFF2)
	}
	if cpu.IM != 0 {
		t.Errorf("SoftReset IM = %d, want 0", cpu.IM)
	}
	if cpu.Halted {
		t.Errorf("SoftReset Halted = true, want false")
	}

	// Preserved across /RESET.
	if cpu.A != 0xAB || cpu.F != 0xCD {
		t.Errorf("SoftReset clobbered A/F: %02X %02X, want AB CD", cpu.A, cpu.F)
	}
	if cpu.B != 0x11 || cpu.C != 0x22 {
		t.Errorf("SoftReset clobbered BC: %02X %02X", cpu.B, cpu.C)
	}
	if cpu.D != 0x33 || cpu.E != 0x44 {
		t.Errorf("SoftReset clobbered DE: %02X %02X", cpu.D, cpu.E)
	}
	if cpu.H != 0x55 || cpu.L != 0x66 {
		t.Errorf("SoftReset clobbered HL: %02X %02X", cpu.H, cpu.L)
	}
	if cpu.IX != 0x1234 || cpu.IY != 0x5678 {
		t.Errorf("SoftReset clobbered IX/IY: %04X %04X", cpu.IX, cpu.IY)
	}
	if cpu.SP != 0x9ABC {
		t.Errorf("SoftReset clobbered SP: %04X, want 9ABC", cpu.SP)
	}
	if cpu.A_ != 0xCC || cpu.F_ != 0xDD {
		t.Errorf("SoftReset clobbered shadow set: A'=%02X F'=%02X", cpu.A_, cpu.F_)
	}
	// BranchSource = 8 (SourceReset).
	if cpu.BranchSource != 8 {
		t.Errorf("SoftReset BranchSource = %d, want 8 (SourceReset)", cpu.BranchSource)
	}
	if cpu.BranchFrom != 0xDEAD {
		t.Errorf("SoftReset BranchFrom = %04X, want DEAD", cpu.BranchFrom)
	}
}

func TestRemovePostFetchHook(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x00) // NOP

	fires := 0
	cpu.AddPostFetchHook("once", func(pc uint16) { fires++ })
	cpu.RemovePostFetchHook("once")

	cpu.executeInstruction()
	if fires != 0 {
		t.Errorf("removed post-fetch hook still fired %d times", fires)
	}

	// Removing a non-existent post-fetch hook is a no-op.
	cpu.RemovePostFetchHook("never-registered")
}

func TestAddPostFetchHookReplacesByName(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x00)

	var which string
	cpu.AddPostFetchHook("h", func(pc uint16) { which = "first" })
	cpu.AddPostFetchHook("h", func(pc uint16) { which = "second" })

	cpu.executeInstruction()
	if which != "second" {
		t.Errorf("re-add post-fetch by same name should replace: got %q", which)
	}
}

func TestAddPostFetchHookNilRemoves(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x00)

	fires := 0
	cpu.AddPostFetchHook("h", func(pc uint16) { fires++ })
	cpu.AddPostFetchHook("h", nil) // nil-fn variant should deregister

	cpu.executeInstruction()
	if fires != 0 {
		t.Errorf("nil-fn post-fetch add should deregister hook (fires=%d)", fires)
	}
}
