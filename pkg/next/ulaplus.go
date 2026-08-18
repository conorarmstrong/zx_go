package next

import (
	"bytes"
	"encoding/gob"
	"fmt"

	"github.com/conorarmstrong/zx_go/pkg/next/lores"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
)

// ULA+ enable, and the $BF3B register-select latch it is written through.
//
// Both halves are modelled: the enable, and the 64-entry palette behind mode
// group 00.
//
// THE ULA+ PALETTE IS NOT A SEPARATE PALETTE. The Next routes a $FF3B colour
// write into its own NextReg palette stream — the port write presents itself
// as a write to NextReg $FF (zxnext.vhd:4744) with the byte repacked on the
// way (:4745) — and the 64 ULA+ entries land at $C0..$FF of the ULA palette
// (:6958). That is not an implementation detail to paper over: it is why
// lores.vhd's Radastan ULA+ nibble is "11" & offset(1:0), which addresses
// exactly that range.
//
// THE ENABLE HAS ONE STORAGE LOCATION, and that is the whole design here.
// The FPGA writes port_ff3b_ulap_en from two unrelated places and reads it
// back from both; the core's own nr_68_ulap_en field is commented out
// (zxnext.vhd:5448) precisely because an NR$68 write goes to the port's
// register instead. Mirroring that, the enable lives in NR$68 bit 3 of the
// register file and nowhere else: no cached copy to fall out of step, one
// value for Raw, SaveState and ReadReg alike, and it is captured by whoever
// captures the dispatcher rather than needing a second blob of its own.
//
//	port_bf3b_ulap_mode  <= cpu_do(7 downto 6)     zxnext.vhd:4532
//	port_bf3b_ulap_index <= cpu_do(5 downto 0)     zxnext.vhd:4534 (mode 00 only)
//	port_ff3b_ulap_en    <= cpu_do(0)              zxnext.vhd:4549 (mode 01 only)
//	port_ff3b_ulap_en    <= nr_wr_dat(3)           zxnext.vhd:4551 (any NR$68 write)
//	reset clears the enable                        zxnext.vhd:4547
//	reset clears the mode group and the index      zxnext.vhd:4529-4530

// ULAPlus holds the $BF3B mode group and index. The enable is NOT a field
// here; see the note above.
type ULAPlus struct {
	d *nextregs.Dispatcher

	mode  byte // $BF3B bits 7:6
	index byte // $BF3B bits 5:0, latched in mode group 00 only

	pal     *palette.Bank
	refresh func()
}

// SetPalette attaches the palette the ULA+ entries live in. Without it the
// palette mode group declines, which is what it did before the path existed.
func (u *ULAPlus) SetPalette(b *palette.Bank) { u.pal = b }

// ulaPlusEntry resolves the palette and entry index a ULA+ colour addresses:
// the ULA palette wsel bit 2 selects, at $C0 + the 6-bit index.
//
//	nr_palette_index_utm <= '0' & wsel(2) & "11" & ulap_index   zxnext.vhd:6958
//
// wsel is NR$43 bits 6:4, so its bit 2 is NR$43 bit 6.
func (u *ULAPlus) ulaPlusEntry() (*palette.Palette, byte, bool) {
	if u.pal == nil {
		return nil, 0, false
	}
	which := palette.PaletteULAFirst
	if u.d.Raw(0x43)&0x40 != 0 {
		which = palette.PaletteULASecond
	}
	p := u.pal.Palette(int(which))
	if p == nil {
		return nil, 0, false
	}
	return p, 0xC0 | (u.index & 0x3F), true
}

// ulaPlusToNine converts a ULA+ colour byte to a 9-bit palette entry.
//
// The byte is GGGRRRBB. The FPGA repacks it to RRRGGGBB on its way into the
// palette stream (zxnext.vhd:4745) and then expands it the way every 8-bit
// palette write on this machine expands, with a ninth bit that is the OR of
// the two blue bits (:4919).
func ulaPlusToNine(v byte) uint16 {
	g := (v >> 5) & 0x07
	r := (v >> 2) & 0x07
	b := v & 0x03
	packed := r<<5 | g<<2 | b
	nine := uint16(packed) << 1
	if b != 0 {
		nine |= 1
	}
	return nine
}

// nineToULAPlus is the inverse, matching the read mux
// `dat(5:3) & dat(8:6) & dat(2:1)` (zxnext.vhd:4563). The ninth bit is not
// reported, exactly as on hardware: an entry written through NR$44 with a blue
// low bit the 8-bit form cannot express reads back without it.
func nineToULAPlus(e uint16) byte {
	g := byte((e >> 3) & 0x07)
	r := byte((e >> 6) & 0x07)
	b := byte((e >> 1) & 0x03)
	return g<<5 | r<<2 | b
}

// Enabled reports the ULA+ enable, read from its single home in NR$68 bit 3.
func (u *ULAPlus) Enabled() bool { return u.d.Raw(0x68)&0x08 != 0 }

