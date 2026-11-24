package next

// ActiveVideoLine is the value NR$1E/$1F read: cvc, the copper's vertical
// counter (zxnext.vhd:5982-5986), at t video T-states after the frame
// interrupt. sixtyHz selects the 60 Hz frame; offset is NR$64.
//
// cvc is not a line count from the interrupt. video/zxula_timing.vhd:455-466
// reloads it with NR$64 on the first paper line (vc = c_min_vactive), at
// hc = c_min_hactive - 12, counts it up once a line from there and wraps it at
// c_max_vc. The interrupt fires at vc = c_int_v, hc = c_int_h (:551), so at the
// interrupt cvc already reads 248 on a 50 Hz frame. Software that waits for a
// line with these registers (NextZXOS dot commands, games syncing a DMA to the
// raster) is timing itself against that origin.
//
// The constants are the 128K timing the Next runs in, as WireLineInterrupt
// uses (zxula_timing.vhd:172-233); hc counts two per T-state.
func ActiveVideoLine(t int, sixtyHz bool, offset byte) int {
	const (
		hcPerLine = 456 // c_max_hc + 1
		intH      = 128 // c_int_h, 128K
		reloadH   = 124 // c_min_hactive - 12
	)
	lines, minVactive, intV := 311, 64, 1
	if sixtyHz {
		lines, minVactive, intV = 264, 40, 0
	}
	if t < 0 {
		t = 0
	}
	h := intH + 2*t
	vc := (intV + h/hcPerLine) % lines
	hc := h % hcPerLine
	n := vc - minVactive
	if hc < reloadH {
		n--
	}
	n = ((n % lines) + lines) % lines
	return (int(offset) + n) % lines
}
