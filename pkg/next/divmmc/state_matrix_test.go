package divmmc

import "testing"

// divMMC state-transition matrix — the cross-product of
// automap × CONMEM × entry-point-gate × page-out trigger, driven as
// full transition sequences. The individual axes are covered by
// port_e3_state_test / entrypoint_gating_test; this asserts the
// COMBINED transitions a real session walks through.

func newMatrixPager() *Pager {
	p := New(make([]byte, 0x2000))
	// Enable every entry/exit gate so trigger PCs are purely a
	// function of automap + CONMEM + current PC for this matrix.
	p.SetEntryPoints0(0xFF)       // all RST traps enabled (NR$B8)
	p.SetEntryPointsValid0(0xFF)  // B9: divMMC ROM path for every RST
	p.SetEntryPointsTiming0(0xFF) // BA: instant_on, so Step(trigger) pages
	// in this same M1 — isolates the automap/CONMEM/gate axes under test
	// from the instant-vs-delayed timing.
	p.SetEntryPoints1(0xFF) // EP1: $0066/$04C6/.../page-out all on
	return p
}

func TestMatrix_AutomapGatesTriggers(t *testing.T) {
	// automap OFF: a trigger fetch must NOT page in.
	p := newMatrixPager()
	p.SetAutomap(false)
	p.Step(0x0000)
	if p.IsPagedIn() {
		t.Error("automap off: trigger $0000 should not page in")
	}
	// automap ON: same trigger pages in.
	p.SetAutomap(true)
	p.Step(0x0000)
	if !p.IsPagedIn() {
		t.Error("automap on: trigger $0000 should page in")
	}
}

func TestMatrix_PageInThenPageOut(t *testing.T) {
	p := newMatrixPager()
	p.SetAutomap(true)
	p.Step(0x0008) // RST 8 trigger → page in
	if !p.IsPagedIn() {
		t.Fatal("RST8 trigger did not page in")
	}
	// A second trigger while paged-in is a no-op (stays in).
	p.Step(0x0000)
	if !p.IsPagedIn() {
		t.Error("trigger while paged-in should be a no-op, not drop")
	}
	// Page-out area $1FF8-$1FFF via PostStep drops the overlay.
	p.PostStep(0x1FFC)
	if p.IsPagedIn() {
		t.Error("$1FFC page-out trigger should drop overlay")
	}
}

func TestMatrix_CONMEMForcesRegardlessOfAutomap(t *testing.T) {
	for _, automap := range []bool{false, true} {
		p := newMatrixPager()
		p.SetAutomap(automap)
		p.WritePort(0xE3, 0x80) // CONMEM
		if !p.IsPagedIn() {
			t.Errorf("automap=%v: CONMEM write must force paged-in", automap)
		}
		// While CONMEM is set, a page-out trigger must NOT drop the
		// overlay (CONMEM priority is higher than the off-area).
		p.PostStep(0x1FFC)
		if !p.IsPagedIn() {
			t.Errorf("automap=%v: CONMEM should hold overlay through $1FFx", automap)
		}
	}
}

func TestMatrix_CONMEMClear_DependsOnAutomap(t *testing.T) {
	// CONMEM clear with automap OFF → overlay drops.
	p := newMatrixPager()
	p.SetAutomap(false)
	p.WritePort(0xE3, 0x80) // in
	p.WritePort(0xE3, 0x00) // clear CONMEM
	if p.IsPagedIn() {
		t.Error("CONMEM clear + automap off: overlay should drop")
	}
	// CONMEM clear with automap ENABLED but never TRIGGERED → overlay
	// drops. CONMEM is combinational on the FBLabs core and never sets
	// the automap-held latch, so a CONMEM set/clear with no trigger PC
	// fetched in between leaves the overlay out. (This is the NextZXOS
	// $DB41-stub path: enabling automap alone must not strand the
	// overlay paged-in.)
	p = newMatrixPager()
	p.SetAutomap(true)
	p.WritePort(0xE3, 0x80)
	p.WritePort(0xE3, 0x00)
	if p.IsPagedIn() {
		t.Error("CONMEM clear + automap enabled-but-untriggered: overlay should drop")
	}
	// CONMEM clear with automap GENUINELY triggered → overlay survives
	// until a real page-out trigger (the automap-held latch was set by
	// the trigger fetch, independent of CONMEM).
	p = newMatrixPager()
	p.SetAutomap(true)
	p.Step(0x0000) // RST $00 trigger pages in via the automap latch
	p.WritePort(0xE3, 0x80)
	p.WritePort(0xE3, 0x00)
	if !p.IsPagedIn() {
		t.Error("CONMEM clear + automap triggered: overlay should survive")
	}
}

func TestMatrix_EntryGateClosed_NoPageIn(t *testing.T) {
	// automap on but the specific entry-point gate is closed → the
	// trigger PC must not page in.
	p := New(make([]byte, 0x2000))
	p.SetAutomap(true)
	p.SetEntryPoints0(0x00) // disable all RST traps
	p.SetEntryPointsValid0(0x00)
	p.SetEntryPointsTiming0(0x00)
	p.SetEntryPoints1(0x00) // disable EP1 triggers + page-out
	p.Step(0x0000)
	p.Step(0x0008)
	p.Step(0x0066)
	if p.IsPagedIn() {
		t.Error("all gates closed: no trigger should page in")
	}
}

func TestMatrix_PageOutGateClosed_StaysIn(t *testing.T) {
	p := newMatrixPager()
	p.SetAutomap(true)
	p.Step(0x0000)                  // page in
	p.SetEntryPoints1(0xFF &^ 0x40) // close ONLY the page-out gate (bit 6)
	p.PostStep(0x1FFC)
	if !p.IsPagedIn() {
		t.Error("page-out gate closed: $1FFx must not drop overlay")
	}
}

func TestMatrix_MAPRAMStickyAcrossCONMEM(t *testing.T) {
	p := newMatrixPager()
	p.WritePort(0xE3, 0x40) // MAPRAM
	if !p.MAPRAM() {
		t.Fatal("MAPRAM not latched")
	}
	// Toggling CONMEM on/off must not clear the sticky MAPRAM latch.
	p.WritePort(0xE3, 0xC0) // CONMEM + MAPRAM
	p.WritePort(0xE3, 0x00) // both bits clear in the written byte
	if !p.MAPRAM() {
		t.Error("MAPRAM should stay latched until ClearMAPRAM / reset")
	}
	p.ClearMAPRAM()
	if p.MAPRAM() {
		t.Error("ClearMAPRAM should drop the latch")
	}
}
