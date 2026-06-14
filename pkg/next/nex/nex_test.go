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
	numBanks    uint16
	bankLoad    [MaxBanks]bool
	bankData    map[int][]byte // 16K each
	hasPalette  bool
	screenFlags byte // raw byte-10 override (escapes the helper bits)
	hasCopper   bool
	palette     []byte
	copper      []byte
	startDelay  byte
	preserve    byte
	entryBank   byte
}

func buildNEX(o buildOpts) []byte {
	hdr := make([]byte, HeaderSize)
	copy(hdr[0:4], Magic[:])
	if o.version == "" {
		o.version = "V1.2"
	}
	copy(hdr[4:8], []byte(o.version))
	hdr[10] = o.screenFlags
	if o.hasPalette {
		hdr[10] |= flagPalette
	}
	if o.hasCopper {
		hdr[10] |= flagCopper
	}
	hdr[11] = o.border
	binary.LittleEndian.PutUint16(hdr[12:14], o.sp)
	binary.LittleEndian.PutUint16(hdr[14:16], o.pc)
	binary.LittleEndian.PutUint16(hdr[16:18], o.numBanks)
	for i := 0; i < MaxBanks; i++ {
		if o.bankLoad[i] {
			hdr[18+i] = 1
		}
	}
	hdr[130] = o.startDelay
	hdr[134] = o.preserve
	hdr[139] = o.entryBank

	buf := new(bytes.Buffer)
	buf.Write(hdr)
	if o.hasPalette {
		if o.palette == nil {
			o.palette = make([]byte, PaletteSize)
		}
		buf.Write(o.palette)
	}
	if o.hasCopper {
		if o.copper == nil {
			o.copper = make([]byte, CopperSize)
		}
		buf.Write(o.copper)
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
	data := buildNEX(buildOpts{hasPalette: true, palette: pal})
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

func TestParsePaletteCopperAndBank(t *testing.T) {
	// Palette + Copper + one bank — Sprint 4's full read sequence
	// (no screen-mode bits because Sprint 4 refuses those).
	o := buildOpts{
		hasPalette: true,
		hasCopper:  true,
		numBanks:   1,
		bankData:   map[int][]byte{0: bytes.Repeat([]byte{0xC0}, BankSize)},
		entryBank:  0,
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
	if len(got.Copper) != CopperSize {
		t.Errorf("copper length = %d, want %d", len(got.Copper), CopperSize)
	}
	if _, ok := got.Banks[0]; !ok {
		t.Errorf("bank 0 missing")
	}
}

func TestParseRefusesUnsupportedScreenModes(t *testing.T) {
	cases := []struct {
		name string
		flag byte
	}{
		{"Layer 2", flagLayer2},
		{"ULA", flagULA},
		{"LoRes", flagLoRes},
		{"HiRes", flagHiRes},
		{"Timex", flagTimex},
		{"ULAnext", flagULAnext},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := buildNEX(buildOpts{screenFlags: c.flag})
			_, err := Parse(bytes.NewReader(data))
			if err == nil {
				t.Errorf("Parse with %s flag set: expected error, got nil", c.name)
			}
		})
	}
}

func TestParsePinsPreserveByteOffset(t *testing.T) {
	// Regression: Sprint 4 was reading byte 132 instead of byte 134
	// for the Preserve flag. Pin the correct offset.
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

// ParseFile coverage (iter 257). The on-disk wrapper just wraps
// Parse but the open/close failure path needs its own test.

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
