package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// NextReg 0x68 ("ULA Control") decodes five fields, but only bit 7 was ever
// acted on. zxnext.vhd:5444-5450:
//
//	nr_68_ula_en              <= not nr_wr_dat(7);
//	nr_68_blend_mode          <=     nr_wr_dat(6 downto 5);
//	nr_68_cancel_extended_keys<=     nr_wr_dat(4);
//	nr_68_ula_fine_scroll_x   <=     nr_wr_dat(2);
//	nr_68_ula_stencil_mode    <=     nr_wr_dat(0);

func TestDecodeNR68Fields(t *testing.T) {
	for _, tc := range []struct {
		name string
		val  byte
		want nr68
	}{
		{"reset", 0x00, nr68{}},
		{"ULA disabled", 0x80, nr68{ulaDisabled: true}},
		{"blend = ULA+TM", 0x20, nr68{blendMode: 1}},
		{"blend = none", 0x40, nr68{blendMode: 2}},
		{"blend = tilemap", 0x60, nr68{blendMode: 3}},
		{"cancel extended keys", 0x10, nr68{cancelExtendedKeys: true}},
		{"ULA fine scroll X", 0x04, nr68{ulaFineScrollX: true}},
		{"stencil", 0x01, nr68{stencilMode: true}},
		{"all at once", 0xF5, nr68{
			ulaDisabled: true, blendMode: 3, cancelExtendedKeys: true,
			ulaFineScrollX: true, stencilMode: true,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeNR68(tc.val); got != tc.want {
				t.Errorf("decodeNR68(%#02x) = %+v, want %+v", tc.val, got, tc.want)
			}
		})
	}
}

// TestNR68ReadBackMasksReservedBit pins the read-back shape from
// zxnext.vhd:6093: bit 1 is not a stored field and always reads back 0.
func TestNR68ReadBackMasksReservedBit(t *testing.T) {
	for _, tc := range []struct {
		written, want byte
	}{
		{0x00, 0x00},
		{0xFF, 0xFD}, // bit 1 cleared
		{0x02, 0x00}, // only the reserved bit was set
		{0xF5, 0xF5}, // nothing reserved set, unchanged
	} {
		if got := nr68ReadBack(tc.written); got != tc.want {
			t.Errorf("nr68ReadBack(%#02x) = %#02x, want %#02x", tc.written, got, tc.want)
		}
	}
}

// TestDecodeNR68BlendModeIsTwoBits guards the field width: bits 6:5, so the
// four selections must all be reachable and nothing above them leak in.
func TestDecodeNR68BlendModeIsTwoBits(t *testing.T) {
	seen := map[byte]bool{}
	for v := 0; v < 256; v++ {
		seen[decodeNR68(byte(v)).blendMode] = true
	}
	for m := byte(0); m < 4; m++ {
		if !seen[m] {
			t.Errorf("blend mode %d is unreachable", m)
		}
	}
	if len(seen) != 4 {
		t.Errorf("decoded %d distinct blend modes, want 4", len(seen))
	}
}

// TestULAScrollRegistersReachTheULA pins the NR$26 / NR$27 wiring end to end
// on a real Next core: the register write must land on the ULA, not just be
// stored in the register file.
func TestULAScrollRegistersReachTheULA(t *testing.T) {
	emu, err := newEmulator(roms.ModelNext)
	if err != nil {
		t.Skipf("Next core unavailable: %v", err)
	}
	if emu.nextRegs == nil {
		t.Skip("NextRegs not wired on this build")
	}
	emu.nextRegs.WriteReg(0x26, 0x2A)
	emu.nextRegs.WriteReg(0x27, 0x1C)

	x, y := emu.ula.ULAScroll()
	if x != 0x2A {
		t.Errorf("NR$26 -> ULA scrollX = %#02x, want 0x2A", x)
	}
	if y != 0x1C {
		t.Errorf("NR$27 -> ULA scrollY = %#02x, want 0x1C", y)
	}
	// And they read back.
	if got := emu.nextRegs.ReadReg(0x26); got != 0x2A {
		t.Errorf("NR$26 read-back = %#02x, want 0x2A", got)
	}
	if got := emu.nextRegs.ReadReg(0x27); got != 0x1C {
		t.Errorf("NR$27 read-back = %#02x, want 0x1C", got)
	}
}
