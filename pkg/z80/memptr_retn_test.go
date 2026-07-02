package z80

import "testing"

// WZ/MEMPTR on RETI / RETN.
//
// Per Sean Young §3.4: ED $4D (RETI) and ED $45 (RETN), plus all
// their mirror encodings, behave like RET for MEMPTR purposes —
// WZ takes the popped return-address value. Without this update,
// a subsequent BIT n,(HL) inside the post-return code reads stale
// F3/F5 bits.

func runRETI(t *testing.T, op byte) *CPU {
	t.Helper()
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.SP = 0xC000
	cpu.WZ = 0xDEAD
	// Stack pre-populated with $1234 as the return address.
	mem.Write(0xC000, 0x34)
	mem.Write(0xC001, 0x12)
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, op)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	return cpu
}

func TestRETI_SetsWZ(t *testing.T) {
	cpu := runRETI(t, 0x4D)
	if cpu.WZ != 0x1234 {
		t.Errorf("RETI: WZ = $%04X, want $1234", cpu.WZ)
	}
}

func TestRETN_SetsWZ(t *testing.T) {
	cpu := runRETI(t, 0x45)
	if cpu.WZ != 0x1234 {
		t.Errorf("RETN: WZ = $%04X, want $1234", cpu.WZ)
	}
}

func TestRETI_MirrorsSetsWZ(t *testing.T) {
	for _, op := range []byte{0x5D, 0x6D, 0x7D} {
		cpu := runRETI(t, op)
		if cpu.WZ != 0x1234 {
			t.Errorf("RETI mirror $%02X: WZ = $%04X, want $1234", op, cpu.WZ)
		}
	}
}

func TestRETN_MirrorsSetsWZ(t *testing.T) {
	for _, op := range []byte{0x55, 0x65, 0x75} {
		cpu := runRETI(t, op)
		if cpu.WZ != 0x1234 {
			t.Errorf("RETN mirror $%02X: WZ = $%04X, want $1234", op, cpu.WZ)
		}
	}
}
