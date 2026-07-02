package divmmc

import "testing"

// TestSetAutomapHonoursCONMEM verifies that disabling automap
// while CONMEM (port $E3 bit 7) is set leaves the overlay paged
// in. The divMMC ROM's exit routine sets CONMEM=$80, then
// disables automap via NextReg $06 bit 4; the next instruction
// runs from divMMC ROM, so the overlay must stay until an
// explicit OUT($E3),0 (or a $1FF8 off-area fetch) drops it.
//
// Without this guard the overlay drops mid-routine, the CPU
// falls into NextZXOS bank-0 $00D9 (NEXTREG $8E,$03), ROM bank
// swaps to 3, and the system spins in an infinite soft-reset
// loop.
func TestSetAutomapHonoursCONMEM(t *testing.T) {
	p := New([]byte{0xFF})
	p.SetAutomap(true)
	p.pagedIn = true
	// Simulate divMMC ROM's exit-routine first step:
	// OUT ($E3),A=$80 — set CONMEM (forces overlay in).
	p.WritePort(0xE3, 0x80)
	if !p.IsPagedIn() {
		t.Fatalf("after CONMEM=$80: overlay should stay paged")
	}
	// Now disable automap (NextReg $06 bit 4 cleared by exit code).
	p.SetAutomap(false)
	if !p.IsPagedIn() {
		t.Errorf("after SetAutomap(false) WITH CONMEM set: overlay must stay (CONMEM overrides automap)")
	}
	// Clearing CONMEM (with automap already off) drops the overlay.
	p.WritePort(0xE3, 0x00)
	if p.IsPagedIn() {
		t.Errorf("after CONMEM=0 + automap off: overlay should drop")
	}
}

// spyCard records the CS values it is told to write, so a test can
// assert what the pager does to the SPI slave-select on reset.
type spyCard struct {
	csWrites []byte
}

func (s *spyCard) WriteCS(val byte) { s.csWrites = append(s.csWrites, val) }
func (s *spyCard) WriteData(_ byte) {}
func (s *spyCard) ReadData() byte   { return 0xFF }

// TestPagerResetSPIDeassertsCS verifies that ResetSPI() deasserts every
// SPI chip-select on the card (writes $FF to port $E7). On real hardware
// a core reset (hard or soft) sets port_e7_reg to all-ones
// (zxnext.vhd:3308 `if reset='1' then port_e7_reg <= (others => '1')`),
// which aborts any in-flight SD transaction. NextZXOS soft-resets right
// after mounting the card mid-CMD18; without this the card stays mid-
// stream and the post-reset re-init spins forever.
func TestPagerResetSPIDeassertsCS(t *testing.T) {
	p := New([]byte{0xFF})
	spy := &spyCard{}
	p.SetCard(spy)

	p.ResetSPI()

	if len(spy.csWrites) != 1 {
		t.Fatalf("ResetSPI wrote CS %d times, want 1: %v", len(spy.csWrites), spy.csWrites)
	}
	if spy.csWrites[0] != 0xFF {
		t.Errorf("ResetSPI wrote CS=$%02X, want $FF (all slave-selects deasserted)", spy.csWrites[0])
	}
}

// TestPagerResetSPINoCardSafe verifies ResetSPI is a no-op (no panic)
// when no card is wired — classic models / SD-less test paths.
func TestPagerResetSPINoCardSafe(t *testing.T) {
	p := New([]byte{0xFF})
	p.ResetSPI() // must not panic
}

func makeROM() []byte {
	rom := make([]byte, ROMSize)
	for i := range rom {
		rom[i] = byte(0xA0 + (i & 0x1F)) // distinctive pattern per offset
	}
	return rom
}

// newPagerAutomapped returns a Pager with automap enabled, since
// real hardware boots with automap *off* and the bootrom enables
// it via NextReg $06 bit 4. Every test that exercises trigger-PC
// behaviour calls this helper to mirror the post-bootrom state.
func newPagerAutomapped(rom []byte) *Pager {
	p := New(rom)
	p.SetAutomap(true)
	// Open the NR$B9 validation gate: its default limits RST traps to
	// RST $00 only, but a real guest writes NR$B9 to enable the
	// others. Opening it here lets tests exercise the NR$B8 logic in
	// isolation.
	p.SetEntryPointsValid0(0xFF)
	// epTiming0 defaults to $00 (all entry points delayed_on — page-in
	// effective next M1). Tests using this helper assert page-in/read
	// mechanics, not timing, so configure all entry points instant_on
	// (NextReg $BA = $FF): Step(trigger) then pages in on that same M1.
	// Delayed timing is exercised separately by TestPagerDelayedTrigger.
	p.SetEntryPointsTiming0(0xFF)
	return p
}

