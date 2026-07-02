// Package nex implements parsing for the Spectrum Next .NEX V1.2
// executable file format.
//
// The format (per the SpecNext wiki NEX_file_format page and the
// reference C loader at github.com/Threetwosevensixseven/specnext-
// nex/blob/master/SOURCE.md) is:
//
//   - 512 bytes  header  (magic "Next", version "V1.2", flags, etc.)
//   - 512 bytes  palette (if header bit 10:0 set)
//   - N bytes    screen  (if header bit 10:1 set; size depends on
//     the screen-mode sub-flags)
//   - 2048 bytes Copper  (if header bit 10:5 set)
//   - 16K bank blocks in the documented order
//     [5, 2, 0, 1, 3, 4, 6, 7, 8, 9, ..., 111]
//     where each block is present iff BankLoad[bank] is true.
//
// This package handles parsing only; the loader that applies a
// parsed NEX into a pkg/memory.Memory (banks, SP/PC/border, and the
// optional Layer 2 / palette / Copper sections) lives with its
// caller.
package nex

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	HeaderSize  = 512
	PaletteSize = 512
	CopperSize  = 2048
	BankSize    = 16384 // 16K per bank block
	MaxBanks    = 112   // banks 0..111 in the bitmap
)

// Magic is the first 4 bytes of every valid .NEX file.
var Magic = [4]byte{'N', 'e', 'x', 't'}

// ErrBadMagic indicates the file's first 4 bytes weren't "Next".
var ErrBadMagic = errors.New("nex: bad magic")

// LoadOrder is the canonical order in which 16K bank blocks appear
// in a .NEX file when their corresponding BankLoad bit is set.
// Banks not listed (>= 14, except 8 and 9 which are present here
// at positions 8-9) come after in ascending order.
var LoadOrder = computeLoadOrder()

func computeLoadOrder() []int {
	// [5, 2, 0, 1, 3, 4, 6, 7] then 8, 9, ..., 111
	head := []int{5, 2, 0, 1, 3, 4, 6, 7}
	out := make([]int, 0, MaxBanks)
	out = append(out, head...)
	for b := 8; b < MaxBanks; b++ {
		out = append(out, b)
	}
	return out
}

// Header captures the parsed fields from the 512-byte file header
// that drive .NEX loading. Fields not directly modelled here are
// available via RawHeader for future extension.
type Header struct {
	Version     string         // e.g. "V1.2"
	RAMRequired byte           // byte 8: 0 = 768K, 1 = 1792K
	NumBanks    byte           // byte 9: number of 16K banks to load
	ScreenFlags byte           // byte 10: loading-screen flags, see HasPalette / HasScreen
	Border      byte           // byte 11
	SP          uint16         // byte 12-13
	PC          uint16         // byte 14-15
	NumFiles    uint16         // byte 16-17: extra NextZXOS files (usually 0)
	BankLoad    [MaxBanks]bool // byte 18-129: true for each 16K bank in the file
	StartDelay  byte           // byte 133: frames to delay before launching
	Preserve    byte           // byte 134: 1 = preserve NextRegs across load
	CoreMajor   byte           // byte 135
	CoreMinor   byte           // byte 136
	CoreSub     byte           // byte 137
	EntryBank   byte           // byte 139: bank mapped at 0xC000 before JP PC
	FileHandle  uint16         // byte 140-141: 0 = close the .nex, else keep it open
	RawHeader   [HeaderSize]byte
}

// Screen-flag bits in header byte 10 (per the SpecNext NEX_file_format
// page). Each set bit means a loading-screen block of that kind sits
// between the optional palette and the bank stream. Bit 7 suppresses
// the palette block. There is no "palette" or "copper" bit: the palette
// accompanies a loading screen, and standard NEX V1.2 carries no Copper
// section.
const (
	flagLayer2    = 0x01 // 49152-byte Layer 2 (256x192) loading screen
	flagULA       = 0x02 // 6912-byte classic ULA loading screen
	flagLoRes     = 0x04 // 12288-byte LoRes (128x96) loading screen
	flagHiRes     = 0x08 // 12288-byte Timex hi-res loading screen
	flagHiColour  = 0x10 // 12288-byte Timex hi-colour loading screen
	flagNoPalette = 0x80 // when set, the 512-byte palette block is omitted
)

// screenModeBits is the mask of the five loading-screen kinds.
const screenModeBits = flagLayer2 | flagULA | flagLoRes | flagHiRes | flagHiColour

// Loading-screen block sizes, in bytes, indexed by their flag bit.
const (
	screenLayer2   = 49152
	screenULA      = 6912
	screenLoRes    = 12288
	screenHiRes    = 12288
	screenHiColour = 12288
)

