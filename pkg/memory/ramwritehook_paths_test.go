package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// SetRAMWriteHook must observe writes through EVERY dispatch path —
// a D31de-era observation suspected the classic-16K dispatch missed
// the hook; this pins all three (classic 16K, ModelNext
// MMU-override 8K, and +3 special paging) so any future routing
// refactor that drops one fails loudly.
func TestRAMWriteHookFiresOnAllDispatchPaths(t *testing.T) {
	type hit struct {
		bank int
		addr uint16
		val  byte
	}

	t.Run("next-classic-16k", func(t *testing.T) {
		dir := "test_roms_wh_classic"
		createTestROMs(t, dir)
		defer cleanupTestROMs(dir)
		mem, err := New(dir, roms.ModelNext)
		if err != nil {
			t.Fatal(err)
		}
		var got []hit
		mem.SetRAMWriteHook(func(bank int, addr uint16, val byte) {
			got = append(got, hit{bank, addr, val})
		})
		// Slot 2 ($8000) default: classic page map bank 2, no MMU
		// override → the classic 16K dispatch.
		if mem.IsMMUOverridden(4) {
			t.Skip("slot 4 unexpectedly MMU-overridden")
		}
		mem.Write(0x8005, 0x77)
		if len(got) != 1 || got[0] != (hit{2, 5, 0x77}) {
			t.Errorf("classic dispatch hook = %+v, want [{2 5 77}]", got)
		}
	})

	t.Run("next-mmu-override-8k", func(t *testing.T) {
		dir := "test_roms_wh_mmu"
		createTestROMs(t, dir)
		defer cleanupTestROMs(dir)
		mem, err := New(dir, roms.ModelNext)
		if err != nil {
			t.Fatal(err)
		}
		var got []hit
		mem.SetRAMWriteHook(func(bank int, addr uint16, val byte) {
			got = append(got, hit{bank, addr, val})
		})
		mem.SetMMU(4, 20) // slot 4 ($8000-$9FFF) → 8K bank 20 = 16K bank 10 lower
		mem.Write(0x8003, 0x55)
		if len(got) != 1 || got[0] != (hit{10, 3, 0x55}) {
			t.Errorf("MMU dispatch hook = %+v, want [{10 3 85}]", got)
		}
	})

	t.Run("plus3-special-paging", func(t *testing.T) {
		dir := "test_roms_wh_special"
		createTestROMs(t, dir)
		defer cleanupTestROMs(dir)
		mem, err := New(dir, roms.ModelPlus3)
		if err != nil {
			t.Fatal(err)
		}
		var got []hit
		mem.SetRAMWriteHook(func(bank int, addr uint16, val byte) {
			got = append(got, hit{bank, addr, val})
		})
		mem.PageMemoryPlus3(0x01) // all-RAM mode 0: banks 0,1,2,3
		mem.Write(0x0010, 0x99)   // slot 0 → bank 0
		if len(got) != 1 || got[0] != (hit{0, 0x10, 0x99}) {
			t.Errorf("special-paging dispatch hook = %+v, want [{0 16 153}]", got)
		}
	})
}
