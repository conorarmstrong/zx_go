package main

import (
	"bytes"
	"fmt"

	"github.com/conorarmstrong/zx_go/pkg/machinestate"
)

// Reverse execution by replay: stepping backwards with the whole machine.
//
// The machine still does not run backwards. To reach the instant one
// instruction before the present, this restores the newest checkpoint at or
// before it and re-executes forward to it. What comes back is therefore not a
// recording of some of the machine, it IS the machine — memory, the sound
// chip's counters, the disk controller, every bank — because it was produced
// the same way the present was, by running.
//
// That is the difference from the recorded-history reverse mode in
// pkg/debugger/reverse.go, and the reason both exist. The M1 ring reaches back
// as far as it was sized for and answers instantly, but it records registers,
// so it has to refuse any question about memory. Replay answers every question
// about the machine and can only reach back as far as the oldest checkpoint.
//
// Two things make the answer trustworthy rather than plausible:
//
//   - Landing is by step count from the checkpoint, not by instruction counter.
//     The counter tallies M1 fetches, so a prefixed instruction moves it by
//     more than one and a halted CPU does not move it at all: "re-execute until
//     the counter reads N" can stop on the wrong instruction, or on the right
//     count of the wrong pass round a HALT. Both counters are then asserted at
//     the landing, so a walk that drifted is an error rather than a near miss.
//
//   - The walk is proved before it is used. The first pass runs the checkpoint
//     all the way back to the present and compares the machine it produces with
//     the machine that was there — the whole capture, byte for byte. If the
//     forward path cannot reproduce the present it cannot be trusted to produce
//     any instant on the way to it, so the step back is refused and the present
//     is put back.
//
// The forward driver is StepInstructionWithIRQ, which is what the debugger's
// own `step` uses. Execution driven by ExecuteFrame — the frame loop's bulk
// path — is NOT reproduced by it: ExecuteFrame rebases the T-state counter at
// each frame end and asserts the frame interrupt on its own schedule, so a span
// run that way replays as a different machine. In practice that shows up first
// as every checkpoint sitting AHEAD of the present on the clock, which is
// reported as itself; anything subtler is caught by the proof pass. Either way
// it is a refusal rather than a wrong answer.

// replayPoint is one instruction boundary passed during a replay walk. Both
// counters are kept because both are asserted at the landing: the T-state
// counter is what advances on every step (a halted CPU ticks T-states without
// retiring an instruction), and the instruction counter is what the user asked
// to land on.
type replayPoint struct {
	tstates uint64
	insn    uint64
}

