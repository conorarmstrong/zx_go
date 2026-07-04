package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// This file closes a systematic test-coverage gap uncovered while fixing the
// v1.3.5 Night-Knight bug: PageMemory only re-synced Next MMU slots 6/7 from
// the classic $7FFD RAM bank when the encoded bank NUMBER changed from the
// previous write. Real hardware (zxnext.vhd:3810-3814, 4677-4681) re-syncs
// MMU6/7 on every $7FFD-family port write (7FFD, 1FFD normal-mode, DFFD,
// EFF7) regardless of whether the value changed — the only real exception is
// unrelated (a NextReg $8E write with bit 3 clear). SetDFFD had the identical
// bug (fixed alongside PageMemory); PageMemoryPlus3's normal-mode path never
// re-synced MMU6/7 at all outside the special-paging-exit transition (also
// fixed). No existing test ever wrote the SAME value twice and asserted the
// sync still happens, so this whole class of bug was invisible to the suite.
//
// Each write source below is tested in ISOLATION: setup puts the memory in a
// baseline state using a DIFFERENT mechanism than the one under test (so
// setup's own side effects can't accidentally mask a bug in probe), then
// probe -- the specific port write being tested -- is called once (the
// "changed value" case) and then AGAIN with an intervening MMU override
// injected (the "repeated value" case, the one that was missing and would
// have caught the v1.3.5 bug and its SetDFFD/PageMemoryPlus3 siblings).
//
// A real bug was caught writing this file: an earlier draft had each case's
// "write" closure call PageMemory itself before the mechanism under test, so
// PageMemory's own (already-fixed) unconditional sync silently masked
// whether DFFD/1FFD/EFF7's OWN sync was unconditional. Splitting setup from
// probe, and never calling setup a second time, is what actually isolates
// each mechanism -- worth remembering before trusting a "matrix" test like
// this one.

// mmuSyncCase is one write-source's test fixture. setup establishes RAM
// bank 4 at $C000 via a mechanism OTHER than probe (so setup can't mask a
// bug in probe's own sync behaviour); probe is the specific port write
// under test, called with probeValue in a way that must leave slot 6/7 at
// wantBank/wantBank+1 whether or not the value actually changed anything.
type mmuSyncCase struct {
	name       string
	model      roms.SpectrumModel
	setup      func(m *Memory)
	probe      func(m *Memory, val byte)
	probeValue byte
	wantBank   byte
}

func mmuSyncCases() []mmuSyncCase {
	return []mmuSyncCase{
		{
			name:       "7FFD",
			model:      roms.ModelNext,
			setup:      func(m *Memory) {}, // default state (bank 0) is fine; probe IS the 7FFD write
			probe:      (*Memory).PageMemory,
			probeValue: 0x04, // RAM bank 4 -> 8K banks 8/9
			wantBank:   8,
		},
		{
			name:  "1FFD_normal_mode",
			model: roms.ModelNext,
			setup: func(m *Memory) {
				// 1FFD normal-mode writes don't carry a bank of their own --
				// they just need to trigger a re-sync from whatever 7FFD
				// already selected. Establish that via 7FFD alone, once,
				// before probe ever runs.
				m.PageMemory(0x04)
			},
			probe:      (*Memory).PageMemoryPlus3,
			probeValue: 0x00, // bit0=0 (normal mode), no ROM-select bits set
			wantBank:   8,
		},
		{
			name:  "DFFD",
			model: roms.ModelNext,
			setup: func(m *Memory) {
				m.PageMemory(0x04) // low 3 bits of the combined bank
			},
			probe:      (*Memory).SetDFFD,
			probeValue: 0x00, // high nibble 0 -> combined bank stays 4
			wantBank:   8,
		},
		{
			name:  "EFF7",
			model: roms.ModelNext,
			setup: func(m *Memory) {
				m.PageMemory(0x04)
			},
			probe:      (*Memory).SetEFF7,
			probeValue: 0x00, // bits 2/3 clear; EFF7 itself carries no bank
			wantBank:   8,
		},
	}
}

