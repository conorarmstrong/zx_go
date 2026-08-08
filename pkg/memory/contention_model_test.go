package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The contention model is derived from the FPGA core, which is the project's
// reference for hardware behaviour:
//
//   - Which banks contend: zxnext.vhd:4489-4493
//
//     mem_contend <= '0' when mem_active_page(7 downto 4) /= "0000" else
//     '1' when machine_timing_48  = '1' and mem_active_page(3 downto 1) = "101"
//     '1' when machine_timing_128 = '1' and mem_active_page(1) = '1'
//     '1' when machine_timing_p3  = '1' and mem_active_page(3) = '1'
//     '0';
//
//     mem_active_page is an 8K page, so "bits 7:4 = 0000" means 16K banks 0-7,
//     "3:1 = 101" means 16K bank 5, "bit 1" means the odd 16K banks (1/3/5/7),
//     and "bit 3" means 16K banks >= 4.
//
//   - How long the stall is: zxula.vhd:582-583
//
//     hc_adj <= i_hc(3 downto 0) + 1;
//     wait_s <= '1' when ((hc_adj(3 downto 2) /= "00") or
//     (hc_adj(3 downto 1) = "000" and i_timing_p3 = '1')) and ...
//
//     The 48K/128K term contends 12 of every 16 pixel slots (6 of 8 T-states);
//     the extra +3 term contends 14 of 16 (7 of 8). Those are the classic
//     documented patterns 6,5,4,3,2,1,0,0 and 1,0,7,6,5,4,3,2.

// newContentionMem builds a Memory for the model with contention live.
func newContentionMem(t *testing.T, dir string, model roms.SpectrumModel) (*Memory, *uint64) {
	t.Helper()
	createTestROMsPlus(t, dir)
	t.Cleanup(func() { cleanupTestROMs(dir) })
	mem, err := New(dir, model)
	if err != nil {
		t.Fatal(err)
	}
	ts := new(uint64)
	mem.SetTStatePtr(ts)
	return mem, ts
}

// delayAt charges one contended access at T and reports the stall added.
func delayAt(mem *Memory, ts *uint64, t uint64, addr uint16) uint64 {
	*ts = t
	before := *ts
	mem.ContendMemory(addr)
	return *ts - before
}

// firstContendedT is the first contended T-state of display line 0 for a
// model: one T-state before the line-0 display fetch (the canonical "-1").
// Only the 48K's is a whole 64 scanlines from the interrupt — see
// roms.DisplayStartTState.
func firstContendedT(model roms.SpectrumModel) uint64 {
	return uint64(roms.FirstContendedTState(model))
}

// TestContentionTracksPerModelLineLength pins that the contention window
// advances by the MODEL's own scanline length. The 48K ULA runs a 224-T line
// (zxula_timing.vhd c_max_hc = 447); using the 128K's 228 makes the window
// drift 4 T-states per line, so display line 1 gets no contention at all.
func TestContentionTracksPerModelLineLength(t *testing.T) {
	for _, tc := range []struct {
		name     string
		model    roms.SpectrumModel
		dir      string
		tPerLine int
	}{
		{"48K", roms.Model48K, "test_roms_contend_line_48", 224},
		{"128K", roms.Model128K, "test_roms_contend_line_128", 228},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem, ts := newContentionMem(t, tc.dir, tc.model)
			base := firstContendedT(tc.model)
			for _, line := range []int{0, 1, 2, 10, 100, 191} {
				at := base + uint64(line*tc.tPerLine)
				if got := delayAt(mem, ts, at, 0x4000); got != 6 {
					t.Errorf("display line %d (T=%d): delay %d, want 6", line, at, got)
				}
			}
			// One line past the display area: no contention.
			at := base + uint64(192*tc.tPerLine)
			if got := delayAt(mem, ts, at, 0x4000); got != 0 {
				t.Errorf("bottom border (T=%d): delay %d, want 0", at, got)
			}
		})
	}
}

