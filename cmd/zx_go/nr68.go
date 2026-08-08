package main

// nr68 is the decoded NextReg 0x68 ("ULA Control").
//
// Only bit 7 used to be acted on; the blend selection and the stencil bit
// were dropped, so NR$15's two blend orderings could only ever see the reset
// blend source.
type nr68 struct {
	// ulaDisabled is bit 7. The FPGA stores the inverse (nr_68_ula_en).
	ulaDisabled bool
	// blendMode is bits 6:5 — which layer supplies the colour summed with
	// Layer 2 in NR$15 modes 110 and 111. See compositor.SetBlendMode.
	blendMode byte
	// cancelExtendedKeys is bit 4, driving o_KBD_CANCEL (zxnext.vhd:1584).
	cancelExtendedKeys bool
	// ulaFineScrollX is bit 2, the ULA half-pixel horizontal scroll.
	ulaFineScrollX bool
	// stencilMode is bit 0: with the ULA and tilemap both enabled, combine
	// them into a stencil instead of layering.
	stencilMode bool
}

// decodeNR68 splits the register into its fields (zxnext.vhd:5444-5450).
// Bit 3 (ULA+ enable) is commented out in the core — the enable is driven
// from port $FF3B instead — and bit 1 is reserved.
func decodeNR68(v byte) nr68 {
	return nr68{
		ulaDisabled:        v&0x80 != 0,
		blendMode:          (v >> 5) & 0x03,
		cancelExtendedKeys: v&0x10 != 0,
		ulaFineScrollX:     v&0x04 != 0,
		stencilMode:        v&0x01 != 0,
	}
}

// nr68ReadBack shapes the value the register reads back as. Per
// zxnext.vhd:6093 the reserved bit 1 is a literal '0' rather than a stored
// field, so it never reads back what was written.
//
// Bit 3 is reproduced as written rather than sourced from port $FF3B's ULA+
// enable, which is not modelled.
func nr68ReadBack(written byte) byte { return written &^ 0x02 }