// TestMMUSync_RepeatedValueStillResyncs is the test that would have caught
// the v1.3.5 bug (and its SetDFFD/PageMemoryPlus3 siblings): call probe with
// the same value TWICE and assert MMU6/7 reflect the correct bank both
// times, including reclaiming the slots from an earlier NextReg $56/$57
// override that a "only sync if changed" implementation would have left in
// place after the second (unchanged-value) probe call.
func TestMMUSync_RepeatedValueStillResyncs(t *testing.T) {
	for _, tc := range mmuSyncCases() {
		t.Run(tc.name, func(t *testing.T) {
			m := newMemoryForMMUTest(t, tc.model)
			tc.setup(m)

			// First probe call: establishes the bank via the mechanism
			// under test alone (setup never touches slot 6/7 directly).
			tc.probe(m, tc.probeValue)
			if got := m.GetMMU(6); got != tc.wantBank {
				t.Fatalf("setup: slot 6 = %#x, want %#x", got, tc.wantBank)
			}

			// Simulate an unrelated NextReg $56/$57 MMU override
			// happening in between -- e.g. a dot-command overlay, or
			// (as in Night-Knight) the game's own init code briefly
			// repurposing $C000 for something else.
			m.SetMMU(6, 0xAA)
			m.SetMMU(7, 0xAB)
			if !m.IsMMUOverridden(6) || !m.IsMMUOverridden(7) {
				t.Fatalf("precondition: slot 6/7 should be overridden after SetMMU")
			}

			// Second probe call: the SAME value as before, and nothing
			// else runs in between. Real hardware re-syncs MMU6/7 from
			// port_7ffd_bank regardless -- this must reclaim the slots
			// from the override, not leave the stale 0xAA/0xAB in place.
			tc.probe(m, tc.probeValue)

			if m.IsMMUOverridden(6) || m.IsMMUOverridden(7) {
				t.Errorf("%s: repeated-value write left slot 6/7 MMU-overridden; real hardware always re-syncs (zxnext.vhd:4677-4681)", tc.name)
			}
			if got := m.GetMMU(6); got != tc.wantBank {
				t.Errorf("%s: slot 6 after repeated-value write = %#x, want %#x", tc.name, got, tc.wantBank)
			}
			if got := m.GetMMU(7); got != tc.wantBank+1 {
				t.Errorf("%s: slot 7 after repeated-value write = %#x, want %#x", tc.name, got, tc.wantBank+1)
			}
		})
	}
}

// TestMMUSync_ChangedValueResyncs is the "value actually changes" half of
// the matrix -- the case existing tests already covered piecemeal, included
// here so the two sub-cases sit side by side for every write source. setup
// runs first (establishing bank 4 via a DIFFERENT mechanism, same as
// above), then an MMU override is injected, then probe alone must reclaim
// it -- probe's very first call in this test IS the "value changes from
// the override" case.
func TestMMUSync_ChangedValueResyncs(t *testing.T) {
	for _, tc := range mmuSyncCases() {
		t.Run(tc.name, func(t *testing.T) {
			m := newMemoryForMMUTest(t, tc.model)
			tc.setup(m)

			m.SetMMU(6, 0xAA)
			m.SetMMU(7, 0xAB)

			tc.probe(m, tc.probeValue)

			if m.IsMMUOverridden(6) || m.IsMMUOverridden(7) {
				t.Errorf("%s: slot 6/7 still MMU-overridden after probe", tc.name)
			}
			if got := m.GetMMU(6); got != tc.wantBank {
				t.Errorf("%s: slot 6 = %#x, want %#x", tc.name, got, tc.wantBank)
			}
			if got := m.GetMMU(7); got != tc.wantBank+1 {
				t.Errorf("%s: slot 7 = %#x, want %#x", tc.name, got, tc.wantBank+1)
			}
		})
	}
}

// TestMMUSync_1FFDNormalMode_LeavesMMU2Through5Alone pins the narrower rule
// for the OTHER classic slots: unlike MMU6/7, MMU2/3 and MMU4/5 only get
// reset to their fixed defaults on the special-paging EXIT transition
// (zxnext.vhd:4653-4667, gated on the one-cycle port_1ffd_special_old
// pulse) -- an ordinary $1FFD write that was already in normal mode must
// leave an existing MMU2-5 override alone, unlike MMU6/7 which always gets
// reclaimed. This is the deliberate asymmetry PageMemoryPlus3 now
// implements; this test exists so a future "just make everything
// unconditional" pass doesn't erase it by mistake.
func TestMMUSync_1FFDNormalMode_LeavesMMU2Through5Alone(t *testing.T) {
	m := newMemoryForMMUTest(t, roms.ModelNext)
	m.PageMemoryPlus3(0x00) // ensure we start in normal mode

	m.SetMMU(2, 0xAA)
	m.SetMMU(3, 0xAB)
	m.SetMMU(4, 0xCC)
	m.SetMMU(5, 0xCD)

	// An ordinary $1FFD write that stays in normal mode (bit 0 clear,
	// e.g. just toggling the ROM-select bit) must not touch MMU2-5.
	m.PageMemoryPlus3(0x04)

	if !m.IsMMUOverridden(2) || m.GetMMU(2) != 0xAA {
		t.Errorf("slot 2 disturbed by an ordinary (non-transition) 1FFD write: overridden=%v val=%#x", m.IsMMUOverridden(2), m.GetMMU(2))
	}
	if !m.IsMMUOverridden(3) || m.GetMMU(3) != 0xAB {
		t.Errorf("slot 3 disturbed by an ordinary (non-transition) 1FFD write: overridden=%v val=%#x", m.IsMMUOverridden(3), m.GetMMU(3))
	}
	if !m.IsMMUOverridden(4) || m.GetMMU(4) != 0xCC {
		t.Errorf("slot 4 disturbed by an ordinary (non-transition) 1FFD write: overridden=%v val=%#x", m.IsMMUOverridden(4), m.GetMMU(4))
	}
	if !m.IsMMUOverridden(5) || m.GetMMU(5) != 0xCD {
		t.Errorf("slot 5 disturbed by an ordinary (non-transition) 1FFD write: overridden=%v val=%#x", m.IsMMUOverridden(5), m.GetMMU(5))
	}
}