func TestPagerStartsOut(t *testing.T) {
	p := New(makeROM())
	if p.IsPagedIn() {
		t.Errorf("new pager should start with overlay out")
	}
	if p.AutomapEnabled() {
		t.Errorf("new pager should start with automap off (real hardware behaviour)")
	}
	if _, ok := p.HandleRead(0x0000); ok {
		t.Errorf("reads should fall through when not paged in")
	}
}

func TestPagerTriggersOnEachKnownPC(t *testing.T) {
	for _, pc := range TriggerPCs {
		p := newPagerAutomapped(makeROM())
		if pc == 0x0066 {
			p.AssertNMIButton() // $0066 NMI automap requires button_nmi (divmmc.vhd:120)
		}
		p.Step(pc)
		// The NR$BB code entry points ($04C6/$0562/$04D7/$056A) are
		// rom3_delayed_on (zxnext.vhd:2901): the overlay appears on the
		// M1 AFTER the trigger. Drive one more fetch so both instant
		// and delayed triggers register.
		p.Step(pc + 1)
		if !p.IsPagedIn() {
			t.Errorf("trigger PC %#x should have paged the overlay in", pc)
		}
	}
}

func TestPagerStaysInWhileInLowMemory(t *testing.T) {
	p := newPagerAutomapped(makeROM())
	p.Step(0x0008)
	if !p.IsPagedIn() {
		t.Fatalf("precondition: overlay should be in after trigger")
	}
	// Walk through non-trigger PCs in the bottom 16 KB. The overlay
	// stays in for all of these — page-out only happens at the
	// $1FF8-$1FFF off-area or on RETN.
	for _, pc := range []uint16{0x0009, 0x0100, 0x2000, 0x3FFF, 0x4000} {
		p.Step(pc)
		if !p.IsPagedIn() {
			t.Errorf("PC=%#x: overlay should still be in (page-out is gated to $1FF8-$1FFF)", pc)
		}
	}
}

func TestPagerUnpagesAtOffArea(t *testing.T) {
	p := newPagerAutomapped(makeROM())
	p.AssertNMIButton() // $0066 NMI automap requires button_nmi (divmmc.vhd:120)
	p.Step(0x0066)
	if !p.IsPagedIn() {
		t.Fatalf("precondition: overlay should be in")
	}
	// Per FPGA timing (divmmc.vhd lines 138-143), automap_held only
	// updates on the rising edge of MREQ at end-of-M1 — so the M1
	// opcode fetch at $1FF8-$1FFF still reads divMMC ROM. The drop
	// is therefore in PostStep (PostFetchHook), not Step
	// (PreFetchHook). Step at $1FF8 must KEEP the overlay paged in.
	p.Step(0x1FF8)
	if !p.IsPagedIn() {
		t.Errorf("Step at $1FF8 (M1 pre-fetch) should keep overlay paged in for opcode read")
	}
	p.PostStep(0x1FF8)
	if p.IsPagedIn() {
		t.Errorf("PostStep at $1FF8 (M1 post-fetch) should have paged the overlay out")
	}
}

func TestPagerUnpagesOnRETN(t *testing.T) {
	p := newPagerAutomapped(makeROM())
	p.AssertNMIButton() // $0066 NMI automap requires button_nmi (divmmc.vhd:120)
	p.Step(0x0066)
	if !p.IsPagedIn() {
		t.Fatalf("precondition: overlay should be in")
	}
	p.HandleRETN()
	if p.IsPagedIn() {
		t.Errorf("RETN should have paged the overlay out")
	}
}

func TestPagerRETNRespectsConmem(t *testing.T) {
	p := newPagerAutomapped(makeROM())
	// CONMEM forces overlay in; RETN should NOT drop it.
	p.WritePort(0xE3, 0x80)
	if !p.IsPagedIn() {
		t.Fatalf("precondition: CONMEM should have forced overlay in")
	}
	p.HandleRETN()
	if !p.IsPagedIn() {
		t.Errorf("RETN with CONMEM=1 should leave overlay paged in")
	}
}

