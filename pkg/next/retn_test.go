package next

import "testing"

type fakeMF struct{ active bool }

func (m *fakeMF) MultifaceActive() bool      { return m.active }
func (m *fakeMF) SetMultifaceActive(on bool) { m.active = on }

type fakeRETNPager struct{ seen int }

func (p *fakeRETNPager) HandleRETN() { p.seen++ }

// RETN pages the Multiface out. The divMMC sees it only when the Multiface was
// not active: divmmc_retn_seen is z80_retn_seen_28 and not mf_is_active
// (zxnext.vhd:4111).
func TestRETNHook(t *testing.T) {
	mf, pager := &fakeMF{active: true}, &fakeRETNPager{}
	h := RETNHook(mf, pager)
	h()
	if mf.active {
		t.Error("RETN left the Multiface paged in")
	}
	if pager.seen != 0 {
		t.Error("the divMMC saw a RETN that ended a Multiface session")
	}
	h()
	if pager.seen != 1 {
		t.Errorf("divMMC saw %d RETNs with the Multiface idle, want 1", pager.seen)
	}
}
