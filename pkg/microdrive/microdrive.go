// Package microdrive implements the .mdr cartridge format used by the
// Sinclair ZX Interface 1 microdrive — Sinclair's tape-loop mass storage
// for the ZX Spectrum 48K. A cartridge holds up to 254 fixed-size blocks;
// each block is laid out as:
//
//	+----------+----------+----------+----------+
//	| 15 bytes | 15 bytes |  512 b   |  1 byte  |
//	|  block   |  record  |   data   |   data   |
//	|  header  |  header  |          | checksum |
//	+----------+----------+----------+----------+
//	      \                                /
//	       `------ 543 bytes total -------'
//
// .mdr files are the raw concatenated blocks plus an optional trailing
// byte that flags the cartridge as write-protected. The format is the
// same one libspectrum uses (microdrive.c) so cartridges from FUSE,
// Spectaculator, and the World of Spectrum archive all interoperate.
package microdrive

import (
	"errors"
	"fmt"
	"os"
)

// Block-layout constants — match libspectrum.h.in:924-931.
const (
	HeadLen     = 15  // length of one header (block or record)
	DataLen     = 512 // length of the user data area
	BlockLen    = HeadLen + HeadLen + DataLen + 1
	MaxBlocks   = 254
	maxRawBytes = MaxBlocks * BlockLen // 137,922
)

// ErrCorrupt is returned when a .mdr buffer doesn't have the right shape.
var ErrCorrupt = errors.New("microdrive: corrupt .mdr image")

// Cartridge is a single microdrive cartridge: a flat byte array sized
// to a whole number of blocks, plus a write-protect flag.
//
// Layout matches libspectrum's libspectrum_microdrive struct
// (microdrive.c:32-38) so the on-disk .mdr format round-trips byte-for-byte.
type Cartridge struct {
	// data is sized exactly cartridgeLen × BlockLen. We don't always
	// allocate maxRawBytes to keep memory usage proportional to the
	// declared size — most cartridges in the wild are 180 blocks or so.
	data []byte

	cartridgeLen byte
	writeProtect bool
}

// New allocates an empty cartridge with the given length in blocks.
// All bytes are initialised to 0xFF — the value an unwritten microdrive
// tape returns when read, matching the if1_mdr_new behaviour at
// fuse-1.6.0/peripherals/if1.c:1084-1107.
func New(blocks int) *Cartridge {
	if blocks < 1 {
		blocks = 1
	}
	if blocks > MaxBlocks {
		blocks = MaxBlocks
	}
	c := &Cartridge{
		data:         make([]byte, blocks*BlockLen),
		cartridgeLen: byte(blocks),
	}
	for i := range c.data {
		c.data[i] = 0xFF
	}
	return c
}

// Length returns the cartridge length in blocks.
func (c *Cartridge) Length() int { return int(c.cartridgeLen) }

// SetLength resizes the cartridge to the given number of blocks.
// Existing data is preserved up to the new length; any new bytes are
// filled with 0xFF.
func (c *Cartridge) SetLength(blocks int) {
	if blocks < 1 {
		blocks = 1
	}
	if blocks > MaxBlocks {
		blocks = MaxBlocks
	}
	newSize := blocks * BlockLen
	if newSize > len(c.data) {
		grow := make([]byte, newSize)
		copy(grow, c.data)
		for i := len(c.data); i < newSize; i++ {
			grow[i] = 0xFF
		}
		c.data = grow
	} else if newSize < len(c.data) {
		c.data = c.data[:newSize]
	}
	c.cartridgeLen = byte(blocks)
}

// WriteProtect reports whether the cartridge is currently write-protected.
func (c *Cartridge) WriteProtect() bool { return c.writeProtect }

// SetWriteProtect toggles the write-protect flag.
func (c *Cartridge) SetWriteProtect(wp bool) { c.writeProtect = wp }

// DataAt reads the raw byte at the given absolute offset within the
// cartridge (0 .. Length()*BlockLen - 1). Used by the IF1 controller
// when streaming bytes off the tape head.
func (c *Cartridge) DataAt(offset int) byte {
	if offset < 0 || offset >= len(c.data) {
		return 0xFF
	}
	return c.data[offset]
}