// TestPagerOffAreaPageOutClearsLatchUnderConmem locks in the fix for the
// NextBASIC Invaders crash. The esxDOS IM1 handler pages the overlay out
// via JP C,$1FFC — and that carry is the OLD CONMEM bit, so the page-out
// fetch lands in the $1FF8-$1FFF off-area precisely WHILE CONMEM is set.
// Per device/divmmc.vhd the automap_held latch is cleared by the off-area
// (delayed_off) with NO CONMEM term (line 131) — CONMEM is an orthogonal
// force-in (lines 94-95). So the off-area fetch must clear the automap
// latch even under CONMEM; the overlay stays mapped via CONMEM until the
// firmware clears CONMEM, at which point it must DROP (no stranded latch).
func TestPagerOffAreaPageOutClearsLatchUnderConmem(t *testing.T) {
	p := newPagerAutomapped(makeROM())
	p.Step(0x0008) // genuine automap page-in (sets the automap latch)
	if !p.IsPagedIn() {
		t.Fatalf("precondition: overlay should be in via automap trigger")
	}
	p.WritePort(0xE3, 0x80) // CONMEM set — the firmware's page-out runs with CONMEM=1
	p.Step(0x1FFC)          // off-area opcode fetch: overlay still mapped for the read
	p.PostStep(0x1FFC)      // post-fetch: must clear the automap latch
	if !p.IsPagedIn() {
		t.Fatalf("during CONMEM hold the overlay must stay mapped (CONMEM force-in)")
	}
	p.WritePort(0xE3, 0x00) // firmware (trampoline) clears CONMEM
	if p.IsPagedIn() {
		t.Errorf("after off-area page-out then CONMEM clear: overlay must drop — automap latch leaked in")
	}
}

// TestPagerRETNClearsLatchUnderConmem is the RETN dual of the off-area
// test. device/divmmc.vhd:139 clears automap_held on i_retn_seen
// unconditionally (no CONMEM term). So a RETN while CONMEM is set must
// clear the automap latch; the overlay holds via CONMEM until cleared,
// then drops. TestPagerRETNRespectsConmem above still holds (IsPagedIn
// stays true under CONMEM); this adds that the latch itself is cleared.
func TestPagerRETNClearsLatchUnderConmem(t *testing.T) {
	p := newPagerAutomapped(makeROM())
	p.Step(0x0008) // genuine automap page-in (sets the automap latch)
	if !p.IsPagedIn() {
		t.Fatalf("precondition: overlay should be in via automap trigger")
	}
	p.WritePort(0xE3, 0x80) // CONMEM set
	p.HandleRETN()          // RETN clears automap_held unconditionally
	if !p.IsPagedIn() {
		t.Fatalf("during CONMEM hold the overlay must stay mapped after RETN")
	}
	p.WritePort(0xE3, 0x00) // CONMEM cleared
	if p.IsPagedIn() {
		t.Errorf("after RETN under CONMEM then CONMEM clear: overlay must drop — automap latch leaked in")
	}
}

func TestPagerNonTriggerLowPCDoesNotPageIn(t *testing.T) {
	// Real divMMC requires entering via a trigger. A jump that
	// lands at a non-trigger low address must NOT page in.
	p := newPagerAutomapped(makeROM())
	p.Step(0x0100)
	if p.IsPagedIn() {
		t.Errorf("non-trigger low PC should not page the overlay in")
	}
}

func TestPagerROMReadable(t *testing.T) {
	rom := makeROM()
	p := newPagerAutomapped(rom)
	p.Step(0x0000)
	val, ok := p.HandleRead(0x0010)
	want := rom[0x10]
	if !ok || val != want {
		t.Errorf("ROM read after page-in: val=%#x ok=%v, want %#x true", val, ok, want)
	}
}

func TestPagerROMOpenBusOnShortROM(t *testing.T) {
	p := newPagerAutomapped([]byte{0xDE, 0xAD})
	p.Step(0x0000)
	val, ok := p.HandleRead(0x0010)
	if !ok || val != 0xFF {
		t.Errorf("ROM read past end: val=%#x ok=%v, want 0xFF true (open bus)", val, ok)
	}
}