// loadingScreens lists the loading-screen blocks in the file order the
// canonical loader reads them (after the optional palette).
var loadingScreens = []struct {
	flag byte
	size int
	name string
}{
	{flagLayer2, screenLayer2, "Layer 2"},
	{flagULA, screenULA, "ULA"},
	{flagLoRes, screenLoRes, "LoRes"},
	{flagHiRes, screenHiRes, "HiRes"},
	{flagHiColour, screenHiColour, "HiColour"},
}

// HasScreen reports whether any loading-screen block precedes the banks.
func (h *Header) HasScreen() bool { return h.ScreenFlags&screenModeBits != 0 }

// HasPalette reports whether the 512-byte palette block is present. The
// palette accompanies a loading screen unless the no-palette bit is set.
func (h *Header) HasPalette() bool {
	return h.ScreenFlags != 0 && h.ScreenFlags&flagNoPalette == 0
}

// NEX is a fully-parsed .NEX file.
type NEX struct {
	Header  Header
	Palette []byte
	Screen  []byte // empty if absent; raw bytes regardless of mode
	Copper  []byte
	Banks   map[int][]byte // 16K bank id -> 16384-byte slice
}

// ParseFile reads and parses a .NEX file from disk.
func ParseFile(path string) (*NEX, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("nex: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return Parse(f)
}

// Parse reads a .NEX file from any io.Reader. Returns an error
// (ErrBadMagic) if the magic does not match.
//
// Each loading-screen block set in ScreenFlags (Layer 2, classic
// ULA, LoRes, HiRes, HiColour — a file may set more than one) is
// read at its documented size and appended to Screen in file order;
// interpreting those bytes for display is left to the caller.
func Parse(r io.Reader) (*NEX, error) {
	hdrBytes := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, hdrBytes); err != nil {
		return nil, fmt.Errorf("nex: read header: %w", err)
	}
	if !bytes.Equal(hdrBytes[0:4], Magic[:]) {
		return nil, ErrBadMagic
	}
	h := Header{}
	copy(h.RawHeader[:], hdrBytes)
	h.Version = string(bytes.TrimRight(hdrBytes[4:8], "\x00 "))
	h.RAMRequired = hdrBytes[8]
	h.NumBanks = hdrBytes[9]
	h.ScreenFlags = hdrBytes[10]
	h.Border = hdrBytes[11]
	h.SP = binary.LittleEndian.Uint16(hdrBytes[12:14])
	h.PC = binary.LittleEndian.Uint16(hdrBytes[14:16])
	h.NumFiles = binary.LittleEndian.Uint16(hdrBytes[16:18])
	for i := 0; i < MaxBanks; i++ {
		h.BankLoad[i] = hdrBytes[18+i] != 0
	}
	// Bytes 130-132 hold loading-bar on/off, colour and per-bank
	// delay (kept via RawHeader). Byte 133 is the launch start
	// delay; byte 134 the "preserve NextRegs across load" flag.
	h.StartDelay = hdrBytes[133]
	h.Preserve = hdrBytes[134]
	h.CoreMajor = hdrBytes[135]
	h.CoreMinor = hdrBytes[136]
	h.CoreSub = hdrBytes[137]
	h.EntryBank = hdrBytes[139]
	h.FileHandle = binary.LittleEndian.Uint16(hdrBytes[140:142])

	out := &NEX{Header: h, Banks: make(map[int][]byte)}

	// Optional sections precede the bank stream, in file order:
	// the 512-byte palette (unless suppressed), then each requested
	// loading screen. Reading the wrong size here mis-aligns every
	// subsequent bank, so the sizes must match the format exactly.
	if h.HasPalette() {
		out.Palette = make([]byte, PaletteSize)
		if _, err := io.ReadFull(r, out.Palette); err != nil {
			return nil, fmt.Errorf("nex: read palette: %w", err)
		}
	}
	for _, s := range loadingScreens {
		if h.ScreenFlags&s.flag == 0 {
			continue
		}
		block := make([]byte, s.size)
		if _, err := io.ReadFull(r, block); err != nil {
			return nil, fmt.Errorf("nex: read %s loading screen: %w", s.name, err)
		}
		out.Screen = append(out.Screen, block...)
	}

	for _, bank := range LoadOrder {
		if !h.BankLoad[bank] {
			continue
		}
		data := make([]byte, BankSize)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, fmt.Errorf("nex: read bank %d: %w", bank, err)
		}
		out.Banks[bank] = data
	}
	return out, nil
}
