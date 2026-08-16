package main

import (
	"fmt"
	"log/slog"
	"strconv"
)

// `replay-back N` over the telnet protocol. The mechanism is in
// replayback.go; this file is the front-end.
//
// It is deliberately NOT `step-back`, and the two are not going to be merged.
// They answer the same question with different guarantees, and a user cannot
// tell which one they got from the response alone:
//
//   - step-back walks the recorded M1 ring. Instant, reaches back as far as the
//     ring was sized for, and gives REGISTERS ONLY — memory is not recorded per
//     instruction, so a memory read after a step-back is a read of the present.
//   - replay-back re-executes from a time-travel checkpoint. It gives the WHOLE
//     machine, memory included, because the machine really is at that
//     instruction again; it needs `tt-on`, reaches back only as far as the
//     oldest checkpoint, and costs one walk of the checkpoint interval.
//
// Conflating them would mean one command that sometimes carries memory. Someone
// would eventually read a byte off the wrong one, and nothing on screen would
// have told them.

// cmdReplayBack steps the machine back N instructions by replay. Default 1.
func (d *remoteDebugger) cmdReplayBack(args []string) string {
	if d.emu.timeTravel == nil {
		return "ERR replay-back: no checkpoints (replay steps back by re-executing from one, " +
			"so there has to be one — `tt-on EVERY [KEEP]`, then run the machine forward). " +
			"`step-back` needs no checkpoints but gives registers only, not memory"
	}
	n := 1
	if len(args) > 0 {
		v, err := strconv.Atoi(args[0])
		if err != nil || v < 1 {
			return "ERR usage: replay-back [N]   (N >= 1, default 1)"
		}
		n = v
	}
	landed, err := d.emu.timeTravel.replayBack(n)
	if err != nil {
		return "ERR " + err.Error()
	}
	slog.Info("replay-back", "instructions", n, "to_insn", landed, "pc", annotateAddr(d.emu.cpu.PC))
	return fmt.Sprintf("OK replayed back %d instruction(s): insn=%d %s "+
		"(the whole machine, memory included — it is really at that instruction)",
		n, landed, annotateAddr(d.emu.cpu.PC))
}
