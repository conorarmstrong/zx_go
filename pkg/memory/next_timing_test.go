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

// On the Next, port_contend also covers $7FFD while 128K or +3 timing is in
// force, and the ULA+ ports $BF3B and $FF3B (zxnext.vhd:4496, port_7ffd_active
// :2594), so they take the ULA-port shape, N:1 then C:3. (+3 timing contends
// no port at all, zxula.vhd:600.)
func TestNextPortContendCoversPagingAndULAPlusPorts(t *testing.T) {
	late := func(nr03 byte, port uint16) uint64 {
		m := newNextMem(t)
		m.ContentionEnabled = true
		m.SetNextMachineTiming(nr03)
		m.LatchVideoTiming()
		ts := uint64(roms.FirstContendedTStateFor(m.TimingModel(), false))
		m.TStates = &ts
		start := ts
		m.ContendPortLate(port)
		return ts - start
	}
	for _, c := range []struct {
		nr03      byte
		port      uint16
		contended bool
	}{
		// $00FD and $2FFD alias $7FFD (A15 = 0, A1:0 = 01, not $1FFD) and lie
		// outside $4000-$7FFF, so only port_contend can contend them.
		{0x02, 0x00FD, true}, {0x02, 0x2FFD, true}, {0x02, 0xBF3B, true}, {0x02, 0xFF3B, true},
		{0x02, 0x1FFD, false}, {0x02, 0xFFFD, false}, {0x02, 0x00FF, false},
		{0x01, 0x00FD, false}, {0x01, 0xBF3B, true}, {0x01, 0xFF3B, true},
	} {
		if got := late(c.nr03, c.port) > 0; got != c.contended {
			t.Errorf("timing %d port $%04X: contended=%v, want %v", c.nr03, c.port, got, c.contended)
		}
	}
}

// NR$08 bit 6 takes effect at the next 7 MHz column with hc(8) set
// (eff_nr_08_contention_disable, zxnext.vhd:5822-5824), and it gates
// i_contention_en, so port contention as well as memory (:4481).
func TestNR08ContentionDisableLatchesAtHC256(t *testing.T) {
	m := newNextMem(t)
	m.ContentionEnabled = true
	m.SetNextMachineTiming(0x02) // 128K: hc = 128 + 2t at the interrupt's line
	m.LatchVideoTiming()
	start := uint64(roms.FirstContendedTStateFor(roms.Model128K, false))
	ts := start
	m.TStates = &ts
	m.PageMemory(0x01) // bank 1 at $C000: contended on 128K timing
	// The first contended T-state is one before the first paper fetch: raw
	// hc 122 (the fetch is at hc_ula 0, hc 124), so hc reaches 256 67
	// T-states later.
	m.SetRAMContentionDisable(true)
	before := ts
	m.ContendMemory(0xC000)
	if ts == before {
		t.Fatal("the disable took effect before hc(8)")
	}
	ts = start + 66 // hc 254
	before = ts
	m.ContendMemory(0xC000)
	if ts == before {
		t.Fatal("the disable took effect at hc 254")
	}
	ts = start + 67 // hc 256
	before = ts
	m.ContendMemory(0xC000)
	m.ContendPort(0x40FE)
	if ts != before {
		t.Errorf("contention charged %d T after hc reached 256", ts-before)
	}
}