func TestPagerRAMReadable(t *testing.T) {
	p := newPagerAutomapped(makeROM())
	p.Step(0x0000)
	// RAM at 0x2000–0x3FFF; bank 0 is selected by default.
	p.ram[0][0x100] = 0x55
	val, ok := p.HandleRead(0x2100)
	if !ok || val != 0x55 {
		t.Errorf("RAM read after page-in: val=%#x ok=%v, want 0x55 true", val, ok)
	}
}

func TestPagerRAMWritable(t *testing.T) {
	p := newPagerAutomapped(makeROM())
	p.Step(0x0000)
	handled := p.HandleWrite(0x2123, 0xAA)
	if !handled {
		t.Errorf("RAM write should be handled")
	}
	if p.ram[0][0x0123] != 0xAA {
		t.Errorf("RAM byte = %#x, want 0xAA", p.ram[0][0x0123])
	}
}

func TestPagerMAPRAMSwapsROMForBank3(t *testing.T) {
	rom := makeROM()
	p := newPagerAutomapped(rom)
	p.Step(0x0000)
	// Stamp a marker byte into bank 3 to prove the swap path
	// reads from bank 3, not the ROM image.
	p.ram[MAPRAMBank][0x10] = 0xBE
	// Before MAPRAM: low window reads ROM byte.
	if v, _ := p.HandleRead(0x0010); v != rom[0x10] {
		t.Fatalf("pre-MAPRAM: got %#x, want ROM byte %#x", v, rom[0x10])
	}
	// Latch MAPRAM via port 0xE3 bit 6. CONMEM (bit 7) clear so
	// the MAPRAM swap is honoured (CONMEM=1 would force ROM).
	p.WritePort(0xE3, 0x40)
	if !p.MAPRAM() {
		t.Fatalf("MAPRAM bit should be latched after writing 0x40 to 0xE3")
	}
	// Low window should now read the bank-3 marker, not ROM.
	if v, _ := p.HandleRead(0x0010); v != 0xBE {
		t.Errorf("post-MAPRAM: got %#x, want bank-3 marker 0xBE", v)
	}
}

// TestPagerConmemMAPRAMServesBank3 pins the FPGA-faithful behaviour: when
// MAPRAM is latched, the $0000-$1FFF window serves divMMC RAM bank 3 even
// when CONMEM is also set. divmmc.vhd:94-95 — `rom_en` requires `mapram='0'`,
// so under MAPRAM `ram_en` wins and bank 3 substitutes for the ROM; the
// Issue #7 comment (line 91) states this explicitly ("Also bring in divmmc
// bank 3 as rom substitute when conmem is set"). CONMEM only forces the
// overlay paged-in, it never overrides the MAPRAM bank-3 substitution.
// (Was TestPagerConmemForcesROMOverMAPRAM, which enshrined the inverse — the
// `&& !conmem` workaround — contradicting the VHDL.)
func TestPagerConmemMAPRAMServesBank3(t *testing.T) {
	rom := makeROM()
	p := newPagerAutomapped(rom)
	p.WritePort(0xE3, 0xC0) // CONMEM + MAPRAM
	if !p.MAPRAM() {
		t.Fatalf("MAPRAM should latch even when CONMEM is also set")
	}
	p.ram[MAPRAMBank][0x10] = 0xBE
	if v, _ := p.HandleRead(0x0010); v != 0xBE {
		t.Errorf("CONMEM+MAPRAM low window: got %#x, want bank-3 marker 0xBE (divmmc.vhd:94 rom_en needs mapram=0)", v)
	}
}

func TestPagerMAPRAMIsSticky(t *testing.T) {
	p := newPagerAutomapped(makeROM())
	p.Step(0x0000)
	// Set MAPRAM (bit 6) then write zero — MAPRAM should stay set.
	p.WritePort(0xE3, 0x40)
	p.WritePort(0xE3, 0x00)
	if !p.MAPRAM() {
		t.Errorf("MAPRAM should remain latched after subsequent writes without bit 6")
	}
}

