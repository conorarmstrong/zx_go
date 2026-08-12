package ula

import "testing"

// NextReg $68 bit 2 is the ULA's half-pixel horizontal scroll. zxula.vhd:353
// builds the shift as `px(2 downto 0) & px(8)` — a 4-bit count in HALF pixels,
// so the low bit moves the picture by half of one ULA pixel.
//
// At the 320-pixel output width that is not representable and the bit was
// stored and ignored. The 80-column path is different: it already builds a
// 640-pixel frame (each ULA pixel doubled, with the native-640 tilemap
// composited over it), and half of a 320-pixel ULA pixel is exactly one unit
// there. That is where the refinement can be shown, and it is the path the
// NextZXOS Guide renders through.

// wideULA builds a ULA wired for the 640-pixel path with a known screen.
func wideULA(t *testing.T) *ULA {
	t.Helper()
	u, mem := newFloatingBusULA(t)
	page := mem.GetPage(5)
	for i := 0; i < 0x1800; i++ {
		page[i] = 0
	}
	// One lit character cell at the far left of row 0.
	page[screenAddrForRowCol(0, 0)] = 0xFF
	for i := 0x1800; i < 0x1B00; i++ {
		page[i] = 0x07 // white ink, black paper
	}
	u.SetNextCompositor(stubCompositor{})
	return u
}

// TestFineScrollShiftsTheWideFrameByOneUnit pins the refinement: with the bit
// set the 640-pixel frame moves by exactly one of its units, which is half a
// ULA pixel.
func TestFineScrollShiftsTheWideFrameByOneUnit(t *testing.T) {
	plain := wideULA(t)
	plain.Render()
	a := plain.renderWide()
	base := make([]byte, len(a.Pix))
	copy(base, a.Pix)

	fine := wideULA(t)
	fine.SetULAFineScrollX(true)
	fine.Render()
	b := fine.renderWide()

	if string(base) == string(b.Pix) {
		t.Fatal("NR$68 bit 2 changed nothing in the 640-pixel frame")
	}

	// The shift must be exactly one unit left: unit n of the fine frame equals
	// unit n+1 of the plain one, across the row the lit cell is on.
	const ww = 2 * TotalWidth
	row := (BorderTop) * a.Stride
	same := 0
	for x := 0; x < ww-1; x++ {
		p := row + (x+1)*4
		q := row + x*4
		if base[p] == b.Pix[q] && base[p+1] == b.Pix[q+1] && base[p+2] == b.Pix[q+2] {
			same++
		}
	}
	if same < ww-8 {
		t.Errorf("only %d of %d units line up under a one-unit shift; the offset is not half a pixel", same, ww-1)
	}
}

// TestFineScrollOffLeavesTheFrameAlone guards the default: every classic model
// and the Next at reset have the bit clear, and must render exactly as before.
func TestFineScrollOffLeavesTheFrameAlone(t *testing.T) {
	u := wideULA(t)
	u.Render()
	before := u.renderWide()
	a := make([]byte, len(before.Pix))
	copy(a, before.Pix)

	u.SetULAFineScrollX(false)
	u.Render()
	after := u.renderWide()
	if string(a) != string(after.Pix) {
		t.Error("with NR$68 bit 2 clear the wide frame changed")
	}
}
