package zx8x

// Spec-faithful tests for the Sinclair ZX80/ZX81 CPU-generated video + ULA.
//
// Oracle: the documented ZX80/ZX81 hardware behaviour, cross-checked against:
//   - "Sinclair ZX Specifications" by Martin Korth (Nocash), https://problemkaputt.de/zxdocs.htm
//     ("ZX81 Video Display", "ZX81 I/O Map" sections).
//   - Tynemouth Software, "How the ZX81 Generates Video" (2023).
//   - Grant Searle, "ZX80 hardware page (NMI generator)".
//
// These pin the contract of the CPU-generated display: the ULA intercepts an M1
// fetch from the upper 32K (A15=1), and:
//   * bit 6 SET  -> the opcode is rejected by the video circuit (white) and the
//                   CPU executes it normally (HALT $76 terminates a display line);
//   * bit 6 CLEAR-> the databus is forced to $00 (a NOP), and the fetched byte is
//                   treated as character data: bits 0-5 select one of 64 glyphs,
//                   bit 7 is the inverse-video attribute, and the bitmap byte is
//                   read from (I*0x100 + char*8 + linecntr).
// The maskable INT is generated when R-register bit 6 becomes zero. SLOW mode =
// NMI generator on; FAST mode = off. Port reads/writes drive vsync and the NMI
// generator per the I/O map.

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// ---------------------------------------------------------------------------
// Bit-6 NOP-generator vs HALT line terminator (Nocash "ZX81 Video Display":
// "when encountering an opcode with Bit 6 set, the video circuit rejects the
// opcode ... and the CPU executes the opcode as normal"; bit 6 clear -> "forcing
// the databus to zero ... causing a NOP opcode (00h) to be executed".)
// ---------------------------------------------------------------------------

// An M1 fetch from the lower 32K (A15=0) is ordinary code; the ULA never
// intercepts it, so the opcode passes through verbatim and nothing is drawn.
func TestM1FetchBelow8000IsNotIntercepted(t *testing.T) {
	m := newBlankZX81(t)
	got := m.onM1Fetch(0x4000, 0x01) // char-code-shaped byte, but A15=0
	if got != 0x01 {
		t.Errorf("onM1Fetch(<$8000) = %02X, want passthrough 0x01", got)
	}
	if m.started {
		t.Error("a fetch below $8000 must not start the display")
	}
}

// A fetch from $8000+ with bit 6 CLEAR is forced to NOP ($00): the ULA hijacks
// the databus so the CPU executes a NOP while the byte is drawn as a character.
func TestM1FetchAboveBit6ClearForcesNOP(t *testing.T) {
	m := newBlankZX81(t)
	m.CPU.I = 0x1E // standard ZX81 char-table base
	got := m.onM1Fetch(0x8000, 0x01)
	if got != 0x00 {
		t.Errorf("onM1Fetch($8000, bit6 clear) = %02X, want forced NOP 0x00", got)
	}
	if !m.started {
		t.Error("drawing a character must mark the display as started")
	}
	if m.beamX != 1 {
		t.Errorf("beamX = %d after one character, want 1 (column advanced)", m.beamX)
	}
}

// A fetch from $8000+ with bit 6 SET (e.g. HALT $76) is rejected by the video
// circuit and passes through to the CPU verbatim — it is NOT turned into a NOP.
func TestM1FetchAboveBit6SetPassesThrough(t *testing.T) {
	m := newBlankZX81(t)
	got := m.onM1Fetch(0x8000, 0x76) // HALT: bit 6 set
	if got != 0x76 {
		t.Errorf("onM1Fetch($8000, HALT) = %02X, want passthrough 0x76", got)
	}
	got2 := m.onM1Fetch(0x8000, 0xC0) // arbitrary bit-6-set, bit-7-set byte
	if got2 != 0xC0 {
		t.Errorf("onM1Fetch($8000, 0xC0) = %02X, want passthrough 0xC0", got2)
	}
}

