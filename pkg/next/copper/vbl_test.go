package copper

import "testing"

// TestStartOnVBLRestartsEachFrame verifies the StartOnVBL mode resets the
// program counter to 0 at the start of each frame (when the raster wraps
// back to the top), so the list re-runs every frame.
func TestStartOnVBLRestartsEachFrame(t *testing.T) {
	c := New()
	// MOVE reg 0x10, val 0xAA at index 0; the rest stay NOOP (0x0000).
	c.SetWritePtrLow(0)
	c.WriteData(0x10)
	c.WriteData(0xAA)

	rw := &fakeRegWriter{}
	c.SetRegWriter(rw)
	// Start-on-VBL (mode 3); mode-set resets pc to 0.
	c.SetWritePtrHighAndMode(byte(StartOnVBL) << 6)

	// Frame 1: the MOVE runs once.
	c.Step(0, 0, 1)
	if len(rw.writes) != 1 {
		t.Fatalf("frame 1: %d writes, want 1", len(rw.writes))
	}
	// Later in the same frame: only NOOPs, no new writes.
	c.Step(200, 0, 1)
	if len(rw.writes) != 1 {
		t.Fatalf("mid-frame: %d writes, want 1 (program parked)", len(rw.writes))
	}
	// Next frame starts (raster wraps to 0) → pc resets → MOVE re-runs.
	c.Step(0, 0, 1)
	if len(rw.writes) != 2 || rw.writes[1].val != 0xAA {
		t.Fatalf("after VBL wrap: %d writes, want 2 (MOVE re-ran)", len(rw.writes))
	}
}
