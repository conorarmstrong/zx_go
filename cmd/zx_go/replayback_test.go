package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The time-travel ring is the checkpoint store replay steps back from, so what
// it captures decides what a step back can restore. It used to capture the CPU
// registers, the eight visible RAM pages and the border — which is why the old
// reverse mode had to say "no memory": rewinding to one of those checkpoints
// left the sound chip, the keyboard, the disk controller and every unmapped
// bank in the present.
//
// The claim now is that a checkpoint IS the machine. Capture, let the machine
// run on, rewind, and the machine must re-capture as the bytes it was captured
// as — measured through the registry, which covers every device the model
// carries.
//
// The "the machine actually moved" guard is what stops this passing on an
// inert fixture: without it, a restore that did nothing at all would look
// identical to a restore that did everything.
func TestTimeTravelCheckpointIsTheWholeMachine(t *testing.T) {
	for _, model := range []roms.SpectrumModel{roms.Model48K, roms.Model128K, roms.ModelPlus3} {
		t.Run(roms.GetModelName(model), func(t *testing.T) {
			emu := quietEmulator(t, model)
			emu.paused.Store(false)
			for i := 0; i < 60; i++ {
				runOneFrameHeadless(emu, model)
			}

			tt := newTimeTravelBuffer(emu, 1000, 8)
			reg := emu.stateRegistry()
			want := reg.Capture()
			tt.capture("checkpoint", emu.cpu.InstructionCount())

			for i := 0; i < 20; i++ {
				runOneFrameHeadless(emu, model)
			}
			if bytes.Equal(reg.Capture().Bytes(), want.Bytes()) {
				t.Fatal("no registered device changed over 20 frames — the fixture is inert, " +
					"so this proves nothing about the restore")
			}

			e := tt.FindAtOrBefore(emu.cpu.InstructionCount())
			if e == nil {
				t.Fatal("the checkpoint just taken is not in the ring")
			}
			if err := tt.Restore(e); err != nil {
				t.Fatalf("Restore: %v", err)
			}

			if got := reg.Capture(); !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Error("a rewound machine does not re-capture as the machine that was " +
					"captured — the checkpoint is not the whole machine")
			}
		})
	}
}

// replayModels are the machines the replay property is measured on. The Next
// is added by its own test, which skips when its ROMs are absent.
var replayModels = []roms.SpectrumModel{
	roms.Model48K, roms.Model128K, roms.ModelPlus2, roms.ModelPlus2A, roms.ModelPlus3,
}

// armReplayFixture boots a machine into the oracle's exerciser — a guest that
// writes the screen, the border, the speaker and the sound chip every time
// round its loop — hands it to the step driver, and arms the checkpoint ring.
//
// The exerciser is not decoration. A Spectrum left alone after boot sits in its
// ROM keyboard-scan loop with the display file untouched and the AY never
// addressed, so "the replayed machine matches" would be a comparison between
// two idle machines and would hold with most of the capture deleted.
func armReplayFixture(t *testing.T, model roms.SpectrumModel, every, keep int) (*emulator, *timeTravelBuffer) {
	t.Helper()
	emu := quietEmulator(t, model)
	oracleBoot(emu, model, oracleBootFrames)

	tt := newTimeTravelBuffer(emu, every, keep)
	if tt == nil {
		t.Fatalf("newTimeTravelBuffer(%d, %d) refused", every, keep)
	}
	emu.timeTravel = tt
	emu.cpu.AddPreFetchHook("debug-time-travel", tt.Step)
	t.Cleanup(func() { emu.cpu.RemovePreFetchHook("debug-time-travel") })
	return emu, tt
}

// This is the claim the whole capture effort was for.
//
// Run the machine forward under the step driver with checkpoints being taken.
// Fingerprint it at some instant. Run on. Then step back to that instant by
// replay, and require the machine to be the SAME MACHINE — every device, memory
// included, byte for byte — as the one execution passed through on its way
// past.
//
// Nothing weaker is worth having. A step back that lands on the right PC with
// the wrong memory is the failure mode the recorded-history reverse mode has to
// warn about; if replay only got close, it would be that mode with a longer
// error message.
func TestReplayStepBackLandsOnTheMachineThePresentPassedThrough(t *testing.T) {
	for _, model := range replayModels {
		t.Run(roms.GetModelName(model), func(t *testing.T) {
			assertReplayStepBackIsExact(t, model)
		})
	}
}