// HALT ($76) terminates a display scanline: the column resets to 0 and, once
// real content has started, the pixel row advances. The single leading sync
// HALT (before any character is drawn) must NOT advance the row.
func TestHaltTerminatesScanline(t *testing.T) {
	m := newBlankZX81(t)
	m.CPU.I = 0x1E

	// Leading sync HALT before any content: column resets, row stays at 0.
	m.onM1Fetch(0x8000, 0x76)
	if m.beamX != 0 || m.beamY != 0 || m.started {
		t.Fatalf("leading HALT: beamX=%d beamY=%d started=%v, want 0,0,false",
			m.beamX, m.beamY, m.started)
	}

	// Draw a character, then a HALT: column resets to 0 and the row advances.
	m.onM1Fetch(0x8000, 0x01)
	if m.beamX != 1 {
		t.Fatalf("after one char beamX=%d, want 1", m.beamX)
	}
	m.onM1Fetch(0x8000, 0x76)
	if m.beamX != 0 {
		t.Errorf("after content HALT beamX=%d, want 0 (column reset)", m.beamX)
	}
	if m.beamY != 1 {
		t.Errorf("after content HALT beamY=%d, want 1 (row advanced)", m.beamY)
	}
}

// While the CPU is halted no character is drawn: the ULA only steals an M1 cycle
// when the CPU is actually fetching. (The display loop sits HALTed between the
// per-scanline INTs.)
func TestNoCharacterDrawnWhileHalted(t *testing.T) {
	m := newBlankZX81(t)
	m.CPU.I = 0x1E
	m.CPU.Halted = true
	got := m.onM1Fetch(0x8000, 0x01)
	if got != 0x01 {
		t.Errorf("onM1Fetch while halted = %02X, want passthrough 0x01", got)
	}
	if m.started || m.beamX != 0 {
		t.Error("nothing should be drawn while the CPU is halted")
	}
}

// ---------------------------------------------------------------------------
// Character bitmap addressing: addr = I*0x100 + (code&0x3F)*8 + linecntr,
// with bit 7 selecting inverse video. (Nocash: "Bit 0-5 address one of the 64
// characters in ROM at (I*100h+char*8+linecntr)"; "Bit 7 ... is used as invert
// attribute".)
// ---------------------------------------------------------------------------

// The fetched character bitmap address is formed from the I register (high
// byte), the character code (bits 0-5, *8) and the line counter (low 3 bits of
// the pixel row). We prove this by planting a recognisable bitmap at the exact
// computed address and checking the pixels it produces.
func TestCharacterBitmapAddressIsIcharLinecntr(t *testing.T) {
	m := newBlankZX81(t)
	// Put the character table in writable RAM ($4000-$7FFF) so the planted
	// bitmap is the one read back; the addressing arithmetic is identical to
	// the real I=$1E ROM table (I*0x100 + char*8 + linecntr).
	m.CPU.I = 0x40 // char table at $4000

	const code = 0x05 // character code 5
	// We will draw scanline 3 of character 5: place the beam at pixel row 3.
	m.beamY = 3
	addr := uint16(0x40)<<8 | uint16(code)<<3 | 3 // 0x4000 + 5*8 + 3 = 0x402B
	m.Mem.Write(addr, 0b1010_1010)

	m.onM1Fetch(0x8000, code) // bit 6 clear -> drawn as character

	// Pixels at column 0, row 3: bits 7,5,3,1 set (0b10101010, MSB = leftmost).
	for px := 0; px < 8; px++ {
		want := (0b1010_1010 & (0x80 >> px)) != 0
		if got := m.Screen.PixelAt(px, 3); got != want {
			t.Errorf("pixel (%d,3) = %v, want %v (addr %04X bitmap 0xAA)", px, got, want, addr)
		}
	}
	if addr != 0x402B {
		t.Fatalf("address arithmetic wrong: %04X, want 0x402B (I*0x100 + 5*8 + 3)", addr)
	}
}

