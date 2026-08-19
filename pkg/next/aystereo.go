package next

import (
	"github.com/conorarmstrong/zx_go/pkg/ay"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
)

// AY stereo panning wiring.
//
// Two registers drive it, and both were already decoded, stored and read back
// correctly while reaching no sound chip at all:
//
//	nr_08_psg_stereo_mode <= nr_wr_dat(5);          zxnext.vhd:5177
//	nr_09_psg_mono        <= nr_wr_dat(7 downto 5); zxnext.vhd:5186
//
// which turbosound.vhd takes as stereo_mode_i and mono_mode_i. A guest
// selecting ACB heard ABC, and one holding a chip mono heard it panned.
//
// The Engine owns the resolved mode; see pkg/ay/stereo.go for the law itself.

// WireAYStereo installs the NR$08 / NR$09 handlers that drive the TurboSound
// engine's panning.
//
// ORDER MATTERS, and nothing can enforce it. Both registers already have
// owners — NR$08 carries the RAM contention disable and the $7FFD paging-lock
// clear, NR$09 the MAPRAM one-shot — so this chains rather than displacing
// them, and the chaining only works if it runs AFTER WireContentionDisable and
// WirePeripheral3. Called earlier, this function's own handler is the one
// silently replaced, the registers still read back correctly, and the panning
// quietly stops following them. next.Wire is where the order is fixed.
func WireAYStereo(d *nextregs.Dispatcher, engine *ay.Engine) {
	if d == nil || engine == nil {
		// Classic models reach Wire without a TurboSound engine. Nothing to
		// drive, and the registers' own owners are already installed.
		return
	}

	apply := func() {
		engine.SetStereoMode(d.Raw(0x08)&0x20 != 0)
		engine.SetMonoMask(d.Raw(0x09))
	}

	// Both handlers re-read both registers rather than each caching its own
	// half. The mono mask outranks the stereo bit, so the resolved mode is a
	// function of the pair, and recomputing it from the register file is what
	// keeps a write to one from resurrecting a stale copy of the other.
	chainOnWrite(d, 0x08, func(byte) { apply() })
	chainOnWrite(d, 0x09, func(byte) { apply() })

	// A reset clears both registers on the FPGA, so the panning has to follow
	// them back down rather than staying where the last guest left it.
	// SetOnReset appends rather than replacing, so this needs no chaining.
	d.SetOnReset(apply)

	apply()
}
