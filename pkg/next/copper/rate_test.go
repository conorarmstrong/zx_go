package copper

import "testing"

// The per-scanline instruction budget used to be a flat 64, derived from the
// CPU clock ("at 3.5 MHz the Copper runs ~one instruction per 4 CPU
// T-states"). That reasoning does not hold: the copper is clocked from
// i_CLK_28 (zxnext.vhd:3942-3944), so its throughput is fixed by the video
// clock and does not move with the CPU speed at all. A copper list with more
// than 64 instructions on one scanline was being starved.

// TestInstructionsPerScanlineMatchesTheCopperClock pins the budget against
// the hardware: hcount advances at the 7 MHz pixel clock, the copper runs at
// 28 MHz, so there are four copper clocks per hcount tick, and a MOVE
// occupies two of them.
func TestInstructionsPerScanlineMatchesTheCopperClock(t *testing.T) {
	for _, tc := range []struct {
		name          string
		hcountPerLine int
		want          int
	}{
		{"48K line (448 columns)", 448, 896},
		{"128K line (456 columns)", 456, 912},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := InstructionsPerScanline(tc.hcountPerLine); got != tc.want {
				t.Errorf("InstructionsPerScanline(%d) = %d, want %d",
					tc.hcountPerLine, got, tc.want)
			}
			if InstructionsPerScanline(tc.hcountPerLine) <= 64 {
				t.Error("budget is still the CPU-derived 64")
			}
		})
	}
}

// TestLongMoveListRetiresWithinOneScanline pins the practical consequence: a
// copper list writing several hundred registers on a single line must fully
// retire within that line's budget, as it does on hardware.
func TestLongMoveListRetiresWithinOneScanline(t *testing.T) {
	const moves = 500

	c := New()
	// A list of `moves` MOVEs to NextReg 0x40.
	c.SetWritePtrLow(0)
	for i := 0; i < moves; i++ {
		c.WriteData(0x40) // MOVE: reg 0x40
		c.WriteData(byte(i))
	}
	rw := &fakeRegWriter{}
	c.SetRegWriter(rw)
	c.SetWritePtrHighAndMode(byte(StartFromZero) << 6)

	// The budget is in copper clocks, and a MOVE costs two of them.
	c.Step(0, 511, ClocksPerScanline(456))

	if len(rw.writes) != moves {
		t.Errorf("retired %d MOVEs in one scanline, want %d", len(rw.writes), moves)
	}
}