// replayBack moves the machine n instruction boundaries into the past and
// reports the instruction count it landed on.
//
// coreMu is held for the whole operation. A replay restores a checkpoint and
// steps the CPU, so the emulation goroutine must not be inside a frame at the
// same time — and the machine must not be visible to anyone else in the
// half-rewound state a walk passes through.
func (t *timeTravelBuffer) replayBack(n int) (uint64, error) {
	if n < 1 {
		return 0, fmt.Errorf("replay-back: N must be at least 1 (asked for %d)", n)
	}

	t.emu.coreMu.Lock()
	defer t.emu.coreMu.Unlock()

	reg := t.emu.stateRegistry()
	if reg.Len() == 0 {
		return 0, fmt.Errorf("replay-back: this machine registers no devices, so there is " +
			"nothing to capture and nothing to replay")
	}
	present := reg.Capture()
	now := replayPoint{tstates: t.emu.cpu.Tstates(), insn: t.emu.cpu.InstructionCount()}

	// Walk from the newest checkpoint that reaches far enough back, falling
	// back to older ones. The newest is one instruction old immediately after a
	// capture, so a step back of any size would otherwise fail for as long as it
	// took the ring to fill again. Each fallback costs another walk, all of them
	// bounded by the capture interval.
	//
	// Checkpoints AHEAD of the present are skipped rather than walked. They
	// exist routinely: a step back leaves the machine behind entries the ring is
	// still holding, so the next step back has to ignore them or it would replay
	// a window the machine has already left. Both counters are compared, because
	// a halted CPU ticks T-states without retiring an instruction — a checkpoint
	// can be later in time on the same instruction count.
	entries := t.Snapshots()
	ahead := 0
	for i := len(entries) - 1; i >= 0; i-- {
		cp := entries[i]
		if cp.insn > now.insn || cp.tstates > now.tstates {
			ahead++
			continue
		}
		points, err := t.walkToPresent(reg, &cp, now, present)
		if err != nil {
			// An older checkpoint cannot fix a walk that reached the present
			// and produced a different machine — the forward path is what is
			// wrong, and a longer run of it will not be more faithful.
			return t.refuse(reg, present, err)
		}
		if n >= len(points) {
			continue // this checkpoint is too recent; try the one before it
		}
		target := points[len(points)-1-n]
		if err := t.landOn(reg, &cp, len(points)-1-n, target); err != nil {
			return t.refuse(reg, present, err)
		}
		return target.insn, nil
	}

	// Every checkpoint being ahead of the present is a different situation from
	// the ring being too short, and it has a different cause: the machine was
	// advanced by the frame loop's bulk driver, which rebases the T-state
	// counter at every frame end, so what the ring holds is not a past this
	// present has.
	if ahead == len(entries) && ahead > 0 {
		newest := entries[len(entries)-1]
		return t.refuse(reg, present, fmt.Errorf(
			"replay-back: every checkpoint is ahead of the present — the newest is at "+
				"instruction %d, T-state %d, against the present's instruction %d, T-state %d. "+
				"The machine was last advanced by the frame loop's bulk driver, whose timeline "+
				"replay does not reproduce; pause and `step` to put it back on the single-step "+
				"path, or take a fresh `tt-snap` and run forward from there",
			newest.insn, newest.tstates, now.insn, now.tstates))
	}

	oldest := "the ring is empty"
	if len(entries) > 0 {
		oldest = fmt.Sprintf("the oldest usable checkpoint is at instruction %d", entries[0].insn)
	}
	return t.refuse(reg, present, fmt.Errorf(
		"replay-back: cannot reach %d instruction(s) before instruction %d — %s "+
			"(raise KEEP or lower EVERY on tt-on, then run the machine forward again)",
		n, now.insn, oldest))
}

// walkToPresent restores the checkpoint, re-executes forward to the present,
// and returns the instruction boundaries it passed through — the first being
// the checkpoint itself.
//
// Reaching the present is not enough: the machine it produces has to BE the
// present. The comparison is against the capture taken before the walk started,
// over every registered device, so a forward path that diverged anywhere in the
// window is caught here rather than handed back as a step-back result.
func (t *timeTravelBuffer) walkToPresent(reg *machinestate.Registry, cp *ttEntry, now replayPoint, present machinestate.State) ([]replayPoint, error) {
	if err := t.restoreLocked(cp); err != nil {
		return nil, fmt.Errorf("replay-back: restoring the checkpoint at instruction %d: %w", cp.insn, err)
	}
	start := t.here()
	if start != (replayPoint{tstates: cp.tstates, insn: cp.insn}) {
		// The ring's idea of where this checkpoint sits decided it was usable.
		// If restoring it lands somewhere else, the two disagree and every
		// judgement built on the ring's copy is unsound.
		return nil, fmt.Errorf("replay-back: the checkpoint recorded as instruction %d, T-state %d "+
			"restores as instruction %d, T-state %d", cp.insn, cp.tstates, start.insn, start.tstates)
	}

	// Every step advances the T-state counter by at least the four a halted M1
	// cycle costs, so this bounds the walk without assuming anything about what
	// the guest does inside it. A checkpoint taken at the present walks zero
	// steps rather than one: stepping "to where we already are" would overshoot
	// and be reported as a divergence.
	budget := (now.tstates-start.tstates)/4 + 2
	points := []replayPoint{start}
	for points[len(points)-1].tstates < now.tstates {
		if uint64(len(points)) > budget {
			return nil, fmt.Errorf("replay-back: the walk from instruction %d ran past %d steps "+
				"without reaching the present at T-state %d", cp.insn, budget, now.tstates)
		}
		t.replayStep()
		points = append(points, t.here())
	}

	landed := points[len(points)-1]
	if landed != now {
		return nil, fmt.Errorf("replay-back: replaying from instruction %d arrived at instruction %d "+
			"(T-state %d) instead of the present's instruction %d (T-state %d)",
			cp.insn, landed.insn, landed.tstates, now.insn, now.tstates)
	}
	if got := reg.Capture(); !bytes.Equal(got.Bytes(), present.Bytes()) {
		return nil, fmt.Errorf("replay-back: re-executing from the checkpoint at instruction %d "+
			"reached the present instruction but produced a DIFFERENT machine, so no instant on "+
			"the way there can be trusted either. Either the window was advanced by the frame "+
			"loop's bulk driver, which replay does not reproduce, or something outside the CPU "+
			"changed the machine inside it (a write-memory, a key press, a disk arriving) and "+
			"does not happen again on the way back", cp.insn)
	}
	return points, nil
}

