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
// The forward path drives the machine the way it was driven. Spans the
// debugger single-stepped are stepped with StepInstructionWithIRQ, which is
// what `step` uses. Spans the frame loop ran are re-run turn by turn with the
// turns the ring recorded (timeTravelBuffer.runFrame): ExecuteFrame rebases the
// T-state counter at each frame end and raises the frame interrupt on its own
// schedule, and the GUI loop renders after each frame, so stepping such a span
// would replay a different machine. Anything the ring did not record, a frame
// run outside the loop or a machine with its own frame driver, is caught by the
// proof pass and refused rather than answered wrongly.

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
		// The frame loop rewinds the T-state counter at every frame end, so
		// T-states order two instants only on the same instruction count.
		if cp.insn > now.insn || (cp.insn == now.insn && cp.tstates > now.tstates) {
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
		if err := t.landOn(&cp, now, len(points)-1-n, target); err != nil {
			return t.refuse(reg, present, err)
		}
		return target.insn, nil
	}

	// Every checkpoint being ahead of the present is a different situation from
	// the ring being too short: the machine has been stepped back past all of
	// them, or reset, so what the ring holds is not a past this present has.
	if ahead == len(entries) && ahead > 0 {
		newest := entries[len(entries)-1]
		return t.refuse(reg, present, fmt.Errorf(
			"replay-back: every checkpoint is ahead of the present — the newest is at "+
				"instruction %d, T-state %d, against the present's instruction %d, T-state %d. "+
				"Take a fresh `tt-snap` and run forward from there",
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
// and returns the instants it passed through — the first being the checkpoint
// itself.
//
// Reaching the present is not enough: the machine it produces has to BE the
// present. The comparison is against the capture taken before the walk started,
// over every registered device, so a forward path that diverged anywhere in the
// window is caught here rather than handed back as a step-back result.
func (t *timeTravelBuffer) walkToPresent(reg *machinestate.Registry, cp *ttEntry, now replayPoint, present machinestate.State) ([]replayPoint, error) {
	points, _, err := t.walk(cp, now, -1)
	if err != nil {
		return nil, err
	}
	if got := reg.Capture(); !bytes.Equal(got.Bytes(), present.Bytes()) {
		return nil, fmt.Errorf("replay-back: re-executing from the checkpoint at instruction %d "+
			"reached the present instruction but produced a DIFFERENT machine, so no instant on "+
			"the way there can be trusted either. Either the window was advanced by something "+
			"the ring does not record (a frame run outside the frame loop, a tape turbo burst, "+
			"a machine with its own frame driver), or something outside the CPU changed the "+
			"machine inside it (a write-memory, a key press, a disk arriving) and does not "+
			"happen again on the way back", cp.insn)
	}
	return points, nil
}

// landOn re-runs the checkpoint forward to the instant at index idx of the walk
// and requires the machine to be at want.
//
// The assertion is the point. Stopping when the instruction counter first
// reaches a value would land on the first boundary carrying it, which for a
// halted CPU or a repeated counter value is a different instant with the same
// number on it.
func (t *timeTravelBuffer) landOn(cp *ttEntry, now replayPoint, idx int, want replayPoint) error {
	points, ran, err := t.walk(cp, now, idx)
	if err != nil {
		return err
	}
	if got := t.here(); len(points) != idx+1 || got != want {
		return fmt.Errorf("replay-back: walking %d instants from the checkpoint at instruction %d "+
			"landed on instruction %d (T-state %d), not the instruction %d (T-state %d) the same "+
			"walk reached a moment ago — execution is not reproducible from this checkpoint",
			idx, cp.insn, got.insn, got.tstates, want.insn, want.tstates)
	}
	t.rewriteHistory(cp, ran, want)
	return nil
}

// rewriteHistory makes the ring describe the past of the machine it has just
// landed on rather than of the present it left.
//
// The turns after the landing are a future this machine no longer has, and a
// turn the landing stopped part way is now a turn that stopped there, as one a
// breakpoint stops; checkpoints after the landing are dropped for the same
// reason, because resuming a cut frame need not retrace the frame that ran
// whole. Without it the next step back would replay the abandoned future and
// arrive somewhere other than the present. A landing that cuts nothing leaves
// the ring as it was: the future after it is still the one recorded.
func (t *timeTravelBuffer) rewriteHistory(cp *ttEntry, ran []ttFrame, at replayPoint) {
	t.mu.Lock()
	defer t.mu.Unlock()
	keep := cp.frame - t.framesBase
	if keep < 0 {
		keep = 0
	}
	if keep > len(t.frames) {
		keep = len(t.frames)
	}
	old := t.frames[keep:]
	changed := len(ran) != len(old)
	if !changed && len(ran) > 0 {
		last := len(ran) - 1
		changed = ran[last].stopped != old[last].stopped || ran[last].end != old[last].end
	}
	if !changed {
		// The landing is on the recorded history, between turns or on a
		// turn's end, and the future after it is still the one it had.
		return
	}
	t.frames = append(t.frames[:keep], ran...)
	kept := t.entries[:0]
	for _, e := range t.entries {
		ahead := e.insn > at.insn || (e.insn == at.insn && e.tstates > at.tstates)
		if !ahead && e.frame <= t.framesBase+len(t.frames) {
			kept = append(kept, e)
		}
	}
	t.entries = kept
}

// walk restores the checkpoint and re-executes forward, one instant at a time,
// until it reaches now or, when stop is not negative, the instant at index stop
// (index 0 is the checkpoint). It returns the instants passed.
//
// The machine is driven the way it was driven the first time. The frame-loop
// turns the ring recorded after the checkpoint are re-run as turns, and the
// spans between them, which nothing recorded, are stepped as the debugger
// steps. Within a turn, every instruction boundary the frame passes is an
// instant, the boundary a breakpoint could have stopped it at; stopping the
// walk at one stops that frame there, as a breakpoint would, and lets the turn
// finish its per-frame work. The end of a turn is an instant too. For the
// headless loop, which does nothing after a frame, a landing inside one is the
// machine the live run had at that boundary; for the GUI loop it is that
// machine with the render applied, as a breakpoint there leaves it.
//
// It also returns the turns it ran, the last of them cut short where the walk
// stopped inside it, which is the history of the machine it leaves.
func (t *timeTravelBuffer) walk(cp *ttEntry, now replayPoint, stop int) ([]replayPoint, []ttFrame, error) {
	if err := t.restoreLocked(cp); err != nil {
		return nil, nil, fmt.Errorf("replay-back: restoring the checkpoint at instruction %d: %w", cp.insn, err)
	}
	start := t.here()
	if start != (replayPoint{tstates: cp.tstates, insn: cp.insn}) {
		// The ring's idea of where this checkpoint sits decided it was usable.
		// If restoring it lands somewhere else, the two disagree and every
		// judgement built on the ring's copy is unsound.
		return nil, nil, fmt.Errorf("replay-back: the checkpoint recorded as instruction %d, T-state %d "+
			"restores as instruction %d, T-state %d", cp.insn, cp.tstates, start.insn, start.tstates)
	}
	points := []replayPoint{start}
	if stop == 0 {
		return points, nil, nil
	}
	var ran []ttFrame
	done := false
	for _, f := range t.framesFrom(cp.frame) {
		if points, done = t.stepTo(points, f.start, stop); done {
			return points, ran, nil
		}
		if t.here() != f.start {
			return nil, nil, fmt.Errorf("replay-back: stepping from the checkpoint at instruction %d "+
				"did not arrive at the frame recorded at instruction %d (T-state %d); it is at "+
				"instruction %d (T-state %d)", cp.insn, f.start.insn, f.start.tstates, t.here().insn, t.here().tstates)
		}
		var cut bool
		if points, cut, done = t.replayFrame(points, f, stop); done {
			if cut {
				f.stopped, f.end = true, t.here()
			}
			return points, append(ran, f), nil
		}
		if t.here() != f.end {
			return nil, nil, fmt.Errorf("replay-back: re-running the frame recorded at instruction %d "+
				"ended at instruction %d (T-state %d), not where it ended the first time, "+
				"instruction %d (T-state %d)", f.start.insn, t.here().insn, t.here().tstates, f.end.insn, f.end.tstates)
		}
		ran = append(ran, f)
	}
	if points, done = t.stepTo(points, now, stop); done {
		return points, ran, nil
	}
	if landed := t.here(); landed != now {
		return nil, nil, fmt.Errorf("replay-back: replaying from instruction %d arrived at instruction %d "+
			"(T-state %d) instead of the present's instruction %d (T-state %d)",
			cp.insn, landed.insn, landed.tstates, now.insn, now.tstates)
	}
	return points, ran, nil
}

// stepTo single-steps until the machine is at to, recording each instant, and
// reports whether it reached the instant at index stop on the way.
//
// Every step advances the T-state counter by at least the four a halted M1
// cycle costs, and nothing in a stepped span rewinds it, so this stops as soon
// as the counter reaches to's. A walk that is already there takes no step:
// stepping "to where we already are" would overshoot and be reported as a
// divergence.
func (t *timeTravelBuffer) stepTo(points []replayPoint, to replayPoint, stop int) ([]replayPoint, bool) {
	for t.here() != to && t.here().tstates < to.tstates {
		t.replayStep()
		points = append(points, t.here())
		if stop == len(points)-1 {
			return points, true
		}
	}
	return points, false
}

// replayFrame re-runs one recorded turn, recording an instant at every
// instruction boundary its frame passes and one at its end, and stops the frame
// at the instant at index stop if that falls inside it.
//
// The frame is run with the ring's own BreakpointCheck in place of the
// debugger's, which must neither stop the replay where the user set a
// breakpoint nor log tracepoints a second time, and with the ULA holding its
// audio back from the host, which has already played this frame.
//
// It reports whether the walk stopped the frame part way (cut) and whether it
// reached the instant at index stop (done).
func (t *timeTravelBuffer) replayFrame(points []replayPoint, f ttFrame, stop int) (out []replayPoint, cut, done bool) {
	cpu := t.emu.cpu
	saved := cpu.BreakpointCheck
	reached := false
	cpu.BreakpointCheck = func(uint16) bool {
		at := t.here()
		if f.stopped && at == f.end {
			return true // where the breakpoint stopped it the first time
		}
		if at == points[len(points)-1] {
			// The frame's first boundary, when no interrupt was taken at
			// its start, is the instant the walk is already standing on:
			// one instant, not two, or a step back would not move.
			return false
		}
		points = append(points, at)
		if stop == len(points)-1 {
			reached = true
			return true
		}
		return false
	}
	if t.emu.ula != nil {
		t.emu.ula.HoldAudio(true)
	}
	t.replaying = true
	defer func() {
		t.replaying = false
		if t.emu.ula != nil {
			t.emu.ula.HoldAudio(false)
		}
		cpu.BreakpointCheck = saved
	}()
	f.run()
	if reached {
		return points, true, true
	}
	if at := t.here(); at != points[len(points)-1] {
		points = append(points, at)
	}
	return points, false, stop == len(points)-1
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