func TestReplayStepBackLandsOnTheMachineThePresentPassedThroughNext(t *testing.T) {
	if !nextROMsInstalled() {
		t.Skip("Next ROMs not installed")
	}
	assertReplayStepBackIsExact(t, roms.ModelNext)
}

// assertReplayStepBackIsExact steps back a range of distances, each from a
// fresh present, and compares the whole machine.
//
// The distances span a checkpoint interval deliberately: 1 lands inside the
// newest interval, and 400 (with checkpoints every 250 steps) forces the walk
// to start from an older checkpoint than the newest one. A replay that only
// ever looked at the newest checkpoint would pass the first and fail the last.
func assertReplayStepBackIsExact(t *testing.T, model roms.SpectrumModel) {
	t.Helper()
	for _, back := range []int{1, 2, 63, 400} {
		emu, tt := armReplayFixture(t, model, 250, 16)
		for i := 0; i < 3000; i++ {
			emu.cpu.StepInstructionWithIRQ()
		}

		reg := emu.stateRegistry()
		want := reg.Capture()
		wantInsn := emu.cpu.InstructionCount()

		for i := 0; i < back; i++ {
			emu.cpu.StepInstructionWithIRQ()
		}
		if bytes.Equal(reg.Capture().Bytes(), want.Bytes()) {
			t.Fatalf("back=%d: the machine did not move over %d instructions — "+
				"stepping back to an unchanged machine proves nothing", back, back)
		}

		landed, err := tt.replayBack(back)
		if err != nil {
			t.Fatalf("back=%d: replayBack: %v", back, err)
		}
		if landed != wantInsn {
			t.Errorf("back=%d: landed on instruction %d, want %d", back, landed, wantInsn)
		}
		if got := reg.Capture(); !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Errorf("back=%d: the replayed machine is not the machine execution passed "+
				"through at instruction %d", back, wantInsn)
		}
	}
}

// Stepping back repeatedly is the actual workflow, and it is the one that can
// go wrong quietly: after the first step the present is BEHIND checkpoints that
// are still in the ring, so a second step has to ignore the ones ahead of it
// and walk from an older one. Picking a checkpoint from the timeline the
// machine has left would replay the wrong window.
func TestReplayStepBackTwiceWalksBackTwoInstants(t *testing.T) {
	emu, tt := armReplayFixture(t, roms.Model128K, 250, 16)
	for i := 0; i < 2000; i++ {
		emu.cpu.StepInstructionWithIRQ()
	}

	reg := emu.stateRegistry()
	twoBack := reg.Capture()
	emu.cpu.StepInstructionWithIRQ()
	oneBack := reg.Capture()
	emu.cpu.StepInstructionWithIRQ()

	// A checkpoint AT the present, which the first step back leaves behind and
	// the second must ignore. `tt-snap` here, run on, step back is an ordinary
	// session; without this the ring holds nothing ahead of the present and the
	// test would pass whether or not the ahead ones are skipped.
	tt.capture("present", emu.cpu.InstructionCount())

	if _, err := tt.replayBack(1); err != nil {
		t.Fatalf("first step back: %v", err)
	}
	if got := reg.Capture(); !bytes.Equal(got.Bytes(), oneBack.Bytes()) {
		t.Fatal("the first step back did not land on the instant before the present")
	}
	if _, err := tt.replayBack(1); err != nil {
		t.Fatalf("second step back: %v", err)
	}
	if got := reg.Capture(); !bytes.Equal(got.Bytes(), twoBack.Bytes()) {
		t.Error("the second step back did not land on the instant before that")
	}
}

