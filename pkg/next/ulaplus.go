package next

import (
	"github.com/conorarmstrong/zx_go/pkg/next/lores"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
)

// ULA+ enable, and the $BF3B register-select latch it is written through.
//
// This models the ENABLE, not the ULA+ palette. The 64-entry palette itself
// still goes through the NextReg path; what was missing is the one bit that
// says whether ULA+ is on, which the LoRes layer needs as ulap_en_i to pick
// the Radastan high nibble. Without it Config.ULAPlus was permanently false
// and Radastan-with-ULA+ resolved into the wrong palette block.
//
// The bit is worth modelling rather than approximating because it is written
// from two unrelated places into ONE storage location, and read back from
// both. The core makes that explicit: NR$68's own nr_68_ulap_en field is
// commented out (zxnext.vhd:5448) precisely because an NR$68 write goes to the
// port's register instead (zxnext.vhd:4551).
//
//	port_bf3b_ulap_mode  <= cpu_do(7 downto 6)     zxnext.vhd:4532
//	port_bf3b_ulap_index <= cpu_do(5 downto 0)     zxnext.vhd:4534 (mode 00 only)
//	port_ff3b_ulap_en    <= cpu_do(0)              zxnext.vhd:4549 (mode 01 only)
//	port_ff3b_ulap_en    <= nr_wr_dat(3)           zxnext.vhd:4551 (any NR$68 write)
//	port_ff3b_ulap_en    <= '0'                    zxnext.vhd:4547 (reset)

// ULAPlus holds the $BF3B mode group and index, and the single enable bit.
type ULAPlus struct {
	mode    byte // $BF3B bits 7:6
	index   byte // $BF3B bits 5:0, latched in mode group 00 only
	enabled bool

	// onChange is fired whenever the enable moves, so the LoRes layer's
	// ulap_en_i input follows without waiting for an unrelated write.
	onChange func()
}

// NewULAPlus returns the power-on state: disabled, mode group 00, index 0.
func NewULAPlus() *ULAPlus { return &ULAPlus{} }

// Enabled reports the ULA+ enable bit.
func (u *ULAPlus) Enabled() bool { return u.enabled }

// Index is the $BF3B palette index, 6 bits.
func (u *ULAPlus) Index() byte { return u.index }

// Mode is the $BF3B mode group, 2 bits.
func (u *ULAPlus) Mode() byte { return u.mode }

// WriteBF3B applies a write to the register-select port. The index latches
// only in mode group 00, so selecting a different group does not scribble over
// the palette index a program set up beforehand (zxnext.vhd:4531-4535).
func (u *ULAPlus) WriteBF3B(v byte) {
	u.mode = (v >> 6) & 0x03
	if u.mode == 0 {
		u.index = v & 0x3F
	}
}

// WriteFF3B applies a write to the data port. Only mode group 01 is the
// enable; group 00 is palette data, which this does not model.
func (u *ULAPlus) WriteFF3B(v byte) {
	if u.mode == 0x01 {
		u.setEnabled(v&0x01 != 0)
	}
}

// ReadFF3B returns the enable read-back and whether this port answers at all.
// In mode group 00 the port returns palette data, which is not modelled here,
// so ok is false and the caller falls through (zxnext.vhd:4560-4568).
func (u *ULAPlus) ReadFF3B() (byte, bool) {
	if u.mode == 0 {
		return 0, false
	}
	if u.enabled {
		return 0x01, true
	}
	return 0x00, true
}

func (u *ULAPlus) setEnabled(on bool) {
	if u.enabled == on {
		return
	}
	u.enabled = on
	if u.onChange != nil {
		u.onChange()
	}
}

// WireULAPlus installs the NextReg side and connects the layer input.
//
// The layer input is NOT the enable on its own:
//
//	ulap_en_i <= ulap_en_0 and not ulanext_en_0     zxnext.vhd:4246
//
// with the port comment "translate radastan pixel to ula+ palette". ULAnext
// mode (NR$43 bit 0) takes priority, and with it on Radastan resolves through
// the plain palette offset instead. Wiring the enable straight through would
// be wrong for every program that uses both.
func WireULAPlus(d *nextregs.Dispatcher, cfg *lores.Config) *ULAPlus {
	if d == nil || cfg == nil {
		panic("next: WireULAPlus needs a dispatcher and a config")
	}
	u := NewULAPlus()

	refresh := func() {
		cfg.ULAPlus = u.enabled && d.Raw(0x43)&0x01 == 0
	}
	u.onChange = refresh

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

	// Any NR$68 write drives the enable from bit 3 — the same bit the port
	// writes, into the same place.
	chain(0x68, func(val byte) {
		u.setEnabled(val&0x08 != 0)
		refresh()
	})
	// NR$43 bit 0 is ULAnext, the gate above.
	chain(0x43, func(byte) { refresh() })

	// NR$68 bit 3 reads back the real enable rather than what was last written
	// to the register (zxnext.vhd:6093).
	prevRead := d.OnReadFn(0x68)
	d.SetOnRead(0x68, func(disp *nextregs.Dispatcher) byte {
		var v byte
		if prevRead != nil {
			v = prevRead(disp)
		} else {
			v = disp.Raw(0x68)
		}
		v &^= 0x08
		if u.enabled {
			v |= 0x08
		}
		return v
	})

	d.SetOnReset(func() {
		u.mode, u.index = 0, 0
		u.enabled = false
		refresh()
	})

	refresh()
	return u
}