func TestPagerMAPRAMProtectsBank3InHighWindow(t *testing.T) {
	p := newPagerAutomapped(makeROM())
	p.Step(0x0000)
	// MAPRAM on, bank 3 selected at $2000-$3FFF, CONMEM off so
	// MAPRAM behaviour is active.
	p.WritePort(0xE3, 0x40|MAPRAMBank)
	// Writes via the LOW window are dropped (it's the read-only
	// ROM shadow under MAPRAM).
	p.HandleWrite(0x0010, 0x11)
	if p.ram[MAPRAMBank][0x10] == 0x11 {
		t.Errorf("MAPRAM should drop writes to 0x0000-0x1FFF")
	}
	// Writes to bank 3 via the HIGH window are ALSO dropped under
	// MAPRAM (the reference Spectrum Next emulator/FPGA semantics — MAPRAM globally protects
	// bank 3, not only the low-window shadow).
	p.HandleWrite(0x2000, 0x99)
	if p.ram[MAPRAMBank][0] == 0x99 {
		t.Errorf("MAPRAM should drop writes to bank 3 via $2000-$3FFF; got %#x", p.ram[MAPRAMBank][0])
	}
	// With a different bank selected, writes go through.
	p.WritePort(0xE3, 0x40|2)
	p.HandleWrite(0x2000, 0x77)
	if p.ram[2][0] != 0x77 {
		t.Errorf("write to bank 2 should land; got %#x", p.ram[2][0])
	}
}

func TestPagerBankSelector(t *testing.T) {
	p := newPagerAutomapped(makeROM())
	p.Step(0x0000)
	// Select bank 5 via port 0xE3 (CONMEM bit 7 keeps overlay paged).
	p.WritePort(0xE3, 0x80|5)
	p.HandleWrite(0x2000, 0x77)
	if p.ram[5][0] != 0x77 {
		t.Errorf("write to 0x2000 with bank=5 selected should land in bank 5, got ram[5][0]=%#x", p.ram[5][0])
	}
	val, _ := p.HandleRead(0x2000)
	if val != 0x77 {
		t.Errorf("read from 0x2000 with bank=5 selected should return 0x77, got %#x", val)
	}
	// Switch to bank 6 — reads should miss the bank-5 write.
	p.WritePort(0xE3, 0x80|6)
	val, _ = p.HandleRead(0x2000)
	if val == 0x77 {
		t.Errorf("after selecting bank 6, read at 0x2000 still saw bank-5 value")
	}
}

func TestPagerROMNotWritable(t *testing.T) {
	p := newPagerAutomapped(makeROM())
	p.Step(0x0000)
	handled := p.HandleWrite(0x0000, 0xAA)
	if !handled {
		t.Errorf("ROM-area write should still claim handled (so host memory doesn't write through)")
	}
	if val, _ := p.HandleRead(0x0000); val == 0xAA {
		t.Errorf("ROM-area write actually mutated ROM (val=%#x)", val)
	}
}

func TestPagerAutomapDisableDropsOverlay(t *testing.T) {
	p := newPagerAutomapped(makeROM())
	p.Step(0x0000)
	if !p.IsPagedIn() {
		t.Fatalf("precondition: overlay should be in")
	}
	p.SetAutomap(false)
	if p.IsPagedIn() {
		t.Errorf("SetAutomap(false) should drop the overlay")
	}
	// Subsequent trigger fetches do nothing.
	p.Step(0x0008)
	if p.IsPagedIn() {
		t.Errorf("trigger should be inert while automap disabled")
	}
	// Re-enable and the next trigger re-activates.
	p.SetAutomap(true)
	p.AssertNMIButton() // $0066 NMI automap requires button_nmi (divmmc.vhd:120)
	p.Step(0x0066)
	if !p.IsPagedIn() {
		t.Errorf("trigger after re-enable should page in")
	}
}

func TestPagerHighAddressesFallThrough(t *testing.T) {
	p := newPagerAutomapped(makeROM())
	p.Step(0x0000)
	if _, ok := p.HandleRead(0x8000); ok {
		t.Errorf("address 0x8000 should fall through to host memory")
	}
	if p.HandleWrite(0x8000, 0xAA) {
		t.Errorf("write at 0x8000 should fall through")
	}
}

