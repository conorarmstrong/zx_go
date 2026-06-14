package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// iter 347: I/O + memory contention model tests.
//
// Two layers:
//  1. contentionDelay() — the per-T-state ULA hold pattern
//     {6,5,4,3,2,1,0,0} over the 128-T contended window of each of
//     the 192 display lines (Sean Young §contention / FUSE model).
//  2. ContendPort() — the four ULA I/O contention shapes (§4.2):
//     C:1,C:3 / C:1×4 / N:1,C:3 / N:4. Tested for the fixed wait
//     T-states (delay forced to 0 by positioning outside the display)
//     so the structure is unambiguous, plus an in-display check that
//     the delay actually contributes.

func newContendMem(t *testing.T) (*Memory, *uint64) {
	t.Helper()
	m := newTestMemory(t, roms.Model48K)
	m.ContentionEnabled = true
	var ts uint64
	m.TStates = &ts
	return m, &ts
}

func TestContentionDelay_Pattern(t *testing.T) {
	m, ts := newContendMem(t)
	// Window starts at T=14335. Offsets 0..15 walk two periods of the
	// 8-T pattern {6,5,4,3,2,1,0,0}.
	want := []uint64{6, 5, 4, 3, 2, 1, 0, 0, 6, 5, 4, 3, 2, 1, 0, 0}
	for off, w := range want {
		*ts = 14335 + uint64(off)
		if got := m.contentionDelay(); got != w {
			t.Errorf("offset %d (T=%d): delay=%d, want %d", off, *ts, got, w)
		}
	}
}

func TestContentionDelay_Boundaries(t *testing.T) {
	m, ts := newContendMem(t)
	cases := []struct {
		name string
		t    uint64
		want uint64
	}{
		{"before window", 14334, 0},
		{"first contended T", 14335, 6},
		{"past pos-128 (border of line)", 14335 + 128, 0},
		{"end of pos window-1", 14335 + 127, 0}, // pos 127 → >=128? no, 127<128 → pattern[127%8=7]=0
		{"after display (line 192+)", 14335 + 192*228, 0},
		{"way after display", 57344, 0},
		{"nil-tstate safe", 0, 0},
	}
	for _, c := range cases {
		*ts = c.t
		if got := m.contentionDelay(); got != c.want {
			t.Errorf("%s (T=%d): delay=%d, want %d", c.name, c.t, got, c.want)
		}
	}
	// nil TStates → 0 regardless.
	m.TStates = nil
	if got := m.contentionDelay(); got != 0 {
		t.Errorf("nil TStates: delay=%d, want 0", got)
	}
}

// TestContendPort_FixedWaits positions the counter OUTSIDE the display
// region so contentionDelay()==0; what remains is the fixed I/O wait
// T-state structure for each of the four cases.
func TestContendPort_FixedWaits(t *testing.T) {
	cases := []struct {
		name string
		port uint16
		want uint64
	}{
		{"contended ULA (C:1,C:3)", 0x40FE, 4},      // 0+1+0+3
		{"contended non-ULA (C:1x4)", 0x40FF, 4},    // (0+1)*4
		{"non-contended ULA (N:1,C:3)", 0x00FE, 4},  // 1+0+3
		{"non-contended non-ULA (none)", 0x00FF, 0}, // untouched
	}
	for _, c := range cases {
		m, ts := newContendMem(t)
		*ts = 100000 // well past the 57343 display end → delay 0
		before := *ts
		m.ContendPort(c.port)
		if got := *ts - before; got != c.want {
			t.Errorf("%s: +%d T, want +%d", c.name, got, c.want)
		}
	}
}

// TestContendPort_DelayContributesInDisplay confirms that the same
// port access costs MORE inside the contended display window than
// outside it — i.e. the ULA hold is actually applied.
func TestContendPort_DelayContributesInDisplay(t *testing.T) {
	m, ts := newContendMem(t)
	*ts = 100000 // outside display
	b1 := *ts
	m.ContendPort(0x40FE)
	outside := *ts - b1

	m2, ts2 := newContendMem(t)
	*ts2 = 14335 // first contended T → delay 6
	b2 := *ts2
	m2.ContendPort(0x40FE)
	inside := *ts2 - b2

	if inside <= outside {
		t.Errorf("in-display ContendPort (%d T) should exceed out-of-display (%d T)", inside, outside)
	}
}

// TestContendMemory wires the (previously call-site-less) memory
// contention helper: a contended-address access in the display window
// adds the ULA hold; an uncontended address or RAMContentionDisabled
// adds nothing.
func TestContendMemory(t *testing.T) {
	m, ts := newContendMem(t)
	*ts = 14335 // delay 6
	b := *ts
	m.ContendMemory(0x4000) // bank-5 screen RAM → contended
	if got := *ts - b; got != 6 {
		t.Errorf("contended ContendMemory: +%d T, want +6", got)
	}
	// Uncontended address adds nothing.
	*ts = 14335
	b = *ts
	m.ContendMemory(0x0000) // ROM → not contended
	if got := *ts - b; got != 0 {
		t.Errorf("uncontended ContendMemory: +%d T, want 0", got)
	}
	// RAMContentionDisabled suppresses it.
	m.RAMContentionDisabled = true
	*ts = 14335
	b = *ts
	m.ContendMemory(0x4000)
	if got := *ts - b; got != 0 {
		t.Errorf("RAMContentionDisabled ContendMemory: +%d T, want 0", got)
	}
}