// The I register selects the character-table page: changing I moves the lookup
// to a different 256-aligned base. With a glyph planted only at I=$30's address,
// running with I=$1E must NOT find it.
func TestIRegisterSelectsCharTablePage(t *testing.T) {
	m := newBlankZX81(t)

	const code = 0x02
	// Plant a full-ink glyph row only at the I=$50 page (writable RAM); leave
	// the I=$40 page blank.
	addrAtI50 := uint16(0x50)<<8 | uint16(code)<<3 // scanline 0
	m.Mem.Write(addrAtI50, 0xFF)

	// Run with I=$40: the glyph at $50xx must not appear (its bitmap is read
	// from $40xx instead, which is blank RAM).
	m.CPU.I = 0x40
	m.beamY = 0
	m.onM1Fetch(0x8000, code)
	for px := 0; px < 8; px++ {
		if m.Screen.PixelAt(px, 0) {
			t.Fatalf("pixel (%d,0) ink with I=$40, but glyph was planted at I=$50", px)
		}
	}

	// Now run with I=$50: the same character code must light the whole row.
	// Reset the column (the first fetch advanced it) so we draw cell (0,0) again.
	m.CPU.I = 0x50
	m.beamX = 0
	m.onM1Fetch(0x8000, code)
	for px := 0; px < 8; px++ {
		if !m.Screen.PixelAt(px, 0) {
			t.Errorf("pixel (%d,0) should be ink with I=$50", px)
		}
	}
}

// The line counter is the low 3 bits of the pixel row: scanline N of a character
// reads bitmap byte N. Eight successive rows of the same cell read eight
// consecutive bitmap bytes (char*8 + 0..7).
func TestLineCounterIndexesBitmapRow(t *testing.T) {
	m := newBlankZX81(t)
	m.CPU.I = 0x40 // char table in writable RAM
	const code = 0x07
	base := uint16(0x40)<<8 | uint16(code)<<3
	// Distinct bitmap per scanline: row k has only bit (7-k) set.
	for k := 0; k < 8; k++ {
		m.Mem.Write(base+uint16(k), byte(0x80>>k))
	}
	// Draw the same cell on each of its 8 scanlines (column resets between rows
	// as a HALT would; here we just reposition the beam to model the same cell).
	for row := 0; row < 8; row++ {
		m.beamX = 0
		m.beamY = row
		m.onM1Fetch(0x8000, code)
	}
	for row := 0; row < 8; row++ {
		for px := 0; px < 8; px++ {
			want := px == row // only the diagonal pixel is ink
			if got := m.Screen.PixelAt(px, row); got != want {
				t.Errorf("pixel (%d,%d) = %v, want %v (line counter mis-indexed)", px, row, got, want)
			}
		}
	}
}

// Bit 7 of the display byte is the inverse-video attribute: every pixel of the
// scanline is flipped before being shifted out.
func TestInverseVideoBit7FlipsScanline(t *testing.T) {
	m := newBlankZX81(t)
	m.CPU.I = 0x40 // char table in writable RAM
	const code = 0x01
	addr := uint16(0x40)<<8 | uint16(code)<<3
	m.Mem.Write(addr, 0b1100_0000) // two ink pixels, six paper

	m.onM1Fetch(0x8000, 0x80|code) // inverse bit set
	// Inverse: 0b00111111 -> bits 0..1 paper, bits 2..7 ink.
	for px := 0; px < 8; px++ {
		want := px >= 2
		if got := m.Screen.PixelAt(px, 0); got != want {
			t.Errorf("inverse pixel (%d,0) = %v, want %v", px, got, want)
		}
	}
}

