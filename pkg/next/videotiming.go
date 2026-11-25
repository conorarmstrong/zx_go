package next

// VideoTiming is one machine-timing personality of video/zxula_timing.vhd,
// in raw 7 MHz counter units: HC columns a line (c_max_hc+1), Lines a frame
// (c_max_vc+1), the frame interrupt at (IntV, IntH) (c_int_v, c_int_h), and
// the first paper pixel at MinHActive, MinVActive (c_min_hactive,
// c_min_vactive). Two columns make one 3.5 MHz T-state.
type VideoTiming struct {
	HC, Lines              int
	IntH, IntV             int
	MinHActive, MinVActive int
}

// VideoTimingFor returns the personality NR$03's machine-timing field selects
// (00X 48K, 010 128K, 011 +3, 100 Pentagon; zxnext.vhd:5124-5133), at 50 or
// 60 Hz (zxula_timing.vhd:150-298). The Pentagon has no 60 Hz timing.
func VideoTimingFor(nr03 byte, sixtyHz bool) VideoTiming {
	switch t := nr03 & 0x07; {
	case t&0x04 != 0: // Pentagon, :150-168
		return VideoTiming{HC: 448, Lines: 320, IntH: 448 + 3 - 12, IntV: 319, MinHActive: 128, MinVActive: 80}
	case t&0x02 != 0: // 128K and +3, :178-238
		v := VideoTiming{HC: 456, Lines: 311, IntH: 136 + 4 - 12, IntV: 1, MinHActive: 136, MinVActive: 64}
		if t&0x01 != 0 {
			v.IntH = 136 + 2 - 12 // +3
		}
		if sixtyHz {
			v.Lines, v.IntV, v.MinVActive = 264, 0, 40
		}
		return v
	default: // 48K, :252-298
		v := VideoTiming{HC: 448, Lines: 312, IntH: 128 + 0 - 12, IntV: 0, MinHActive: 128, MinVActive: 64}
		if sixtyHz {
			v.Lines, v.MinVActive = 264, 40
		}
		return v
	}
}

// TFromInt is the time in T-states from the frame interrupt to raw counter
// position (vc, hc), wrapped at the frame.
func (v VideoTiming) TFromInt(vc, hc int) int {
	frame := v.Lines * v.HC
	d := ((vc*v.HC+hc)-(v.IntV*v.HC+v.IntH))%frame + frame
	return (d % frame) / 2
}

// ActiveVideoLine is cvc, the copper's vertical counter that NR$1E/$1F read
// (zxnext.vhd:5982-5986), t T-states after the frame interrupt. cvc reloads
// with offset (NR$64) on the first paper line at hc = c_min_hactive - 12,
// counts once a line from there and wraps at c_max_vc
// (zxula_timing.vhd:455-466).
func (v VideoTiming) ActiveVideoLine(t int, offset byte) int {
	if t < 0 {
		t = 0
	}
	h := v.IntV*v.HC + v.IntH + 2*t
	vc := (h / v.HC) % v.Lines
	n := vc - v.MinVActive
	if h%v.HC < v.MinHActive-12 {
		n--
	}
	n = ((n % v.Lines) + v.Lines) % v.Lines
	return (int(offset) + n) % v.Lines
}

// LineIntOffset is where the NR$22/$23 line interrupt for target fires, in
// T-states from the frame interrupt, with NR$64 at offset. zxula_timing.vhd
// compares cvc with target-1 (c_max_vc for target 0) at hc_ula 255
// (:566-581). cvc is offset plus the paper row, and takes each value once a
// frame, so the row is target-1-offset; hc_ula counts from c_min_hactive-12,
// so the pulse is at hc = c_min_hactive - 12 + 256 of raw line
// c_min_vactive + row.
func (v VideoTiming) LineIntOffset(target int, offset byte) int {
	row := ((target-1-int(offset))%v.Lines + 2*v.Lines) % v.Lines
	return v.TFromInt((v.MinVActive+row)%v.Lines, v.MinHActive-12+256)
}