// landOn re-runs the checkpoint forward by exactly steps instruction
// boundaries and requires the machine to be at want.
//
// The assertion is the point. Stopping when the instruction counter first
// reaches a value would land on the first boundary carrying it, which for a
// halted CPU or a repeated counter value is a different instant with the same
// number on it.
func (t *timeTravelBuffer) landOn(reg *machinestate.Registry, cp *ttEntry, steps int, want replayPoint) error {
	if err := t.restoreLocked(cp); err != nil {
		return fmt.Errorf("replay-back: restoring the checkpoint at instruction %d: %w", cp.insn, err)
	}
	for i := 0; i < steps; i++ {
		t.replayStep()
	}
	if got := t.here(); got != want {
		return fmt.Errorf("replay-back: %d steps from the checkpoint at instruction %d landed on "+
			"instruction %d (T-state %d), not the instruction %d (T-state %d) the same walk "+
			"reached a moment ago — execution is not reproducible from this checkpoint",
			steps, cp.insn, got.insn, got.tstates, want.insn, want.tstates)
	}
	return nil
}

// replayStep advances one instruction boundary, with checkpoint capture
// suppressed.
//
// Capture has to be off. The pre-fetch hook that fills the ring is still
// installed during a walk, so leaving it armed would write checkpoints of
// instants the machine is passing for the second time, and evict the older ones
// the walk itself is standing on.
//
// The debugger's M1 history hook is deliberately left armed, because the
// instructions really are being executed again and the newest ring entry after
// a walk is the instruction the machine has landed on — the ring stays
// consistent with the present. The cost is that a long walk writes a second
// copy of the window into it, shortening how far `step-back` can then reach.
func (t *timeTravelBuffer) replayStep() {
	t.replaying = true
	t.emu.cpu.StepInstructionWithIRQ()
	t.replaying = false
}

// here is the machine's position on its timeline.
func (t *timeTravelBuffer) here() replayPoint {
	return replayPoint{tstates: t.emu.cpu.Tstates(), insn: t.emu.cpu.InstructionCount()}
}

// refuse abandons the step back, returns the machine to the present, and
// reports why.
//
// A caller that asked for an instant replay could not produce must be left
// where it was rather than part way down the failed walk. If even that cannot
// be done the machine is genuinely inconsistent, and both facts are reported —
// the second one matters more, because it means nothing on this machine can be
// trusted from here.
func (t *timeTravelBuffer) refuse(reg *machinestate.Registry, present machinestate.State, err error) (uint64, error) {
	if rbErr := reg.Restore(present); rbErr != nil {
		return 0, fmt.Errorf("%w — AND the machine could not be returned to the present "+
			"afterwards (%v), so it is now neither where it was nor where it was going",
			err, rbErr)
	}
	return 0, err
}
