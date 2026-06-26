package sam

import (
	"image/color"
	"testing"
)

// asic_spec_test.go is a spec-faithful test suite pinning the SAM Coupé ASIC's
// documented register behaviour. Every assertion cites the SAM Coupé Technical
// Manual v3.0 (MGT plc, 1990) — bit layouts taken from its "Input/Output Ports"
// chapter (the port-249/250/251/252/248/254 register tables) and the four screen
// modes. Port numbering follows the manual's decimal addresses:
//
//	248 = 0xF8 CLUT / light pen
//	249 = 0xF9 STATUS (read) / LINE interrupt (write)
//	250 = 0xFA LMPR    251 = 0xFB HMPR    252 = 0xFC VMPR
//	254 = 0xFE keyboard (read) / BORDER (write)
//
// These tests drive register writes through the public port dispatch and assert
// the resulting paging / video / interrupt / palette state.

// ---------------------------------------------------------------------------
// LMPR — Low Memory Page Register (port 250 / 0xFA).
// Manual: lower 5 bits select the page mapped into section A ($0000); section B
// ($4000) follows at page+1. Bit 5 (RAM0): RAM replaces ROM0 in A. Bit 6 (ROM1):
// ROM1 replaces RAM in section D ($C000). Bit 7 (WPRAM): write-protect section A.
// ---------------------------------------------------------------------------

func TestLMPRSectionBFollowsA(t *testing.T) {
	m := markMem(NewMemory(emptyROMHalves()))
	// Manual: "section B always follows section A by one page."
	m.SetLMPR(lmprROM0Off | 4) // RAM page 4 in A → page 5 in B
	if got := m.Read(0x0000); got != (0x80 | 4) {
		t.Errorf("section A should be RAM page 4 marker, got %#02x", got)
	}
	if got := m.Read(0x4000); got != (0x80 | 5) {
		t.Errorf("section B should follow A as RAM page 5, got %#02x", got)
	}
}

func TestLMPRPageWrapAtTop(t *testing.T) {
	m := markMem(NewMemory(emptyROMHalves()))
	// Page is 5 bits: A=31 → B=page+1 wraps to page 0 (mask 0x1F).
	m.SetLMPR(lmprROM0Off | 31)
	if got := m.Read(0x0000); got != (0x80 | 31) {
		t.Errorf("section A page 31 marker = %#02x, want %#02x", got, 0x80|31)
	}
	if got := m.Read(0x4000); got != 0x80 { // page 32 & 0x1F = 0
		t.Errorf("section B should wrap to RAM page 0, got %#02x", got)
	}
}

func TestLMPRBitDecodeIsExclusive(t *testing.T) {
	// Manual bit positions: RAM0=bit5(0x20), ROM1=bit6(0x40), WPRAM=bit7(0x80).
	if lmprROM0Off != 0x20 || lmprROM1 != 0x40 || lmprWProt != 0x80 || pageMask != 0x1F {
		t.Fatalf("LMPR bit constants drifted from the manual: ROM0Off=%#02x ROM1=%#02x WProt=%#02x mask=%#02x",
			lmprROM0Off, lmprROM1, lmprWProt, pageMask)
	}
}

func TestLMPRWriteProtectOnlyAffectsSectionA(t *testing.T) {
	m := markMem(NewMemory(emptyROMHalves()))
	// WPRAM protects A; B must remain writable (manual: protection is "of the RAM
	// in section A of the CPU address map").
	m.SetLMPR(lmprROM0Off | lmprWProt) // A=page0 protected, B=page1 writable
	m.Write(0x0000, 0x42)
	if got := m.Read(0x0000); got != 0x80 {
		t.Errorf("write-protected A must ignore writes, got %#02x", got)
	}
	m.Write(0x4000, 0x42)
	if got := m.Read(0x4000); got != 0x42 {
		t.Errorf("section B must stay writable under WPRAM, got %#02x", got)
	}
}

func TestLMPRROM1InDDoesNotBlockSectionC(t *testing.T) {
	m := markMem(NewMemory(emptyROMHalves()))
	m.SetLMPR(lmprROM1)
	m.SetHMPR(3) // section C = RAM page 3
	if got := m.Read(0xC000); got != 0x11 {
		t.Errorf("section D should be ROM1 (0x11), got %#02x", got)
	}
	if got := m.Read(0x8000); got != (0x80 | 3) {
		t.Errorf("section C should be RAM page 3 (unaffected by ROM1), got %#02x", got)
	}
}

// ---------------------------------------------------------------------------
// HMPR — High Memory Page Register (port 251 / 0xFB).
// Manual: lower 5 bits select section C ($8000), D ($C000) follows at page+1.
// Bit 7 (MCNTRL/XMEM) routes sections C/D to external RAM. Bits 5/6 supply the
// extra CLUT-base address lines used in MODE 3 (BCD4 / BCD8).
// ---------------------------------------------------------------------------