func TestPagerConmemPagesInWithoutAutomap(t *testing.T) {
	// CONMEM forces the overlay in regardless of automap. This is
	// how esxDOS's NMI button enters its menu even when automap is
	// off.
	p := New(makeROM())
	if p.AutomapEnabled() {
		t.Fatalf("precondition: automap should default off")
	}
	p.WritePort(0xE3, 0x80) // CONMEM
	if !p.IsPagedIn() {
		t.Errorf("CONMEM write should force overlay in even with automap off")
	}
	// Clearing CONMEM with automap off drops the overlay.
	p.WritePort(0xE3, 0x00)
	if p.IsPagedIn() {
		t.Errorf("clearing CONMEM with automap off should drop the overlay")
	}
}

// TestStubProtectionShadowsBank1$2009 locks in the FPGA-shadow
// emulation: when SetStubProtected(true) is on, guest writes to
// bank-1 offset $0009 (= $2009 paged in) are silently dropped so
// a pre-loaded TBBLUE.FW IRQ stub byte survives NextZXOS's $C9
// placeholder write. Other addresses are unaffected.
func TestStubProtectionShadowsBank1At2009(t *testing.T) {
	p := New(makeROM())
	p.SetStubProtected(true)
	// Seed bank 1 offset 9 = $23 (the TBBLUE.FW-installed INC HL).
	p.ram[1][0x09] = 0x23
	// Page in + select bank 1.
	p.WritePort(0xE3, 0x80|0x01) // CONMEM + bank 1
	if !p.IsPagedIn() {
		t.Fatalf("setup: pager should be in after CONMEM write")
	}
	if p.selectedBank() != 1 {
		t.Fatalf("setup: bank = %d, want 1", p.selectedBank())
	}
	// Guest writes $C9 (NextZXOS placeholder) — must be silently
	// dropped while protection is on.
	p.HandleWrite(0x2009, 0xC9)
	if got := p.ram[1][0x09]; got != 0x23 {
		t.Errorf("after protected write: bank1[$9] = $%02X, want $23 (protection failed)", got)
	}
	// Adjacent bytes must still be writable so the IRQ handler's
	// stack/sysvar writes work — only $2009 is shadowed.
	p.HandleWrite(0x2008, 0xAA)
	if got := p.ram[1][0x08]; got != 0xAA {
		t.Errorf("$2008 write got dropped (over-protected): bank1[$8] = $%02X, want $AA", got)
	}
	p.HandleWrite(0x200A, 0xBB)
	if got := p.ram[1][0x0A]; got != 0xBB {
		t.Errorf("$200A write got dropped (over-protected): bank1[$A] = $%02X, want $BB", got)
	}
}

// TestStubProtectionOffDoesNotShadow confirms the gate: with
// SetStubProtected(false) (the default), writes to $2009 land
// normally. Guards against the protection being always-on which
// would break the existing FRAMES-bumper path used when no
// snapshot is installed.
func TestStubProtectionOffDoesNotShadow(t *testing.T) {
	p := New(makeROM())
	if p.StubProtected() {
		t.Fatalf("precondition: stub protection should default off")
	}
	p.ram[1][0x09] = 0x23
	p.WritePort(0xE3, 0x80|0x01)
	p.HandleWrite(0x2009, 0xC9)
	if got := p.ram[1][0x09]; got != 0xC9 {
		t.Errorf("default (unprotected) write: bank1[$9] = $%02X, want $C9", got)
	}
}

// TestStubProtectionOnlyShieldsBank1 — the same offset in OTHER
// banks must remain writable. The FPGA shadow is bank-1-specific.
func TestStubProtectionOnlyShieldsBank1(t *testing.T) {
	p := New(makeROM())
	p.SetStubProtected(true)
	p.ram[0][0x09] = 0x00
	// Switch to bank 0 + page in.
	p.WritePort(0xE3, 0x80)
	p.HandleWrite(0x2009, 0xC9)
	if got := p.ram[0][0x09]; got != 0xC9 {
		t.Errorf("bank 0 $2009 should be writable even with stub protection: bank0[$9] = $%02X, want $C9", got)
	}
}

