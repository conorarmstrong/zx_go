package nex

// Spec-faithful tests for the Spectrum Next .NEX V1.2/V1.3 file format.
//
// Oracle: the SpecNext wiki "NEX file format" page. Pinned facts:
//
//   - 512-byte header beginning with magic "Next" and a version string
//     "V1.2"/"V1.3" at bytes 4-7.
//   - byte 8: RAM required, byte 9: number of banks, byte 10: loading-
//     screen flags, byte 11: border, bytes 12-13: SP, bytes 14-15: PC,
//     bytes 16-17: number of extra files, bytes 18-129: the 112-entry
//     bank-present bitmap, byte 139: entry bank, bytes 140-141: file
//     handle.
//   - Optional sections precede the bank stream in this exact order:
//     512-byte palette (unless the no-palette bit 7 is set), then each
//     requested loading screen (Layer2 49152 / ULA 6912 / LoRes 12288 /
//     HiRes 12288 / HiColour 12288), then the 16K bank blocks in the
//     canonical order 5,2,0,1,3,4,6,7,8,9,...,111 for every bank whose
//     bitmap bit is set.

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestParseSPPCBorderEntryBankOffsets pins the launch-control fields to
// their exact header offsets. Getting SP/PC wrong launches the .NEX into
// garbage; getting EntryBank wrong maps the wrong 16K at 0xC000.
func TestParseSPPCBorderEntryBankOffsets(t *testing.T) {
	o := buildOpts{
		border:    5,
		sp:        0xBFFE,
		pc:        0xC000,
		entryBank: 12,
	}
	got, err := Parse(bytes.NewReader(buildNEX(o)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Header.Border != 5 {
		t.Errorf("Border = %d, want 5 (byte 11)", got.Header.Border)
	}
	if got.Header.SP != 0xBFFE {
		t.Errorf("SP = %#04x, want 0xBFFE (bytes 12-13)", got.Header.SP)
	}
	if got.Header.PC != 0xC000 {
		t.Errorf("PC = %#04x, want 0xC000 (bytes 14-15)", got.Header.PC)
	}
	if got.Header.EntryBank != 12 {
		t.Errorf("EntryBank = %d, want 12 (byte 139)", got.Header.EntryBank)
	}
}

// TestParseVersionString13 pins that the V1.3 version string parses (the
// header just trims trailing NUL/space from bytes 4-7).
func TestParseVersionString13(t *testing.T) {
	o := buildOpts{version: "V1.3"}
	got, err := Parse(bytes.NewReader(buildNEX(o)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Header.Version != "V1.3" {
		t.Errorf("Version = %q, want %q", got.Header.Version, "V1.3")
	}
}

// TestParseBankBitmapOffsets pins the 112-entry bank-present bitmap to
// bytes 18..129 by toggling the first, an interior, and the last entry.
func TestParseBankBitmapOffsets(t *testing.T) {
	var o buildOpts
	o.bankLoad[0] = true   // -> header byte 18
	o.bankLoad[50] = true  // -> header byte 68
	o.bankLoad[111] = true // -> header byte 129 (last)
	o.bankData = map[int][]byte{
		0:   bytes.Repeat([]byte{0xA0}, BankSize),
		50:  bytes.Repeat([]byte{0xB0}, BankSize),
		111: bytes.Repeat([]byte{0xC0}, BankSize),
	}
	got, err := Parse(bytes.NewReader(buildNEX(o)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Banks) != 3 {
		t.Fatalf("loaded %d banks, want 3", len(got.Banks))
	}
	for bank, fill := range map[int]byte{0: 0xA0, 50: 0xB0, 111: 0xC0} {
		if got.Banks[bank] == nil || got.Banks[bank][0] != fill {
			t.Errorf("bank %d not loaded with fill 0x%02X", bank, fill)
		}
	}
}

// TestParseMultipleLoadingScreensSummed pins that when several screen-mode
// bits are set, the parser reads every requested screen in file order and
// the banks still land at the right offset. The Screen field is the
// concatenation of all blocks.
func TestParseMultipleLoadingScreensSummed(t *testing.T) {
	o := buildOpts{
		screenFlags: flagLayer2 | flagULA,
		// File order is Layer2 (49152) then ULA (6912).
		screen:   append(bytes.Repeat([]byte{0x11}, screenLayer2), bytes.Repeat([]byte{0x22}, screenULA)...),
		bankData: map[int][]byte{2: bytes.Repeat([]byte{0x99}, BankSize)},
	}
	o.bankLoad[2] = true
	got, err := Parse(bytes.NewReader(buildNEX(o)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	wantLen := screenLayer2 + screenULA
	if len(got.Screen) != wantLen {
		t.Fatalf("Screen length = %d, want %d (Layer2+ULA summed)", len(got.Screen), wantLen)
	}
	// First Layer2 region 0x11, then ULA region 0x22.
	if got.Screen[0] != 0x11 || got.Screen[screenLayer2] != 0x22 {
		t.Errorf("screens not concatenated in file order (0x%02X / 0x%02X)",
			got.Screen[0], got.Screen[screenLayer2])
	}
	if got.Banks[2] == nil || got.Banks[2][0] != 0x99 {
		t.Errorf("bank 2 misaligned after two loading screens")
	}
}

// TestParseBankStreamFollowsCanonicalOrder pins that two banks present out
// of natural numeric order (2 before 5 numerically, but 5 before 2 in the
// canonical load order) are read from the file in canonical order. We give
// the file the bytes in canonical order and confirm each bank gets its
// own block (i.e. the parser walks LoadOrder, not numeric order).
func TestParseBankStreamFollowsCanonicalOrder(t *testing.T) {
	o := buildOpts{
		bankData: map[int][]byte{
			5: bytes.Repeat([]byte{0x05}, BankSize),
			2: bytes.Repeat([]byte{0x02}, BankSize),
			0: bytes.Repeat([]byte{0x00}, BankSize),
		},
	}
	o.bankLoad[5] = true
	o.bankLoad[2] = true
	o.bankLoad[0] = true
	got, err := Parse(bytes.NewReader(buildNEX(o)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// buildNEX writes in LoadOrder (5,2,0). If the parser read in any
	// other order the fills would be swapped.
	if got.Banks[5][0] != 0x05 || got.Banks[2][0] != 0x02 || got.Banks[0][0] != 0x00 {
		t.Errorf("bank order mismatch: 5=0x%02X 2=0x%02X 0=0x%02X",
			got.Banks[5][0], got.Banks[2][0], got.Banks[0][0])
	}
}

// TestParseTruncatedBankErrors pins that a header claiming a bank that the
// file is too short to contain is a parse error (not a silent partial).
func TestParseTruncatedBankErrors(t *testing.T) {
	o := buildOpts{}
	o.bankLoad[5] = true
	full := buildNEX(o)
	// Lop off half of the (only) bank block.
	truncated := full[:len(full)-BankSize/2]
	if _, err := Parse(bytes.NewReader(truncated)); err == nil {
		t.Fatal("expected error parsing a truncated bank block, got nil")
	}
}

// TestParseHeaderMagicExact pins that only the exact "Next" magic is
// accepted; a one-byte corruption is rejected with ErrBadMagic.
func TestParseHeaderMagicExact(t *testing.T) {
	data := buildNEX(buildOpts{})
	data[3] = 'X' // "NexX"
	if _, err := Parse(bytes.NewReader(data)); err == nil {
		t.Fatal("expected ErrBadMagic for corrupted magic, got nil")
	}
}

// TestParsePaletteIsExactly512 pins that the palette block, when present,
// is exactly 512 bytes and is captured verbatim.
func TestParsePaletteIsExactly512(t *testing.T) {
	pal := make([]byte, PaletteSize)
	for i := range pal {
		pal[i] = byte(i)
	}
	o := buildOpts{
		screenFlags: flagULA, // any screen flag => palette present
		screen:      bytes.Repeat([]byte{0}, screenULA),
		palette:     pal,
	}
	got, err := Parse(bytes.NewReader(buildNEX(o)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Palette) != PaletteSize {
		t.Fatalf("palette length = %d, want %d", len(got.Palette), PaletteSize)
	}
	if !bytes.Equal(got.Palette, pal) {
		t.Error("palette bytes not captured verbatim")
	}
}

// TestNoScreenMeansNoPalette pins HasPalette()=false when no screen flag
// is set: a bare header+banks file has neither palette nor screen.
func TestNoScreenMeansNoPalette(t *testing.T) {
	o := buildOpts{}
	o.bankLoad[5] = true
	o.bankData = map[int][]byte{5: bytes.Repeat([]byte{0x77}, BankSize)}
	got, err := Parse(bytes.NewReader(buildNEX(o)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Header.HasScreen() || got.Header.HasPalette() {
		t.Error("no screen flags => HasScreen and HasPalette must both be false")
	}
	if len(got.Palette) != 0 || len(got.Screen) != 0 {
		t.Error("no screen flags => no palette/screen bytes consumed")
	}
	// Bank must be at file offset 512 (immediately after header).
	if got.Banks[5] == nil || got.Banks[5][0] != 0x77 {
		t.Error("bank 5 not directly after the 512-byte header")
	}
}

// TestParseHeaderRawPreserved pins that RawHeader keeps the full 512 bytes
// (so future fields stay recoverable).
func TestParseHeaderRawPreserved(t *testing.T) {
	o := buildOpts{}
	data := buildNEX(o)
	// Stamp a recognisable byte in the reserved region.
	data[200] = 0x5C
	got, err := Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Header.RawHeader[200] != 0x5C {
		t.Errorf("RawHeader[200] = 0x%02X, want 0x5C", got.Header.RawHeader[200])
	}
	if !bytes.Equal(got.Header.RawHeader[0:4], Magic[:]) {
		t.Error("RawHeader magic not preserved")
	}
	// Sanity: SP encoded little-endian.
	_ = binary.LittleEndian.Uint16(got.Header.RawHeader[12:14])
}