// TestContentionOnlyDuringPaperFetch pins the 128-T paper window: the ULA
// fetches for 128 T-states of each display line and leaves the rest of the
// line uncontended.
func TestContentionOnlyDuringPaperFetch(t *testing.T) {
	mem, ts := newContentionMem(t, "test_roms_contend_paper", roms.Model48K)
	base := firstContendedT(roms.Model48K)

	// Offset 120 opens the last 8-T block inside the window, so it carries
	// the pattern's first (longest) stall.
	if got := delayAt(mem, ts, base+120, 0x4000); got != 6 {
		t.Errorf("T=+120 (last in-window block): delay %d, want 6", got)
	}
	// Offset 128 is the first T-state past the fetch window. If the window
	// were unbounded this would look like another pattern slot 0.
	if got := delayAt(mem, ts, base+128, 0x4000); got != 0 {
		t.Errorf("T=+128 (past paper): delay %d, want 0", got)
	}
	if got := delayAt(mem, ts, base-1, 0x4000); got != 0 {
		t.Errorf("T=-1 (pre-window): delay %d, want 0", got)
	}
}

// TestPlus3ContentionPattern pins the +2A/+3 delay pattern. zxula.vhd:583 adds
// a second contended term for i_timing_p3, taking the line from 12 contended
// pixel slots of 16 to 14 of 16 — 7 contended T-states of every 8, the
// documented 1,0,7,6,5,4,3,2.
func TestPlus3ContentionPattern(t *testing.T) {
	want := [8]uint64{1, 0, 7, 6, 5, 4, 3, 2}
	for _, tc := range []struct {
		name  string
		model roms.SpectrumModel
		dir   string
	}{
		{"+2A", roms.ModelPlus2A, "test_roms_contend_p2a"},
		{"+3", roms.ModelPlus3, "test_roms_contend_p3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem, ts := newContentionMem(t, tc.dir, tc.model)
			base := firstContendedT(tc.model)
			for i, w := range want {
				at := base + uint64(i)
				if got := delayAt(mem, ts, at, 0x4000); got != w {
					t.Errorf("pattern slot %d (T=%d): delay %d, want %d", i, at, got, w)
				}
			}
		})
	}
}

// TestContendedBanksPerModel pins the per-model contended bank set from
// zxnext.vhd:4489-4493. The pre-fix model contended purely on address
// (0x4000-0x7FFF), so a contended bank paged into 0xC000 was charged nothing
// and an uncontended one was charged as if it were bank 5.
func TestContendedBanksPerModel(t *testing.T) {
	// The 48K has no pageable 0xC000, so its bank rule is pinned against the
	// fixed layout instead: ROM, bank 5, bank 2, bank 0.
	t.Run("48K", func(t *testing.T) {
		mem, ts := newContentionMem(t, "test_roms_contend_bank_48", roms.Model48K)
		base := firstContendedT(roms.Model48K)
		for addr, want := range map[uint16]bool{
			0x0000: false, // ROM
			0x4000: true,  // bank 5
			0x8000: false, // bank 2
			0xC000: false, // bank 0
		} {
			if got := delayAt(mem, ts, base, addr) != 0; got != want {
				t.Errorf("48K addr %#04x: contended=%v, want %v", addr, got, want)
			}
		}
	})

	for _, tc := range []struct {
		name  string
		model roms.SpectrumModel
		dir   string
		// want[bank] is whether that bank contends when paged at 0xC000.
		want [8]bool
	}{
		// 128K timing: the odd banks.
		{"128K", roms.Model128K, "test_roms_contend_bank_128",
			[8]bool{false, true, false, true, false, true, false, true}},
		{"+2", roms.ModelPlus2, "test_roms_contend_bank_p2",
			[8]bool{false, true, false, true, false, true, false, true}},
		// +2A/+3 timing: banks >= 4.
		{"+2A", roms.ModelPlus2A, "test_roms_contend_bank_p2a",
			[8]bool{false, false, false, false, true, true, true, true}},
		{"+3", roms.ModelPlus3, "test_roms_contend_bank_p3",
			[8]bool{false, false, false, false, true, true, true, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem, ts := newContentionMem(t, tc.dir, tc.model)
			base := firstContendedT(tc.model)
			for bank := 0; bank < 8; bank++ {
				mem.PageMemory(byte(bank))
				got := delayAt(mem, ts, base, 0xC000) != 0
				if got != tc.want[bank] {
					t.Errorf("bank %d at 0xC000: contended=%v, want %v", bank, got, tc.want[bank])
				}
			}
		})
	}
}

