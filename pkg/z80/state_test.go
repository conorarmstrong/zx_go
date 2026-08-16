package z80

import (
	"bytes"
	"testing"
)

// The CPU was missing from the machine capture entirely.
//
// Every peripheral had a Device and the processor did not, so a rewind
// restored the RAM, the sound chip and the disk controller, and left PC, the
// register file, the interrupt flip-flops and the T-state counter exactly
// where the present had them. The machine would resume from the wrong
// instruction with the right memory, which is a far more confusing failure
// than restoring nothing at all.
//
// Tests here follow what a mutation audit taught the AY: assert on the CAPTURE
// rather than on behaviour. Drive the CPU so every captured field changes,
// restore, re-capture, and compare blobs. Behavioural assertions only observe
// the fields that reach behaviour, and 11 of the AY's 19 fields did not.

// primedCPU returns a CPU with a distinct value in every captured field, so
// nothing is left at a zero that a lost restore would coincidentally match.
func primedCPU() *CPU {
	c := New(nil, nil)
	c.A, c.F, c.B, c.C, c.D, c.E, c.H, c.L = 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88
	c.A_, c.F_, c.B_, c.C_ = 0x99, 0xAA, 0xBB, 0xCC
	c.D_, c.E_, c.H_, c.L_ = 0xDD, 0xEE, 0x0F, 0x1F
	c.IX, c.IY = 0x1234, 0x5678
	c.SP, c.PC = 0x9ABC, 0xDEF0
	c.I, c.R = 0x3E, 0x7F
	c.IFF1, c.IFF2 = true, false
	c.IM = 2
	c.Halted = true
	c.HaltWakeOnInt = true
	c.WZ = 0x4321
	c.BranchSource, c.BranchFrom = 3, 0x8000
	c.IRQPending.Store(true)
	c.PendingNMI.Store(true)
	c.LineIntOffsetTstates = 4242
	c.IntAssertTstate, c.IntPulseTstates = 111, 222
	c.FrameIntDisabled = true
	c.IM2Vector = 0xFD
	c.SetTstates(123456)
	c.SetInstructionCount(9999)
	// Unexported interrupt and speed state. Reachable because this test is in
	// package z80, and it must be set: a mutation audit found seven fields
	// surviving deletion purely because no fixture ever moved them.
	c.eiDelay = true
	c.lineIntFired = true
	c.frameIntFired = true
	c.frameIntDeasct = true
	c.nextFrameBoundary = 70908
	c.speedSelect = 3
	c.speedLocked = true
	return c
}

// driveEverythingCPU changes every captured field. Parity matters: an even
// number of flag toggles returns the flag to where it started and hides a lost
// restore, which is how eleven AY fields went uncovered.
func driveEverythingCPU(c *CPU) {
	c.A, c.F, c.B, c.C, c.D, c.E, c.H, c.L = 0xF1, 0xF2, 0xF3, 0xF4, 0xF5, 0xF6, 0xF7, 0xF8
	c.A_, c.F_, c.B_, c.C_ = 0xE1, 0xE2, 0xE3, 0xE4
	c.D_, c.E_, c.H_, c.L_ = 0xE5, 0xE6, 0xE7, 0xE8
	c.IX, c.IY = 0x0FED, 0x0CBA
	c.SP, c.PC = 0x0765, 0x0432
	c.I, c.R = 0x01, 0x02
	c.IFF1, c.IFF2 = false, true // both flip, single toggle each
	c.IM = 1
	c.Halted = false
	c.HaltWakeOnInt = false
	c.WZ = 0x0BEE
	c.BranchSource, c.BranchFrom = 7, 0x0111
	c.IRQPending.Store(false)
	c.PendingNMI.Store(false)
	c.LineIntOffsetTstates = 1
	c.IntAssertTstate, c.IntPulseTstates = 9, 8
	c.FrameIntDisabled = false
	c.IM2Vector = 0x01
	c.SetTstates(7)
	c.SetInstructionCount(3)
	// Single toggles, not pairs: an even number returns the flag to where it
	// started and hides a lost restore.
	c.eiDelay = false
	c.lineIntFired = false
	c.frameIntFired = false
	c.frameIntDeasct = false
	c.nextFrameBoundary = 1
	c.speedSelect = 0
	c.speedLocked = false
}

func TestEveryCapturedCPUFieldSurvivesARoundTrip(t *testing.T) {
	c := primedCPU()
	want := c.SaveState()

	driveEverythingCPU(c)
	if bytes.Equal(c.SaveState(), want) {
		t.Fatal("the drive sequence changed nothing, so this test cannot detect a lost restore")
	}

	if err := c.LoadState(want); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := c.SaveState(); !bytes.Equal(got, want) {
		t.Error("re-capturing after a restore did not reproduce the captured state: " +
			"some field is captured but not restored")
	}
}

