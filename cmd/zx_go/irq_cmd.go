package main

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// cmdIRQStats reports interrupt-taken vs interrupt-rejected counts
// since the last `irq-stats reset` (or process boot). Surfaces the
// canonical "IRQ pulse missed every frame" pattern in one line.
func (d *remoteDebugger) cmdIRQStats(args []string) string {
	if len(args) >= 1 && strings.ToLower(args[0]) == "reset" {
		d.irqStatsBaseTaken = z80.IntFireCount
		d.irqStatsBaseRejected = z80.IntRejectCount
		return "OK irq-stats baseline reset"
	}
	taken := z80.IntFireCount - d.irqStatsBaseTaken
	rejected := z80.IntRejectCount - d.irqStatsBaseRejected
	lastTakenInfo := "—"
	if taken > 0 {
		lastTakenInfo = fmt.Sprintf("PC=$%04X insns=%d", z80.LastIntPC, z80.LastIntInsns)
	}
	lastRejectInfo := "—"
	if rejected > 0 {
		lastRejectInfo = fmt.Sprintf("PC=$%04X insns=%d", z80.LastRejectPC, z80.LastRejectInsns)
	}
	return fmt.Sprintf("OK taken=%d rejected=%d  last-taken=%s  last-rejected=%s",
		taken, rejected, lastTakenInfo, lastRejectInfo)
}

// cmdCatchIRQ arms / disarms the "halt on next IRQ-taken" trap.
//
//	catch irq        arm (halt on next taken IRQ)
//	catch irq off    disarm
func (d *remoteDebugger) cmdCatchIRQ(args []string) string {
	if len(args) >= 1 && strings.ToLower(args[0]) == "off" {
		d.catchIRQ.Store(false)
		return "OK catch irq off"
	}
	d.catchIRQ.Store(true)
	d.ensureIRQHook()
	return "OK catch irq armed"
}

// ensureIRQHook plants the OnInterruptTakenHook once. The hook
// halts the CPU when catchIRQ is set, logs an "irq-taken" record
// otherwise no-op cost.
func (d *remoteDebugger) ensureIRQHook() {
	if d.irqHookInstalled {
		return
	}
	d.irqHookInstalled = true
	z80.OnInterruptTakenHook = func(pcBeforePush uint16) {
		if d.catchIRQ.Load() {
			d.paused.Store(true)
			slog.Info("catch-irq hit", "pc", fmt.Sprintf("$%04X", pcBeforePush))
			d.snapshotOnBPHit(pcBeforePush, "catch-irq")
		}
	}
}