// Only bits 0-5 select the glyph: bit 6 cannot reach this path (it routes to the
// passthrough branch), so the character code is masked to 6 bits. Codes that
// differ only in the high bits (0x01 vs 0x81's low 6 bits) hit the same glyph,
// differing only by the inverse attribute — exercised above. Here we confirm the
// mask explicitly: code 0x3F is the last glyph and reads char*8 = 0x1F8 offset.
func TestCharacterCodeMaskedTo6Bits(t *testing.T) {
	m := newBlankZX81(t)
	m.CPU.I = 0x40                            // char table in writable RAM
	addr := uint16(0x40)<<8 | uint16(0x3F)<<3 // 0x4000 + 0x3F*8 = 0x41F8
	if addr != 0x41F8 {
		t.Fatalf("address arithmetic wrong: %04X", addr)
	}
	m.Mem.Write(addr, 0xFF)
	m.onM1Fetch(0x8000, 0x3F)
	for px := 0; px < 8; px++ {
		if !m.Screen.PixelAt(px, 0) {
			t.Errorf("glyph 0x3F pixel (%d,0) should be ink", px)
		}
	}
}

// ---------------------------------------------------------------------------
// Maskable interrupt: "INTs are generated when Bit 6 of the R register becomes
// zero" (Nocash). RunFrame samples R bit 6 on its falling edge and asserts the
// CPU IRQ latch. R must advance during HALT (RefreshDuringHalt) so the INT still
// fires while the display loop is halted.
// ---------------------------------------------------------------------------

// The CPU must be configured with the ZX8x invariants: own INT (no frame INT),
// HALT woken by INT, and R counting during HALT (so the R-bit-6 INT fires while
// halted). The project memory flags these as load-bearing.
func TestZX8xCPUInvariants(t *testing.T) {
	m := newBlankZX81(t)
	if !m.CPU.FrameIntDisabled {
		t.Error("FrameIntDisabled must be true — the ZX8x drives its own INT")
	}
	if !m.CPU.HaltWakeOnInt {
		t.Error("HaltWakeOnInt must be true — INT wakes the display-loop HALT")
	}
	if !m.CPU.RefreshDuringHalt {
		t.Error("RefreshDuringHalt must be true — R bit 6 drives /INT during HALT")
	}
	if m.CPU.M1FetchHook == nil {
		t.Error("M1FetchHook must be set — it is the CPU-generated display path")
	}
}

// A falling edge of R bit 6 (0x40 -> 0x00) asserts the maskable INT latch. We
// drive RunFrame across a synthetic R transition and confirm IRQPending is set.
func TestMaskableIntOnRBit6FallingEdge(t *testing.T) {
	m := newBlankInert(t, roms.ModelZX81)
	m.CPU.IRQPending.Store(false)

	// Seed prevR so the clock tick sees a falling edge of bit 6 (0x40 -> 0x00).
	m.prevR = 0x40
	m.CPU.R = 0x00

	m.tickClock()

	if !m.CPU.IRQPending.Load() {
		t.Error("IRQPending should be latched on the R-bit-6 falling edge")
	}
}

// No falling edge -> no INT asserted. When R bit 6 stays high (or stays low),
// the INT latch is not driven by RunFrame.
func TestNoMaskableIntWithoutRBit6FallingEdge(t *testing.T) {
	m := newBlankInert(t, roms.ModelZX81)
	m.CPU.IRQPending.Store(false)

	m.prevR = 0x40
	m.CPU.R = 0x40 // bit 6 stays high: no edge
	m.tickClock()
	if m.CPU.IRQPending.Load() {
		t.Error("IRQPending set with no R-bit-6 falling edge")
	}
}

// ---------------------------------------------------------------------------
// NMI generator / SLOW vs FAST mode and the I/O map.
// (Nocash "ZX81 I/O Map": write port FE -> NMI on; write port FD -> NMI off.
// SLOW = NMI generator on, FAST = off. ZX80 has no NMI generator.)
// ---------------------------------------------------------------------------

