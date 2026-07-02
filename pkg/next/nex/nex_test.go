package nex

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"testing"
)

// buildNEX is a tiny synthetic .NEX builder for the parser tests.
// It writes a valid header and the optional sections / banks
// described by opts so each test can pin the parser against a
// known artefact without committing a binary fixture.
type buildOpts struct {
	version     string
	border      byte
	sp, pc      uint16
	numBanks    byte // byte 9 (informational; the bank stream is driven by bankLoad)
	numFiles    uint16
	bankLoad    [MaxBanks]bool
	bankData    map[int][]byte // 16K each
	screenFlags byte           // raw byte-10 loading-screen flags
	palette     []byte         // written iff the format says a palette is present (512)
	screen      []byte         // loading-screen block bytes (caller sizes to match screenFlags)
	startDelay  byte
	preserve    byte
	entryBank   byte
	fileHandle  uint16
}

func buildNEX(o buildOpts) []byte {
	hdr := make([]byte, HeaderSize)
	copy(hdr[0:4], Magic[:])
	if o.version == "" {
		o.version = "V1.2"
	}
	copy(hdr[4:8], []byte(o.version))
	hdr[9] = o.numBanks
	hdr[10] = o.screenFlags
	hdr[11] = o.border
	binary.LittleEndian.PutUint16(hdr[12:14], o.sp)
	binary.LittleEndian.PutUint16(hdr[14:16], o.pc)
	binary.LittleEndian.PutUint16(hdr[16:18], o.numFiles)
	for i := 0; i < MaxBanks; i++ {
		if o.bankLoad[i] {
			hdr[18+i] = 1
		}
	}
	hdr[133] = o.startDelay
	hdr[134] = o.preserve
	hdr[139] = o.entryBank
	binary.LittleEndian.PutUint16(hdr[140:142], o.fileHandle)

	buf := new(bytes.Buffer)
	buf.Write(hdr)
	// The palette block precedes the loading screen, present whenever a
	// screen flag is set and the no-palette bit is clear.
	if o.screenFlags != 0 && o.screenFlags&flagNoPalette == 0 {
		pal := o.palette
		if pal == nil {
			pal = make([]byte, PaletteSize)
		}
		buf.Write(pal)
	}
	if o.screen != nil {
		buf.Write(o.screen)
	}
	for _, bank := range LoadOrder {
		if !o.bankLoad[bank] {
			continue
		}
		data, ok := o.bankData[bank]
		if !ok {
			data = make([]byte, BankSize)
		}
		buf.Write(data)
	}
	return buf.Bytes()
}

// TestParseSkipsLayer2LoadingScreen pins the real-world bug: a .NEX
// whose offset-10 flags request a Layer 2 loading screen (bit 0)
// carries a 512-byte palette AND a 49152-byte Layer 2 screen before
// the bank stream. The parser must skip both so the banks land at the
// right file offset. (Most published Next games — nextoid, warhawk,
// spectron — set bit 0, so getting this wrong corrupts every bank and
// the game crashes back to the menu.)
func TestParseSkipsLayer2LoadingScreen(t *testing.T) {
	hdr := make([]byte, HeaderSize)
	copy(hdr[0:4], Magic[:])
	copy(hdr[4:8], []byte("V1.2"))
	hdr[9] = 1     // 1 bank to load (banks-to-load count is byte 9)
	hdr[10] = 0x01 // bit 0 = Layer 2 loading screen present
	hdr[18+5] = 1  // bank 5 present (first in load order)

	buf := new(bytes.Buffer)
	buf.Write(hdr)
	buf.Write(bytes.Repeat([]byte{0xAA}, 512))      // palette
	buf.Write(bytes.Repeat([]byte{0xBB}, 49152))    // Layer 2 screen
	buf.Write(bytes.Repeat([]byte{0x55}, BankSize)) // bank 5 payload

	got, err := Parse(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Banks[5] == nil || got.Banks[5][0] != 0x55 || got.Banks[5][BankSize-1] != 0x55 {
		t.Fatalf("bank 5 misaligned: got %#x, want 0x55 (Layer 2 screen not skipped)", got.Banks[5][0])
	}
	if len(got.Palette) != 512 || got.Palette[0] != 0xAA {
		t.Errorf("palette not parsed: len=%d", len(got.Palette))
	}
	if len(got.Screen) != 49152 || got.Screen[0] != 0xBB {
		t.Errorf("Layer 2 screen not parsed: len=%d", len(got.Screen))
	}
}

func TestParseRejectsBadMagic(t *testing.T) {
	junk := make([]byte, HeaderSize+10)
	copy(junk[0:4], []byte("XXXX"))
	_, err := Parse(bytes.NewReader(junk))
	if !errors.Is(err, ErrBadMagic) {
		t.Errorf("expected ErrBadMagic, got %v", err)
	}
}

func TestParseHeaderOnly(t *testing.T) {
	data := buildNEX(buildOpts{
		border:    3,
		sp:        0x8100,
		pc:        0x8000,
		entryBank: 5,
	})
	got, err := Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Header.Version != "V1.2" {
		t.Errorf("Version = %q, want V1.2", got.Header.Version)
	}
	if got.Header.Border != 3 || got.Header.SP != 0x8100 || got.Header.PC != 0x8000 {
		t.Errorf("border/SP/PC mismatch: %+v", got.Header)
	}
	if got.Header.EntryBank != 5 {
		t.Errorf("EntryBank = %d, want 5", got.Header.EntryBank)
	}
}

func TestParseWithPalette(t *testing.T) {
	pal := make([]byte, PaletteSize)
	for i := range pal {
		pal[i] = byte(i & 0xFF)
	}
	// A palette only appears alongside a loading screen; pair it with a
	// ULA screen (the smallest, 6912 bytes).
	data := buildNEX(buildOpts{
		screenFlags: flagULA,
		palette:     pal,
		screen:      bytes.Repeat([]byte{0xCC}, screenULA),
	})
	got, err := Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !got.Header.HasPalette() || len(got.Palette) != PaletteSize {
		t.Fatalf("palette section not parsed correctly")
	}
	for i, b := range pal {
		if got.Palette[i] != b {
			t.Errorf("palette[%d] = %#x, want %#x", i, got.Palette[i], b)
		}
	}
	if len(got.Screen) != screenULA || got.Screen[0] != 0xCC {
		t.Errorf("ULA screen not parsed: len=%d", len(got.Screen))
	}
}

func TestParseBanksInLoadOrder(t *testing.T) {
	// Mark banks 5 (first in load order) and 2 (second) as present.
	o := buildOpts{bankData: make(map[int][]byte)}
	o.bankLoad[5] = true
	o.bankLoad[2] = true
	o.numBanks = 2

	b5 := bytes.Repeat([]byte{0x55}, BankSize)
	b2 := bytes.Repeat([]byte{0x22}, BankSize)
	o.bankData[5] = b5
	o.bankData[2] = b2

	data := buildNEX(o)
	got, err := Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Banks) != 2 {
		t.Errorf("got %d banks, want 2", len(got.Banks))
	}
	if got.Banks[5][0] != 0x55 || got.Banks[5][BankSize-1] != 0x55 {
		t.Errorf("bank 5 mismatch")
	}
	if got.Banks[2][0] != 0x22 || got.Banks[2][BankSize-1] != 0x22 {
		t.Errorf("bank 2 mismatch")
	}
}