func TestHMPRSectionDFollowsC(t *testing.T) {
	m := markMem(NewMemory(emptyROMHalves()))
	m.SetHMPR(6) // C=page6, D=page7
	if got := m.Read(0x8000); got != (0x80 | 6) {
		t.Errorf("section C should be RAM page 6, got %#02x", got)
	}
	if got := m.Read(0xC000); got != (0x80 | 7) {
		t.Errorf("section D should follow C as RAM page 7, got %#02x", got)
	}
}

func TestHMPRMCNTRLBitIsBit7(t *testing.T) {
	if hmprMCNTRL != 0x80 {
		t.Fatalf("HMPR MCNTRL bit = %#02x, manual says bit 7 (0x80)", hmprMCNTRL)
	}
}

// TestHMPRMode3ColourBase pins the mode-3 sub-CLUT base from HMPR bits 5/6
// (manual: MD3S0 = BCD4, MD3S1 = BCD8 of the colour look-up address, MODE 3
// only). The renderer takes base = (HMPR & 0x60) >> 3, giving CLUT entries
// {0,4,8,12} for the four HMPR bit-5/6 combinations.
func TestHMPRMode3ColourBase(t *testing.T) {
	cases := map[byte]byte{0x00: 0, 0x20: 4, 0x40: 8, 0x60: 12}
	for hmpr, wantBase := range cases {
		m, _ := NewFromROM(synthROM())
		m.Mem.SetVMPR(0x40) // MODE 3
		m.Mem.SetHMPR(hmpr)
		// Make each candidate base entry a unique colour, then render pixel value 0
		// (sub-entry 0 == clut[base]).
		for i := range m.clut {
			m.clut[i] = byte(i) // distinct master indices
		}
		writeDisplay(m, 0, 0x00) // all four 2bpp pixels = value 0 → sub-entry 0
		img := m.renderAll()
		if got := activeAt(img, 0, 0); got != PaletteColour(byte(wantBase)) {
			t.Errorf("HMPR=%#02x: MODE3 pixel-0 colour = %v, want clut[%d]=%v",
				hmpr, got, wantBase, PaletteColour(byte(wantBase)))
		}
	}
}

// ---------------------------------------------------------------------------
// VMPR — Video Memory Page Register (port 252 / 0xFC).
// Manual: lower 5 bits select the displayed page (0-31). Bits 5/6 (MDE0/MDE1)
// select the mode: 00=MODE1, 01=MODE2, 10=MODE3, 11=MODE4. Bit 7 reads MIDI-IN
// (RXMIDI) and is not written back. VMPR does NOT change the CPU memory map.
// ---------------------------------------------------------------------------

func TestVMPRModeBitCombinations(t *testing.T) {
	// Exact MDE1:MDE0 → mode table from the manual.
	cases := []struct {
		bits byte
		mode int
	}{
		{0x00, 1}, // 00
		{0x20, 2}, // 01 (MDE0)
		{0x40, 3}, // 10 (MDE1)
		{0x60, 4}, // 11
	}
	m := NewMemory(emptyROMHalves())
	for _, c := range cases {
		m.SetVMPR(c.bits)
		if got := m.ScreenMode(); got != c.mode {
			t.Errorf("VMPR mode bits %#02x → mode %d, want %d", c.bits, got, c.mode)
		}
	}
}

func TestVMPRBit7IsReadOnlyRXMIDI(t *testing.T) {
	m := NewMemory(emptyROMHalves())
	// Writing bit 7 must not be stored (manual: bit 7 is the MIDI-IN input).
	m.SetVMPR(0x80 | 0x05)
	if m.ScreenMode() != 1 || m.VisibleScreenPage() != 5 {
		t.Errorf("VMPR bit7 must be dropped on write: mode=%d page=%d", m.ScreenMode(), m.VisibleScreenPage())
	}
	// ...but it is always set in the read-back.
	if m.VMPR()&0x80 == 0 {
		t.Errorf("VMPR read-back should OR in RXMIDI bit 7, got %#02x", m.VMPR())
	}
}

func TestVMPRDoesNotChangeCPUMap(t *testing.T) {
	m := markMem(NewMemory(emptyROMHalves()))
	before := [4]byte{m.Read(0x0000), m.Read(0x4000), m.Read(0x8000), m.Read(0xC000)}
	m.SetVMPR(0x60 | 0x0A) // mode 4, page 10
	after := [4]byte{m.Read(0x0000), m.Read(0x4000), m.Read(0x8000), m.Read(0xC000)}
	if before != after {
		t.Errorf("VMPR must not alter the CPU memory map: before %v after %v", before, after)
	}
}

