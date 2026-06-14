package divmmc

import "testing"

// TestDivMMCNeverClaimsUpperMemory enforces the SpecNext Memory_map
// invariant: divMMC paging replaces ONLY $0000-$3FFF (8K ROM + 8K RAM).
// It must never claim $C000-$FFFF, so it can never shadow or perturb
// MMU8 slots 6/7. This is the spec-faithful guarantee that the slot-7
// bank backing $E000 (e.g. the NextZXOS descriptor table) is decided
// purely by NextReg $56/$57 / 128 paging, regardless of divMMC state.
//
// Verified across every paged-in mechanism, with a positive control
// proving the pager IS active in those states (so "declines upper" is
// not trivially because nothing is paged).
func TestDivMMCNeverClaimsUpperMemory(t *testing.T) {
	upper := []uint16{0x4000, 0x8000, 0xC000, 0xE000, 0xE01B, 0xFFFF}

	states := []struct {
		name       string
		setup      func(*Pager)
		liveProbe  uint16 // an address the pager SHOULD claim in this state
		expectLive bool
	}{
		{"cold (not paged)", func(p *Pager) {}, 0x0000, false},
		{"CONMEM", func(p *Pager) { p.WritePort(0x00E3, 0x80) }, 0x2000, true},
		{"MAPRAM+CONMEM", func(p *Pager) { p.WritePort(0x00E3, 0xC0) }, 0x2000, true},
		{"automap paged", func(p *Pager) { p.SetAutomap(true); p.SetEntryPointsTiming0(0xFF); p.Step(0x0000) }, 0x0000, true},
	}

	for _, st := range states {
		p := New([]byte{0xFF})
		st.setup(p)

		// Positive control: the pager must actually be active where
		// expected, otherwise the upper-memory checks prove nothing.
		if _, ok := p.HandleRead(st.liveProbe); ok != st.expectLive {
			t.Errorf("%s: HandleRead($%04X) claimed=%v, want %v (state setup wrong?)",
				st.name, st.liveProbe, ok, st.expectLive)
		}

		// Invariant: upper memory ($4000+, incl. slots 6/7) is never
		// claimed for read or write, in any state.
		for _, a := range upper {
			if _, ok := p.HandleRead(a); ok {
				t.Errorf("%s: HandleRead($%04X) claimed upper memory — would shadow MMU slot", st.name, a)
			}
			if p.HandleWrite(a, 0x5A) {
				t.Errorf("%s: HandleWrite($%04X) claimed upper memory — would perturb MMU slot", st.name, a)
			}
		}
	}
}