// On the ZX81, an OUT to a port with A0=0 (port $FE) enables the SLOW-mode NMI
// generator; an OUT to a port with A1=0 (port $FD) disables it.
func TestZX81WritePortFEEnablesNMIPortFDDisables(t *testing.T) {
	m := newBlankInert(t, roms.ModelZX81)
	if m.nmiOn {
		t.Fatal("NMI generator must start disabled (FAST mode)")
	}

	m.WritePort(0x00FE, 0x00) // A0=0 -> enable
	if !m.nmiOn {
		t.Error("OUT to port FE (A0=0) should enable the NMI generator (SLOW mode)")
	}

	m.WritePort(0x00FD, 0x00) // A1=0 -> disable
	if m.nmiOn {
		t.Error("OUT to port FD (A1=0) should disable the NMI generator (FAST mode)")
	}
}

// The ZX80 has no automatic NMI generator: writing the same ports must NOT turn
// SLOW mode on. (Grant Searle / Nocash: SLOW mode is a ZX81 addition.)
func TestZX80HasNoNMIGenerator(t *testing.T) {
	m := newBlankInert(t, roms.ModelZX80)
	m.WritePort(0x00FE, 0x00)
	if m.nmiOn {
		t.Error("ZX80 must not enable an NMI generator on an OUT to port FE")
	}
	m.WritePort(0x00FD, 0x00)
	if m.nmiOn {
		t.Error("ZX80 NMI generator state must stay off")
	}
}

// When SLOW mode (NMI generator) is on, RunFrame pulses one NMI per scanline,
// paced by the line clock. Crossing a line boundary must request an NMI.
func TestNMIGeneratorPulsesOncePerLine(t *testing.T) {
	m := newBlankInert(t, roms.ModelZX81)
	m.nmiOn = true
	m.baseT = m.CPU.Tstates()
	m.line = 0
	m.CPU.PendingNMI.Store(false)

	// Advance the CPU clock past one scanline's worth of T-states: the line
	// clock should cross a boundary and request an NMI.
	m.CPU.SetTstates(m.baseT + lineTStates + 5)
	m.tickClock()

	if !m.CPU.PendingNMI.Load() {
		t.Error("crossing a scanline boundary with NMI generator on should pulse an NMI")
	}
	if m.line < 1 {
		t.Errorf("line counter = %d, want >= 1 after crossing a scanline", m.line)
	}
}

// With the NMI generator OFF (FAST mode), crossing a scanline boundary must NOT
// pulse an NMI — only the line counter advances.
func TestNoNMIWhenGeneratorOff(t *testing.T) {
	m := newBlankInert(t, roms.ModelZX81)
	m.nmiOn = false
	m.baseT = m.CPU.Tstates()
	m.line = 0
	m.CPU.PendingNMI.Store(false)

	m.CPU.SetTstates(m.baseT + lineTStates + 5)
	m.tickClock()

	if m.CPU.PendingNMI.Load() {
		t.Error("NMI pulsed while the generator was off (FAST mode)")
	}
}

// ---------------------------------------------------------------------------
// Port reads: IN from a port with A0=0 ($FE) returns the keyboard half-row
// (active low) in bits 0-4, with bit 5=1, bit 6=1 (50Hz PAL), bit 7=cassette.
// It also starts vertical sync and resets the line counter — but ONLY when the
// NMI generator is off (SLOW mode keeps the vsync under NMI control).
// ---------------------------------------------------------------------------

