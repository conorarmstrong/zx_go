package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestMMUOverlayReadOverROM reproduces the NextZXOS sprite-attribute-cache
// access that NextBASIC Invaders' SPRITE AT relies on (and which throws
// "Integer out of range, 590:1" when it returns garbage). NextZXOS keeps the
// sprite cache in 16K RAM bank 8 and reads it by paging that bank's 8K pages
// (16/17) into the MMU slots over the $0000-$3FFF ROM region, then reading
// there. The read MUST come from the mapped RAM bank, not the ROM. Verified
// the cache write path is correct in the live game; this pins the read path.
func TestMMUOverlayReadOverROM(t *testing.T) {
	mem, err := New(mmuTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}

	// Put a sentinel in 16K RAM bank 8 at offset $04DF (where NextZXOS's
	// uvec8_sprite_data + sprite0 X byte lives in the live game).
	const off = 0x04DF
	mem.RAM8KPage(16)[off] = 200 // bank 8, low 8K (page 16)
	mem.RAM8KPage(18)[off] = 111 // bank 9 low (page 18) — distinct sentinel

	// Map bank 8 low (8K page 16) into each MMU slot in turn and read it back.
	// The live game reads the cache at $44DF (slot 2, $4000) — the >= $4000
	// path. Slots 0/1 ($0000-$3FFF) and slots 2..7 ($4000-$FFFF) must ALL
	// honour an MMU RAM override on a read.
	for slot := byte(0); slot < 8; slot++ {
		mem.SetMMU(slot, 0xFF) // reset all slots to ROM/default first
	}
	for slot := byte(0); slot < 8; slot++ {
		mem.SetMMU(slot, 16) // map bank 8 low page into this slot
		addr := uint16(slot)*0x2000 + off
		if got := mem.Read(addr); got != 200 {
			t.Errorf("MMU slot %d <- page16, read $%04X = %d, want 200 (RAM bank 8 over default)", slot, addr, got)
		}
		mem.SetMMU(slot, 0xFF) // restore
	}
}

// TestMMUMidSlotsSurviveLegacyPaging: per the FPGA ports doc, a write to
// $7FFD/$1FFD/$DFFD/$EFF7 sets mmu0=mmu1=$FF (ROM reveal) and mmu6/mmu7 (the
// selected bank) — it must NOT disturb mmu2,3,4,5. NextZXOS keeps the sprite
// cache reachable via a mid-slot MMU mapping that persists across the
// interpreter's constant legacy-paging churn; if ours clears it, SPRITE AT
// reverts to the default bank (the NBI 590 bug).
func TestMMUMidSlotsSurviveLegacyPaging(t *testing.T) {
	mem, err := New(mmuTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	const off = 0x04DF
	mem.RAM8KPage(16)[off] = 200 // bank 8 low

	// Per the FPGA ports doc: $7FFD and *normal* $1FFD only set mmu0,1,6,7,
	// so a mid-slot (mmu2) override MUST survive them. (Special $1FFD paging
	// is a separate case — entering remaps all slots and exiting deliberately
	// restores mmu2-5 to banks 5,2, so it legitimately clears mmu2; that is
	// faithful and not tested here.)
	check := func(label string, paging func()) {
		mem.SetMMU(2, 16) // bank 8 low -> slot 2 ($4000-$5FFF)
		paging()
		if got := mem.Read(0x4000 + off); got != 200 {
			t.Errorf("%s: slot2 (mmu2) lost its override, read=%d want 200", label, got)
		}
	}
	check("$7FFD bank5", func() { mem.PageMemory(0x05) })
	check("$1FFD normal", func() { mem.PageMemoryPlus3(0x04) })
}
