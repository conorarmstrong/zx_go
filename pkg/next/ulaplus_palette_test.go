package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/lores"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
)

// The ULA+ palette path: $BF3B mode group 00 selects an entry, $FF3B carries
// its colour. Every step below is from the FPGA source, because none of it is
// guessable from the ULA+ standard alone — the Next routes the port write into
// its own NextReg palette stream rather than keeping a separate ULA+ palette.
//
//	cpu_requester_reg <= ... else X"FF"                      zxnext.vhd:4744
//	cpu_requester_dat <= ... else (cpu_do(4:2) & cpu_do(7:5) & cpu_do(1:0))
//	                                                          zxnext.vhd:4745
//	nr_palette_value  <= nr_wr_dat & (nr_wr_dat(1) or nr_wr_dat(0))
//	                                                          zxnext.vhd:4919
//	nr_palette_index_utm <= '0' & wsel(2) & "11" & ulap_index  zxnext.vhd:6958
//	read: dat(5:3) & dat(8:6) & dat(2:1)                       zxnext.vhd:4563
//
// So a ULA+ colour byte is GGGRRRBB, it is repacked to RRRGGGBB on the way in
// and back to GGGRRRBB on the way out, and the 64 ULA+ entries live at
// $C0..$FF of the ULA palette rather than in a palette of their own.

func newULAPlusPalette(t *testing.T) (*ULAPlus, *nextregs.Dispatcher, *palette.Bank) {
	t.Helper()
	d := nextregs.New()
	b := palette.NewBank()
	WirePalette(d, b)
	var cfg lores.Config
	u := WireULAPlus(d, &cfg)
	u.SetPalette(b)
	return u, d, b
}

// A ULA+ colour write lands at $C0 + index of the ULA palette, not in a
// palette of its own: "11" & ulap_index is the low 8 bits of the address.
func TestAULAPlusColourLandsAtC0PlusTheIndex(t *testing.T) {
	u, _, b := newULAPlusPalette(t)

	u.WriteBF3B(0x05) // mode group 00, index 5
	if !u.WriteFF3B(0xFF) {
		t.Fatal("the palette-group write was declined")
	}

	pal := b.Palette(int(palette.PaletteULAFirst))
	if got := pal.Get(0xC0 + 5); got == 0 {
		t.Errorf("entry $C5 is still zero: the colour did not land where the FPGA " +
			"addresses it (zxnext.vhd:6958)")
	}
	if got := pal.Get(5); got != 0 {
		t.Errorf("entry $05 = %#03x: the index was used raw instead of offset by $C0", got)
	}
}

// The byte is GGGRRRBB going in and coming out, and the round trip has to
// preserve it. Getting the repack backwards would swap red and green on every
// ULA+ colour, which looks plausible enough to ship.
func TestAULAPlusColourRoundTripsThroughThePort(t *testing.T) {
	u, _, _ := newULAPlusPalette(t)

	for _, want := range []byte{0x00, 0xFF, 0xE0, 0x1C, 0x03, 0xA5, 0x5A} {
		u.WriteBF3B(0x11) // index $11
		u.WriteFF3B(want)

		got, ok := u.ReadFF3B()
		if !ok {
			t.Fatalf("the palette group declined a read")
		}
		if got != want {
			t.Errorf("wrote %#02x, read back %#02x: the GGGRRRBB repack is not symmetric",
				want, got)
		}
	}
}

// Red and green must not be interchangeable, or the test above would pass with
// the repack reversed. A pure-red ULA+ byte has to produce a palette entry
// whose red is full and green zero.
func TestRedAndGreenAreNotSwapped(t *testing.T) {
	u, _, b := newULAPlusPalette(t)

	u.WriteBF3B(0x00)
	u.WriteFF3B(0x1C) // GGGRRRBB: G=000, R=111, B=00 -> pure red

	pal := b.Palette(int(palette.PaletteULAFirst))
	e := pal.Get(0xC0)
	r := (e >> 6) & 0x07
	g := (e >> 3) & 0x07
	if r != 0x07 || g != 0x00 {
		t.Errorf("pure-red ULA+ byte gave palette r=%d g=%d, want 7 and 0: red and "+
			"green are swapped in the repack", r, g)
	}
}

// The ninth palette bit is the OR of the two blue bits, which is how every
// 8-bit palette write on this machine expands (zxnext.vhd:4919).
func TestTheNinthBitIsTheOrOfTheBlueBits(t *testing.T) {
	u, _, b := newULAPlusPalette(t)
	pal := b.Palette(int(palette.PaletteULAFirst))

	u.WriteBF3B(0x00)
	u.WriteFF3B(0x00) // blue 00
	if e := pal.Get(0xC0); e&0x01 != 0 {
		t.Errorf("entry = %#03x, want the low bit clear when both blue bits are", e)
	}

	u.WriteFF3B(0x01) // blue 01
	if e := pal.Get(0xC0); e&0x01 == 0 {
		t.Errorf("entry = %#03x, want the low bit set when a blue bit is", e)
	}
}

// wsel bit 2 chooses which of the two ULA palettes the write lands in, exactly
// as it does for a NextReg palette write.
func TestTheWriteSelectChoosesWhichULAPalette(t *testing.T) {
	u, d, b := newULAPlusPalette(t)

	// NR$43 bits 6:4 are wsel; wsel 4 is ULA Second.
	d.WriteReg(0x43, 4<<4)
	u.WriteBF3B(0x07)
	u.WriteFF3B(0xFF)

	if got := b.Palette(int(palette.PaletteULASecond)).Get(0xC7); got == 0 {
		t.Error("the colour did not land in the second ULA palette (zxnext.vhd:6958)")
	}
	if got := b.Palette(int(palette.PaletteULAFirst)).Get(0xC7); got != 0 {
		t.Error("the colour landed in the first ULA palette despite wsel selecting the second")
	}
}

// The ULA+ index does not auto-increment. Only $BF3B moves it, so a program
// writing several colours must reselect between them; incrementing would be
// the NextReg path's behaviour leaking in.
func TestTheULAPlusIndexDoesNotAutoIncrement(t *testing.T) {
	u, _, b := newULAPlusPalette(t)

	u.WriteBF3B(0x00)
	u.WriteFF3B(0xE0)
	u.WriteFF3B(0x1C) // same entry, overwritten

	pal := b.Palette(int(palette.PaletteULAFirst))
	if pal.Get(0xC1) != 0 {
		t.Error("a second colour write advanced to the next entry")
	}
	if got, _ := u.ReadFF3B(); got != 0x1C {
		t.Errorf("entry $C0 reads %#02x, want the second colour %#02x", got, 0x1C)
	}
}

// Without a palette attached the group still declines rather than pretending,
// which is what it did before the palette existed.
func TestThePaletteGroupDeclinesWithNoPaletteAttached(t *testing.T) {
	d := nextregs.New()
	var cfg lores.Config
	u := WireULAPlus(d, &cfg)

	u.WriteBF3B(0x00)
	if u.WriteFF3B(0xFF) {
		t.Error("a palette write was accepted with no palette wired")
	}
	if _, ok := u.ReadFF3B(); ok {
		t.Error("a palette read was answered with no palette wired")
	}
}