// TestPagerDelayedTrigger pins the instant-vs-delayed automap timing
// (NextReg $BA / epTiming0). Per zxnext.vhd 2898-2901 + divmmc.vhd
// 129-148, an RST entry point whose timing bit is CLEAR is
// *delayed_on*: it sets automap_held, which only takes effect on the
// NEXT M1 — so the trigger instruction itself still executes from the
// pre-overlay map. (epValid0 bit set = rom variant, so the low window
// serves divMMC ROM once paged; this test isolates timing.)
//
// This is the divMMC half of the cold-boot blocker: the $0038 IM1
// vector is delayed (BA bit7=0), so $0038 must run the NextZXOS IM1
// handler, NOT the divMMC esxDOS ROM.
func TestPagerDelayedTrigger(t *testing.T) {
	p := New(makeROM())
	p.SetAutomap(true)
	p.SetEntryPointsValid0(0x80)  // $0038 = bit7: rom (valid), not rom3
	p.SetEntryPointsTiming0(0x00) // $0038 timing bit7 CLEAR => delayed
	// $0038 = bit7 is enabled by the soft-reset default entryPoints0=$83.

	p.Step(0x0038) // trigger M1: delayed => overlay NOT in yet
	if p.IsPagedIn() {
		t.Fatalf("delayed trigger: overlay must NOT be paged in on the trigger M1")
	}
	if _, ok := p.HandleRead(0x0000); ok {
		t.Errorf("delayed trigger: low window must still pass through on the trigger M1")
	}

	p.Step(0x0039) // next M1: overlay now in
	if !p.IsPagedIn() {
		t.Errorf("delayed trigger: overlay must be paged in on the NEXT M1")
	}
}

// TestPagerRom3EngagedServesDivMMCWindows verifies that when a rom3 entry
// point engages (ROM3 selected), the divMMC ROM shows at $0000-$1FFF
// (divmmc.vhd:94 — rom_en has no rom3 term) AND the $2000-$3FFF divMMC RAM
// window is overlaid. rom3 does NOT pass the machine ROM through — that was
// the wrong model that made $0038 IM1 run the machine ROM where the real
// machine runs the divMMC esxDOS ROM.
func TestPagerRom3EngagedServesDivMMCWindows(t *testing.T) {
	rom := makeROM()
	p := New(rom)
	p.SetAutomap(true)
	p.SetEntryPointsValid0(0x00)                      // $0038 bit7 = 0 => rom3 entry
	p.SetEntryPointsTiming0(0x80)                     // bit7 = 1 => instant
	p.SetRom3Query(func(uint16) bool { return true }) // ROM3 selected => engages
	p.Step(0x0038)
	if !p.IsPagedIn() {
		t.Fatal("precondition: rom3 trigger with ROM3 selected should page in")
	}
	// Low window: divMMC ROM (NOT a machine-ROM pass-through).
	if v, ok := p.HandleRead(0x0000); !ok || v != rom[0] {
		t.Errorf("rom3 engaged: $0000 must serve divMMC ROM, got val=%#x ok=%v want %#x true", v, ok, rom[0])
	}
	if v, ok := p.HandleRead(0x1FFF); !ok || v != rom[0x1FFF] {
		t.Errorf("rom3 engaged: $1FFF must serve divMMC ROM, got val=%#x ok=%v", v, ok)
	}
	// High window: divMMC RAM overlay.
	if _, ok := p.HandleRead(0x2000); !ok {
		t.Errorf("rom3 engaged: $2000 must be claimed (divMMC RAM)")
	}
}

// TestPagerRom3ConmemStillServesDivMMCROM verifies rom3 mode does NOT
// suppress the divMMC ROM in the low window when CONMEM forces the
// overlay: CONMEM (port $E3 bit 7) always maps the divMMC esxDOS ROM at
// $0000-$1FFF (dot-command launchers depend on it), regardless of the
// automap ROM3 variant that paged the overlay in.
func TestPagerRom3ConmemStillServesDivMMCROM(t *testing.T) {
	p := New(makeROM())
	p.SetAutomap(true)
	p.SetEntryPointsValid0(0x00)  // rom3
	p.SetEntryPointsTiming0(0x80) // instant
	p.Step(0x0038)
	p.WritePort(0xE3, 0x80) // CONMEM force-in
	if _, ok := p.HandleRead(0x0000); !ok {
		t.Errorf("rom3 + CONMEM: low window must serve divMMC ROM (ok=true)")
	}
}

