package ctc

import "testing"

// TestNewIsHardReset confirms a fresh channel starts idle: not running, ZC/TO
// low, count zero, interrupt disabled (ctc_chan.vhd hard-reset defaults).
func TestNewIsHardReset(t *testing.T) {
	c := New()
	if c.Count() != 0 {
		t.Errorf("Count = %d, want 0", c.Count())
	}
	if c.ZCTO() {
		t.Error("ZCTO high after reset")
	}
	if c.IntEnabled() {
		t.Error("IntEnabled true after reset")
	}
}

// TestTimerPrescale16Period confirms a /16 timer with TC=2 produces a ZC/TO
// every 32 cycles and reloads. This is the headline timer behaviour and a
// hand-derivable check independent of the golden file.
func TestTimerPrescale16Period(t *testing.T) {
	c := New()
	c.Write(0x05) // timer, /16, auto-start, TC follows
	c.Write(0x02) // TC = 2

	zcCycles := []int{}
	for i := 0; i < 96; i++ {
		c.Tick()
		if c.ZCTO() {
			zcCycles = append(zcCycles, i)
		}
	}
	if len(zcCycles) != 3 {
		t.Fatalf("got %d ZC/TO pulses in 96 cycles, want 3 (every 32): %v", len(zcCycles), zcCycles)
	}
	// spacing must be 32 cycles
	for i := 1; i < len(zcCycles); i++ {
		if d := zcCycles[i] - zcCycles[i-1]; d != 32 {
			t.Errorf("ZC/TO spacing %d, want 32 (TC=2 * /16)", d)
		}
	}
}

// TestCounterModeExternalEdges confirms counter mode decrements on the selected
// trigger edge and fires ZC/TO after TC edges, reloading.
func TestCounterModeExternalEdges(t *testing.T) {
	c := New()
	c.Write(0x57) // counter, rising edge, TC follows
	c.Write(0x02) // TC = 2
	c.Tick()      // settle into run
	if c.Count() != 2 {
		t.Fatalf("initial count = %d, want 2", c.Count())
	}

	fireEdge := func() bool {
		c.SetTrigger(true)
		c.Tick()
		got := c.ZCTO()
		c.SetTrigger(false)
		c.Tick()
		return got
	}
	if fireEdge() { // first edge: 2 -> 1
		t.Error("ZC/TO after first edge")
	}
	if c.Count() != 1 {
		t.Errorf("count after one edge = %d, want 1", c.Count())
	}
	if !fireEdge() { // second edge: 1 -> 0 -> reload, ZC/TO
		t.Error("expected ZC/TO on the second edge")
	}
	if c.Count() != 2 {
		t.Errorf("count after reload = %d, want 2 (TC)", c.Count())
	}
}

// TestIntEnableViaControlWordAndSeparateWrite confirms both interrupt-enable
// paths: the control-word D7 bit and the separate WriteIntEnable.
func TestIntEnableViaControlWordAndSeparateWrite(t *testing.T) {
	c := New()
	c.Write(0x85) // D7=1 -> int enabled
	c.Write(0x02)
	if !c.IntEnabled() {
		t.Error("IntEnabled false after CW D7=1")
	}
	c.WriteIntEnable(false)
	if c.IntEnabled() {
		t.Error("IntEnabled true after separate clear")
	}
	c.WriteIntEnable(true)
	if !c.IntEnabled() {
		t.Error("IntEnabled false after separate set")
	}
}
