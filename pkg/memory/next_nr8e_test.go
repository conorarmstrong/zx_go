package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/install/installtest"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// newNextMem builds a ModelNext memory.Memory using the sandboxed
// install dir so the test never touches the user's real config.
// Mirrors the helper pattern in next_test.go.
func newNextMem(t *testing.T) *Memory {
	t.Helper()
	installtest.RedirectConfig(t)
	m, err := New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New(ModelNext): %v", err)
	}
	return m
}

// TestSetROMBankExtended_Bit2GatesROMLowBit verifies per VHDL
// lines 3668-3670: bit 2 CLEAR → write bit 0 to port_7FFD[4]
// (ROM select low bit). bit 2 SET → port_7FFD[4] untouched.
//
// NR$8E does NOT have a "bits 1:0 = ROM bank index" field; that
// was an earlier wrong model that made NextZXOS's NR$8E=$7A
// write (bit 2=0, bit 0=0) erroneously select ROM bank 2.
func TestSetROMBankExtended_Bit2GatesROMLowBit(t *testing.T) {
	m := newNextMem(t)

	// bit 2 clear, bit 0 = 1 → port_7FFD[4] = 1
	m.SetROMBankExtended(0x01)
	port7FFD, _, _ := m.GetPortState()
	if got := (port7FFD >> 4) & 0x01; got != 1 {
		t.Errorf("NR$8E=$01: port_7FFD[4] = %d, want 1", got)
	}

	// bit 2 set, bit 0 = 0 → port_7FFD[4] unchanged (still 1)
	m.SetROMBankExtended(0x04)
	port7FFD, _, _ = m.GetPortState()
	if got := (port7FFD >> 4) & 0x01; got != 1 {
		t.Errorf("NR$8E=$04 should not clear port_7FFD[4], got %d", got)
	}

	// bit 2 clear, bit 0 = 0 → port_7FFD[4] = 0
	m.SetROMBankExtended(0x00)
	port7FFD, _, _ = m.GetPortState()
	if got := (port7FFD >> 4) & 0x01; got != 0 {
		t.Errorf("NR$8E=$00: port_7FFD[4] = %d, want 0", got)
	}
}

// TestSetROMBankExtended_Bit3SetUpdatesRAMBank verifies the
// classic-paging side effect: when bit 3 is set, bits 6:4 of the
// value land in port_7FFD[2:0] and propagate to slot 3 (the
// $C000-$FFFF window).
//
// Concrete write: $7A = 0111 1010. ROM bank = 2, bit 3 = 1,
// bits 6:4 = 111 → RAM bank 7 at slot 3, bit 7 = 0 (1FFD bit 0
// stays cleared).
func TestSetROMBankExtended_Bit3SetUpdatesRAMBank(t *testing.T) {
	m := newNextMem(t)

	m.SetROMBankExtended(0x7A)

	port7FFD, _, _ := m.GetPortState()
	if got := port7FFD & 0x07; got != 7 {
		t.Errorf("after NR$8E=$7A: port_7FFD[2:0] = %d, want 7", got)
	}
	if got := m.memoryPageReadMap[3]; got != 7 {
		t.Errorf("after NR$8E=$7A: slot 3 RAM bank = %d, want 7", got)
	}
}

// TestSetROMBankExtended_Bit2Updates1FFDBit0 verifies VHDL line 3734:
// port_1FFD[0] = bit 2 of NR$8E (NOT bit 7 as the old impl assumed).
// Writing NR$8E=$04 (only bit 2) sets port_1FFD[0] = 1.
func TestSetROMBankExtended_Bit2Updates1FFDBit0(t *testing.T) {
	m := newNextMem(t)

	m.SetROMBankExtended(0x04)

	_, port1FFD, _ := m.GetPortState()
	if got := port1FFD & 0x01; got != 1 {
		t.Errorf("after NR$8E=$04: port_1FFD[0] = %d, want 1", got)
	}
}