func TestParsePaletteScreenAndBank(t *testing.T) {
	// Palette + Layer 2 screen + one bank — the full optional-section
	// read sequence, with the bank required to land correctly after it.
	o := buildOpts{
		screenFlags: flagLayer2,
		screen:      bytes.Repeat([]byte{0xBB}, screenLayer2),
		numBanks:    1,
		bankData:    map[int][]byte{0: bytes.Repeat([]byte{0xC0}, BankSize)},
		entryBank:   0,
	}
	o.bankLoad[0] = true
	data := buildNEX(o)

	got, err := Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Palette) != PaletteSize {
		t.Errorf("palette length = %d, want %d", len(got.Palette), PaletteSize)
	}
	if len(got.Screen) != screenLayer2 {
		t.Errorf("screen length = %d, want %d", len(got.Screen), screenLayer2)
	}
	if b, ok := got.Banks[0]; !ok || b[0] != 0xC0 {
		t.Errorf("bank 0 missing or misaligned")
	}
}

// TestParseLoadingScreenSizes verifies each loading-screen kind is read
// at its exact size so the bank that follows it stays aligned. A bank
// filled with a marker byte must survive intact (it would be corrupted
// by the screen bytes if the size were wrong).
func TestParseLoadingScreenSizes(t *testing.T) {
	cases := []struct {
		name string
		flag byte
		size int
	}{
		{"Layer2", flagLayer2, screenLayer2},
		{"ULA", flagULA, screenULA},
		{"LoRes", flagLoRes, screenLoRes},
		{"HiRes", flagHiRes, screenHiRes},
		{"HiColour", flagHiColour, screenHiColour},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := buildOpts{
				screenFlags: c.flag,
				screen:      bytes.Repeat([]byte{0xEE}, c.size),
				bankData:    map[int][]byte{5: bytes.Repeat([]byte{0x55}, BankSize)},
			}
			o.bankLoad[5] = true
			got, err := Parse(bytes.NewReader(buildNEX(o)))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(got.Screen) != c.size {
				t.Errorf("%s screen length = %d, want %d", c.name, len(got.Screen), c.size)
			}
			if got.Banks[5] == nil || got.Banks[5][0] != 0x55 {
				t.Errorf("%s: bank 5 misaligned (screen size wrong)", c.name)
			}
		})
	}
}