// Stepping back further than the ring reaches must be an error, and must leave
// the machine where it was.
//
// Handing back the oldest checkpoint instead would be the near miss this whole
// mechanism exists to avoid: the caller asked for instruction N and would be
// shown some earlier machine with nothing on screen saying so.
func TestReplayStepBackRefusesToGoFurtherBackThanTheRingReaches(t *testing.T) {
	emu, tt := armReplayFixture(t, roms.Model128K, 500, 2)
	for i := 0; i < 2000; i++ {
		emu.cpu.StepInstructionWithIRQ()
	}

	reg := emu.stateRegistry()
	present := reg.Capture()

	if landed, err := tt.replayBack(1 << 20); err == nil {
		t.Fatalf("stepping back 1048576 instructions was accepted, landing at %d", landed)
	}
	if got := reg.Capture(); !bytes.Equal(got.Bytes(), present.Bytes()) {
		t.Error("a refused step back left the machine somewhere else")
	}
}

// A step back of zero or less has no meaning, and silently treating it as one
// would move a machine the caller asked to leave alone.
func TestReplayStepBackRefusesANonPositiveDistance(t *testing.T) {
	_, tt := armReplayFixture(t, roms.Model48K, 500, 4)
	for _, n := range []int{0, -1} {
		if _, err := tt.replayBack(n); err == nil {
			t.Errorf("replayBack(%d) was accepted", n)
		}
	}
}

// Replay follows the debugger's single-step driver. The frame loop's bulk
// driver is a different timeline — it rebases the T-state counter at every
// frame end and asserts the frame interrupt on its own schedule — so a span the
// machine ran in bulk does not come back the same way.
//
// The requirement is not that replay handle it. It is that replay NOTICE, and
// say so, instead of handing back a machine that never existed. The proof pass
// is what notices: it re-runs the window to the present and compares.
func TestReplayStepBackRefusesASpanTheFrameLoopRanInBulk(t *testing.T) {
	emu, tt := armReplayFixture(t, roms.Model128K, 200, 8)
	for i := 0; i < 600; i++ {
		emu.cpu.StepInstructionWithIRQ()
	}
	for i := 0; i < 3; i++ {
		runOneFrameHeadless(emu, roms.Model128K)
	}

	reg := emu.stateRegistry()
	present := reg.Capture()

	landed, err := tt.replayBack(1)
	if err == nil {
		t.Fatalf("a bulk-driven span replayed as if it were reproducible, landing at %d", landed)
	}
	t.Logf("refused, as it must be: %v", err)
	if got := reg.Capture(); !bytes.Equal(got.Bytes(), present.Bytes()) {
		t.Error("a refused step back left the machine somewhere other than the present")
	}
}

// A checkpoint plus the CPU determines the replay. Anything that reaches into
// the machine from outside it during the window — a debugger write-memory, a
// key press, a disk arriving — does not happen again when the window is
// re-run, so the walk arrives at a machine that is not the present.
//
// Refusing is the only honest answer. The instant asked for genuinely cannot be
// reconstructed from what was kept, and a step back that returned the
// reconstructed-but-different machine would be wrong in exactly the way that is
// hardest to notice: right PC, right registers, one byte of RAM from a
// different history.
func TestReplayStepBackRefusesWhenSomethingOutsideTheCPUChangedTheMachine(t *testing.T) {
	emu, tt := armReplayFixture(t, roms.Model128K, 200, 8)
	for i := 0; i < 600; i++ {
		emu.cpu.StepInstructionWithIRQ()
	}
	// $9000 is RAM the exerciser never touches, so nothing re-writes it during
	// the replay and nothing else can account for the difference.
	emu.mem.Write(0x9000, emu.mem.Read(0x9000)^0xFF)
	for i := 0; i < 20; i++ {
		emu.cpu.StepInstructionWithIRQ()
	}

	reg := emu.stateRegistry()
	present := reg.Capture()

	landed, err := tt.replayBack(1)
	if err == nil {
		t.Fatalf("a window containing a write replay cannot reproduce was accepted, landing at %d", landed)
	}
	t.Logf("refused, as it must be: %v", err)
	if got := reg.Capture(); !bytes.Equal(got.Bytes(), present.Bytes()) {
		t.Error("a refused step back left the machine somewhere other than the present")
	}
}

