package memory

import "testing"

// TestTurboContentionDisabled verifies ULA memory contention is DISABLED
// at every turbo CPU speed and only applies at 3.5 MHz — matching the
// FPGA, which asserts i_contention_en only when cpu_speed = "00"
// (zxnext.vhd:4481 ANDs in `(not cpu_speed(1)) and (not cpu_speed(0))`).
//
// The earlier model scaled the contention magnitude by the speed
// multiplier (6→12→24→48). That was wrong: it injected ~35% phantom
// T-states per frame at 28 MHz, which shifted where the frame interrupt
// landed in the instruction stream and derailed the interrupt-driven
// 128K-BASIC launch (it forks on the interrupted PC).
func TestTurboContentionDisabled(t *testing.T) {
	var ts uint64
	mult := 1
	m := &Memory{TStates: &ts, ContentionEnabled: true, SpeedMultiplier: func() int { return mult }}

	// 3.5 MHz (×1): contended access at ULA T-state 14335 → pattern[0]=6.
	ts = 14335
	before := ts
	m.ContendMemory(0x4000)
	if got := ts - before; got != 6 {
		t.Errorf("3.5MHz: contention delay = %d, want 6", got)
	}

	// 7/14/28 MHz: contention is OFF — no delay added regardless of the
	// ULA-scale position.
	for _, mhz := range []struct {
		name string
		mult int
	}{{"7MHz", 2}, {"14MHz", 4}, {"28MHz", 8}} {
		mult = mhz.mult
		ts = 14335 * uint64(mhz.mult) // same ULA position
		before = ts
		m.ContendMemory(0x4000)
		if got := ts - before; got != 0 {
			t.Errorf("%s: contention delay = %d, want 0 (contention disabled at turbo)", mhz.name, got)
		}
	}
}
