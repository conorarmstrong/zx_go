package sam

import "testing"

// TestSamBootsToBasic is the Sprint 2 milestone: the genuine SAM ROM 3.0 runs
// its power-on sequence, enables interrupts, and reaches its interrupt-driven
// main loop — i.e. it boots to the SAM BASIC prompt. We can't render yet
// (Sprint 3), so we detect boot by evidence: the frame interrupt (IM1 → $0038)
// is serviced repeatedly and interrupts end up enabled.
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

	const frames = 300 // ~6 emulated seconds
	for i := 0; i < frames; i++ {
		m.RunFrame()
	}

	t.Logf("after %d frames: PC=%#04x IFF1=%v IM=%d ISR-hits=%d insns=%d",
		frames, m.CPU.PC, m.CPU.IFF1, m.CPU.IM, isrHits, m.CPU.InstructionCount())

	if !m.CPU.IFF1 {
		t.Errorf("interrupts should be enabled once booted (IFF1=false)")
	}
	if isrHits < frames/2 {
		t.Errorf("frame ISR serviced %d/%d times — ROM not in its interrupt loop", isrHits, frames)
	}
}