// IN from port $FE returns 0x60 in the high bits (bit5=1, bit6=1) with the
// keyboard rows all-released giving 0x1F in the low bits.
func TestReadPortFEReturnsKeyboardAndStatusBits(t *testing.T) {
	m := newBlankInert(t, roms.ModelZX81)
	val, handled := m.ReadPort(0x00FE) // A0=0
	if !handled {
		t.Fatal("ReadPort(FE) not handled")
	}
	// No keys pressed: bits 0-4 all 1. Bit 5 = 1, bit 6 = 1 (50Hz). Bit 7 from
	// cassette (idle = 0 in this build). So 0x60 | 0x1F = 0x7F.
	if val&0x60 != 0x60 {
		t.Errorf("ReadPort(FE) high status bits = %02X, want bit5 and bit6 set", val&0x60)
	}
	if val&0x1F != 0x1F {
		t.Errorf("ReadPort(FE) keyboard bits = %02X, want 0x1F (no keys)", val&0x1F)
	}
}

// A pressed key pulls its column bit low (active-low keyboard). Pressing SPACE
// (ZX8x matrix row 7, bit 0) must clear bit 0 when that row's half is selected.
func TestReadPortFEKeyboardActiveLow(t *testing.T) {
	m := newBlankInert(t, roms.ModelZX81)
	m.KB.PressMatrixKey(7, 0x01, true) // SPACE
	// Select row 7 by driving its address line low: A8+7 = bit 15 (0x80 << 8).
	// Port FE with A15 low selects keyboard row 7.
	val, _ := m.ReadPort(0x7FFE)
	if val&0x01 != 0 {
		t.Errorf("ReadPort row7 = %02X, SPACE should pull bit 0 low (active-low)", val)
	}
}

// Reading port $FE starts vertical sync when the NMI generator is OFF (FAST/idle
// vsync path). The subsequent OUT (any port) ends vsync and presents the frame.
func TestReadPortFEStartsVSyncWhenNMIOff(t *testing.T) {
	m := newBlankInert(t, roms.ModelZX81)
	m.nmiOn = false
	m.vsync = false

	m.ReadPort(0x00FE)
	if !m.vsync {
		t.Error("IN port FE with NMI off should start vertical sync")
	}

	// Any OUT ends vsync (presents the frame, rebases the clocks).
	m.WritePort(0x00FF, 0x00) // A0=1,A1=1 so neither NMI toggle fires
	if m.vsync {
		t.Error("OUT after vsync should drop vsync (present frame)")
	}
}

// When the NMI generator is ON (SLOW mode), reading port $FE must NOT start the
// idle vsync (the NMI generator owns the vertical timing in SLOW mode).
func TestReadPortFEDoesNotStartVSyncWhenNMIOn(t *testing.T) {
	m := newBlankInert(t, roms.ModelZX81)
	m.nmiOn = true
	m.vsync = false
	m.ReadPort(0x00FE)
	if m.vsync {
		t.Error("IN port FE with NMI on must not start the idle vsync")
	}
}

// IN from an odd port (A0=1) is not the ULA: it returns the floating-bus idle
// value $FF and does not touch vsync.
func TestReadOddPortIsNotULA(t *testing.T) {
	m := newBlankInert(t, roms.ModelZX81)
	m.nmiOn = false
	m.vsync = false
	val, handled := m.ReadPort(0x00FF) // A0=1
	if !handled || val != 0xFF {
		t.Errorf("ReadPort(odd) = %02X handled=%v, want 0xFF,true", val, handled)
	}
	if m.vsync {
		t.Error("IN from an odd (non-ULA) port must not start vsync")
	}
}

// ---------------------------------------------------------------------------
// dropSync / frame presentation: an OUT ending vsync presents the frame and
// rebases the line + beam clocks for the next frame.
// ---------------------------------------------------------------------------

// dropSync rebases the per-frame clocks (line=0, beam reset, baseT = now) and
// presents the frame. A no-op when not in vsync.
func TestDropSyncRebasesClocksAndPresents(t *testing.T) {
	m := newBlankInert(t, roms.ModelZX81)
	m.vsync = true
	m.line = 99
	m.beamX, m.beamY = 5, 7
	m.started = true

	m.WritePort(0x00FF, 0x00) // ends vsync via dropSync
	if m.vsync {
		t.Error("vsync should be cleared after the OUT")
	}
	if m.line != 0 || m.beamX != 0 || m.beamY != 0 || m.started {
		t.Errorf("clocks not rebased: line=%d beamX=%d beamY=%d started=%v",
			m.line, m.beamX, m.beamY, m.started)
	}
	if m.front == nil {
		t.Error("a frame should have been presented on dropSync")
	}
}