// TestROMAndHighBanksNeverContend pins the first VHDL clause: contention can
// only occur in 16K banks 0-7, so the ROM slot and any Next high bank
// (reachable at 0xC000 via $DFFD) are never charged.
func TestROMAndHighBanksNeverContend(t *testing.T) {
	mem, ts := newContentionMem(t, "test_roms_contend_rom", roms.Model128K)
	base := firstContendedT(roms.Model128K)

	// 0x0000-0x3FFF holds a ROM page on every classic model.
	if got := delayAt(mem, ts, base, 0x0000); got != 0 {
		t.Errorf("ROM at 0x0000: delay %d, want 0", got)
	}
	// Bank 2 is permanently mapped at 0x8000 and is even, so uncontended
	// under 128K timing.
	if got := delayAt(mem, ts, base, 0x8000); got != 0 {
		t.Errorf("bank 2 at 0x8000: delay %d, want 0", got)
	}
	// A bank above 7 at 0xC000 (Next $DFFD extension) never contends.
	mem.PageMemory(0x01) // bank 1 -> contended
	if got := delayAt(mem, ts, base, 0xC000); got == 0 {
		t.Fatalf("precondition: bank 1 at 0xC000 should contend")
	}
	mem.SetDFFD(0x01) // lift the 0xC000 bank above 7
	if got := delayAt(mem, ts, base, 0xC000); got != 0 {
		t.Errorf("high bank at 0xC000: delay %d, want 0", got)
	}
}

// TestPlus3SpecialPagingContendsAllRAM pins the +3 all-RAM special-paging
// mode: with banks 4,5,6,7 mapped across all four slots, every slot is a bank
// >= 4, so every access contends (zxnext.vhd:4492).
func TestPlus3SpecialPagingContendsAllRAM(t *testing.T) {
	mem, ts := newContentionMem(t, "test_roms_contend_p3_special", roms.ModelPlus3)
	base := firstContendedT(roms.ModelPlus3)

	// $1FFD bit 0 = special paging, bits 2:1 = 01 -> banks 4,5,6,7.
	mem.PageMemoryPlus3(0x01 | 0x02)
	for _, addr := range []uint16{0x0000, 0x4000, 0x8000, 0xC000} {
		if got := delayAt(mem, ts, base, addr); got == 0 {
			t.Errorf("+3 special paging 4/5/6/7, addr %#04x: delay 0, want contended", addr)
		}
	}
}

// TestPentagonNeverContends guards the existing behaviour: the Pentagon has no
// contended memory at all.
func TestPentagonNeverContends(t *testing.T) {
	mem, ts := newContentionMem(t, "test_roms_contend_pentagon", roms.ModelPentagon)
	for _, addr := range []uint16{0x4000, 0xC000} {
		if got := delayAt(mem, ts, 14335, addr); got != 0 {
			t.Errorf("Pentagon addr %#04x: delay %d, want 0", addr, got)
		}
	}
}

// TestSwitchModelReapplaysContentionShape guards the cached contention shape
// (contendTiming / contendTPerLine, set by setupModel so isContendedBank stays
// cheap on the hot path): a runtime machine switch must refresh it, or the new
// machine keeps contending like the old one.
func TestSwitchModelReapplaysContentionShape(t *testing.T) {
	mem, ts := newContentionMem(t, "test_roms_contend_switch", roms.Model48K)

	// 48K: bank 2 at 0x8000 is uncontended, and the window uses a 224-T line.
	if got := delayAt(mem, ts, firstContendedT(roms.Model48K), 0x8000); got != 0 {
		t.Fatalf("48K bank 2: delay %d, want 0", got)
	}

	if err := mem.SwitchModel(roms.ModelPlus3); err != nil {
		t.Fatal(err)
	}

	// +3: banks >= 4 contend, so bank 5 at 0x4000 does; the pattern is the
	// +3 table and the line is 228 T.
	base := firstContendedT(roms.ModelPlus3)
	if got := delayAt(mem, ts, base, 0x4000); got != plus3ContentionPattern[0] {
		t.Errorf("+3 bank 5 after switch: delay %d, want %d (still on the 48K shape?)",
			got, plus3ContentionPattern[0])
	}
	// A second display line proves the 228-T line length came across too.
	if got := delayAt(mem, ts, base+228, 0x4000); got != plus3ContentionPattern[0] {
		t.Errorf("+3 display line 1 after switch: delay %d, want %d",
			got, plus3ContentionPattern[0])
	}
}
