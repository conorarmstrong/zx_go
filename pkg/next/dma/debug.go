package dma

import "fmt"

// DebugLines describes the controller for a debugger: the ports, the counter,
// the timing and where the transfer engine stands. It reads only; nothing in
// the device moves.
func (d *DMA) DebugLines() []string {
	port := func(name string, start, cur uint16, io bool, mode, cyc byte) string {
		kind := "mem"
		if io {
			kind = "io "
		}
		step := [...]string{"dec", "inc", "fixed", "fixed"}[mode&3]
		return fmt.Sprintf("%s  %s start $%04X  now $%04X  %-5s  cycle %d", name, kind, start, cur, step, cyc)
	}
	dir := "A->B"
	if !d.aToB {
		dir = "B->A"
	}
	mode := "continuous"
	if d.mode == modeBurst {
		mode = "burst"
	}
	chip := "zxn"
	if d.zMode {
		chip = "z80"
	}
	state := "idle"
	switch {
	case d.wedged:
		state = "wedged in R4_BYTE_2"
	case d.stalled:
		state = fmt.Sprintf("parked by dma_delay_i, %d bytes left", d.remaining)
	case d.activeBurst:
		state = fmt.Sprintf("burst in flight, %d bytes left, next due T%d", d.remaining, d.nextDue)
	case d.inTransfer:
		state = "moving a block"
	}
	return []string{
		fmt.Sprintf("mode %s  %s  %s  prescaler %d  auto-restart %v", chip, dir, mode, d.prescaler, d.autoRestart),
		port("A", d.portAStart, d.curA, d.aIsIO, d.aMode, d.aCycleLen),
		port("B", d.portBStart, d.curB, d.bIsIO, d.bMode, d.bCycleLen),
		fmt.Sprintf("length %d  counter %d  status $%02X  end-of-block %v  at-least-one %v", d.blockLen, d.counter, d.statusByte(), d.endOfBlock, d.atLeastOne),
		fmt.Sprintf("dma_delay_i %v  state: %s  last block %d T", d.busDelay, state, d.lastDuration),
	}
}
