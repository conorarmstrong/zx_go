package sam

import "testing"

// TestContentionTables spot-checks the precomputed delay tables against the SAM
// ASIC algorithm: delay = mask − ((t+2)&mask), mask 7 in the screen window else
// 3; mode 1 also contends alternating 64-cycle bands; 4T is always mask 3.
func TestContentionTables(t *testing.T) {
	round := func(mask, t int) byte { return byte(mask - ((t + 2) & mask)) }

	// A T-state well inside the main screen window (line 100, past the side
	// border): mode234 uses mask 7, 4T uses mask 3.
	ts := 100*samCyclesPerLine + 200
	if got, want := contentionMode234[ts], round(7, ts); got != want {
		t.Errorf("mode234[%d] = %d, want %d (screen → mask 7)", ts, got, want)
	}
	if got, want := contention4T[ts], round(3, ts); got != want {
		t.Errorf("4T[%d] = %d, want %d (always mask 3)", ts, got, want)
	}

	// A T-state in the top border (line 10): mode234 uses mask 3 (no screen).
	tb := 10 * samCyclesPerLine
	if got, want := contentionMode234[tb], round(3, tb); got != want {
		t.Errorf("mode234[%d] = %d, want %d (border → mask 3)", tb, got, want)
	}
}

// TestContendMemoryDelaysInternalRAM confirms a contended (internal-RAM) access
// advances the CPU clock, an uncontended (ROM) access does not, and the env
// opt-out disables it.
func TestContendMemoryDelaysInternalRAM(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	ts := m.CPU.Tstates()

	// Section A at reset is ROM0 (uncontended) → no delay.
	before := m.CPU.Tstates()
	m.Mem.ContendMemory(0x0000)
	if m.CPU.Tstates() != before {
		t.Errorf("ROM access should not be contended (tstates %d → %d)", before, m.CPU.Tstates())
	}

	// Section C ($8000) is internal RAM (contended) → may add delay.
	_ = ts
	total := uint64(0)
	for i := 0; i < 64; i++ {
		b := m.CPU.Tstates()
		m.Mem.ContendMemory(0x8000)
		total += m.CPU.Tstates() - b
	}
	if total == 0 {
		t.Error("internal-RAM access should incur contention delay over a range of T-states")
	}
}

func TestContentionOptOut(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.Mem.SetContentionEnabled(false)
	before := m.CPU.Tstates()
	for i := 0; i < 64; i++ {
		m.Mem.ContendMemory(0x8000)
		m.Mem.ContendPort(0x00FA)
	}
	if m.CPU.Tstates() != before {
		t.Errorf("contention opt-out should add no delay (tstates %d → %d)", before, m.CPU.Tstates())
	}
}

// TestContendPortASIC confirms I/O contention applies to ASIC ports (≥0xF8)
// but not to lower ports.
func TestContendPortASIC(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	before := m.CPU.Tstates()
	m.Mem.ContendPort(0x001F) // low port → no I/O contention
	if m.CPU.Tstates() != before {
		t.Errorf("non-ASIC port should not be contended (%d → %d)", before, m.CPU.Tstates())
	}
	total := uint64(0)
	for i := 0; i < 16; i++ {
		b := m.CPU.Tstates()
		m.Mem.ContendPort(0x00FE) // ASIC port (≥0xF8)
		total += m.CPU.Tstates() - b
	}
	if total == 0 {
		t.Error("ASIC port should incur I/O contention over a range of T-states")
	}
}

// TestContentionTableSelection confirms the screen mode picks the table.
func TestContentionTableSelection(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.Mem.SetVMPR(0x00) // mode 1
	if &m.Mem.activeContention[0] != &contentionMode1[0] {
		t.Error("mode 1 should select the mode-1 contention table")
	}
	m.Mem.SetVMPR(0x40) // mode 3
	if &m.Mem.activeContention[0] != &contentionMode234[0] {
		t.Error("mode 3 should select the mode-2/3/4 contention table")
	}
	m.Mem.SetScreenOff(true) // SOFF in mode 3 → screen disabled → 4T
	if &m.Mem.activeContention[0] != &contention4T[0] {
		t.Error("SOFF in mode 3/4 should select the 4T table")
	}
}
