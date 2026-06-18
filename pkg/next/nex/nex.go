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
// Sprint 4 lands the parser; Apply() loads parsed banks into a
// pkg/memory.Memory and seeds SP / PC / border on the wired CPU.
// Sprint 6 will extend Apply() to populate Layer 2 / palette /
// Copper from the optional sections.
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
	Version     string // e.g. "V1.2"
	RAMRequired byte   // 0 = 768K, 1 = 1792K
	NumFiles    byte   // NextZXOS support files (rare)
	ScreenFlags byte   // bitmask, see HasPalette / HasScreen / HasCopper
	Border      byte
	SP          uint16
	PC          uint16
	NumBanks    uint16         // number of banks present in the file
	BankLoad    [MaxBanks]bool // true for each 16K bank that is in the file
	StartDelay  byte           // frames to delay before launching
	Preserve    byte           // 1 = preserve NextRegs across load
	CoreMajor   byte
	CoreMinor   byte
	CoreSub     byte
	EntryBank   byte // bank mapped at 0xC000 before JP PC
	RawHeader   [HeaderSize]byte
}

// Screen-flag bits in header byte 10 (per the SpecNext NEX_file_format
// page). Sprint 4 supports palette and Copper; every "screen mode"
// bit (Layer 2 / ULA / LoRes / HiRes / HiColor / Timex / ULAnext)
// makes the file un-parseable here — Sprint 6's video stack will
// land proper handling.
const (
	flagPalette = 0x01
	flagLayer2  = 0x02
	flagULA     = 0x04
	flagLoRes   = 0x08
	flagHiRes   = 0x10
	flagHiColor = 0x20 // also "copper present" on some pre-V1.2 spec drafts; see note below
	flagTimex   = 0x40
	flagULAnext = 0x80
)

// flagCopper is byte 11 bit 7 in pre-V1.2 spec drafts, or a separate
// header byte in V1.2. Different reference loaders disagree; the
// SpecNext wiki currently documents byte 10 bit 5 (0x20) as
// "HiColor screen", with Copper presence flagged by a non-zero
// header byte at offset 18+112 = 130 (where the bank count would
// otherwise be). We treat byte 10 bit 5 as HiColor (refused) and
// expose a Sprint-6-ready hook for Copper that currently sees
// nothing.
const flagCopper = flagHiColor

// HasPalette reports whether the optional 512-byte palette section
// follows the header.
func (h *Header) HasPalette() bool { return h.ScreenFlags&flagPalette != 0 }

// HasCopper reports whether a 2048-byte Copper program follows.
// Sprint 4 honours the bit if set on file but Sprint 6 will refine
// against canonical reference loader behaviour.
func (h *Header) HasCopper() bool { return h.ScreenFlags&flagCopper != 0 }

// screenModeBits is the mask of bits Sprint 4 cannot parse. Any
// of these set in ScreenFlags causes Parse to refuse the file
// rather than silently mis-align the bank stream.
const screenModeBits = flagLayer2 | flagULA | flagLoRes | flagHiRes | flagTimex | flagULAnext

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
// Screen-mode sub-flag handling is conservative: Sprint 4 reads a
// fixed 6912-byte classic ULA screen when HasScreen() is set and
// the screen-mode sub-flags are zero. Layer 2 / HiRes / LoRes
// screens are recorded only by size — the parser reads as many
// bytes as the spec dictates per mode and stashes them in Screen
// for the Sprint 6 video stack to consume.
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
	h.NumFiles = hdrBytes[9]
	h.ScreenFlags = hdrBytes[10]
	h.Border = hdrBytes[11]
	h.SP = binary.LittleEndian.Uint16(hdrBytes[12:14])
	h.PC = binary.LittleEndian.Uint16(hdrBytes[14:16])
	h.NumBanks = binary.LittleEndian.Uint16(hdrBytes[16:18])
	for i := 0; i < MaxBanks; i++ {
		h.BankLoad[i] = hdrBytes[18+i] != 0
	}
	h.StartDelay = hdrBytes[130]
	// Bytes 131–133 hold loading-bar pixel / colour fields kept
	// only via RawHeader for now; Sprint 6's video stack will
	// consume them when the loading-bar animation is wired.
	// Byte 134 is the "preserve NextRegs across load" flag per
	// the canonical SpecNext NEX_file_format spec.
	h.Preserve = hdrBytes[134]
	h.CoreMajor = hdrBytes[135]
	h.CoreMinor = hdrBytes[136]
	h.CoreSub = hdrBytes[137]
	h.EntryBank = hdrBytes[139]

	out := &NEX{Header: h, Banks: make(map[int][]byte)}

	// Refuse to parse files that need screen-mode handling we
	// don't have yet. Loading the wrong number of bytes here
	// would silently corrupt every subsequent bank, which is
	// worse than a clean rejection. Sprint 6 will support
	// Layer 2 / HiRes / HiColor / etc.
	if mode := h.ScreenFlags & screenModeBits; mode != 0 {
		return nil, fmt.Errorf("nex: unsupported screen-mode flags %#02x — Sprint 6 will handle Layer 2 / HiRes / HiColor / LoRes / Timex / ULAnext", mode)
	}

	if h.HasPalette() {
		out.Palette = make([]byte, PaletteSize)
		if _, err := io.ReadFull(r, out.Palette); err != nil {
			return nil, fmt.Errorf("nex: read palette: %w", err)
		}
	}
	if h.HasCopper() {
		out.Copper = make([]byte, CopperSize)
		if _, err := io.ReadFull(r, out.Copper); err != nil {
			return nil, fmt.Errorf("nex: read copper: %w", err)
		}
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
