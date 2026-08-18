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
// the registers that drive lores.vhd's input ports to the Config the renderer
// reads.
//
// Bit positions are from the FPGA source, not from folklore:
//
//	nr_15_lores_en             <= nr_wr_dat(7)        zxnext.vhd:5229
//	nr_32_lores_scrollx        <= nr_wr_dat           zxnext.vhd:5340
//	nr_33_lores_scrolly        <= nr_wr_dat           zxnext.vhd:5343
//	nr_6a_lores_radastan       <= nr_wr_dat(5)        zxnext.vhd:5456
//	nr_6a_lores_radastan_xor   <= nr_wr_dat(4)        zxnext.vhd:5457
//	nr_6a_lores_palette_offset <= nr_wr_dat(3 downto 0)  zxnext.vhd:5458
//
// ONE INPUT IS NOT CONNECTED, and saying so is the point of this paragraph.
// lores.vhd takes five inputs from outside the raster; the fifth is ulap_en_i,
// which selects the Radastan high nibble (lores.go's ULAPlus). Nothing in this
// tree tracks whether ULA+ is enabled, so there is no source to wire it from
// and Config.ULAPlus stays false. In Radastan mode with ULA+ on, every pixel
// then resolves into the wrong palette block. That is a real gap, not an
// omission from this comment.

// LoResState is the part of the layer's input that is not in lores.Config.
//
// It deliberately caches almost nothing. The master enable and the xor half of
// the display-file select are bits of NR$15 and NR$6A, and the dispatcher's
// own LoadState assigns its register array directly without firing any OnWrite
// handler (pkg/next/nextregs/state.go). A cached copy would therefore survive a
// rewind while the register it mirrors moved underneath it, and the next write
// to the OTHER half of the display file would recompute it from a value the
// machine no longer holds. Reading through to the register file cannot go
// stale, and needs no capture of its own.
type LoResState struct {
	d   *nextregs.Dispatcher
	cfg *lores.Config

	// timexDfile is port $FF screen-mode bit 0, which belongs to the ULA
	// rather than to the NextReg file, so it is the one value that has to be
	// pushed in. SetTimexDfile is the only writer.
	timexDfile bool
}

// Enabled reports NR$15 bit 7, the layer's master enable, read live from the
// register file. The FPGA gates the layer's own per-pixel clip enable with it
// (`lores_pixel_en_1 <= lores_pixel_en_1a and lores_en_1`, zxnext.vhd:6933),
// so with this clear the LoRes picture is not merely hidden: the ULA pixel is
// what reaches the palette. That is why the whole path stays inert until a
// guest asks for it.
func (s *LoResState) Enabled() bool { return s.d.Raw(0x15)&0x80 != 0 }

// SetTimexDfile records port $FF screen-mode bit 0 and recomputes the layer's
// display-file select.
func (s *LoResState) SetTimexDfile(on bool) {
	s.timexDfile = on
	s.refreshDfile()
}

// refreshDfile combines the two halves the way the FPGA does:
//
//	lores_dfile_0 <= port_ff_screen_mode(0) xor nr_6a_lores_radastan_xor;
//	                                     -- zxnext.vhd:6796
//
// whose own comment explains the xor: "radastan can coexist with standard
// timex display files". Feeding the Timex bit straight through would put
// Radastan on the wrong display file for every program that sets NR$6A bit 4,
// and the layer has no way to tell.
func (s *LoResState) refreshDfile() {
	s.cfg.Dfile = s.timexDfile != (s.d.Raw(0x6A)&0x10 != 0)
}

// Refresh recomputes every derived value from the register file. It runs at
// install time and after a reset, so the Config never starts out disagreeing
// with the registers it mirrors — the trap SetULAClipSink exists to avoid for
// the clip window, in the one other place this package pushes register bytes
// into a layer.
func (s *LoResState) Refresh() {
	v := s.d.Raw(0x6A)
	s.cfg.Radastan = v&0x20 != 0
	s.cfg.PaletteOffset = v & 0x0F
	s.cfg.ScrollX = s.d.Raw(0x32)
	s.cfg.ScrollY = s.d.Raw(0x33)
	s.refreshDfile()
}

// WireLoRes installs the NextReg handlers that drive cfg, and returns the
// state that does not live in cfg.
//
// ORDER MATTERS, and nothing can enforce it. Dispatcher.SetOnWrite replaces
// whatever was installed for a register, so WireLoRes chains to the previous
// NR$15 and NR$6A handlers rather than displacing them — NR$15 also carries the
// sprite enable, the layer priority and three sprite flags. That chaining only
// works if WireLoRes runs AFTER the subsystems owning those registers
// (WireLayerPriority for NR$15, WirePeripheralMasks for NR$6A). Called earlier,
// this function's own handler is the one silently replaced, the register still
// reads back correctly, and the LoRes side effect vanishes with no diagnostic.
// next.Wire is where the order is fixed; do not call this before it.
func WireLoRes(d *nextregs.Dispatcher, cfg *lores.Config) *LoResState {
	if d == nil || cfg == nil {
		// Failing here beats a nil dereference on the emulation goroutine
		// during a guest register write, which is where the panic would
		// otherwise land -- far from the mistake and mid-frame.
		panic("next: WireLoRes needs a dispatcher and a config")
	}
	s := &LoResState{d: d, cfg: cfg}

	// Each of these chains to the handler already installed for the register,
	// including NR$32/$33 which nothing wires today: a bare SetOnWrite there
	// would be a silent clobber waiting for the first subsystem that does.
	chain := func(reg byte, after func(val byte)) {
		prev := d.OnWriteFn(reg)
		d.SetOnWrite(reg, func(disp *nextregs.Dispatcher, val byte) {
			if prev != nil {
				prev(disp, val)
			} else {
				disp.Store(reg, val)
			}
			after(val)
		})
	}

	chain(0x15, func(byte) {}) // read live by Enabled; nothing to cache
	chain(0x6A, func(val byte) {
		cfg.Radastan = val&0x20 != 0
		cfg.PaletteOffset = val & 0x0F
		s.refreshDfile()
	})
	chain(0x32, func(val byte) { cfg.ScrollX = val })
	chain(0x33, func(val byte) { cfg.ScrollY = val })

	// A reset clears port $FF on the FPGA, so the cached Timex bit must go
	// with it. Without this the pre-reset display file leaks through the xor
	// and Radastan reads from $6000 where hardware reads $4000.
	d.SetOnReset(func() {
		s.timexDfile = false
		s.Refresh()
	})

	s.Refresh()
	return s
}
