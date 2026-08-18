package next

import (
	"github.com/conorarmstrong/zx_go/pkg/next/lores"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
)

// LoRes register wiring.
//
// pkg/next/lores is a faithful port of video/lores.vhd and was golden-tested
// against the FPGA from the day it was written, but nothing fed it: NR$6A was
// decoded, stored in the dispatcher, and read by no render path. This connects
// the four registers that drive lores.vhd's input ports to the Config the
// renderer reads.
//
// Bit positions are from the FPGA source, not from folklore:
//
//	nr_15_lores_en             <= nr_wr_dat(7)        zxnext.vhd:5229
//	nr_32_lores_scrollx        <= nr_wr_dat           zxnext.vhd:5340
//	nr_33_lores_scrolly        <= nr_wr_dat           zxnext.vhd:5343
//	nr_6a_lores_radastan       <= nr_wr_dat(5)        zxnext.vhd:5456
//	nr_6a_lores_radastan_xor   <= nr_wr_dat(4)        zxnext.vhd:5457
//	nr_6a_lores_palette_offset <= nr_wr_dat(3 downto 0)  zxnext.vhd:5458

// LoResState is the part of the layer's input that is not in lores.Config:
// the master enable, and the two halves of the display-file select that have
// to be combined before the layer sees them.
type LoResState struct {
	cfg *lores.Config

	enabled bool
	// timexDfile is port $FF screen-mode bit 0, and radastanXor is NR$6A
	// bit 4. Neither is what the layer wants on its own — see refreshDfile.
	timexDfile  bool
	radastanXor bool
}

// Enabled reports NR$15 bit 7, the layer's master enable. The FPGA gates the
// layer's own per-pixel clip enable with it (`lores_pixel_en_1 <=
// lores_pixel_en_1a and lores_en_1`, zxnext.vhd:6933), so with this clear the
// LoRes picture is not merely hidden — the ULA pixel is what reaches the
// palette, which is why the whole path is inert until a guest asks for it.
func (s *LoResState) Enabled() bool { return s.enabled }

// SetTimexDfile records port $FF screen-mode bit 0.
func (s *LoResState) SetTimexDfile(on bool) {
	s.timexDfile = on
	s.refreshDfile()
}

// refreshDfile combines the two inputs the way the FPGA does:
//
//	lores_dfile_0 <= port_ff_screen_mode(0) xor nr_6a_lores_radastan_xor;
//	                                     -- zxnext.vhd:6796
//
// whose own comment explains the xor: "radastan can coexist with standard
// timex display files". Feeding the Timex bit straight through would put
// Radastan on the wrong display file for every program that sets NR$6A bit 4,
// and the layer has no way to tell.
func (s *LoResState) refreshDfile() {
	s.cfg.Dfile = s.timexDfile != s.radastanXor
}

// WireLoRes installs the NextReg handlers that drive cfg, and returns the
// state that does not live in cfg. Existing NR$15 behaviour is preserved:
// the handler chains to whatever was installed before it, because that
// register also carries the sprite enable, the layer priority and three
// sprite flags.
func WireLoRes(d *nextregs.Dispatcher, cfg *lores.Config) *LoResState {
	s := &LoResState{cfg: cfg}

	prev15 := d.OnWriteFn(0x15)
	d.SetOnWrite(0x15, func(disp *nextregs.Dispatcher, val byte) {
		if prev15 != nil {
			prev15(disp, val)
		} else {
			disp.Store(0x15, val)
		}
		s.enabled = val&0x80 != 0
	})

	prev6A := d.OnWriteFn(0x6A)
	d.SetOnWrite(0x6A, func(disp *nextregs.Dispatcher, val byte) {
		if prev6A != nil {
			prev6A(disp, val)
		} else {
			disp.Store(0x6A, val&0x3F)
		}
		cfg.Radastan = val&0x20 != 0
		cfg.PaletteOffset = val & 0x0F
		s.radastanXor = val&0x10 != 0
		s.refreshDfile()
	})

	d.SetOnWrite(0x32, func(disp *nextregs.Dispatcher, val byte) {
		disp.Store(0x32, val)
		cfg.ScrollX = val
	})
	d.SetOnWrite(0x33, func(disp *nextregs.Dispatcher, val byte) {
		disp.Store(0x33, val)
		cfg.ScrollY = val
	})

	return s
}
