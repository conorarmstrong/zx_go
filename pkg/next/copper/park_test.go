package copper

import "testing"

// The copper has no halt. A list is terminated by parking it on a WAIT that
// its raster never satisfies, and the idiomatic terminator $FFFF is just
// WAIT x=63, y=511: it parks because vcount never reaches 511, not because the
// word is special. device/copper.vhd has one WAIT branch and no other stop
// condition (device/copper.vhd:91-98).
//
// Both behaviours below were proved under GHDL against the real
// device/copper.vhd (_tools/copper-vhdl-test).

// TestAllOnesWordIsAWaitNotAHalt pins the decode.
func TestAllOnesWordIsAWaitNotAHalt(t *testing.T) {
	got := Decode(0xFFFF)
	if got.Op != OpWAIT || got.X != 63 || got.Y != 511 {
		t.Errorf("Decode($FFFF) = %+v, want WAIT x=63 y=511", got)
	}
}

// TestAllOnesWordParksOnItsLineNotOnItsColumn pins WHY it parks. With the
// column-63 wrap its horizontal threshold is 4, cleared almost immediately, so
// the only thing holding the list is the vcount = 511 equality.
func TestAllOnesWordParksOnItsLineNotOnItsColumn(t *testing.T) {
	c := New()
	c.SetWritePtrLow(0)
	c.WriteData(0xFF)
	c.WriteData(0xFF)
	c.WriteData(0x50) // MOVE reg $50
	c.WriteData(0xAA) // val $AA
	rw := &fakeRegWriter{}
	c.SetRegWriter(rw)
	c.SetWritePtrHighAndMode(byte(StartFromZero) << 6)

	for y := uint16(0); y < 312; y++ {
		for hc := uint16(0); hc < 448; hc++ {
			c.Step(y, hc, ClocksPerHCount)
		}
	}
	if len(rw.writes) != 0 {
		t.Fatalf("$FFFF released somewhere in a whole frame; writes = %+v", rw.writes)
	}
	// On line 511 it releases, at hcount 4, like any other WAIT x=63.
	c.Step(511, 4, ClocksPerHCount)
	if len(rw.writes) != 1 {
		t.Errorf("$FFFF did not release on line 511 at hcount 4; writes = %+v", rw.writes)
	}
}

// TestAllOnesWordRestartsEveryFrameInVBLMode is the idiom the invented HALT
// opcode broke: a list written the standard way, mode 11 (run and restart at
// frame start), terminated with $FFFF, must run EVERY frame.
//
// GHDL, program "ffff-restarts-at-vbl": MOVE $50,$AA followed by $FFFF in mode
// 3 emits at frame A (v=0,h=1) and again at frame B (v=0,h=1). A model that
// treats $FFFF as a permanent stop runs it once and never again.
func TestAllOnesWordRestartsEveryFrameInVBLMode(t *testing.T) {
	c := New()
	c.SetWritePtrLow(0)
	c.WriteData(0x50) // MOVE reg $50
	c.WriteData(0xAA) // val $AA
	c.WriteData(0xFF) // the list terminator
	c.WriteData(0xFF)
	rw := &fakeRegWriter{}
	c.SetRegWriter(rw)
	c.SetWritePtrHighAndMode(byte(StartOnVBL) << 6)

	frame := func() {
		for y := uint16(0); y < 4; y++ {
			c.Step(y, 0, ClocksPerHCount)
		}
	}
	frame()
	if len(rw.writes) != 1 {
		t.Fatalf("frame A made %d NextReg writes, want 1; writes = %+v", len(rw.writes), rw.writes)
	}
	frame()
	if len(rw.writes) != 2 {
		t.Errorf("frame B made %d NextReg writes in total, want 2 (the list restarts at VBL); writes = %+v",
			len(rw.writes), rw.writes)
	}
}

// TestWaitParksWhenItsTargetLineIsBehindTheRaster pins the vertical test as the
// equality the hardware writes: `vcount_i = unsigned(...)`
// (device/copper.vhd:94). A WAIT whose line has already gone by does not
// release: it waits for that line to come round again next frame.
//
// GHDL, program "wait-behind-raster-parks": WAIT x=0,v=3 driven at v=7 emits
// nothing across 31 columns. The functional-model fallback that released on
// `scanline > Y` was there for a caller that skipped lines; the render loop now
// presents every line and every column, so it only produced divergence: a list
// of WAIT y=100; MOVE; WAIT y=50; MOVE ran both MOVEs on line 100 instead of
// deferring the second to the next frame.
func TestWaitParksWhenItsTargetLineIsBehindTheRaster(t *testing.T) {
	c := New()
	c.SetWritePtrLow(0)
	wait := uint16(0x8000) | 3 // WAIT x=0, y=3
	c.WriteData(byte(wait >> 8))
	c.WriteData(byte(wait))
	c.WriteData(0x72) // MOVE reg $72
	c.WriteData(0x55) // val $55
	rw := &fakeRegWriter{}
	c.SetRegWriter(rw)
	c.SetWritePtrHighAndMode(byte(StartFromZero) << 6)

	for hc := uint16(0); hc < 31; hc++ {
		c.Step(7, hc, ClocksPerHCount)
	}
	if len(rw.writes) != 0 {
		t.Fatalf("WAIT y=3 released while the raster was on line 7; writes = %+v", rw.writes)
	}
	// Line 3 comes round again on the next frame, and then it releases.
	c.Step(3, 12, ClocksPerHCount)
	if len(rw.writes) != 1 {
		t.Errorf("WAIT y=3 did not release when line 3 came round; writes = %+v", rw.writes)
	}
}

// TestASecondWaitDefersToTheNextFrame is the divergence the fallback caused,
// driven end to end: two WAITs whose lines run backwards.
func TestASecondWaitDefersToTheNextFrame(t *testing.T) {
	c := New()
	c.SetWritePtrLow(0)
	for _, w := range []uint16{
		0x8000 | 100, // WAIT x=0, y=100
		0x2A01,       // MOVE reg $2A, val $01
		0x8000 | 50,  // WAIT x=0, y=50
		0x2B02,       // MOVE reg $2B, val $02
		0xFFFF,       // park
	} {
		c.WriteData(byte(w >> 8))
		c.WriteData(byte(w))
	}
	rw := &fakeRegWriter{}
	c.SetRegWriter(rw)
	c.SetWritePtrHighAndMode(byte(StartOnVBL) << 6)

	for y := uint16(0); y < 312; y++ {
		for hc := uint16(0); hc < 448; hc++ {
			c.Step(y, hc, ClocksPerHCount)
		}
	}
	if len(rw.writes) != 1 || rw.writes[0].reg != 0x2A {
		t.Fatalf("frame 1 wrote %+v, want only the line-100 MOVE (reg $2A)", rw.writes)
	}
}