// The guard the test above depends on. If a later change leaves a field
// unchanged by the drive sequence, the round trip silently stops covering it.
func TestTheCPUDriveSequenceChangesEveryCapturedField(t *testing.T) {
	c := primedCPU()
	before := c.stateForTest()
	driveEverythingCPU(c)
	after := c.stateForTest()

	if before == after {
		t.Fatal("the drive sequence changed nothing at all")
	}
	for _, f := range []struct {
		name string
		same bool
	}{
		{"A", before.A == after.A}, {"F", before.F == after.F},
		{"B", before.B == after.B}, {"C", before.C == after.C},
		{"D", before.D == after.D}, {"E", before.E == after.E},
		{"H", before.H == after.H}, {"L", before.L == after.L},
		{"A_", before.Alt.A == after.Alt.A}, {"F_", before.Alt.F == after.Alt.F},
		{"B_", before.Alt.B == after.Alt.B}, {"C_", before.Alt.C == after.Alt.C},
		{"D_", before.Alt.D == after.Alt.D}, {"E_", before.Alt.E == after.Alt.E},
		{"H_", before.Alt.H == after.Alt.H}, {"L_", before.Alt.L == after.Alt.L},
		{"IX", before.IX == after.IX}, {"IY", before.IY == after.IY},
		{"SP", before.SP == after.SP}, {"PC", before.PC == after.PC},
		{"I", before.I == after.I}, {"R", before.R == after.R},
		{"IFF1", before.IFF1 == after.IFF1}, {"IFF2", before.IFF2 == after.IFF2},
		{"IM", before.IM == after.IM}, {"Halted", before.Halted == after.Halted},
		{"HaltWakeOnInt", before.HaltWakeOnInt == after.HaltWakeOnInt},
		{"WZ", before.WZ == after.WZ},
		{"BranchSource", before.BranchSource == after.BranchSource},
		{"BranchFrom", before.BranchFrom == after.BranchFrom},
		{"IRQPending", before.IRQPending == after.IRQPending},
		{"PendingNMI", before.PendingNMI == after.PendingNMI},
		{"LineIntOffsetTstates", before.LineIntOffsetTstates == after.LineIntOffsetTstates},
		{"IntAssertTstate", before.IntAssertTstate == after.IntAssertTstate},
		{"IntPulseTstates", before.IntPulseTstates == after.IntPulseTstates},
		{"FrameIntDisabled", before.FrameIntDisabled == after.FrameIntDisabled},
		{"IM2Vector", before.IM2Vector == after.IM2Vector},
		{"Tstates", before.Tstates == after.Tstates},
		{"InstructionCount", before.InstructionCount == after.InstructionCount},
		{"EIDelay", before.EIDelay == after.EIDelay},
		{"LineIntFired", before.LineIntFired == after.LineIntFired},
		{"FrameIntFired", before.FrameIntFired == after.FrameIntFired},
		{"FrameIntDeasserted", before.FrameIntDeasserted == after.FrameIntDeasserted},
		{"NextFrameBoundary", before.NextFrameBoundary == after.NextFrameBoundary},
		{"SpeedSelect", before.SpeedSelect == after.SpeedSelect},
		{"SpeedLocked", before.SpeedLocked == after.SpeedLocked},
	} {
		if f.same {
			t.Errorf("%s is unchanged by the drive sequence, so a lost restore of it "+
				"would go undetected", f.name)
		}
	}
}

// The behavioural claim underneath all of it: a CPU restored from a capture
// executes the same instructions and lands in the same place.
func TestReplayingFromCapturedStateExecutesIdentically(t *testing.T) {
	// flatMem is conformance_test.go's 64K of unpaged RAM, which is all this
	// needs.
	mem := &flatMem{}
	// A short loop with arithmetic, a branch and a stack operation, so the
	// replay exercises flags, PC, SP and memory together.
	prog := []byte{
		0x3E, 0x05, // LD A,5
		0x06, 0x03, // LD B,3
		0x80,       // ADD A,B
		0xF5,       // PUSH AF
		0x3D,       // DEC A
		0x20, 0xFC, // JR NZ,-4
		0xF1, // POP AF
		0x76, // HALT
	}
	copy(mem[0x8000:], prog)

	c := New(mem, nil)
	c.PC = 0x8000
	c.SP = 0xFF00
	for i := 0; i < 6; i++ {
		c.StepInstruction()
	}

	st := c.SaveState()
	first := runAndFingerprint(c, 40)

	if err := c.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := runAndFingerprint(c, 40)

	if first != second {
		t.Errorf("replay from a captured state diverged:\n first  = %+v\n second = %+v", first, second)
	}
}

type cpuPrint struct {
	PC, SP, AF, BC, DE, HL uint16
	Tstates                uint64
	Insns                  uint64
	Halted                 bool
}

func runAndFingerprint(c *CPU, steps int) cpuPrint {
	for i := 0; i < steps; i++ {
		c.StepInstruction()
	}
	return cpuPrint{
		PC: c.PC, SP: c.SP,
		AF: uint16(c.A)<<8 | uint16(c.F),
		BC: c.BC(), DE: c.DE(), HL: c.HL(),
		Tstates: c.Tstates(), Insns: c.InstructionCount(),
		Halted: c.Halted,
	}
}

func TestCPUStateIDIsStable(t *testing.T) {
	if got := New(nil, nil).StateID(); got != "cpu" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "cpu")
	}
}

func TestCPULoadStateRejectsRubbish(t *testing.T) {
	c := New(nil, nil)
	before := c.SaveState()
	if err := c.LoadState([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Fatal("a malformed state blob must be reported, not half-applied")
	}
	if !bytes.Equal(c.SaveState(), before) {
		t.Error("a rejected LoadState must leave the CPU untouched")
	}
}
