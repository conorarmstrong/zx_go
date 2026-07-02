package divmmc

import (
	"testing"
)

// Port $E3 state-transition tests.
//
// Verifies the documented semantics of divMMC port $E3 writes per
// the divIDE/divMMC documentation + zxnext.vhd:
//
//   bit 7 = CONMEM (force paging in — non-sticky, drops when cleared)
//   bit 6 = MAPRAM (replace divMMC ROM with RAM bank 3 at slot 0 —
//                   sticky: once set, latched until hardware reset)
//   bits 3:0 = RAM bank selector (16 banks: 0-15)
//
// Each test exercises a single transition or interaction.

// TestE3_CONMEM_ForcesPagedIn verifies bit 7 forces paging in
// regardless of automap state.
func TestE3_CONMEM_ForcesPagedIn(t *testing.T) {
	p := New(makeROM())
	p.SetAutomap(false) // automap off — only CONMEM can page in
	if p.IsPagedIn() {
		t.Fatal("precondition: overlay paged in with automap off")
	}
	p.WritePort(0xE3, 0x80) // CONMEM only
	if !p.IsPagedIn() {
		t.Errorf("CONMEM=1 should force paging in even with automap off")
	}
}

// TestE3_CONMEM_ClearDropsOverlay verifies that clearing CONMEM with
// automap also off drops the overlay back out.
func TestE3_CONMEM_ClearDropsOverlay(t *testing.T) {
	p := New(makeROM())
	p.SetAutomap(false)
	p.WritePort(0xE3, 0x80)
	if !p.IsPagedIn() {
		t.Fatal("precondition: CONMEM=1 should have paged in")
	}
	p.WritePort(0xE3, 0x00) // clear CONMEM
	if p.IsPagedIn() {
		t.Errorf("CONMEM=0 with automap=off should drop overlay")
	}
}

// TestE3_CONMEM_ToggleDoesNotLatchAutomap verifies the FBLabs FPGA
// behaviour: CONMEM (port $E3 bit 7) is a combinational force-in
// override that does NOT write the automap paged-in latch. Setting
// CONMEM then clearing it — with automap *enabled* but no automap
// trigger PC fetched in between — leaves the overlay dropped, exactly
// as the real core does (effective-mapped = conmem OR automap_held;
// CONMEM never sets automap_held).
//
// NextZXOS's $DB41 stub (CONMEM-on; write $2009; CONMEM-off; RET)
// relies on this: the overlay must revert to the underlying enNextZX
// ROM the instant CONMEM clears, so the RET returns into real ROM at
// the saved $3Exx address. The previous (wrong) "keeps overlay while
// automap enabled" behaviour stranded the overlay paged-in, the RET
// landed in divMMC RAM (zeros), and the CPU ran off into a NOP-slide.
func TestE3_CONMEM_ToggleDoesNotLatchAutomap(t *testing.T) {
	p := New(makeROM())
	p.SetAutomap(true) // automap enabled, but nothing has triggered it
	if p.IsPagedIn() {
		t.Fatal("precondition: overlay should be out (no trigger fetched)")
	}
	p.WritePort(0xE3, 0x82) // CONMEM forces in
	if !p.IsPagedIn() {
		t.Fatal("CONMEM=1 should force the overlay in")
	}
	p.WritePort(0xE3, 0x00) // clear CONMEM — automap never triggered
	if p.IsPagedIn() {
		t.Errorf("CONMEM toggle must NOT latch automap: overlay should drop")
	}
	// $2000-$3FFF must revert to underlying memory: HandleRead returns
	// ok=false so the memory map falls through to enNextZX ROM.
	if _, ok := p.HandleRead(0x2000); ok {
		t.Errorf("after CONMEM clear, $2000 read should fall through (ok=false)")
	}
}

// TestE3_CONMEM_ToggleKeepsGenuineAutomapPaging verifies the converse:
// when an automap trigger HAS genuinely paged the overlay in, a later
// CONMEM set/clear leaves that automap latch intact — the overlay
// survives the CONMEM toggle (and only drops on a real page-out
// trigger). This proves the fix separates CONMEM from the latch
// without breaking real automap-held paging.
func TestE3_CONMEM_ToggleKeepsGenuineAutomapPaging(t *testing.T) {
	p := New(makeROM())
	p.SetAutomap(true)
	p.SetEntryPointsTiming0(0xFF) // instant_on: page in on the trigger M1
	p.Step(0x0000)                // RST $00 trigger genuinely pages the overlay in
	if !p.IsPagedIn() {
		t.Fatal("precondition: automap trigger at $0000 should page in")
	}
	p.WritePort(0xE3, 0x80) // CONMEM set
	p.WritePort(0xE3, 0x00) // CONMEM clear
	if !p.IsPagedIn() {
		t.Errorf("genuine automap paging must survive a CONMEM toggle")
	}
}

// TestE3_MAPRAM_IsSticky verifies bit 6 latches: once set, subsequent
// writes with bit 6 = 0 do NOT clear it.
func TestE3_MAPRAM_IsSticky(t *testing.T) {
	p := New(makeROM())
	if p.MAPRAM() {
		t.Fatal("precondition: MAPRAM should default to false")
	}
	p.WritePort(0xE3, 0x40) // set MAPRAM
	if !p.MAPRAM() {
		t.Fatal("MAPRAM set: should be true")
	}
	p.WritePort(0xE3, 0x00) // attempt clear
	if !p.MAPRAM() {
		t.Errorf("MAPRAM should be sticky — writing 0 should NOT clear it")
	}
	p.WritePort(0xE3, 0xBF) // every bit except bit 6
	if !p.MAPRAM() {
		t.Errorf("MAPRAM should still be set after writing 0xBF (bit 6 clear)")
	}
}

