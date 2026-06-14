package main

import (
	"fmt"
	"log/slog"
	"strings"
)

// cmdTimeTravel implements the live `tt` / `tt-status` and the
// sub-commands `tt-snap`, `tt-rewind`, `tt-find-pc`, `tt-clear`,
// `tt-on`, `tt-off`. Routed from the central dispatch in
// (remoteDebugger).handleCommand.
//
// The buffer itself lives on emulator.timeTravel (shared with the
// visual debugger's Time-Travel tab) and is created by `tt-on N K`
// (auto-capture every N insns, keep K snapshots). The pre-fetch hook
// is installed when the buffer comes up and removed via `tt-off`.
func (d *remoteDebugger) cmdTimeTravel(sub string, args []string) string {
	switch sub {
	case "tt", "tt-status":
		return d.ttStatus()
	case "tt-on":
		return d.ttOn(args)
	case "tt-off":
		return d.ttOff()
	case "tt-clear":
		return d.ttClear()
	case "tt-snap":
		return d.ttSnap(args)
	case "tt-rewind":
		return d.ttRewind(args)
	case "tt-find-pc":
		return d.ttFindPC(args)
	}
	return "ERR time-travel: unknown subcommand"
}

func (d *remoteDebugger) ttStatus() string {
	if d.emu.timeTravel == nil {
		return "OK time-travel off"
	}
	tt := d.emu.timeTravel
	entries := tt.Snapshots()
	var b strings.Builder
	fmt.Fprintf(&b, "OK time-travel on every=%d keep=%d entries=%d",
		tt.everyInsns, tt.maxEntries, len(entries))
	for _, e := range entries {
		fmt.Fprintf(&b, "\r\n  insn=%d  PC=$%04X", e.insn, e.pc)
		if e.label != "" {
			fmt.Fprintf(&b, "  [%s]", e.label)
		}
		if e.bankCtx != "" {
			fmt.Fprintf(&b, "  %s", e.bankCtx)
		}
	}
	return b.String()
}

func (d *remoteDebugger) ttOn(args []string) string {
	// Defaults: every 50,000 insns, keep 16 snapshots.
	everyInsns := 50000
	keep := 16
	if len(args) >= 1 {
		var v int
		if _, err := fmt.Sscanf(args[0], "%d", &v); err == nil && v > 0 {
			everyInsns = v
		} else {
			return "ERR tt-on: bad EVERY value " + args[0]
		}
	}
	if len(args) >= 2 {
		var v int
		if _, err := fmt.Sscanf(args[1], "%d", &v); err == nil && v > 0 {
			keep = v
		} else {
			return "ERR tt-on: bad KEEP value " + args[1]
		}
	}
	if d.emu.timeTravel != nil {
		d.emu.cpu.RemovePreFetchHook("debug-time-travel")
		d.emu.timeTravel = nil
	}
	tt := newTimeTravelBuffer(d.emu, everyInsns, keep)
	if tt == nil {
		return "ERR tt-on: refused (bad args)"
	}
	d.emu.timeTravel = tt
	d.emu.cpu.AddPreFetchHook("debug-time-travel", tt.Step)
	return fmt.Sprintf("OK time-travel on every=%d keep=%d", everyInsns, keep)
}

func (d *remoteDebugger) ttOff() string {
	if d.emu.timeTravel == nil {
		return "OK time-travel already off"
	}
	d.emu.cpu.RemovePreFetchHook("debug-time-travel")
	d.emu.timeTravel = nil
	return "OK time-travel off"
}

func (d *remoteDebugger) ttClear() string {
	if d.emu.timeTravel == nil {
		return "ERR time-travel off (try `tt-on EVERY [KEEP]`)"
	}
	d.emu.timeTravel.Clear()
	return "OK time-travel cleared"
}

func (d *remoteDebugger) ttSnap(args []string) string {
	if d.emu.timeTravel == nil {
		return "ERR time-travel off (try `tt-on EVERY [KEEP]`)"
	}
	label := ""
	if len(args) > 0 {
		label = strings.Join(args, " ")
	}
	d.emu.timeTravel.capture(label, d.emu.cpu.InstructionCount())
	return fmt.Sprintf("OK snap captured insn=%d PC=$%04X label=%q",
		d.emu.cpu.InstructionCount(), d.emu.cpu.PC, label)
}

func (d *remoteDebugger) ttRewind(args []string) string {
	if d.emu.timeTravel == nil {
		return "ERR time-travel off (try `tt-on EVERY [KEEP]`)"
	}
	if len(args) < 1 {
		return "ERR tt-rewind: need INSN target"
	}
	var target uint64
	if _, err := fmt.Sscanf(args[0], "%d", &target); err != nil {
		return "ERR tt-rewind: bad INSN " + args[0]
	}
	e := d.emu.timeTravel.FindAtOrBefore(target)
	if e == nil {
		return fmt.Sprintf("ERR tt-rewind: no snapshot ≤ %d", target)
	}
	if err := d.emu.timeTravel.Restore(e); err != nil {
		return "ERR tt-rewind: restore failed: " + err.Error()
	}
	slog.Info("tt-rewind", "to_insn", e.insn, "pc", annotateAddr(e.pc))
	return fmt.Sprintf("OK rewound to insn=%d PC=$%04X label=%q",
		e.insn, e.pc, e.label)
}

func (d *remoteDebugger) ttFindPC(args []string) string {
	if d.emu.timeTravel == nil {
		return "ERR time-travel off (try `tt-on EVERY [KEEP]`)"
	}
	if len(args) < 1 {
		return "ERR tt-find-pc: need PC hex"
	}
	pc, err := parseHex(args[0])
	if err != nil {
		return "ERR tt-find-pc: bad hex " + args[0]
	}
	hits := d.emu.timeTravel.FindPC(pc)
	if len(hits) == 0 {
		return fmt.Sprintf("OK tt-find-pc $%04X: no matches", pc)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "OK tt-find-pc $%04X: %d match(es)", pc, len(hits))
	for _, e := range hits {
		fmt.Fprintf(&b, "\r\n  insn=%d", e.insn)
		if e.label != "" {
			fmt.Fprintf(&b, "  [%s]", e.label)
		}
	}
	return b.String()
}