// Index is the $BF3B palette index, 6 bits: which of the 64 ULA+ entries the
// next colour write or read addresses.
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

// WriteFF3B applies a write to the data port, reporting whether this port
// consumed it. Mode group 00 is a palette colour; group 01 is the enable.
// Groups 10 and 11 carry nothing the hardware writes, so they are declined
// rather than swallowed and the caller falls through.
func (u *ULAPlus) WriteFF3B(v byte) bool {
	if u.mode == 0 {
		p, idx, ok := u.ulaPlusEntry()
		if !ok {
			return false
		}
		p.Set(idx, ulaPlusToNine(v))
		return true
	}
	if u.mode != 0x01 {
		return false
	}
	raw := u.d.Raw(0x68) &^ 0x08
	if v&0x01 != 0 {
		raw |= 0x08
	}
	u.d.Store(0x68, raw)
	u.refresh()
	return true
}

// ReadFF3B returns the enable read-back and whether this port answers.
//
// Mode group 00 serves the selected palette colour. EVERY other group returns
// the enable, including 10 and 11: the core's read mux is
// `if mode = "00" then palette else enable` (zxnext.vhd:4560-4568), which is
// deliberately not the same gate as the write side's `mode = "01"`. Narrowing
// this to group 01 to match the write would diverge from the hardware.
func (u *ULAPlus) ReadFF3B() (byte, bool) {
	if u.mode == 0 {
		p, idx, ok := u.ulaPlusEntry()
		if !ok {
			return 0, false
		}
		return nineToULAPlus(p.Get(idx)), true
	}
	if u.Enabled() {
		return 0x01, true
	}
	return 0x00, true
}

// ulaPlusState is the wire form: the $BF3B latch, and only that. The enable
// lives in NR$68 and is captured with the register file, so putting it here as
// well would be the "one register in two blobs, restore leaves whichever was
// applied last" failure the machinestate registry exists to prevent.
type ulaPlusState struct {
	Mode  byte
	Index byte
}

// StateID identifies the ULA+ select latch in a captured machine state.
func (u *ULAPlus) StateID() string { return "next.ulaplus" }

// SaveState captures the $BF3B latch.
func (u *ULAPlus) SaveState() []byte {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(ulaPlusState{Mode: u.mode, Index: u.index}); err != nil {
		// Runs from the capture hook, which skips a failed capture rather than
		// stopping the machine; a nil blob is rejected by LoadState.
		return nil
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
func (u *ULAPlus) LoadState(b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("next: empty ULA+ state (the capture failed)")
	}
	var s ulaPlusState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("next: decoding ULA+ state: %w", err)
	}
	if s.Mode > 3 {
		return fmt.Errorf("next: ULA+ state has mode group %d, which is 2 bits", s.Mode)
	}
	if s.Index > 0x3F {
		return fmt.Errorf("next: ULA+ state has index %d, which is 6 bits", s.Index)
	}
	u.mode, u.index = s.Mode, s.Index
	return nil
}

// WireULAPlus installs the NextReg side and connects the layer input.
//
// The layer input is NOT the enable on its own:
//
//	ulap_en_i <= ulap_en_0 and not ulanext_en_0     zxnext.vhd:4246
//
// with the port comment "translate radastan pixel to ula+ palette". ULAnext
// mode (NR$43 bit 0) takes priority, and with it on Radastan resolves through
// the plain palette offset instead.
//
// ORDER MATTERS, and nothing can enforce it — the same hazard WireLoRes
// carries. Dispatcher.SetOnWrite replaces whatever was installed, so this
// chains to the existing NR$43 and NR$68 handlers rather than displacing them
// (NR$43 is the palette select, NR$68 the ULA control register). That chaining
// only works if WireULAPlus runs AFTER next.Wire has installed them. Called
// earlier, this function's own handler is the one silently replaced, the
// registers still read back correctly, and the ULAnext gate never fires.
func WireULAPlus(d *nextregs.Dispatcher, cfg *lores.Config) *ULAPlus {
	if d == nil || cfg == nil {
		panic("next: WireULAPlus needs a dispatcher and a config")
	}
	u := &ULAPlus{d: d}
	u.refresh = func() {
		cfg.ULAPlus = u.Enabled() && d.Raw(0x43)&0x01 == 0
	}

	// An NR$68 write already stores bit 3, which IS the enable; there is
	// nothing to copy, only the layer input to recompute.
	chainOnWrite(d, 0x68, func(byte) { u.refresh() })
	// NR$43 bit 0 is ULAnext, the gate above.
	chainOnWrite(d, 0x43, func(byte) { u.refresh() })

	// zxnext.vhd:4529-4530 resets the mode group and the index; :4547 resets
	// the enable, which lives in the register file and is cleared by the
	// dispatcher's own reset pass.
	d.SetOnReset(func() {
		u.mode, u.index = 0, 0
		u.refresh()
	})

	u.refresh()
	return u
}
