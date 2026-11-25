package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// NR$03's machine timing selects the Next's video and contention timing
// (zxnext.vhd:5758-5778), latched at the frame sync (eff_nr_03_machine_timing,
// :6694-6703). Power-on is "011", +3 timing (:1099).
func TestNextMachineTiming(t *testing.T) {
	m := newNextMem(t)
	if got := m.TimingModel(); got != roms.ModelPlus3 {
		t.Fatalf("power-on timing model %v, want +3", got)
	}
	changes := 0
	m.OnVideoTimingChange(func() { changes++ })

	m.SetNextMachineTiming(0x01)
	if got := m.TimingModel(); got != roms.ModelPlus3 {
		t.Errorf("a write took effect before the frame sync: %v", got)
	}
	if !m.LatchVideoTiming() || changes != 1 {
		t.Fatalf("the frame sync did not latch the new timing (changes=%d)", changes)
	}
	if m.LatchVideoTiming() || changes != 1 {
		t.Error("a second frame sync with nothing pending reported a change")
	}

	for _, c := range []struct {
		nr03      byte
		model     roms.SpectrumModel
		contended []int // 16K banks contended at $C000
	}{
		{0x01, roms.Model48K, []int{5}},
		{0x02, roms.Model128K, []int{1, 3, 5, 7}},
		{0x03, roms.ModelPlus3, []int{4, 5, 6, 7}},
		{0x04, roms.ModelPentagon, nil},
	} {
		m.SetNextMachineTiming(c.nr03)
		m.LatchVideoTiming()
		if got := m.TimingModel(); got != c.model {
			t.Errorf("NR$03 timing %d: model %v, want %v", c.nr03, got, c.model)
		}
		want := map[int]bool{}
		for _, b := range c.contended {
			want[b] = true
		}
		for b := 0; b < 8; b++ {
			m.PageMemory(byte(b))
			if got := m.isContendedBank(0xC000); got != want[b] {
				t.Errorf("NR$03 timing %d: bank %d contended=%v, want %v", c.nr03, b, got, want[b])
			}
		}
		if got, wantStart := m.contendStart, uint64(roms.FirstContendedTStateFor(c.model, false)); c.model != roms.ModelPentagon && got != wantStart {
			t.Errorf("NR$03 timing %d: contention starts at %d, want %d", c.nr03, got, wantStart)
		}
	}
}
