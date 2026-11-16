package next

// MultifacePager is the Next Multiface's paged-in state.
type MultifacePager interface {
	MultifaceActive() bool
	SetMultifaceActive(on bool)
}

// RETNPager is the divMMC's end-of-NMI input.
type RETNPager interface{ HandleRETN() }

// RETNHook returns the CPU's RETN hook. A RETN (ED 45; im2_control.vhd:236
// o_retn_seen) pages the Multiface out (core 3.01.09: "executing a retn will
// now disable the multiface and the divmmc"). The divMMC sees it only when the
// Multiface was not active: divmmc_retn_seen is z80_retn_seen_28 and not
// mf_is_active (zxnext.vhd:4111), so the RETN that ends a Multiface session
// leaves the divMMC alone.
func RETNHook(mf MultifacePager, divmmc RETNPager) func() {
	return func() {
		if mf.MultifaceActive() {
			mf.SetMultifaceActive(false)
		} else {
			divmmc.HandleRETN()
		}
	}
}