// Which checkpoints are usable is decided from the position the RING records,
// before anything is restored. That shortcut is only sound while the recorded
// position and the captured state agree, so the walk checks them against each
// other rather than trusting the cheaper copy.
func TestReplayRefusesACheckpointWhoseRecordedPositionIsNotWhereItRestoresTo(t *testing.T) {
	emu, tt := armReplayFixture(t, roms.Model128K, 200, 8)
	for i := 0; i < 600; i++ {
		emu.cpu.StepInstructionWithIRQ()
	}
	cp := tt.FindAtOrBefore(emu.cpu.InstructionCount())
	if cp == nil {
		t.Fatal("no checkpoint to replay from")
	}
	cp.insn += 7 // the ring now says something the state does not

	reg := emu.stateRegistry()
	now := replayPoint{tstates: emu.cpu.Tstates(), insn: emu.cpu.InstructionCount()}
	_, err := tt.walkToPresent(reg, cp, now, reg.Capture())
	if err == nil {
		t.Fatal("a checkpoint that restores somewhere other than where the ring says it sits " +
			"was walked from anyway")
	}
}

// Landing is asserted, not assumed. If the second pass over the same window
// does not arrive where the first one said it would, that is an error — the
// alternative is handing back a machine at the wrong instruction with nothing
// saying so.
func TestReplayLandingSomewhereElseIsAnError(t *testing.T) {
	emu, tt := armReplayFixture(t, roms.Model128K, 200, 8)
	for i := 0; i < 600; i++ {
		emu.cpu.StepInstructionWithIRQ()
	}
	cp := tt.FindAtOrBefore(emu.cpu.InstructionCount())
	if cp == nil {
		t.Fatal("no checkpoint to replay from")
	}

	reg := emu.stateRegistry()
	err := tt.landOn(reg, cp, 5, replayPoint{tstates: 1, insn: 1})
	if err == nil {
		t.Fatal("landing five steps from the checkpoint was accepted as arriving at " +
			"instruction 1, T-state 1")
	}
	if !strings.Contains(err.Error(), "landed on") {
		t.Errorf("the error does not say where it actually landed: %v", err)
	}
}

// A replay re-executes instructions the machine has already run, with the
// capture hook still installed. Left armed it would checkpoint the second pass
// and evict the older entries the walk is standing on — so a step back would
// quietly shorten the history it is reading from, and repeating it would walk
// off the end.
// The distance is deliberately several capture intervals: a step back short
// enough to replay inside one interval could not fire a capture whatever the
// hook did, and would pass with the suppression deleted.
func TestReplayStepBackWritesNoCheckpointsForTheInstantsItRepeats(t *testing.T) {
	const every, back = 100, 500
	emu, tt := armReplayFixture(t, roms.Model128K, every, 16)
	for i := 0; i < 3000; i++ {
		emu.cpu.StepInstructionWithIRQ()
	}

	before := tt.Snapshots()
	if len(before) < 2 {
		t.Fatalf("fixture took %d checkpoints; it must take several for eviction to be possible", len(before))
	}
	if _, err := tt.replayBack(back); err != nil {
		t.Fatalf("replayBack: %v", err)
	}

	after := tt.Snapshots()
	if len(after) != len(before) {
		t.Fatalf("the ring went from %d checkpoints to %d across a replay", len(before), len(after))
	}
	for i := range before {
		if before[i].insn != after[i].insn {
			t.Errorf("checkpoint %d moved from instruction %d to %d across a replay",
				i, before[i].insn, after[i].insn)
		}
	}
}

// With no checkpoint there is nothing to replay from, and that is an error
// rather than a silent no-op.
func TestReplayStepBackWithoutACheckpointIsAnError(t *testing.T) {
	emu := quietEmulator(t, roms.Model48K)
	emu.paused.Store(false)
	for i := 0; i < 20; i++ {
		runOneFrameHeadless(emu, roms.Model48K)
	}
	tt := newTimeTravelBuffer(emu, 500, 4)
	if _, err := tt.replayBack(1); err == nil {
		t.Fatal("replayBack succeeded with an empty ring")
	}
}