func TestVMPRModes34ForceEvenPage(t *testing.T) {
	m := NewMemory(emptyROMHalves())
	// Manual: modes 3/4 use 24 KB spanning two pages, "the principal of even to
	// odd always applies" → the displayed page is forced even.
	for _, modeBits := range []byte{0x40, 0x60} { // MODE 3, MODE 4
		m.SetVMPR(modeBits | 0x07) // odd page 7
		if got := m.VisibleScreenPage(); got != 6 {
			t.Errorf("mode bits %#02x page 7 should force even to 6, got %d", modeBits, got)
		}
		m.SetVMPR(modeBits | 0x08) // even page 8 unchanged
		if got := m.VisibleScreenPage(); got != 8 {
			t.Errorf("mode bits %#02x page 8 should stay 8, got %d", modeBits, got)
		}
	}
	// Modes 1/2: the page is used as-is (single 16 KB page).
	for _, modeBits := range []byte{0x00, 0x20} {
		m.SetVMPR(modeBits | 0x07)
		if got := m.VisibleScreenPage(); got != 7 {
			t.Errorf("mode bits %#02x page 7 should stay 7, got %d", modeBits, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Paging port dispatch (read-back through the I/O ports).
// ---------------------------------------------------------------------------

func TestPagingPortReadbackThroughPorts(t *testing.T) {
	m := newTestMachine(t)
	m.WritePort(0x00FA, 0x2A) // LMPR
	m.WritePort(0x00FB, 0x83) // HMPR (MCNTRL set)
	m.WritePort(0x00FC, 0x55) // VMPR
	if v, _ := m.ReadPort(0x00FA); v != 0x2A {
		t.Errorf("LMPR port read-back = %#02x, want 0x2A", v)
	}
	if v, _ := m.ReadPort(0x00FB); v != 0x83 {
		t.Errorf("HMPR port read-back = %#02x, want 0x83", v)
	}
	// VMPR stores only bits 0-6 (0x55), reads OR in RXMIDI (bit 7) → 0xD5.
	if v, _ := m.ReadPort(0x00FC); v != 0xD5 {
		t.Errorf("VMPR port read-back = %#02x, want 0xD5", v)
	}
}

// ---------------------------------------------------------------------------
// CLUT — 16-entry colour look-up table (port 248 / 0xF8, index = high byte & 15).
// Each entry is a 7-bit master-palette index; bit 7 is masked off.
// ---------------------------------------------------------------------------

func TestCLUTAllSixteenEntries(t *testing.T) {
	m := newTestMachine(t)
	for idx := 0; idx < 16; idx++ {
		// High byte carries the CLUT index; value carries the 7-bit colour (+ a
		// stray bit 7 that must be masked).
		addr := uint16(idx)<<8 | portCLUT
		m.WritePort(addr, byte(0x80|idx))
		if got := m.CLUT()[idx]; got != byte(idx) {
			t.Errorf("CLUT[%d] = %#02x, want %#02x (bit 7 masked)", idx, got, idx)
		}
	}
}

func TestCLUTIndexWrapsHighNibble(t *testing.T) {
	m := newTestMachine(t)
	// Only the low nibble of the high byte selects the entry (index = high & 0x0F).
	m.WritePort(uint16(0x1A)<<8|portCLUT, 0x33) // 0x1A & 0x0F = 10
	if got := m.CLUT()[10]; got != 0x33 {
		t.Errorf("CLUT index high-byte 0x1A should target entry 10, got CLUT[10]=%#02x", got)
	}
}

// ---------------------------------------------------------------------------
// Master palette colour encoding (port 248 colour bits).
// Manual port-248 bit table:
//
//	bit0 BLU0  bit1 RED0  bit2 GRN0  bit3 BRIGHT
//	bit4 BLU1  bit5 RED1  bit6 GRN1
//
// Each channel = MSB·4 + LSB·2 + BRIGHT·1 (0-7), scaled 0-255.
// ---------------------------------------------------------------------------

func TestPaletteChannelBitWeights(t *testing.T) {
	lvl := func(c3 int) uint8 { return uint8(c3 * 255 / 7) }
	cases := []struct {
		idx     byte
		r, g, b uint8
		desc    string
	}{
		{0x00, 0, 0, 0, "all bits clear → black"},
		{0x7F, 255, 255, 255, "all bits set → white (7/7 each)"},
		{0x02, lvl(2), 0, 0, "RED0 only → red 2/7"},         // bit1
		{0x20, lvl(4), 0, 0, "RED1 only → red 4/7"},         // bit5
		{0x04, 0, lvl(2), 0, "GRN0 only → green 2/7"},       // bit2
		{0x40, 0, lvl(4), 0, "GRN1 only → green 4/7"},       // bit6
		{0x01, 0, 0, lvl(2), "BLU0 only → blue 2/7"},        // bit0
		{0x10, 0, 0, lvl(4), "BLU1 only → blue 4/7"},        // bit4
		{0x08, lvl(1), lvl(1), lvl(1), "BRIGHT → grey 1/7"}, // bit3, all channels
	}
	for _, c := range cases {
		got := PaletteColour(c.idx)
		want := color.RGBA{R: c.r, G: c.g, B: c.b, A: 0xFF}
		if got != want {
			t.Errorf("%s: PaletteColour(%#02x) = %v, want %v", c.desc, c.idx, got, want)
		}
	}
}

func TestPaletteFullRedGreenBlue(t *testing.T) {
	// BRIGHT (bit 3) is shared across all three channels, so the maximum 7/7 in
	// one channel necessarily lifts the others by 1/7. The "saturated" colours are
	// therefore MSB+LSB only (6/7) with the other channels at 0.
	bright := uint8(255 * 1 / 7)
	full := uint8(255 * 6 / 7)
	// RED1+RED0 = bits 5,1 = 0x22 → red 6/7, no green/blue.
	if got := PaletteColour(0x22); got != (color.RGBA{full, 0, 0, 255}) {
		t.Errorf("saturated red index 0x22 = %v, want red 6/7", got)
	}
	// GRN1+GRN0 = bits 6,2 = 0x44 → green 6/7.
	if got := PaletteColour(0x44); got != (color.RGBA{0, full, 0, 255}) {
		t.Errorf("saturated green index 0x44 = %v, want green 6/7", got)
	}
	// BLU1+BLU0 = bits 4,0 = 0x11 → blue 6/7.
	if got := PaletteColour(0x11); got != (color.RGBA{0, 0, full, 255}) {
		t.Errorf("saturated blue index 0x11 = %v, want blue 6/7", got)
	}
	// Adding BRIGHT to the red index lifts ALL channels — proving BRIGHT is shared
	// (bits 5,1,3 = 0x2A → red 7/7 but green/blue 1/7).
	if got := PaletteColour(0x2A); got != (color.RGBA{255, bright, bright, 255}) {
		t.Errorf("red+BRIGHT index 0x2A = %v, want red 7/7 + grey BRIGHT on g/b", got)
	}
}

// ---------------------------------------------------------------------------
// BORDER + SOFF (port 254 / 0xFE write).
// Manual: colour from bits 0-2 plus bit 5 (the 4th colour bit); bit 3 MIC,
// bit 4 BEEP, bit 7 SOFF (screen off — modes 3/4 only, also removes contention).
// ---------------------------------------------------------------------------

func TestBorderColourThroughCLUT(t *testing.T) {
	m := newTestMachine(t)
	// GRN1+GRN0 (bits 6,2) = master index 0x44 → pure green 6/7, no BRIGHT so
	// red/blue stay 0.
	m.WritePort(uint16(7)<<8|portCLUT, 0x44) // CLUT[7] = green 6/7
	m.WritePort(0x00FE, 0x07)                // border colour index 7
	want := PaletteColour(0x44)
	if got := m.borderColour(); got != want {
		t.Errorf("border colour 7 should resolve to CLUT[7]=%v, got %v", want, got)
	}
}

func TestBorderBit5IsFourthColourBit(t *testing.T) {
	// Manual: the border colour is a 4-bit CLUT index built from BORDER bits 0-2
	// and bit 5. Bit 3 (MIC) and bit 4 (BEEP) are NOT colour bits.
	if got := borderColourIndex(0x18); got != 0 { // MIC+BEEP, no colour
		t.Errorf("MIC+BEEP must not contribute colour, got index %d", got)
	}
	if got := borderColourIndex(0x25); got != 0x0D { // bit5 + 0b101
		t.Errorf("border 0x25 → index %#x, want 0x0D", got)
	}
}

func TestSOFFReadsBackOnKeyboardPort(t *testing.T) {
	m := newTestMachine(t)
	m.WritePort(0x00FE, borderSOFF) // SOFF set
	v, _ := m.ReadPort(0xFFFE)
	if v&0x80 == 0 {
		t.Errorf("SOFF (bit 7) should read high on the keyboard port, got %#02x", v)
	}
	m.WritePort(0x00FE, 0x00) // SOFF clear
	v, _ = m.ReadPort(0xFFFE)
	if v&0x80 != 0 {
		t.Errorf("SOFF clear should read low on the keyboard port, got %#02x", v)
	}
}

func TestSOFFBlanksMode3Only(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.clut[1] = 0x7F // white
	// MODE 3: a set pixel would be visible, but SOFF blanks it.
	m.Mem.SetVMPR(0x40)
	m.Mem.SetHMPR(0x00)
	for i := range m.clut {
		m.clut[i] = 0x7F // every sub-entry white
	}
	writeDisplay(m, 0, 0xFF)
	m.border = borderSOFF
	if got := activeAt(m.renderAll(), 0, 0); got != (color.RGBA{0, 0, 0, 255}) {
		t.Errorf("SOFF should blank MODE 3 to black, got %v", got)
	}
	// MODE 2 must NOT be blanked by SOFF (manual: only modes 3 and 4).
	m.Mem.SetVMPR(0x20)
	m.clut[0] = 0
	m.clut[1] = 0x7F
	writeDisplay(m, 0, 0x80)                 // pixel 0 ink
	writeDisplay(m, mode2AttrOffset+0, 0x01) // ink=1 paper=0
	if got := activeAt(m.renderAll(), 0, 0); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("SOFF must not blank MODE 2, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// STATUS register (port 249 / 0xF9 read).
// Manual: bit 0 LINE int (active low), bit 3 FRAME int (active low); bits 5-7
// carry keyboard matrix lines K6/K7/K8. Read it inside / outside the interrupt
// windows and assert the active-low bits.
// ---------------------------------------------------------------------------

func TestStatusBitPositionsMatchManual(t *testing.T) {
	if statusLine != 0x01 {
		t.Errorf("LINE int status bit = %#02x, manual says bit 0 (0x01)", statusLine)
	}
	if statusFrame != 0x08 {
		t.Errorf("FRAME int status bit = %#02x, manual says bit 3 (0x08)", statusFrame)
	}
	if statusKeyMask != 0xE0 {
		t.Errorf("status keyboard mask = %#02x, manual says bits 5-7 (0xE0)", statusKeyMask)
	}
}

func TestStatusFrameInterruptActiveLow(t *testing.T) {
	m := newTestMachine(t)
	// Inside the frame-INT window: FRAME bit is low (pending).
	setFrameRel(m, samFrameIntTstate+10)
	if v, _ := m.ReadPort(0x00F9); v&statusFrame != 0 {
		t.Errorf("inside frame-INT window FRAME bit should be low, got %#02x", v)
	}
	// Outside the window: FRAME bit is high (inactive).
	setFrameRel(m, samFrameIntTstate+samIntActiveCycles+100)
	if v, _ := m.ReadPort(0x00F9); v&statusFrame == 0 {
		t.Errorf("outside frame-INT window FRAME bit should be high, got %#02x", v)
	}
}

func TestStatusBothInterruptsIndependent(t *testing.T) {
	m := newTestMachine(t)
	m.setLineInterrupt(50) // enable line INT at line 50
	lineT := uint64((50 + 68) * 384)
	// Position so we are inside BOTH windows is unlikely; assert each separately.
	setFrameRel(m, lineT+5)
	s := m.statusByte()
	if s&statusLine != 0 {
		t.Errorf("inside line window LINE bit should be low, got %#02x", s)
	}
	if s&statusFrame == 0 {
		// Frame window is elsewhere, so FRAME must be high here.
		t.Errorf("FRAME bit should be high outside its window, got %#02x", s)
	}
	// Upper status bits (keyboard) must be untouched by the interrupt bits.
	if s&^statusIntMask != s&statusKeyMask {
		t.Errorf("status interrupt bits must stay within mask 0x1F, got %#02x", s)
	}
}

func TestStatusUnusedInterruptBitsHigh(t *testing.T) {
	m := newTestMachine(t)
	// Far from any interrupt window all five interrupt-region bits read high.
	setFrameRel(m, samFrameIntTstate/2)
	m.setLineInterrupt(0xFF) // line INT disabled
	if got := m.statusByte() & statusIntMask; got != statusIntMask {
		t.Errorf("idle interrupt bits = %#02x, want all-high %#02x", got, statusIntMask)
	}
}

// ---------------------------------------------------------------------------
// LINE interrupt register (port 249 / 0xF9 write).
// Manual: the write programs the line on which the maskable INT fires (lines
// 0-191); the fire point is the end of the scan line before the target. Values
// >= 192 disable the line interrupt.
// ---------------------------------------------------------------------------

func TestLineInterruptProgramming(t *testing.T) {
	m := newTestMachine(t)
	for _, line := range []byte{0, 1, 95, 191} {
		m.WritePort(0x00F9, line)
		want := uint64((int(line) + 68) * 384)
		if m.CPU.LineIntOffsetTstates != want {
			t.Errorf("LINE=%d: offset = %d, want %d", line, m.CPU.LineIntOffsetTstates, want)
		}
	}
}

func TestLineInterruptDisabledAtOrAbove192(t *testing.T) {
	m := newTestMachine(t)
	for _, line := range []byte{192, 200, 255} {
		m.WritePort(0x00F9, line)
		if m.CPU.LineIntOffsetTstates != 0 {
			t.Errorf("LINE=%d should disable the line INT (offset 0), got %d", line, m.CPU.LineIntOffsetTstates)
		}
		setFrameRel(m, uint64((int(line)+68)*384)+5)
		if m.statusByte()&statusLine == 0 {
			t.Errorf("LINE=%d disabled: LINE status bit must stay high", line)
		}
	}
}

func TestLineInterruptStatusEdges(t *testing.T) {
	m := newTestMachine(t)
	m.setLineInterrupt(120)
	lt := uint64((120 + 68) * 384)
	// Just before the window → high; inside → low; just after → high.
	setFrameRel(m, lt-1)
	if m.statusByte()&statusLine == 0 {
		t.Errorf("before line window LINE bit should be high")
	}
	setFrameRel(m, lt)
	if m.statusByte()&statusLine != 0 {
		t.Errorf("at line window start LINE bit should be low")
	}
	setFrameRel(m, lt+samIntActiveCycles)
	if m.statusByte()&statusLine == 0 {
		t.Errorf("after the active-cycle window LINE bit should be high again")
	}
}

// ---------------------------------------------------------------------------
// Keyboard (port 254 / 0xFE read = K1-K5; port 249 / 0xF9 read = K6-K8).
// Manual: the high address byte selects matrix lines (active low). Row 8
// (CONTROL + cursor keys) is read when the whole high byte is 0xFF.
// ---------------------------------------------------------------------------

func TestKeyboardLowColumnsOnPort254(t *testing.T) {
	m := newTestMachine(t)
	m.Kbd.SetKey(2, 4, true) // row 2 col 4 (K5, low columns)
	addr := uint16(^byte(1<<2))<<8 | portKeyboard
	v, _ := m.ReadPort(addr)
	if v&0x10 != 0 { // col 4 = bit 4
		t.Errorf("row 2 col 4 should read low on port 254, got %#02x", v)
	}
	// Bits 5-7 of port 254 must NOT carry keyboard columns (they are EAR/SOFF).
	// Set a high-column key and confirm it does not appear on port 254.
	m.Kbd.SetKey(2, 6, true)
	v, _ = m.ReadPort(addr)
	if v&0x40 == 0 {
		t.Errorf("col 6 must NOT appear on port 254 keyboard read, got %#02x", v)
	}
}

func TestKeyboardHighColumnsOnPort249(t *testing.T) {
	m := newTestMachine(t)
	m.Kbd.SetKey(4, 5, true) // K6
	m.Kbd.SetKey(4, 7, true) // K8
	addr := uint16(^byte(1<<4))<<8 | portStatus
	v, _ := m.ReadPort(addr)
	if v&0x20 != 0 {
		t.Errorf("row 4 col 5 (K6) should read low on port 249, got %#02x", v)
	}
	if v&0x80 != 0 {
		t.Errorf("row 4 col 7 (K8) should read low on port 249, got %#02x", v)
	}
	// The low interrupt bits of port 249 must be intact (no key collapses them).
	if v&statusLine == 0 {
		t.Errorf("port 249 LINE bit should be high with no line INT, got %#02x", v)
	}
}

func TestKeyboardEARIdleHigh(t *testing.T) {
	m := newTestMachine(t)
	v, _ := m.ReadPort(0xFEFE) // select row 0
	if v&keyboardEAR == 0 {
		t.Errorf("EAR (tape-in) should idle high (bit 6), got %#02x", v)
	}
}

func TestKeyboardMultipleRowsANDed(t *testing.T) {
	m := newTestMachine(t)
	// Manual: selecting several lines ANDs their columns. Press the same column
	// on two rows and select both.
	m.Kbd.SetKey(0, 0, true)
	m.Kbd.SetKey(1, 0, true)
	addr := uint16(^byte((1<<0)|(1<<1)))<<8 | portKeyboard
	v, _ := m.ReadPort(addr)
	if v&0x01 != 0 {
		t.Errorf("col 0 pressed on both selected rows should read low, got %#02x", v)
	}
}

func TestKeyboardRow8OnAllHigh(t *testing.T) {
	m := newTestMachine(t)
	// Row 8 (CONTROL + cursors) is selected only when the whole high byte is 0xFF.
	m.Kbd.SetKey(8, 3, true) // e.g. a cursor key
	v, _ := m.ReadPort(0xFFFE)
	if v&0x08 != 0 {
		t.Errorf("row 8 col 3 should read low when high byte is 0xFF, got %#02x", v)
	}
	// A different selection must not surface row 8.
	v, _ = m.ReadPort(uint16(^byte(1<<0))<<8 | portKeyboard)
	if v&0x08 == 0 {
		t.Errorf("row 8 must not appear when high byte selects another row, got %#02x", v)
	}
}

// ---------------------------------------------------------------------------
// Per-mode byte → pixel layout (the four documented screen modes).
// ---------------------------------------------------------------------------

// TestMode1ByteLayout pins the ZX-compatible MODE 1: bit-7-first 1bpp pixel data
// with 8×8 attribute cells (manual: "32 cells x 24 lines"), ink in attr bits 0-2
// + bright bit 6, paper in bits 3-5 + bright. Page-0 is the display.
func TestMode1ByteLayout(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.Mem.SetVMPR(0x00)            // MODE 1, page 0
	m.clut[0] = 0                  // paper black
	m.clut[1] = 0x7F               // ink white
	writeDisplay(m, 0, 0b10100000) // line0 cell0: pixels 0 and 2 = ink
	writeDisplay(m, 6144, 0x01)    // attr ink=1 paper=0
	img := m.renderAll()
	white := color.RGBA{255, 255, 255, 255}
	black := color.RGBA{0, 0, 0, 255}
	// Each lo-res pixel is doubled to the 512-wide buffer.
	if got := activeAt(img, 0, 0); got != white {
		t.Errorf("MODE1 pixel 0 (bit7) should be ink, got %v", got)
	}
	if got := activeAt(img, 2, 0); got != black {
		t.Errorf("MODE1 pixel 1 (bit6) should be paper, got %v", got)
	}
	if got := activeAt(img, 4, 0); got != white {
		t.Errorf("MODE1 pixel 2 (bit5) should be ink, got %v", got)
	}
}

// TestMode1BrightAttribute pins the ink-bright bit: attr bit 6 lifts ink to the
// 8-15 CLUT range (manual MODE 1 = "16 colours" via the 4-bit attribute index).
func TestMode1BrightAttribute(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.Mem.SetVMPR(0x00)
	for i := range m.clut {
		m.clut[i] = byte(i) // distinct colours so the ink index is observable
	}
	writeDisplay(m, 0, 0x80) // pixel 0 ink
	// attr ink low bits = 1, bright bit (0x40) set → ink CLUT index 9.
	writeDisplay(m, 6144, 0x40|0x01)
	if got := activeAt(m.renderAll(), 0, 0); got != PaletteColour(9) {
		t.Errorf("MODE1 bright ink should select CLUT[9], got %v", got)
	}
}

// TestMode2ByteLayout: MODE 2 is 32 cells × 192 lines — linear 1bpp data with a
// per-LINE attribute block (manual: separate attribute area). The display data
// is at (line<<5)+cell; attributes start at offset 0x2000.
func TestMode2ByteLayout(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.Mem.SetVMPR(0x20) // MODE 2
	m.clut[0] = 0
	m.clut[1] = 0x7F
	// Line 3 cell 2: a distinct pixel pattern + per-line attribute.
	writeDisplay(m, (3<<5)+2, 0x80)                 // pixel 0 ink
	writeDisplay(m, mode2AttrOffset+(3<<5)+2, 0x01) // ink=1 paper=0
	img := m.renderAll()
	x := (2*8 + 0) * 2 // cell 2 pixel 0, doubled
	if got := activeAt(img, x, 3); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("MODE2 line3 cell2 pixel0 should be ink, got %v", got)
	}
}

// TestMode2PerLineAttributes proves MODE 2 attributes are per scan line, not per
// 8-line character row (this is the key MODE 1 vs MODE 2 difference).
func TestMode2PerLineAttributes(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.Mem.SetVMPR(0x20)
	m.clut[0] = 0    // black
	m.clut[1] = 0x7F // white
	m.clut[2] = 0x2A // red (paper alt)
	// Lines 0 and 1, cell 0: same pixel data, different per-line attributes.
	writeDisplay(m, (0<<5)+0, 0x00)                 // line0 all paper
	writeDisplay(m, (1<<5)+0, 0x00)                 // line1 all paper
	writeDisplay(m, mode2AttrOffset+(0<<5)+0, 0x08) // line0 paper=1 (white)
	writeDisplay(m, mode2AttrOffset+(1<<5)+0, 0x10) // line1 paper=2 (red)
	img := m.renderAll()
	if got := activeAt(img, 0, 0); got != PaletteColour(0x7F) {
		t.Errorf("MODE2 line0 paper should be white (per-line attr), got %v", got)
	}
	if got := activeAt(img, 0, 1); got != PaletteColour(0x2A) {
		t.Errorf("MODE2 line1 paper should be red (different per-line attr), got %v", got)
	}
}

// TestMode3ByteLayout: MODE 3 is 512×192 4-colour, 2bpp MSB-pair first, 128
// bytes/line, no horizontal doubling (manual: "512 pixels x 192 lines").
func TestMode3ByteLayout(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.Mem.SetVMPR(0x40) // MODE 3
	m.Mem.SetHMPR(0x00) // base 0
	// Sub-CLUT: entry0,3 direct; 1,2 swapped (an ASIC quirk these tests pin).
	m.clut[0] = 0x00                  // value 0
	m.clut[1] = 0x2A                  // → pixel value 2 (swapped)
	m.clut[2] = 0x4C                  // → pixel value 1 (swapped)
	m.clut[3] = 0x7F                  // value 3
	writeDisplay(m, 0, 0b00_01_10_11) // pixels 0,1,2,3 = values 0,1,2,3
	img := m.renderAll()
	want := []color.RGBA{
		PaletteColour(0x00), // value 0 → clut[0]
		PaletteColour(0x4C), // value 1 → clut[2] (swap)
		PaletteColour(0x2A), // value 2 → clut[1] (swap)
		PaletteColour(0x7F), // value 3 → clut[3]
	}
	for px := 0; px < 4; px++ {
		if got := activeAt(img, px, 0); got != want[px] {
			t.Errorf("MODE3 pixel %d = %v, want %v", px, got, want[px])
		}
	}
}

func TestMode3NoHorizontalDoubling(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.Mem.SetVMPR(0x40)
	m.Mem.SetHMPR(0x00)
	m.clut[0] = 0x00
	m.clut[3] = 0x7F
	// byte 0 = value 0,0,0,3 → pixel 3 differs from pixel 2 (proves 1:1 mapping).
	writeDisplay(m, 0, 0b00_00_00_11)
	img := m.renderAll()
	if activeAt(img, 2, 0) == activeAt(img, 3, 0) {
		t.Errorf("MODE3 adjacent pixels 2 and 3 must differ (no doubling)")
	}
}

// TestMode4ByteLayout: MODE 4 is 256×192 16-colour — two 4-bit CLUT-indexed
// pixels per byte, high nibble first, each doubled (manual: "256 pixels...16
// colours").
func TestMode4ByteLayout(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.Mem.SetVMPR(0x60) // MODE 4
	for i := range m.clut {
		m.clut[i] = byte(i) // CLUT[n] = master index n
	}
	writeDisplay(m, 0, 0x3C) // hi nibble 3, lo nibble 12
	img := m.renderAll()
	// hi nibble → pixels 0,1 (doubled); lo nibble → pixels 2,3.
	if got := activeAt(img, 0, 0); got != PaletteColour(3) {
		t.Errorf("MODE4 hi-nibble pixel = %v, want CLUT[3]", got)
	}
	if got := activeAt(img, 1, 0); got != PaletteColour(3) {
		t.Errorf("MODE4 hi-nibble doubled pixel = %v, want CLUT[3]", got)
	}
	if got := activeAt(img, 2, 0); got != PaletteColour(12) {
		t.Errorf("MODE4 lo-nibble pixel = %v, want CLUT[12]", got)
	}
}

// TestMode4UsesAllSixteenCLUTEntries walks every nibble value 0-15 and confirms
// it indexes the matching CLUT entry (manual: MODE 4 = 16 colours from the CLUT).
func TestMode4UsesAllSixteenCLUTEntries(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.Mem.SetVMPR(0x60)
	for i := range m.clut {
		m.clut[i] = byte(i * 8) // spread master indices so each is distinct
	}
	for nib := 0; nib < 16; nib++ {
		writeDisplay(m, nib, byte(nib<<4)) // hi nibble = nib, lo = 0
		img := m.renderAll()
		if got := activeAt(img, nib*4, 0); got != PaletteColour(byte(nib*8)) {
			t.Errorf("MODE4 nibble %d should select CLUT[%d], got %v", nib, nib, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Reset state (manual: power-on map ROM0 / RAM1 / RAM0 / RAM1, screen on).
// ---------------------------------------------------------------------------

func TestResetPagingRegistersZero(t *testing.T) {
	m := markMem(NewMemory(emptyROMHalves()))
	m.SetLMPR(0x3F)
	m.SetHMPR(0x9F)
	m.SetVMPR(0x60)
	m.SetLEPR(0x12)
	m.SetHEPR(0x34)
	m.Reset()
	if m.LMPR() != 0 || m.HMPR() != 0 || (m.VMPR()&0x7F) != 0 || m.LEPR() != 0 || m.HEPR() != 0 {
		t.Errorf("reset must zero paging regs: L=%#02x H=%#02x V=%#02x LE=%#02x HE=%#02x",
			m.LMPR(), m.HMPR(), m.VMPR()&0x7F, m.LEPR(), m.HEPR())
	}
	// After reset ROM0 is back in section A.
	if got := m.Read(0x0000); got != 0xF3 {
		t.Errorf("after reset section A should be ROM0 (0xF3), got %#02x", got)
	}
}