// SetDataAt writes the raw byte at the given absolute offset.
func (c *Cartridge) SetDataAt(offset int, b byte) {
	if offset < 0 || offset >= len(c.data) {
		return
	}
	c.data[offset] = b
}

// RawBytes returns a copy of the cartridge's data area sized to its
// declared length. The returned slice is independent of the cartridge.
func (c *Cartridge) RawBytes() []byte {
	out := make([]byte, len(c.data))
	copy(out, c.data)
	return out
}

// Block is the parsed view of one cartridge block. The fields exactly
// mirror libspectrum_microdrive_block (microdrive.c:40-57).
type Block struct {
	HdFlag byte     // bit 0 = 1 → header block, 0 → data block
	HdBNum byte     // block number 1..254
	HdBLen uint16   // unused
	HdBNam [10]byte // cartridge label, NUL-padded
	HdChks byte     // header checksum

	RecFlg  byte     // bit 0 = 0 → data, plus other flags
	RecNum  byte     // data block number
	RecLen  uint16   // record length 0..512
	RecNam  [10]byte // record / file name, NUL-padded
	RecChks byte     // record header checksum

	Data    [DataLen]byte
	DatChks byte
}

// GetBlock parses block N (0-based) into a Block struct. Mirrors
// libspectrum_microdrive_get_block (microdrive.c:122-148).
func (c *Cartridge) GetBlock(which int) Block {
	var b Block
	if which < 0 || which >= int(c.cartridgeLen) {
		return b
	}
	d := c.data[which*BlockLen:]
	b.HdFlag = d[0]
	b.HdBNum = d[1]
	b.HdBLen = uint16(d[2]) | uint16(d[3])<<8
	copy(b.HdBNam[:], d[4:14])
	b.HdChks = d[14]

	b.RecFlg = d[15]
	b.RecNum = d[16]
	b.RecLen = uint16(d[17]) | uint16(d[18])<<8
	copy(b.RecNam[:], d[19:29])
	b.RecChks = d[29]

	copy(b.Data[:], d[30:30+DataLen])
	b.DatChks = d[30+DataLen]
	return b
}

// SetBlock writes a Block back into block N (0-based). Mirrors
// libspectrum_microdrive_set_block (microdrive.c:150-175).
func (c *Cartridge) SetBlock(which int, b Block) {
	if which < 0 || which >= int(c.cartridgeLen) {
		return
	}
	d := c.data[which*BlockLen:]
	d[0] = b.HdFlag
	d[1] = b.HdBNum
	d[2] = byte(b.HdBLen)
	d[3] = byte(b.HdBLen >> 8)
	copy(d[4:14], b.HdBNam[:])
	d[14] = b.HdChks

	d[15] = b.RecFlg
	d[16] = b.RecNum
	d[17] = byte(b.RecLen)
	d[18] = byte(b.RecLen >> 8)
	copy(d[19:29], b.RecNam[:])
	d[29] = b.RecChks

	copy(d[30:30+DataLen], b.Data[:])
	d[30+DataLen] = b.DatChks
}

// ChecksumStatus is the result of validating one block's three
// checksum fields. Values match libspectrum_microdrive_checksum's
// return codes at microdrive.c:178-250.
type ChecksumStatus int

const (
	ChecksumOK        ChecksumStatus = 0  // all three sums match
	ChecksumPresetBad ChecksumStatus = -1 // block flagged as bad at format time
	ChecksumBlockBad  ChecksumStatus = 1  // block-header sum mismatch
	ChecksumRecordBad ChecksumStatus = 2  // record-header sum mismatch
	ChecksumDataBad   ChecksumStatus = 3  // data-area sum mismatch
)

