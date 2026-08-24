package ula

import (
	"fmt"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/copper"
)

// countingRegWriter counts the NextReg writes a copper MOVE makes, into a byte
// the compositor stub stamps into the pixels it composes. Reading the finished
// row back therefore says how many writes had landed by each pixel.
type countingRegWriter struct{ n *byte }

func (w countingRegWriter) WriteReg(_, _ byte) { *w.n++ }

// TestCopperListLandsItsWritesWhereTheRasterIs is the acceptance test for the
// whole raster-accuracy path: a real Copper, a real render walk, and the pixel
// each MOVE first shows in.
//
// The list is a WAIT for column 4 followed by 64 MOVEs. Three separate pieces
// of hardware behaviour decide where those writes land:
//
//   - The WAIT threshold is (X<<3)+12 in hc_ula (device/copper.vhd:94), and
//     hc_ula 12 IS displayed pixel 0 (video/zxula.vhd:44-46), so column 4
//     releases at displayed pixel 32, not pixel 44, which is where treating
//     hcount as the display x put it.
//   - The Copper is clocked at 28 MHz against a 7 MHz hcount
//     (zxnext.vhd:43,46,3944) and a MOVE costs two clocks
//     (device/copper.vhd:87,105), so the burst retires two writes per pixel and
//     occupies 32 pixels of real line. It cannot all land before pixel 0.
//   - The pixel a write lands in is the first that can show it, so the row is
//     divided there rather than at an 8-pixel segment boundary.
func TestCopperListLandsItsWritesWhereTheRasterIs(t *testing.T) {
	const moves = 64
	const releasePixel = 32 // WAIT column 4 -> displayed pixel 8*4

	u, _ := newFloatingBusULA(t)
	var writes byte

	c := copper.New()
	c.SetWritePtrLow(0)
	wait := uint16(0x8000) | (uint16(4) << 9) // WAIT Y=0, X=4
	c.WriteData(byte(wait >> 8))
	c.WriteData(byte(wait))
	for i := 0; i < moves; i++ {
		c.WriteData(0x40) // MOVE reg 0x40
		c.WriteData(byte(i))
	}
	c.WriteData(0xFF) // the $FFFF terminator: parks the list so the row is readable
	c.WriteData(0xFF)
	c.SetRegWriter(countingRegWriter{n: &writes})
	c.SetWritePtrHighAndMode(byte(copper.StartFromZero) << 6)

	u.SetNextCopper(c)
	u.SetNextCompositor(&stateStampCompositor{state: &writes})

	u.applyNextCompositor()

	row := u.img.Pix[BorderTop*u.img.Stride+BorderLeft*4:]
	for x := 0; x < ScreenWidth; x++ {
		want := 0
		switch {
		case x < releasePixel:
			want = 0
		case x < releasePixel+moves/2:
			want = 2 * (x - releasePixel + 1)
		default:
			want = moves
		}
		if got := int(row[x*4]); got != want {
			t.Fatalf("pixel %d shows %d copper writes, want %d", x, got, want)
		}
	}
}

// boundsCheckingCompositor stamps the shared state into the range it is handed,
// exactly as stateStampCompositor does, and additionally records any range a
// real compositor could not survive.
//
// The real one walks "for x := x0; x < x1; x++ { off := x*4; paintBase(off) }"
// over a destination row of exactly ScreenWidth pixels
// (next/compositor/compositor.go:833-838), so an x0 below zero or above
// ScreenWidth indexes dst out of range and panics. A stub that ignores x0/x1
// cannot see that, which is why the two clamps in the render walk survived
// mutation.
type boundsCheckingCompositor struct {
	stubCompositor
	state *byte
	bad   []string
}

func (c *boundsCheckingCompositor) ComposeScanlineRange(y int, _, dst []byte, from, to int) {
	if from < 0 || from > to || to > ScreenWidth {
		c.bad = append(c.bad, fmt.Sprintf("row %d: range [%d,%d)", y, from, to))
		return
	}
	for x := from; x < to; x++ {
		dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = *c.state, 0, 0, 0xFF
	}
}

// TestACopperWriteBeforePixelZeroComposesTheWholeRow pins the low edge of the
// render walk's pixel clamp.
//
// hc_ula 0 is twelve columns before displayed pixel 0 (video/zxula.vhd:44-46),
// so a MOVE retiring in hcount 0..11, twelve of every 448 columns, entirely
// ordinary, maps to a negative displayed pixel. The whole row is generated
// after such a write, so the re-compose has to start at pixel 0; handing the
// compositor the negative number indexes its destination row out of range.
func TestACopperWriteBeforePixelZeroComposesTheWholeRow(t *testing.T) {
	const writeHCount = 5 // p = 5 - 12 = -7

	u, _ := newFloatingBusULA(t)
	var state byte
	comp := &boundsCheckingCompositor{state: &state}
	u.SetNextCopper(&writeAtCopper{atHCount: writeHCount, state: &state})
	u.SetNextCompositor(comp)

	u.applyNextCompositor()

	if len(comp.bad) != 0 {
		t.Fatalf("the compositor was handed %d unusable ranges, first %s",
			len(comp.bad), comp.bad[0])
	}
	row := u.img.Pix[BorderTop*u.img.Stride+BorderLeft*4:]
	for x := 0; x < ScreenWidth; x++ {
		if got := row[x*4]; got != 1 {
			t.Fatalf("row 0 pixel %d composed under state %d, want 1: the MOVE retired at hcount %d, before pixel 0",
				x, got, writeHCount)
		}
	}
}

// TestACopperWriteAfterTheLastPixelComposesNothing pins the high edge.
//
// The line runs on past displayed pixel 255, 448 columns on 48K timing
// (video/zxula_timing.vhd:160), so a MOVE can retire at an hcount no pixel of
// the row corresponds to. The write stands and the next row starts from it, but
// this row must not be re-composed from a pixel index off the end of it.
func TestACopperWriteAfterTheLastPixelComposesNothing(t *testing.T) {
	const writeHCount = 300 // p = 300 - 12 = 288, past the 256-pixel row

	u, _ := newFloatingBusULA(t)
	var state byte
	comp := &boundsCheckingCompositor{state: &state}
	u.SetNextCopper(&writeAtCopper{atHCount: writeHCount, state: &state})
	u.SetNextCompositor(comp)

	u.applyNextCompositor()

	if len(comp.bad) != 0 {
		t.Fatalf("the compositor was handed %d unusable ranges, first %s",
			len(comp.bad), comp.bad[0])
	}
	row := u.img.Pix[BorderTop*u.img.Stride+BorderLeft*4:]
	for x := 0; x < ScreenWidth; x++ {
		if got := row[x*4]; got != 0 {
			t.Fatalf("row 0 pixel %d composed under state %d, want 0: the MOVE retired at hcount %d, past the last pixel of the row",
				x, got, writeHCount)
		}
	}
}