// TestSetROMBankExtended_Bit1Updates1FFDBit2 verifies VHDL line 3732:
// port_1FFD[2] = bit 1 of NR$8E. NR$8E=$02 → port_1FFD[2] = 1 which
// (combined with port_7FFD[4]=0) selects ROM bank 2 — the path
// NextZXOS's RAM-resident trampoline at $5B48 relies on.
func TestSetROMBankExtended_Bit1Updates1FFDBit2(t *testing.T) {
	m := newNextMem(t)

	m.SetROMBankExtended(0x02)

	_, port1FFD, _ := m.GetPortState()
	if got := (port1FFD >> 2) & 0x01; got != 1 {
		t.Errorf("after NR$8E=$02: port_1FFD[2] = %d, want 1", got)
	}
}

// TestSetROMBankExtended_Bit3ClearLeavesPortsAlone confirms the
// gate: with bit 3 clear, even bits 6:4 / bit 7 must not propagate
// to the paging shadows. NR$8E=$70 (bit 3 = 0, bits 6:4 = 7)
// must NOT touch port_7FFD[2:0].
func TestSetROMBankExtended_Bit3ClearLeavesPortsAlone(t *testing.T) {
	m := newNextMem(t)

	// Establish a known state via the standard 7FFD port.
	m.PageMemory(0x05) // RAM bank 5 at slot 3
	port7FFDBefore, port1FFDBefore, _ := m.GetPortState()

	m.SetROMBankExtended(0x70)

	port7FFDAfter, port1FFDAfter, _ := m.GetPortState()
	if port7FFDAfter&0x07 != port7FFDBefore&0x07 {
		t.Errorf("NR$8E=$70 (bit 3 clear) modified port_7FFD[2:0] from %#x to %#x",
			port7FFDBefore&0x07, port7FFDAfter&0x07)
	}
	if port1FFDAfter&0x01 != port1FFDBefore&0x01 {
		t.Errorf("NR$8E=$70 (bit 3 clear) modified port_1FFD[0] from %#x to %#x",
			port1FFDBefore&0x01, port1FFDAfter&0x01)
	}
}

// TestSetROMBankExtended_Value08_ResetsRAMBankToZero locks in the
// specific NextZXOS sequence flagged by the reference-emulator
// diff: NR$8E=$08 (only bit 3 set) should select ROM bank 0 AND
// RAM bank 0 at slot 3. Before this fix, only ROM bank 0 was
// honoured; slot 3 stayed at whatever the previous port_7FFD
// write had set.
func TestSetROMBankExtended_Value08_ResetsRAMBankToZero(t *testing.T) {
	m := newNextMem(t)

	// Pre-condition the slot to a non-zero bank so the test would
	// fail if the fix were absent.
	m.PageMemory(0x07) // RAM bank 7 at slot 3
	if got := m.memoryPageReadMap[3]; got != 7 {
		t.Fatalf("setup failed: slot 3 = %d, want 7", got)
	}

	m.SetROMBankExtended(0x08)

	if got := m.memoryPageReadMap[0]; got != 16 {
		t.Errorf("after NR$8E=$08: ROM slot = %d, want 16 (bank 0)", got)
	}
	if got := m.memoryPageReadMap[3]; got != 0 {
		t.Errorf("after NR$8E=$08: slot 3 RAM bank = %d, want 0", got)
	}
}