// TestParseNoPaletteBit: bit 7 (0x80) suppresses the palette block even
// when a loading screen is present, so the bank must land 512 bytes
// earlier than it otherwise would.
func TestParseNoPaletteBit(t *testing.T) {
	o := buildOpts{
		screenFlags: flagLayer2 | flagNoPalette,
		screen:      bytes.Repeat([]byte{0xBB}, screenLayer2),
		bankData:    map[int][]byte{5: bytes.Repeat([]byte{0x55}, BankSize)},
	}
	o.bankLoad[5] = true
	got, err := Parse(bytes.NewReader(buildNEX(o)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Header.HasPalette() {
		t.Errorf("HasPalette = true with no-palette bit set")
	}
	if len(got.Palette) != 0 {
		t.Errorf("palette read (%d bytes) despite no-palette bit", len(got.Palette))
	}
	if got.Banks[5] == nil || got.Banks[5][0] != 0x55 {
		t.Errorf("bank 5 misaligned with no-palette bit")
	}
}

// TestParseHeaderFieldOffsets pins the header fields whose offsets the
// original parser had wrong (banks-to-load vs extra-files were swapped;
// start delay read from the loading-bar byte) plus the file handle.
func TestParseHeaderFieldOffsets(t *testing.T) {
	o := buildOpts{numBanks: 9, numFiles: 3, startDelay: 7, entryBank: 4, fileHandle: 0x4000}
	got, err := Parse(bytes.NewReader(buildNEX(o)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Header.NumBanks != 9 {
		t.Errorf("NumBanks = %d, want 9 (byte 9)", got.Header.NumBanks)
	}
	if got.Header.NumFiles != 3 {
		t.Errorf("NumFiles = %d, want 3 (byte 16-17)", got.Header.NumFiles)
	}
	if got.Header.StartDelay != 7 {
		t.Errorf("StartDelay = %d, want 7 (byte 133)", got.Header.StartDelay)
	}
	if got.Header.EntryBank != 4 {
		t.Errorf("EntryBank = %d, want 4 (byte 139)", got.Header.EntryBank)
	}
	if got.Header.FileHandle != 0x4000 {
		t.Errorf("FileHandle = %#x, want 0x4000 (byte 140-141)", got.Header.FileHandle)
	}
}

func TestParsePinsPreserveByteOffset(t *testing.T) {
	// Pin the Preserve flag to its correct header offset (134, not 132).
	data := buildNEX(buildOpts{preserve: 0xA5})
	got, err := Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Header.Preserve != 0xA5 {
		t.Errorf("Preserve = %#x, want 0xA5 (reading byte 134)", got.Header.Preserve)
	}
	// And confirm it's NOT reading byte 132 by setting that to a
	// different value.
	rawHdr := make([]byte, HeaderSize)
	copy(rawHdr, data[:HeaderSize])
	rawHdr[132] = 0x77 // poison byte 132
	rawHdr[134] = 0x44 // expected Preserve
	copy(data[:HeaderSize], rawHdr)
	got, err = Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Header.Preserve != 0x44 {
		t.Errorf("Preserve = %#x, want 0x44 (proves byte 134, not 132)", got.Header.Preserve)
	}
}

func TestLoadOrderStartsCanonically(t *testing.T) {
	// The first 8 entries of LoadOrder MUST be the documented
	// "low banks first" order: 5, 2, 0, 1, 3, 4, 6, 7. Subsequent
	// entries are ascending 8..111.
	want := []int{5, 2, 0, 1, 3, 4, 6, 7}
	for i, w := range want {
		if LoadOrder[i] != w {
			t.Errorf("LoadOrder[%d] = %d, want %d", i, LoadOrder[i], w)
		}
	}
	if LoadOrder[8] != 8 || LoadOrder[len(LoadOrder)-1] != 111 {
		t.Errorf("LoadOrder tail wrong: [8]=%d last=%d", LoadOrder[8], LoadOrder[len(LoadOrder)-1])
	}
}

// ParseFile coverage. The on-disk wrapper just wraps Parse but the
// open/close failure path needs its own test.

func TestParseFile_Roundtrip(t *testing.T) {
	data := buildNEX(buildOpts{border: 4, sp: 0xC000, pc: 0x8000, entryBank: 5})
	dir := t.TempDir()
	path := dir + "/test.nex"
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if got.Header.Border != 4 || got.Header.SP != 0xC000 || got.Header.PC != 0x8000 {
		t.Errorf("header mismatch: %+v", got.Header)
	}
}

func TestParseFile_MissingFileErrors(t *testing.T) {
	_, err := ParseFile("/nonexistent/missing.nex")
	if err == nil {
		t.Errorf("ParseFile missing file = nil err")
	}
}

func TestParseFile_BadMagic(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/bad.nex"
	if err := os.WriteFile(path, bytes.Repeat([]byte{0x00}, HeaderSize+10), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ParseFile(path)
	if !errors.Is(err, ErrBadMagic) {
		t.Errorf("ParseFile bad magic: got %v, want ErrBadMagic", err)
	}
}

func TestParseShortFileFails(t *testing.T) {
	// Header says one bank present, but file is truncated.
	o := buildOpts{numBanks: 1}
	o.bankLoad[5] = true
	full := buildNEX(o)
	short := full[:len(full)-BankSize/2] // chop half the bank
	if _, err := Parse(bytes.NewReader(short)); err == nil {
		t.Errorf("expected error on truncated file")
	}
}