// TestE3_MAPRAM_ClearedByClearMAPRAM verifies that ClearMAPRAM (used
// by NR$09 bit 3) DOES force-clear MAPRAM despite stickiness. Without
// this, MAPRAM could only be cleared by hardware reset.
func TestE3_MAPRAM_ClearedByClearMAPRAM(t *testing.T) {
	p := New(makeROM())
	p.WritePort(0xE3, 0x40)
	if !p.MAPRAM() {
		t.Fatal("precondition: MAPRAM should be set")
	}
	p.ClearMAPRAM()
	if p.MAPRAM() {
		t.Errorf("ClearMAPRAM should force MAPRAM off (override stickiness)")
	}
	// Also verify LastE3 bit 6 was cleared.
	if p.LastE3()&0x40 != 0 {
		t.Errorf("ClearMAPRAM should also clear LastE3 bit 6: $%02X",
			p.LastE3())
	}
}

// TestE3_BankSelect_LowFourBits verifies bits 3:0 select the RAM bank.
// 16 banks (0-15) are accessible at $2000-$3FFF when overlay is paged.
func TestE3_BankSelect_LowFourBits(t *testing.T) {
	p := New(makeROM())
	for bank := byte(0); bank < NumBanks; bank++ {
		p.WritePort(0xE3, bank)
		// Read-back via LastE3 bits 3:0.
		if got := p.LastE3() & 0x0F; got != bank {
			t.Errorf("bank %d: LastE3 bits 3:0 = $%02X, want $%02X",
				bank, got, bank)
		}
	}
}

// TestE3_LastE3_RecordsFullWrittenByte verifies that LastE3 returns
// the most recent byte written to port $E3 (used by the debugger
// `get-divmmc` command).
func TestE3_LastE3_RecordsFullWrittenByte(t *testing.T) {
	p := New(makeROM())
	cases := []byte{0x00, 0xFF, 0x80, 0x40, 0xC0, 0x83, 0x47}
	for _, val := range cases {
		p.WritePort(0xE3, val)
		// MAPRAM stickiness means LastE3 may reflect val with bit 6
		// from previous writes — but freshly-written non-sticky bits
		// should match val.
		got := p.LastE3()
		// CONMEM (bit 7) and bank-select bits (3:0) should match val.
		gotMask := got &^ 0x40
		valMask := val &^ 0x40
		if gotMask != valMask {
			t.Errorf("write $%02X: LastE3 non-MAPRAM bits = $%02X, want $%02X",
				val, gotMask, valMask)
		}
	}
}

// TestE3_CONMEM_AND_MAPRAM_BothSet verifies the documented interaction:
// CONMEM forces paging in; MAPRAM is honored.
func TestE3_CONMEM_AND_MAPRAM_BothSet(t *testing.T) {
	p := New(makeROM())
	p.SetAutomap(false)
	p.WritePort(0xE3, 0xC0) // both bits
	if !p.IsPagedIn() {
		t.Errorf("CONMEM+MAPRAM: should be paged in")
	}
	if !p.MAPRAM() {
		t.Errorf("CONMEM+MAPRAM: MAPRAM should be set")
	}
}

// TestE3_PortMismatch_NotHandled verifies WritePort returns false for
// ports that aren't divMMC ports (lets memory dispatch fall through
// to other handlers).
func TestE3_PortMismatch_NotHandled(t *testing.T) {
	p := New(makeROM())
	cases := []uint16{0x00FE, 0x7FFD, 0x1FFD, 0x253B, 0xFFFD}
	for _, port := range cases {
		if p.WritePort(port, 0xFF) {
			t.Errorf("port $%04X: WritePort returned true (should be false — not a divMMC port)",
				port)
		}
	}
}

// TestE3_LowByteMatch verifies the port is matched on the low byte
// only (= $XXE3 should fire regardless of high byte).
func TestE3_LowByteMatch(t *testing.T) {
	p := New(makeROM())
	p.SetAutomap(false)
	// Try various $XXE3 values.
	for _, port := range []uint16{0x00E3, 0x01E3, 0x7FE3, 0xFFE3, 0xABE3} {
		p.WritePort(port, 0x00) // reset
		p.WritePort(port, 0x80) // CONMEM
		if !p.IsPagedIn() {
			t.Errorf("port $%04X with CONMEM: should be paged in (low-byte match)",
				port)
		}
	}
}

// TestE3_RAMBankOnReadAtSlot1 verifies that the bank selector (bits
// 3:0 of port $E3) controls which RAM bank reads at $2000-$3FFF
// return when the overlay is paged in.
func TestE3_RAMBankOnReadAtSlot1(t *testing.T) {
	p := New(makeROM())
	p.SetAutomap(false)
	// Stamp each bank with a known sentinel at offset 0.
	for bank := 0; bank < NumBanks; bank++ {
		p.ram[bank][0] = byte(0xA0 + bank)
	}
	p.WritePort(0xE3, 0x80) // page in, bank 0

	// Page in with each of the 16 banks selected in turn.
	for bank := byte(0); bank < NumBanks; bank++ {
		p.WritePort(0xE3, 0x80|bank)
		got, ok := p.HandleRead(0x2000)
		if !ok {
			t.Errorf("bank %d: HandleRead returned !ok", bank)
			continue
		}
		want := byte(0xA0 + bank)
		if got != want {
			t.Errorf("bank %d: read at $2000 = $%02X, want $%02X",
				bank, got, want)
		}
	}
}