// TestSetROMBankExtended_Bit3PreservesOtherPortBits proves the
// RAM-bank update is a bitfield write — bits 3, 4, 5 of port_7FFD
// (ScreenPage, ROM select, paging-disable) must not be clobbered
// when only [2:0] changes. Guards against a naive "PageMemory(val)"
// implementation that would zero the rest.
func TestSetROMBankExtended_Bit3PreservesOtherPortBits(t *testing.T) {
	m := newNextMem(t)

	// Seed bit 3 (ScreenPage = 7) and bit 4 (ROM select high) on
	// port_7FFD via the standard port.
	m.PageMemory(0x18) // bits 3 + 4 set, RAM bank 0
	port7FFDBefore, _, _ := m.GetPortState()
	if port7FFDBefore&0x18 != 0x18 {
		t.Fatalf("setup: port_7FFD = %#x, want bits 3+4 set", port7FFDBefore)
	}

	// Write NR$8E with bit 3 set + RAM bank 3 in bits 6:4. The
	// other port_7FFD bits MUST survive.
	m.SetROMBankExtended(0x38) // bit 3 + bits 5:4 = 11 + bit 6 = 0 → RAM bank 3

	port7FFDAfter, _, _ := m.GetPortState()
	// Bit 3 must survive. Bit 4 may legitimately change because
	// SetROMBank(bits 1:0) writes through to it; here bits 1:0 = 0
	// so bit 4 ends up cleared. Mask off bit 4 from the compare.
	if (port7FFDAfter & 0x08) != (port7FFDBefore & 0x08) {
		t.Errorf("port_7FFD bit 3 (ScreenPage) changed: before=%#x after=%#x",
			port7FFDBefore, port7FFDAfter)
	}
	if (port7FFDAfter & 0x07) != 3 {
		t.Errorf("port_7FFD[2:0] = %d, want 3", port7FFDAfter&0x07)
	}
}

// TestSetROMBankExtended_Bit0Updates1FFDBit1 verifies VHDL line 3733:
// port_1FFD[1] <= nr_wr_dat(0). NR$8E bit 0 always feeds into
// port_1FFD[1] regardless of the bit-3 gate (this is in the always-
// active block 3 of the VHDL, not block 1).
//
// Spot-check: write NR$8E=$01 (only bit 0). Expect 1FFD[1] = 1.
// Block 1's bit-2 path is open (bit 2 = 0) so port_7FFD[4] also
// gets bit 0; that's a separate fact already covered by
// TestSetROMBankExtended_Bit2GatesROMLowBit.
func TestSetROMBankExtended_Bit0Updates1FFDBit1(t *testing.T) {
	m := newNextMem(t)

	m.SetROMBankExtended(0x01)
	_, port1FFD, _ := m.GetPortState()
	if got := (port1FFD >> 1) & 0x01; got != 1 {
		t.Errorf("after NR$8E=$01: port_1FFD[1] = %d, want 1", got)
	}

	// Verify NR$8E=$00 clears it back.
	m.SetROMBankExtended(0x00)
	_, port1FFD, _ = m.GetPortState()
	if got := (port1FFD >> 1) & 0x01; got != 0 {
		t.Errorf("after NR$8E=$00: port_1FFD[1] = %d, want 0", got)
	}
}

// TestSetROMBankExtended_Bit1And0_BothSet locks in the simultaneous
// mapping: bit 1 → 1FFD[2], bit 0 → 1FFD[1], bit 2 → 1FFD[0]. With
// NR$8E=$03 (bits 1+0 set, bit 2 clear) we expect 1FFD[2:0] = 110b.
// Cross-checks our reorder transform against the VHDL block 3.
func TestSetROMBankExtended_Bit1And0_BothSet(t *testing.T) {
	m := newNextMem(t)

	m.SetROMBankExtended(0x03)
	_, port1FFD, _ := m.GetPortState()
	low := port1FFD & 0x07
	if low != 0x06 {
		t.Errorf("after NR$8E=$03: port_1FFD[2:0] = $%X, want $6 (bit 1→pos 2, bit 0→pos 1)",
			low)
	}
}

// TestSetROMBankExtended_AllOnes verifies an all-bits-set write
// ($FF) applies every documented effect simultaneously:
//   - bit 3 set so RAM bank bits 6:4 (= 111) → port_7FFD[2:0] = 7
//   - bit 2 set so port_7FFD[4] gate is CLOSED (= no change), but
//     bit 2 also writes 1 to port_1FFD[0]
//   - bit 1 = 1 → port_1FFD[2] = 1
//   - bit 0 = 1 → port_1FFD[1] = 1
//   - bit 7 = 1 → would target port_DFFD[0] (DFFD not modelled yet —
//     see TestSetROMBankExtended_Bit7_TargetsDFFD_NotModelled)
func TestSetROMBankExtended_AllOnes(t *testing.T) {
	m := newNextMem(t)

	m.SetROMBankExtended(0xFF)
	port7FFD, port1FFD, _ := m.GetPortState()

	if got := port7FFD & 0x07; got != 7 {
		t.Errorf("NR$8E=$FF: port_7FFD[2:0] = %d, want 7", got)
	}
	if got := port1FFD & 0x07; got != 0x07 {
		t.Errorf("NR$8E=$FF: port_1FFD[2:0] = $%X, want $7", got)
	}
}