// TestMMUSync_NextZXOSBootStackScenario recreates, as literally as
// possible, the historical incident documented in PageMemoryPlus3: NextZXOS
// pushes a return address at $FF55-$FF58 then writes $1FFD with bit 0 clear
// to toggle a ROM-select bit, and that write must not corrupt the pushed
// stack frame. The v1.3.5-class fix makes PageMemoryPlus3 unconditionally
// re-sync MMU6/7 from port_7ffd_bank on every such write (previously that
// only happened on the special-paging-exit transition) -- this test proves
// that widening didn't reopen the original incident, because the re-sync
// reasserts the SAME classic bank that was already backing $FF55-$FF58, not
// a different one: the stack frame is classic-paged memory, not an MMU
// override, at this point in the real boot sequence.
func TestMMUSync_NextZXOSBootStackScenario(t *testing.T) {
	m := newMemoryForMMUTest(t, roms.ModelNext)

	// Establish RAM bank 0 at $C000 via ordinary classic paging (matching
	// NextZXOS's boot-time default), then push a "return address" at
	// $FF55-$FF58.
	m.PageMemory(0x00)
	m.Write(0xFF55, 0x34)
	m.Write(0xFF56, 0x12)
	m.Write(0xFF57, 0x78)
	m.Write(0xFF58, 0x56)

	// Toggle a ROM-select bit via $1FFD, bit 0 clear (normal mode, not a
	// special-paging transition) -- the exact write shape the incident
	// describes.
	m.PageMemoryPlus3(0x04)

	if got := m.Read(0xFF55); got != 0x34 {
		t.Errorf("stack byte at $FF55 corrupted: got %#02x, want $34", got)
	}
	if got := m.Read(0xFF56); got != 0x12 {
		t.Errorf("stack byte at $FF56 corrupted: got %#02x, want $12", got)
	}
	if got := m.Read(0xFF57); got != 0x78 {
		t.Errorf("stack byte at $FF57 corrupted: got %#02x, want $78", got)
	}
	if got := m.Read(0xFF58); got != 0x56 {
		t.Errorf("stack byte at $FF58 corrupted: got %#02x, want $56", got)
	}
}

// TestEFF7_Bit3RevealsRAMBank0AtBottomInsteadOfROM pins zxnext.vhd:4636-4644:
// $EFF7 bit 3 is a standalone override that shows RAM bank 0 (not ROM) at
// $0000-$3FFF, independent of the 7FFD/1FFD ROM-select bits.
func TestEFF7_Bit3RevealsRAMBank0AtBottomInsteadOfROM(t *testing.T) {
	m := newMemoryForMMUTest(t, roms.ModelNext)

	if got := m.GetMMU(0); got != 0xFF {
		t.Fatalf("precondition: slot 0 should default to ROM ($FF), got %#x", got)
	}

	m.SetEFF7(0x08) // bit 3 set
	if got := m.GetMMU(0); got != 0x00 {
		t.Errorf("after EFF7 bit3=1: slot 0 = %#x, want $00 (RAM bank 0 lo)", got)
	}
	if got := m.GetMMU(1); got != 0x01 {
		t.Errorf("after EFF7 bit3=1: slot 1 = %#x, want $01 (RAM bank 0 hi)", got)
	}

	m.SetEFF7(0x00) // bit 3 clear again
	if got := m.GetMMU(0); got != 0xFF {
		t.Errorf("after EFF7 bit3=0: slot 0 = %#x, want $FF (ROM)", got)
	}
	if got := m.GetMMU(1); got != 0xFF {
		t.Errorf("after EFF7 bit3=0: slot 1 = %#x, want $FF (ROM)", got)
	}
}

// TestEFF7_ReadBack pins the bit 2/3 latch read-back shape.
func TestEFF7_ReadBack(t *testing.T) {
	m := newMemoryForMMUTest(t, roms.ModelNext)

	m.SetEFF7(0x0C) // bits 2 and 3 both set
	if got := m.EFF7Value(); got != 0x0C {
		t.Errorf("EFF7Value() = %#02x, want $0C", got)
	}

	m.SetEFF7(0x00)
	if got := m.EFF7Value(); got != 0x00 {
		t.Errorf("EFF7Value() = %#02x, want $00", got)
	}
}

// TestEFF7_NotGatedByPagingEnabled pins the one genuinely distinct rule for
// this port: unlike 7FFD/1FFD/DFFD, the $EFF7 latch is NOT gated by the
// paging-lock flag at all (zxnext.vhd:3777-3782 has no port_7ffd_locked
// term), so it must still take effect even while paging is otherwise
// disabled.
func TestEFF7_NotGatedByPagingEnabled(t *testing.T) {
	m := newMemoryForMMUTest(t, roms.ModelNext)
	m.PagingEnabled = false

	m.SetEFF7(0x08)

	if got := m.GetMMU(0); got != 0x00 {
		t.Errorf("EFF7 write with PagingEnabled=false: slot 0 = %#x, want $00 (EFF7 isn't gated by the paging lock)", got)
	}
}