// TestPagerDelayedArmCancelledByPageOut verifies a page-out between a
// delayed_on arm and its next-M1 conversion cancels the pending page-in:
// the delayed arm has already set automap_held (just not yet reflected in
// the map), and a RETN / $1FFx off-area fetch clears automap_held — so
// the overlay must NOT spuriously resurrect on the following M1.
func TestPagerDelayedArmCancelledByPageOut(t *testing.T) {
	p := New(makeROM())
	p.SetAutomap(true)
	p.SetEntryPointsValid0(0x80)  // $0038 rom (isolate from rom3)
	p.SetEntryPointsTiming0(0x00) // delayed
	p.Step(0x0038)                // arms the pending (delayed) page-in
	p.HandleRETN()                // page-out before the conversion M1
	p.Step(0x0100)                // next M1: must NOT page in
	if p.IsPagedIn() {
		t.Errorf("page-out (RETN) must cancel a pending delayed page-in")
	}
}

// TestPagerRom3EngagesOnlyWhenROM3Selected pins the rom3 automap CONTEXT
// gate. Per zxnext.vhd:3138 (sram_divmmc_automap_rom3_en, gated by
// sram_pre_rom3) a rom3 entry point (epValid0/B9 bit CLEAR) engages the
// overlay ONLY when ROM3 is the currently-selected machine ROM; otherwise
// the trigger is a no-op (overlay stays out → machine ROM shows). And per
// divmmc.vhd:94, when it DOES engage the divMMC ROM shows at $0000-$1FFF —
// rom3 does NOT pass the machine ROM through (that earlier model was wrong:
// it made $0038 IM1 run the machine ROM where the reference runs the esxDOS ROM).
func TestPagerRom3EngagesOnlyWhenROM3Selected(t *testing.T) {
	rom := makeROM()

	// ROM3 NOT selected → rom3 entry must NOT engage.
	p := New(rom)
	p.SetAutomap(true)
	p.SetEntryPointsValid0(0x00)  // $0038 bit7=0 => rom3 entry
	p.SetEntryPointsTiming0(0x80) // bit7=1 => instant (isolate from delayed)
	p.SetRom3Query(func(uint16) bool { return false })
	p.Step(0x0038)
	if p.IsPagedIn() {
		t.Errorf("rom3 entry, ROM3 not selected: overlay must NOT engage")
	}

	// ROM3 selected → engages, and serves the divMMC ROM (not machine ROM).
	p = New(rom)
	p.SetAutomap(true)
	p.SetEntryPointsValid0(0x00)
	p.SetEntryPointsTiming0(0x80)
	p.SetRom3Query(func(uint16) bool { return true })
	p.Step(0x0038)
	if !p.IsPagedIn() {
		t.Fatalf("rom3 entry, ROM3 selected: overlay must engage")
	}
	val, ok := p.HandleRead(0x0010)
	if !ok || val != rom[0x10] {
		t.Errorf("rom3 engaged: low window must serve the divMMC ROM, got val=%#x ok=%v want %#x true", val, ok, rom[0x10])
	}
}

// TestPager3DXXEntryGatedOnROM3 pins that the $3DXX entry point (NR$BB bit7)
// is rom3_instant_on (zxnext.vhd:2898 — port_3dxx_msb feeds
// divmmc_automap_rom3_instant_on, engaged via i_automap_rom3_active): it
// pages the overlay in ONLY when ROM3 is the selected machine ROM. Outside
// ROM3 it is a no-op, so the machine ROM at $3Dxx shows (e.g. NextZXOS's
// bank-0 $3Dxx code) instead of the empty divMMC RAM window. Without the
// gate, ours paged in at $3D97 (empty RAM) where the real machine ran the
// ROM code there.
func TestPager3DXXEntryGatedOnROM3(t *testing.T) {
	// ROM3 NOT selected → $3DXX must NOT engage.
	p := New(makeROM())
	p.SetAutomap(true)
	p.SetEntryPoints1(0x80) // $3DXX trap enabled
	p.SetRom3Query(func(uint16) bool { return false })
	p.Step(0x3D97)
	if p.IsPagedIn() {
		t.Errorf("$3DXX with ROM3 not selected: overlay must NOT engage")
	}
	// ROM3 selected → engages (the NEXTREG $8E,$03 → $3D00 trampoline path).
	p = New(makeROM())
	p.SetAutomap(true)
	p.SetEntryPoints1(0x80)
	p.SetRom3Query(func(uint16) bool { return true })
	p.Step(0x3D97)
	if !p.IsPagedIn() {
		t.Errorf("$3DXX with ROM3 selected: overlay must engage")
	}
}
