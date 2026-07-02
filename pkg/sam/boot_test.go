package sam

import "testing"

// TestSamBootsToBasic drives the genuine SAM ROM 3.0 through its power-on
// sequence and confirms it reaches its interrupt-driven main loop — i.e. it
// boots to the SAM BASIC prompt — by evidence: the frame interrupt
// (IM1 → $0038) is serviced repeatedly and interrupts end up enabled.
func TestSamBootsToBasic(t *testing.T) {
	m, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}

	isrHits := 0
	m.CPU.AddPreFetchHook("count-isr", func(pc uint16) {
		if pc == 0x0038 {
			isrHits++
		}
	})

	// The SAM is a heavily contended machine, so boot can take a while; run
	// until the ROM is clearly in its interrupt loop (or give up after the cap).
	const maxFrames = 2000
	booted := 0
	for i := 0; i < maxFrames; i++ {
		m.RunFrame()
		if m.CPU.IFF1 && isrHits >= 50 {
			booted = i + 1
			break
		}
	}

	t.Logf("booted after %d frames: PC=%#04x IFF1=%v IM=%d ISR-hits=%d insns=%d",
		booted, m.CPU.PC, m.CPU.IFF1, m.CPU.IM, isrHits, m.CPU.InstructionCount())

	if booted == 0 {
		t.Errorf("SAM did not reach its interrupt loop within %d frames (PC=%#04x IFF1=%v ISR=%d)",
			maxFrames, m.CPU.PC, m.CPU.IFF1, isrHits)
	}
}