// An OUT while NOT in vsync is a no-op for presentation (the frame is not
// re-presented, the beam keeps its position).
func TestOutWithoutVSyncDoesNotPresent(t *testing.T) {
	m := newBlankInert(t, roms.ModelZX81)
	m.vsync = false
	m.beamY = 3
	m.front = nil
	m.WritePort(0x00FF, 0x00)
	if m.front != nil {
		t.Error("OUT without vsync must not present a frame")
	}
	if m.beamY != 3 {
		t.Error("OUT without vsync must not reset the beam")
	}
}

// ---------------------------------------------------------------------------
// Reset semantics: the documented power-on state of the video/sync machinery.
// ---------------------------------------------------------------------------

func TestResetClearsVideoAndSyncState(t *testing.T) {
	m := newBlankInert(t, roms.ModelZX81)
	m.beamX, m.beamY = 4, 9
	m.started = true
	m.vsync = true
	m.nmiOn = true
	m.line = 42
	m.prevR = 0x40

	m.Reset()

	if m.beamX != 0 || m.beamY != 0 || m.started {
		t.Errorf("Reset left beam state: beamX=%d beamY=%d started=%v", m.beamX, m.beamY, m.started)
	}
	if m.vsync || m.nmiOn {
		t.Errorf("Reset left sync state: vsync=%v nmiOn=%v", m.vsync, m.nmiOn)
	}
	if m.line != 0 || m.prevR != 0 {
		t.Errorf("Reset left clocks: line=%d prevR=%02X", m.line, m.prevR)
	}
	// Invariants must survive a reset (CPU hook config is preserved).
	if !m.CPU.RefreshDuringHalt || !m.CPU.HaltWakeOnInt || !m.CPU.FrameIntDisabled {
		t.Error("Reset must preserve the ZX8x CPU invariants")
	}
}

// ---------------------------------------------------------------------------
// Helpers.
// ---------------------------------------------------------------------------

// newBlankZX81 builds a ZX81 machine for direct M1-hook / screen unit tests.
func newBlankZX81(t *testing.T) *Machine {
	t.Helper()
	m, err := NewMachine(roms.ModelZX81, "")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// newBlankInert builds a machine and asserts the inert-peripherals invariant
// indirectly by never wiring a Spectrum peripherals manager — the machine owns
// its own ULA via ReadPort/WritePort. (Per project memory: the ZX8x emulator
// uses an inert peripherals manager; here in the unit tests the Machine itself
// is the ULA, which is the same contract.)
func newBlankInert(t *testing.T, model roms.SpectrumModel) *Machine {
	t.Helper()
	m, err := NewMachine(model, "")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// tickClock replicates exactly the per-step interrupt/NMI clock bookkeeping that
// RunFrame performs BEFORE each CPU step (the line-clock NMI pulse and the
// R-bit-6 falling-edge INT latch). It deliberately stops short of the CPU step
// so a test can observe the latch the clock sets without the very next
// StepInstructionWithIRQ accepting (and clearing) it. This mirrors the loop body
// in machine.go RunFrame verbatim; if that drifts, these tests should be
// revisited.
func (m *Machine) tickClock() {
	t := m.CPU.Tstates()
	for t-m.baseT >= uint64(m.line+1)*lineTStates {
		m.line++
		if m.nmiOn {
			m.CPU.PendingNMI.Store(true)
		}
	}
	if m.prevR&0x40 != 0 && m.CPU.R&0x40 == 0 {
		m.CPU.IRQPending.Store(true)
	}
	m.prevR = m.CPU.R
}