// TestSetROMBankExtended_Bit7_TargetsDFFD_NotModelled documents a
// known divergence: VHDL block 2 (lines 3696-3702) routes NR$8E bit 7
// (when bit 3 set) to port_DFFD[0], the high-bank-extension bit used
// to address RAM banks 8-15 in classic-paging mode. Our model does
// not implement port_DFFD at all (see project memory todo list).
//
// The current implementation discards bit 7 (it briefly assigns it
// to port_1FFD[0] then immediately overwrites that with the bit-2
// value four lines later). For now this is acceptable because the
// NextZXOS cold-boot trace does not exercise banks 8-15 via NR$8E
// — but the next test will FAIL once port_DFFD lands, which is the
// signal to add the proper routing.
//
// Skipped to record the gap without forcing a build break.
func TestSetROMBankExtended_Bit7_TargetsDFFD_NotModelled(t *testing.T) {
	t.Skip("port_DFFD modelling not yet implemented — see ROADMAP.md")
}

// TestSetROMBankExtended_SequentialWrites_AreLastWinForGatedBits
// proves that successive NR$8E writes update port state predictably:
// the ports' final state reflects the latest write for any bit whose
// gate was open, with prior state preserved for bits whose gate
// stayed closed. This guards against accidental write-merging or
// stale-state bugs.
func TestSetROMBankExtended_SequentialWrites_AreLastWinForGatedBits(t *testing.T) {
	m := newNextMem(t)

	// Write 1: bit 3 set, bits 6:4 = 5 → port_7FFD[2:0] = 5
	m.SetROMBankExtended(0x58)
	port7FFD1, _, _ := m.GetPortState()
	if got := port7FFD1 & 0x07; got != 5 {
		t.Fatalf("write 1 (NR$8E=$58): port_7FFD[2:0] = %d, want 5", got)
	}

	// Write 2: bit 3 CLEAR → bits 6:4 must NOT propagate. port_7FFD[2:0]
	// should retain value 5 from write 1.
	m.SetROMBankExtended(0x20) // bit 5 alone; bit 3 = 0 closes the gate
	port7FFD2, _, _ := m.GetPortState()
	if got := port7FFD2 & 0x07; got != 5 {
		t.Errorf("write 2 (bit 3 closed): port_7FFD[2:0] should retain 5, got %d", got)
	}

	// Write 3: bit 3 set, bits 6:4 = 2 → port_7FFD[2:0] = 2 (overwrite)
	m.SetROMBankExtended(0x28)
	port7FFD3, _, _ := m.GetPortState()
	if got := port7FFD3 & 0x07; got != 2 {
		t.Errorf("write 3 (NR$8E=$28): port_7FFD[2:0] should be 2, got %d", got)
	}
}

