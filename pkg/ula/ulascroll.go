package ula

// ULA scroll — NextReg 0x26 (horizontal) and 0x27 (vertical), with the
// half-pixel refinement from NextReg 0x68 bit 2.
//
// Transcribed from video/zxula.vhd's address generation (lines 192-216):
//
//	py_s <= i_vc + ('0' & i_ula_scroll_y);
//	px   <= i_ula_fine_scroll_x & (i_hc(7 downto 3) + i_ula_scroll_x(7 downto 3))
//	                            & i_ula_scroll_x(2 downto 0);
//
// So the horizontal scroll splits into a whole-character offset (bits 7:3),
// which moves which cell is fetched, and a pixel shift within the character
// (bits 2:0). Vertical is a 9-bit sum folded back onto the 192-line screen.

// ulaScrollRow maps a display row to the screen row the ULA fetches for it,
// applying the NR$27 vertical scroll.
//
// The fold is not a modulo. 192 is not a power of two, so zxula.vhd:200-206
// folds the 9-bit sum with a pair of special cases:
//
//	py_s[8:7] == "11"            -> bit 7 forced low
//	py_s[8] or py_s[7:6] == "11" -> bits 7:6 incremented, low 6 kept
//	otherwise                    -> the low 8 bits as-is
//
// which keeps every result inside 0..191.
func ulaScrollRow(vc int, scrollY byte) int {
	p := vc + int(scrollY)
	b8, b7, b6 := (p>>8)&1, (p>>7)&1, (p>>6)&1
	switch {
	case b8 == 1 && b7 == 1:
		return p & 0x7F
	case b8 == 1 || (b7 == 1 && b6 == 1):
		return ((((p >> 6) & 3) + 1) & 3 << 6) | (p & 0x3F)
	default:
		return p & 0xFF
	}
}

// ulaScrollColumn splits the NR$26 horizontal scroll into the whole-character
// offset applied to the fetch address and the pixel shift applied within the
// fetched pair of cells.
func ulaScrollColumn(scrollX byte) (chars, pixels int) {
	return int(scrollX>>3) & 31, int(scrollX & 7)
}

// SetULAScrollX installs NextReg 0x26, the ULA horizontal scroll.
func (u *ULA) SetULAScrollX(v byte) { u.ulaScrollX = v }

// SetULAScrollY installs NextReg 0x27, the ULA vertical scroll.
func (u *ULA) SetULAScrollY(v byte) { u.ulaScrollY = v }

// SetULAFineScrollX installs NextReg 0x68 bit 2, the half-pixel horizontal
// refinement.
//
// Stored but not rendered: zxula.vhd:353 builds the shift as
// `px(2 downto 0) & px(8)`, a 4-bit count in HALF pixels, so showing it needs
// the ULA layer rendered at twice the horizontal resolution. Everything else
// here works at whole-pixel granularity, so the flag is carried for read-back
// and for whenever a 2x path exists.
func (u *ULA) SetULAFineScrollX(on bool) { u.ulaFineScrollX = on }

// ULAScroll returns the active NR$26 / NR$27 scroll values.
func (u *ULA) ULAScroll() (x, y byte) { return u.ulaScrollX, u.ulaScrollY }