// Checksum verifies the three checksum fields of block N against the
// stored data using the same Z80-style ADC loop as the IF1 ROM
// (microdrive.c:185-250). Returns ChecksumOK on success, or one of
// the other ChecksumStatus values to identify which sum is wrong.
//
// The "preset bad" check matches a convention used by the IF1 ROM:
// blocks with RecFlg bit 1 set AND a zero record length are
// permanently marked unusable, regardless of their actual checksums.
func (c *Cartridge) Checksum(which int) ChecksumStatus {
	if which < 0 || which >= int(c.cartridgeLen) {
		return ChecksumPresetBad
	}
	base := which * BlockLen
	d := c.data[base:]

	// Preset-bad detection — see microdrive.c:219-222.
	if d[15]&2 != 0 && d[17] == 0 && d[18] == 0 {
		return ChecksumPresetBad
	}

	// Block header: walk 14 bytes (HeadLen-1), compare against d[14].
	sum := blockSum(d[0:14])
	if d[14] != sum {
		return ChecksumBlockBad
	}

	// Record header: walk next 14 bytes, compare against d[29].
	sum = blockSum(d[15:29])
	if d[29] != sum {
		return ChecksumRecordBad
	}

	// Data checksum is irrelevant for empty/erased blocks
	// (d[17:19] = record length = 0).
	if d[17] == 0 && d[18] == 0 {
		return ChecksumOK
	}

	// Data area: 512 bytes starting at d[30], compared against d[30+DataLen].
	sum = blockSum(d[30 : 30+DataLen])
	if d[30+DataLen] != sum {
		return ChecksumDataBad
	}
	return ChecksumOK
}

// blockSum is the IF1 block checksum: a running 8-bit sum where each
// step does ADD then ADC #1, with the special case that a result of
// 0xFF (after the +1) wraps to 0x00 instead of 0xFF. Implements the
// Z80 sequence at microdrive.c:185-200.
//
//	LOOP-C: LD   A,E
//	        ADD  A,(HL)
//	        INC  HL
//	        ADC  A,#01
//	        JR   Z,LSTCHK
//	        DEC  A
//	LSTCHK: LD   E,A
func blockSum(data []byte) byte {
	var sum, carry uint16
	for _, b := range data {
		sum += uint16(b)
		if sum > 255 {
			carry = 1
			sum -= 256
		} else {
			carry = 0
		}
		sum += carry + 1
		if sum == 256 {
			sum -= 256
		} else {
			sum--
		}
	}
	return byte(sum)
}

// Read parses a .mdr byte slice into a fresh Cartridge. The buffer
// length must be a whole number of blocks (10..254) optionally plus
// one trailing byte for the write-protect flag. Mirrors
// libspectrum_microdrive_mdr_read (microdrive.c:254-283).
func Read(data []byte) (*Cartridge, error) {
	if len(data) < BlockLen*10 || len(data)%BlockLen > 1 || len(data) > maxRawBytes+1 {
		return nil, fmt.Errorf("%w: length %d invalid (must be N*543 or N*543+1, 10 ≤ N ≤ 254)", ErrCorrupt, len(data))
	}
	dataBytes := len(data) - len(data)%BlockLen
	c := &Cartridge{
		data:         make([]byte, dataBytes),
		cartridgeLen: byte(dataBytes / BlockLen),
	}
	copy(c.data, data[:dataBytes])
	if len(data)%BlockLen == 1 {
		c.writeProtect = data[dataBytes] != 0
	}
	return c, nil
}

// ReadFile reads a .mdr file from disk and parses it.
func ReadFile(path string) (*Cartridge, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("microdrive: read %s: %w", path, err)
	}
	return Read(data)
}

// Write serialises the cartridge to a fresh byte slice in the .mdr
// format: raw block data followed by one byte for the write-protect
// flag. Mirrors libspectrum_microdrive_mdr_write (microdrive.c:286-297).
func (c *Cartridge) Write() []byte {
	out := make([]byte, len(c.data)+1)
	copy(out, c.data)
	if c.writeProtect {
		out[len(c.data)] = 1
	}
	return out
}

// WriteFile serialises the cartridge to disk at the given path.
func (c *Cartridge) WriteFile(path string) error {
	return os.WriteFile(path, c.Write(), 0644)
}