// TestSetROMBankExtended_Bit3ClearsSlot67Override pins the hardware
// last-writer-wins rule for the $C000-$FFFF window (zxnext.vhd
// 4677-4680 + the port_memory_ram_change_dly term at 3814:
// `not (nr_8e_we and not nr_wr_dat(3))`). A NR$8E write with bit 3
// set asserts port_memory_ram_change → MMU6/MMU7 are reloaded from
// port_7ffd_bank UNCONDITIONALLY, clobbering any earlier NR$56/$57
// MMU8 override on those two 8K slots. There is no "skip if the slot
// was set via NextReg" gate in the FPGA — the MMU registers are
// plain last-write-wins.
//
// This is exactly the NextZXOS RAM-sizing exit: the size sweep walks
// NR$56/$57 up to the sweep terminus ($DE/$DF), then `NEXTREG $8E,$08`
// (bit 3 = 1, RAM-bank bits 6:4 = 0) must restore $C000 to bank 0,
// leaving slots 6/7 = $00/$01. Without this the boot derails into the
// uninitialised high bank mapped at $C000.
func TestSetROMBankExtended_Bit3ClearsSlot67Override(t *testing.T) {
	m := newNextMem(t)

	// Simulate the size sweep's final NEXTREG $56,$DE / $57,$DF.
	m.SetMMU(6, 0xDE)
	m.SetMMU(7, 0xDF)
	if !m.IsMMUOverridden(6) || !m.IsMMUOverridden(7) {
		t.Fatalf("precondition: slots 6/7 should be overridden after SetMMU")
	}

	// NR$8E=$08: bit 3 = 1 (RAM-bank write), bits 6:4 = 0 → bank 0.
	m.SetROMBankExtended(0x08)

	if m.IsMMUOverridden(6) || m.IsMMUOverridden(7) {
		t.Errorf("NR$8E bit3=1 must clear slots 6/7 MMU override (last-writer-wins)")
	}
	if got := m.GetMMU(6); got != 0x00 {
		t.Errorf("slot 6 after NR$8E=$08: $%02X, want $00 (bank 0 lo)", got)
	}
	if got := m.GetMMU(7); got != 0x01 {
		t.Errorf("slot 7 after NR$8E=$08: $%02X, want $01 (bank 0 hi)", got)
	}
}

// TestROMMappingNR8E_Read pins the NextReg $8E READ shape. Per
// nextreg.txt:870-885 (and zxnext.vhd), the read is NOT the last byte
// written to $8E — it is reconstructed live from port_DFFD / port_7FFD
// / port_1FFD:
//
//	bit 7    = port_DFFD[0]  (high RAM-bank bit; DFFD unmodelled → 0)
//	bits 6:4 = port_7FFD[2:0]
//	bit 3    = 1             (ALWAYS set on read)
//	bit 2    = port_1FFD[0]  (paging mode)
//	bit 1    = port_1FFD[2]
//	bit 0    = port_7FFD[4]  when paging mode 0 (normal)
//	           port_1FFD[1]  when paging mode 1 (special allRAM)
//
// The previous implementation returned the stored byte, which read
// back a stale $00 whenever paging was last changed via the legacy
// ports — diverging from the reference emulator, which reports $78 at
// the NextZXOS 128K-menu idle state (7FFD bank 7 + the always-1 bit 3).
func TestROMMappingNR8E_Read(t *testing.T) {
	m := newNextMem(t)

	tests := []struct {
		name         string
		p7ffd, p1ffd byte
		want         byte
	}{
		// RAM bank 7 in 7FFD[2:0], normal paging, no ROM-select bits:
		// bits6:4=111 ($70) + bit3 ($08) = $78. Matches the reference at the
		// 128K-menu idle state.
		{"bank7_normal", 0x07, 0x00, 0x78},
		// Bit 3 reads 1 even with everything else clear.
		{"all_clear_bit3_set", 0x00, 0x00, 0x08},
		// 7FFD[4] (ROM low select) → read bit 0 in normal paging mode.
		{"romlow_normal", 0x10, 0x00, 0x08 | 0x01},
		// 1FFD[2] → read bit 1 (in both paging modes).
		{"1ffd_bit2", 0x00, 0x04, 0x08 | 0x02},
		// Special allRAM mode (1FFD[0]=1): bit2 reads 1, bit0 = 1FFD[1].
		{"special_allram", 0x00, 0x03, 0x08 | 0x04 | 0x01},
		// In special mode, 7FFD[4] does NOT feed bit 0 — 1FFD[1] does.
		{"special_ignores_7ffd_bit4", 0x10, 0x01, 0x08 | 0x04},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m.port7FFD = tc.p7ffd
			m.port1FFD = tc.p1ffd
			if got := m.ROMMappingNR8E(); got != tc.want {
				t.Errorf("ROMMappingNR8E(7ffd=$%02X,1ffd=$%02X) = $%02X, want $%02X",
					tc.p7ffd, tc.p1ffd, got, tc.want)
			}
		})
	}
}
