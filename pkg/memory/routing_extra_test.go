package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// iter 361: alt-ROM read/write redirect routing (NR$8C bits 7,6) and
// +3/+2A special-paging 4-config selection (port $1FFD). These
// exercise the Read/Write redirect paths and PageMemoryPlus3's
// all-RAM configs — the residual memory-routing gaps.

// --- NR$8C alt-ROM read/write redirect ---

func TestAltROM_ReadRedirect(t *testing.T) {
	m := newMemoryForMMUTest(t, roms.ModelNext)
	// Plant a marker in alt-ROM bank 0.
	alt := m.GetAltROM(0)
	if alt == nil {
		t.Fatal("GetAltROM(0) nil")
	}
	alt[0x0000] = 0xA5
	alt[0x1234] = 0x5A

	// Capture the normal ROM byte for comparison.
	m.SetAltROMReg(0x00) // disabled
	normal0 := m.Read(0x0000)

	// Enable alt-ROM with bit 6 clear → reads redirect to alt-ROM.
	// No lock bits + 7FFD bit4=0 → alt-ROM bank 0 selected.
	m.SetAltROMReg(0x80)
	if got := m.Read(0x0000); got != 0xA5 {
		t.Errorf("read-redirect $0000 = $%02X, want $A5 (alt-ROM 0)", got)
	}
	if got := m.Read(0x1234); got != 0x5A {
		t.Errorf("read-redirect $1234 = $%02X, want $5A", got)
	}
	// Above $3FFF must NOT redirect.
	if m.Read(0x4000) == 0xA5 {
		t.Error("alt-ROM redirect must not extend past $3FFF")
	}
	// Disable → reads return to normal ROM.
	m.SetAltROMReg(0x00)
	if got := m.Read(0x0000); got != normal0 {
		t.Errorf("after disable, $0000 = $%02X, want normal $%02X", got, normal0)
	}
}

func TestAltROM_WriteRedirect(t *testing.T) {
	m := newMemoryForMMUTest(t, roms.ModelNext)
	// bit 7 + bit 6 set → writes land in alt-ROM, reads pass through.
	m.SetAltROMReg(0xC0)
	m.Write(0x0000, 0x3C)
	m.Write(0x2000, 0xC3)
	if got := m.GetAltROM(0)[0x0000]; got != 0x3C {
		t.Errorf("write-redirect: alt-ROM[0] = $%02X, want $3C", got)
	}
	if got := m.GetAltROM(0)[0x2000]; got != 0xC3 {
		t.Errorf("write-redirect: alt-ROM[$2000] = $%02X, want $C3", got)
	}
	// With bit 6 set, reads are NOT redirected (normal ROM path), so
	// the just-written $3C must not read back at $0000.
	if got := m.Read(0x0000); got == 0x3C {
		t.Error("write-only redirect must not make reads see alt-ROM")
	}
}

func TestAltROM_LockBitSelectsBank1(t *testing.T) {
	m := newMemoryForMMUTest(t, roms.ModelNext)
	m.GetAltROM(1)[0] = 0x77
	// bit7 enable + bit5 lock-ROM1 (48K) → alt-ROM bank 1, reads redirect.
	m.SetAltROMReg(0x80 | 0x20)
	if got := m.Read(0x0000); got != 0x77 {
		t.Errorf("lock-ROM1: read $0000 = $%02X, want $77 (alt-ROM 1)", got)
	}
}

// --- +3 / +2A special paging (port $1FFD bit 0) ---

func TestPlus3SpecialPaging_FourConfigs(t *testing.T) {
	cases := []struct {
		val   byte
		mode  string
		banks [4]int // 16K banks at slots 0..3
	}{
		{0x01, "mode0", [4]int{0, 1, 2, 3}},
		{0x03, "mode1", [4]int{4, 5, 6, 7}},
		{0x05, "mode2", [4]int{4, 5, 6, 3}},
		{0x07, "mode3", [4]int{4, 7, 6, 3}},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			m := newMemoryForMMUTest(t, roms.ModelPlus3)
			m.PageMemoryPlus3(c.val)
			// Each 16K bank B occupies 8K MMU slots 2s,2s+1 = pages
			// 2B, 2B+1.
			for s := 0; s < 4; s++ {
				bank := c.banks[s]
				wantLo := byte(bank * 2)
				wantHi := byte(bank*2 + 1)
				if got := m.GetMMU(byte(2 * s)); got != wantLo {
					t.Errorf("%s slot %d lo: GetMMU=%d, want %d", c.mode, 2*s, got, wantLo)
				}
				if got := m.GetMMU(byte(2*s + 1)); got != wantHi {
					t.Errorf("%s slot %d hi: GetMMU=%d, want %d", c.mode, 2*s+1, got, wantHi)
				}
			}
		})
	}
}

func TestPlus3SpecialPaging_ExitRestoresNormal(t *testing.T) {
	m := newMemoryForMMUTest(t, roms.ModelPlus3)
	m.PageMemoryPlus3(0x03) // enter special mode 1
	m.PageMemoryPlus3(0x00) // bit 0 clear → leave special paging
	// Slot 0 should be a ROM page again (special all-RAM mode is off);
	// the exact ROM page depends on 7FFD/1FFD ROM-select, but it must
	// no longer be RAM bank 4 (page 8) from mode 1.
	if got := m.GetMMU(0); got == 8 {
		t.Errorf("after exit, slot 0 still RAM page 8 — special paging not cleared")
	}
}
