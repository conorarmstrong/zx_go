package main

import (
	"bytes"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Restoring a captured machine keeps the line interrupt it was captured with.
// A restore once re-ran the timing observers, which recomputed the CPU's
// line-interrupt fields from NR$22/$23 before those had been restored.
func TestRestoreKeepsTheLineInterrupt(t *testing.T) {
	emu, err := newEmulator(roms.ModelNext)
	if err != nil || emu.nextRegs == nil {
		t.Skipf("Next core unavailable: %v", err)
	}
	d := emu.nextRegs
	d.WriteReg(0x23, 100)
	d.WriteReg(0x22, 0x02)
	reg := emu.stateRegistry()
	saved := reg.Capture()
	off := emu.cpu.LineIntOffsetTstates
	d.WriteReg(0x22, 0x04) // line interrupt off, frame INT disabled
	if err := reg.Restore(saved); err != nil {
		t.Fatal(err)
	}
	if emu.cpu.LineIntOffsetTstates != off || emu.cpu.FrameIntDisabled {
		t.Errorf("after restore: line offset %d (want %d), frame INT disabled %v (want false)",
			emu.cpu.LineIntOffsetTstates, off, emu.cpu.FrameIntDisabled)
	}
}

// A machine-timing change latched inside a frame the loop ran replays exactly:
// the frame sync is part of the recorded turn, so stepping back across it
// lands on the machine the live run passed through.
func TestReplayAcrossAMachineTimingChange(t *testing.T) {
	if _, err := newEmulator(roms.ModelNext); err != nil {
		t.Skipf("Next core unavailable: %v", err)
	}
	emu, tt := armReplayFixture(t, roms.ModelNext, 20000, 16)
	for i := 0; i < 3; i++ {
		runOneFrameHeadless(emu, roms.ModelNext)
	}
	// The guest makes the change: DI; NEXTREG $03,$93 (48K timing); JR $.
	emu.mem.SetMMU(4, 0x20)
	for i, b := range []byte{0xF3, 0xED, 0x91, 0x03, 0x93, 0x18, 0xFE} {
		emu.mem.Write(0x8000+uint16(i), b)
	}
	emu.cpu.PC = 0x8000
	reg := emu.stateRegistry()
	passed := reg.Capture()
	passedAt := emu.cpu.InstructionCount()

	n := newInstantCounter(emu)
	emu.cpu.BreakpointCheck = n.check(0)
	runOneFrameHeadless(emu, roms.ModelNext) // runs the NEXTREG
	n.frameEnd()
	runOneFrameHeadless(emu, roms.ModelNext) // its frame sync latches 48K timing
	n.frameEnd()
	if got := emu.mem.TimingModel(); got != roms.Model48K {
		t.Fatalf("timing after the frame %v, want 48K", got)
	}
	const stopAfter = 300
	emu.cpu.BreakpointCheck = n.check(stopAfter)
	runOneFrameHeadless(emu, roms.ModelNext)
	emu.cpu.BreakpointCheck = nil

	landed, err := tt.replayBack(n.instants)
	if err != nil {
		t.Fatalf("replay-back %d: %v", n.instants, err)
	}
	if landed != passedAt {
		t.Errorf("landed on instruction %d, want %d", landed, passedAt)
	}
	if !bytes.Equal(reg.Capture().Bytes(), passed.Bytes()) {
		t.Error("replay across the timing change landed on a different machine")
	}
}

// The same across a 50/60 Hz switch: NR$05 bit 2 latches at the frame sync
// inside a frame the loop ran, and stepping back across it lands on the
// machine the live run passed through, the effective 50/60 Hz included.
func TestReplayAcrossAFiftySixtyChange(t *testing.T) {
	if _, err := newEmulator(roms.ModelNext); err != nil {
		t.Skipf("Next core unavailable: %v", err)
	}
	emu, tt := armReplayFixture(t, roms.ModelNext, 20000, 16)
	for i := 0; i < 3; i++ {
		runOneFrameHeadless(emu, roms.ModelNext)
	}
	// The guest makes the change: DI; NEXTREG $05,$04 (60 Hz); JR $.
	emu.mem.SetMMU(4, 0x20)
	for i, b := range []byte{0xF3, 0xED, 0x91, 0x05, 0x04, 0x18, 0xFE} {
		emu.mem.Write(0x8000+uint16(i), b)
	}
	emu.cpu.PC = 0x8000
	reg := emu.stateRegistry()
	passed := reg.Capture()
	passedAt := emu.cpu.InstructionCount()

	n := newInstantCounter(emu)
	emu.cpu.BreakpointCheck = n.check(0)
	runOneFrameHeadless(emu, roms.ModelNext) // runs the NEXTREG; its sync latches 60 Hz
	n.frameEnd()
	if !emu.video60Hz {
		t.Fatal("60 Hz did not latch")
	}
	const stopAfter = 300
	emu.cpu.BreakpointCheck = n.check(stopAfter)
	runOneFrameHeadless(emu, roms.ModelNext)
	emu.cpu.BreakpointCheck = nil

	landed, err := tt.replayBack(n.instants)
	if err != nil {
		t.Fatalf("replay-back %d: %v", n.instants, err)
	}
	if landed != passedAt {
		t.Errorf("landed on instruction %d, want %d", landed, passedAt)
	}
	if emu.video60Hz || emu.ula.SixtyHz() || emu.mem.SixtyHz() {
		t.Error("the replay landed at 50 Hz's instant still running 60 Hz")
	}
	if !bytes.Equal(reg.Capture().Bytes(), passed.Bytes()) {
		t.Error("replay across the 50/60 Hz change landed on a different machine")
	}
}
