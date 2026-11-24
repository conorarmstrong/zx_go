package divmmc

import "testing"

// While the Multiface memory is paged in, zxnext.vhd:3030-3036 sets
// sram_pre_override to "000", so sram_divmmc_automap_en and
// sram_divmmc_automap_rom3_en (zxnext.vhd:3137-3138) are both low and no
// entry point can automap. NextZXOS's snapshot loader runs from the
// Multiface ROM with interrupts on; automapping at its $0038 fetch ran the
// esxDOS IM1 handler in its place and the snapshot never started.
func TestNoEntryPointAutomapsWhileTheMultifaceIsPaged(t *testing.T) {
	for _, pc := range []uint16{0x0000, 0x0008, 0x0038, 0x04C6, 0x3D00} {
		p := newPagerAutomapped(makeROM())
		p.SetEntryPoints0(0xFF)
		p.SetEntryPoints1(0xFF)
		p.SetMultifaceActiveFn(func() bool { return true })
		p.Step(pc)
		p.Step(pc + 1) // a delayed variant would page in here
		if p.IsPagedIn() {
			t.Errorf("fetch at $%04X automapped with the Multiface paged in", pc)
		}
	}
}

// The same gate holds the automap latch: delayed_off is
// "i_automap_active and i_automap_delayed_off" (device/divmmc.vhd:131), so a
// fetch in $1FF8-$1FFF with the Multiface paged does not page the divMMC out.
func TestTheOffAreaDoesNotUnmapWhileTheMultifaceIsPaged(t *testing.T) {
	p := newPagerAutomapped(makeROM())
	p.SetEntryPoints0(0xFF)
	p.SetEntryPoints1(0xFF)
	mf := false
	p.SetMultifaceActiveFn(func() bool { return mf })
	p.Step(0x0008)
	if !p.IsPagedIn() {
		t.Fatal("precondition: RST $08 did not automap")
	}
	mf = true
	p.Step(0x1FF8)
	p.PostStep(0x1FF8)
	if !p.IsPagedIn() {
		t.Error("the off area unmapped the divMMC with the Multiface paged in")
	}
}
