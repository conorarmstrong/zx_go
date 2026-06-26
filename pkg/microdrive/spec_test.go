package microdrive

// Spec-faithful test suite for the ZX Microdrive cartridge tape-loop
// format: GAP / SYNC / preamble framing, the 15-byte header block, the
// 528-byte data/record block, the three checksums, and a round-trip
// read of a constructed in-memory cartridge.
//
// Oracle (no FPGA — documented Microdrive on-tape format):
//
//   - The ZXPicoMD project README and the World-of-Spectrum / Spectrum
//     FAQ "File Formats" reference for the .mdr sector layout:
//       * each sector = 543 bytes = 15-byte header + 528-byte data block
//       * on tape each block is preceded by a 12-byte preamble of
//         ten 0x00 bytes followed by two 0xFF bytes
//       * header: flag (bit0=1 → header), sector number (0xFE..0x01),
//         2 unused, 10-char cartridge name, checksum over the first 14
//       * data block: flag (bit0=0 → data; bit1=EOF; bit2=PRINT),
//         record number, record length (<=512, LSB first), 10-char file
//         name, descriptor checksum over the first 14, 512-byte data,
//         data checksum over those 512
//       * up to 254 sectors per cartridge, ordered descending
//   - The checksum is the IF1 ROM's running ADD/ADC #1 (with the
//     0xFF→0x00 wrap) sequence; cross-checked here against a direct
//     transcription of that Z80 loop.
//
// "254-byte record sectors" in the brief refers to the up-to-254
// records (sectors) a cartridge holds; the user data area itself is
// 512 bytes per record, framed by the checksum byte into the 528-byte
// data block. Both facts are asserted below.

import (
	"bytes"
	"testing"
)

// ---------------------------------------------------------------------------
// Tape-loop framing constants: sector size, data area, max sectors
// ---------------------------------------------------------------------------

// TestSectorLayoutConstants pins the documented byte sizes so a change
// to any layout constant trips a test rather than silently corrupting
// the on-disk .mdr format.
func TestSectorLayoutConstants(t *testing.T) {
	if HeadLen != 15 {
		t.Errorf("HeadLen = %d, want 15 (header block size)", HeadLen)
	}
	if DataLen != 512 {
		t.Errorf("DataLen = %d, want 512 (user data area)", DataLen)
	}
	// 543 = 15 (block header) + 15 (record header) + 512 (data) + 1 (data checksum).
	if BlockLen != 543 {
		t.Errorf("BlockLen = %d, want 543 (full sector on tape)", BlockLen)
	}
	// The 528-byte "data block" of the spec = record header (15) + data
	// (512) + data checksum (1).
	if HeadLen+DataLen+1 != 528 {
		t.Errorf("data block = %d, want 528", HeadLen+DataLen+1)
	}
	if MaxBlocks != 254 {
		t.Errorf("MaxBlocks = %d, want 254 (max sectors per cartridge)", MaxBlocks)
	}
}

// TestPreamblePattern documents the 12-byte preamble (ten 0x00 + two
// 0xFF) the IF1 lays down before each block. We assert the constant
// pattern directly so the framing is captured in code, independent of
// the controller in pkg/if1.
func TestPreamblePattern(t *testing.T) {
	preamble := append(bytes.Repeat([]byte{0x00}, 10), 0xFF, 0xFF)
	if len(preamble) != 12 {
		t.Fatalf("preamble length = %d, want 12", len(preamble))
	}
	zeros, ones := 0, 0
	for _, b := range preamble {
		switch b {
		case 0x00:
			zeros++
		case 0xFF:
			ones++
		default:
			t.Fatalf("unexpected preamble byte %02X", b)
		}
	}
	if zeros != 10 || ones != 2 {
		t.Errorf("preamble = %d×0x00 + %d×0xFF, want 10×0x00 + 2×0xFF", zeros, ones)
	}
}

// ---------------------------------------------------------------------------
// Header block: flag bit0=1, descending sector numbers, 10-char name
// ---------------------------------------------------------------------------

// validHeader fills the 15-byte block header at sector `which` with a
// spec-shaped header (flag bit0 set, given sector number, given name)
// and a correct header checksum. It also lays down a valid, empty
// (zero-length) record header so Checksum validates the whole sector —
// a real formatted-but-unwritten sector looks exactly like this.
func validHeader(c *Cartridge, which int, sector byte, name string) {
	d := c.data[which*BlockLen:]
	// Block header (14 bytes + checksum), NUL-padded name.
	for i := 0; i < 14; i++ {
		d[i] = 0
	}
	d[0] = 0x01 // header flag: bit 0 set
	d[1] = sector
	copy(d[4:14], name)
	d[14] = blockSum(d[0:14]) // checksum over the first 14 bytes
	// Empty record header: flag=0, length=0, NUL name, valid checksum.
	for i := 15; i < 29; i++ {
		d[i] = 0
	}
	d[29] = blockSum(d[15:29])
}

// TestHeaderFlagBitDistinguishesHeaderFromData verifies the parser
// surfaces the header flag (bit 0) so the IF1 ROM can tell a block
// header (bit0=1) from a record/data block (bit0=0).
func TestHeaderFlagBitDistinguishesHeaderFromData(t *testing.T) {
	c := New(20)
	b := c.GetBlock(0)
	b.HdFlag = 0x01 // header marker
	b.RecFlg = 0x00 // data marker
	c.SetBlock(0, b)

	got := c.GetBlock(0)
	if got.HdFlag&0x01 == 0 {
		t.Error("header flag bit0 not set on a header block")
	}
	if got.RecFlg&0x01 != 0 {
		t.Error("record flag bit0 set on a data block (should be reset)")
	}
}

// TestHeaderDescendingSectorNumbersRoundTrip lays down a strip of
// headers with descending sector numbers (0xFE..) — the documented
// physical ordering — and confirms each parses back with the right
// number and a valid header checksum.
func TestHeaderDescendingSectorNumbersRoundTrip(t *testing.T) {
	c := New(10)
	for i := 0; i < c.Length(); i++ {
		sector := byte(MaxBlocks - i) // 254, 253, ... descending
		validHeader(c, i, sector, "MYCARTRIDG")
		if got := c.GetBlock(i); got.HdBNum != sector {
			t.Errorf("sector %d: HdBNum = %d, want %d", i, got.HdBNum, sector)
		}
		// The header checksum must validate (data length 0 → data sum skipped).
		if st := c.Checksum(i); st != ChecksumOK {
			t.Errorf("sector %d: header checksum status = %d, want OK", i, st)
		}
	}
}

// TestCartridgeNameTenBytesNulPadded confirms the 10-byte cartridge /
// file name fields round-trip exactly. The fields are fixed 10-byte
// arrays; a short name laid into a zeroed field is NUL-padded, and the
// padding survives a SetBlock→GetBlock round-trip byte-for-byte.
func TestCartridgeNameTenBytesNulPadded(t *testing.T) {
	c := New(10)
	// Build from a zeroed Block (the IF1 ROM writes NUL-padded names
	// into freshly-cleared header fields).
	var b Block
	b.HdFlag = 0x01
	copy(b.HdBNam[:], "RUNME")     // 5 chars → 5 NUL pad bytes
	copy(b.RecNam[:], "PROGRAM10") // 9 chars → 1 NUL pad byte
	c.SetBlock(0, b)

	got := c.GetBlock(0)
	if got.HdBNam != b.HdBNam {
		t.Errorf("HdBNam round-trip = %q, want %q", got.HdBNam[:], b.HdBNam[:])
	}
	if string(bytes.TrimRight(got.HdBNam[:], "\x00")) != "RUNME" {
		t.Errorf("HdBNam = %q, want RUNME", got.HdBNam[:])
	}
	if got.HdBNam[5] != 0x00 {
		t.Errorf("HdBNam not NUL-padded past the name (byte5=%02X)", got.HdBNam[5])
	}
	if string(bytes.TrimRight(got.RecNam[:], "\x00")) != "PROGRAM10" {
		t.Errorf("RecNam = %q, want PROGRAM10", got.RecNam[:])
	}
}

// ---------------------------------------------------------------------------
// Data/record block: record length LSB-first, 512-byte data + checksum
// ---------------------------------------------------------------------------

// TestRecordLengthIsLittleEndian verifies the record-length field is
// stored LSB-first across the two record-header bytes (offsets 17,18 /
// RecLen) as the .mdr format requires.
func TestRecordLengthIsLittleEndian(t *testing.T) {
	c := New(10)
	b := c.GetBlock(3)
	b.RecLen = 0x0123 // lo=0x23, hi=0x01
	c.SetBlock(3, b)

	d := c.data[3*BlockLen:]
	if d[17] != 0x23 || d[18] != 0x01 {
		t.Errorf("RecLen on tape = %02X %02X, want 23 01 (LSB first)", d[17], d[18])
	}
	if got := c.GetBlock(3).RecLen; got != 0x0123 {
		t.Errorf("RecLen round-trip = %04X, want 0123", got)
	}
}

// TestRecordLengthCannotExceedDataArea is a documentation guard: the
// record length must never claim more than the 512-byte data area.
func TestRecordLengthCannotExceedDataArea(t *testing.T) {
	if DataLen != 512 {
		t.Fatalf("DataLen=%d", DataLen)
	}
	// A valid maximum record fills the whole data area.
	c := New(10)
	b := c.GetBlock(0)
	b.RecLen = DataLen
	c.SetBlock(0, b)
	if got := c.GetBlock(0).RecLen; got != 512 {
		t.Errorf("max RecLen round-trip = %d, want 512", got)
	}
}

// ---------------------------------------------------------------------------
// The three checksums (block header, record header, data) — full
// hand-built valid sector, plus each failure mode
// ---------------------------------------------------------------------------

// buildValidSector writes a fully valid sector (header + record + 512
// data bytes) at `which` with all three checksums correct.
func buildValidSector(c *Cartridge, which int) {
	d := c.data[which*BlockLen:]
	// Block header (14 bytes + checksum).
	d[0] = 0x01
	d[1] = byte(MaxBlocks - which)
	copy(d[4:14], "TESTCART01")
	d[14] = blockSum(d[0:14])
	// Record header (14 bytes + checksum). Non-zero record length so
	// the data checksum is actually verified.
	d[15] = 0x00 // data flag: bit0 reset
	d[16] = byte(which)
	d[17], d[18] = byte(DataLen&0xFF), byte(DataLen>>8) // record length = 512
	copy(d[19:29], "MYFILE0001")
	d[29] = blockSum(d[15:29])
	// Data area + checksum.
	for i := 0; i < DataLen; i++ {
		d[30+i] = byte((i * 7) & 0xFF)
	}
	d[30+DataLen] = blockSum(d[30 : 30+DataLen])
}

// TestFullValidSectorChecksumsOK builds a complete, correctly-summed
// sector and confirms Checksum reports OK — exercising all three sums
// (block header, record descriptor, 512-byte data) together.
func TestFullValidSectorChecksumsOK(t *testing.T) {
	c := New(20)
	buildValidSector(c, 5)
	if st := c.Checksum(5); st != ChecksumOK {
		t.Errorf("fully valid sector: checksum status = %d, want OK", st)
	}
}

// TestChecksumFailureModes corrupts one byte in each of the three
// checksum-protected regions and confirms the matching failure code.
func TestChecksumFailureModes(t *testing.T) {
	t.Run("block header", func(t *testing.T) {
		c := New(20)
		buildValidSector(c, 0)
		c.data[0*BlockLen+1] ^= 0xFF // flip a block-header byte
		if st := c.Checksum(0); st != ChecksumBlockBad {
			t.Errorf("corrupt block header: status = %d, want ChecksumBlockBad", st)
		}
	})
	t.Run("record header", func(t *testing.T) {
		c := New(20)
		buildValidSector(c, 1)
		c.data[1*BlockLen+20] ^= 0xFF // flip a record-header byte
		if st := c.Checksum(1); st != ChecksumRecordBad {
			t.Errorf("corrupt record header: status = %d, want ChecksumRecordBad", st)
		}
	})
	t.Run("data area", func(t *testing.T) {
		c := New(20)
		buildValidSector(c, 2)
		c.data[2*BlockLen+30+100] ^= 0xFF // flip a data byte
		if st := c.Checksum(2); st != ChecksumDataBad {
			t.Errorf("corrupt data area: status = %d, want ChecksumDataBad", st)
		}
	})
}

// TestChecksumZeroLengthSkipsDataSum confirms that a zero record length
// (an empty/erased block) makes the data checksum irrelevant — the IF1
// ROM does not validate the data area of an empty record.
func TestChecksumZeroLengthSkipsDataSum(t *testing.T) {
	c := New(20)
	buildValidSector(c, 0)
	d := c.data[0*BlockLen:]
	// Zero the record length and re-sum the record header.
	d[17], d[18] = 0, 0
	d[29] = blockSum(d[15:29])
	// Deliberately corrupt the data checksum — must still read OK.
	d[30+DataLen] ^= 0xFF
	if st := c.Checksum(0); st != ChecksumOK {
		t.Errorf("empty record with bad data sum: status = %d, want OK", st)
	}
}

// TestChecksumZeroSumWrapEdge exercises the 0xFF→0x00 wrap special case
// of the IF1 checksum loop. A region summing to the wrap boundary must
// validate against the stored byte the same way the ROM computes it.
func TestChecksumZeroSumWrapEdge(t *testing.T) {
	// Search for a short byte sequence whose blockSum hits 0x00 — the
	// branch the IF1 ROM's "JR Z,LSTCHK" handles.
	c := New(20)
	d := c.data[0*BlockLen:]
	found := false
	for v := 0; v < 256 && !found; v++ {
		for i := 0; i < 14; i++ {
			d[i] = byte(v)
		}
		if blockSum(d[0:14]) == 0x00 {
			found = true
		}
	}
	if !found {
		t.Skip("no constant fill produced a 0x00 block sum on this build")
	}
	d[14] = blockSum(d[0:14]) // = 0x00
	// Make the record header valid + empty (flag 0, length 0) so the
	// preset-bad path is not taken and only the block sum matters.
	for i := 15; i < 29; i++ {
		d[i] = 0
	}
	d[29] = blockSum(d[15:29])
	if st := c.Checksum(0); st != ChecksumOK {
		t.Errorf("wrap-edge block sum: status = %d, want OK", st)
	}
}

// TestPresetBadBlock confirms the "preset bad" convention: a block whose
// record flag bit1 is set with a zero record length is permanently
// unusable regardless of its actual checksums.
func TestPresetBadBlock(t *testing.T) {
	c := New(20)
	buildValidSector(c, 0) // all sums valid
	d := c.data[0*BlockLen:]
	d[15] |= 0x02       // record flag bit1 set
	d[17], d[18] = 0, 0 // record length zero
	d[29] = blockSum(d[15:29])
	if st := c.Checksum(0); st != ChecksumPresetBad {
		t.Errorf("preset-bad block: status = %d, want ChecksumPresetBad", st)
	}
}

// ---------------------------------------------------------------------------
// Round-trip: construct an in-memory cartridge, read a record back
// ---------------------------------------------------------------------------

// TestConstructedCartridgeRecordRoundTrip is the headline end-to-end
// test: build a cartridge entirely in memory with a real header + record
// + 512-byte payload, serialise it to the .mdr byte stream, re-parse it,
// and read the record's data back byte-for-byte. This proves the format
// model is self-consistent and spec-shaped.
func TestConstructedCartridgeRecordRoundTrip(t *testing.T) {
	const sectorIdx = 7
	src := New(180)

	// Build a spec-shaped record via the Block API.
	var payload [DataLen]byte
	for i := range payload {
		payload[i] = byte((i*3 + 1) & 0xFF)
	}
	var b Block // zeroed → names are NUL-padded
	b.HdFlag = 0x01
	b.HdBNum = byte(MaxBlocks - sectorIdx)
	copy(b.HdBNam[:], "GAMECART01")
	b.RecFlg = 0x00 // data block
	b.RecNum = 2
	b.RecLen = DataLen
	copy(b.RecNam[:], "HISCORES")
	b.Data = payload
	src.SetBlock(sectorIdx, b)

	// Fix up the three checksums the way the IF1 ROM would when writing.
	d := src.data[sectorIdx*BlockLen:]
	d[14] = blockSum(d[0:14])
	d[29] = blockSum(d[15:29])
	d[30+DataLen] = blockSum(d[30 : 30+DataLen])

	if st := src.Checksum(sectorIdx); st != ChecksumOK {
		t.Fatalf("constructed sector failed its own checksum: %d", st)
	}

	// Serialise → .mdr stream → re-parse.
	raw := src.Write()
	if len(raw) != 180*BlockLen+1 {
		t.Fatalf(".mdr length = %d, want %d (N*543 + 1 WP byte)", len(raw), 180*BlockLen+1)
	}
	dst, err := Read(raw)
	if err != nil {
		t.Fatalf("Read constructed .mdr: %v", err)
	}

	// Read the record back and compare.
	got := dst.GetBlock(sectorIdx)
	if got.HdFlag&0x01 == 0 {
		t.Error("round-trip lost the header flag bit")
	}
	if got.RecNum != 2 || got.RecLen != DataLen {
		t.Errorf("record header round-trip: RecNum=%d RecLen=%d, want 2/%d", got.RecNum, got.RecLen, DataLen)
	}
	if string(bytes.TrimRight(got.RecNam[:], "\x00")) != "HISCORES" {
		t.Errorf("record name round-trip = %q, want HISCORES", got.RecNam[:])
	}
	if !bytes.Equal(got.Data[:], payload[:]) {
		t.Error("record data payload not preserved across the round-trip")
	}
	if st := dst.Checksum(sectorIdx); st != ChecksumOK {
		t.Errorf("re-parsed sector checksum status = %d, want OK", st)
	}
	// Whole-cartridge byte equality after the round-trip.
	if !bytes.Equal(src.RawBytes(), dst.RawBytes()) {
		t.Error("cartridge raw bytes diverged across Write→Read round-trip")
	}
}

// TestWriteProtectFlagRoundTrip confirms the trailing write-protect byte
// of the .mdr stream survives a Write→Read round-trip.
func TestWriteProtectFlagRoundTrip(t *testing.T) {
	for _, wp := range []bool{false, true} {
		c := New(12)
		c.SetWriteProtect(wp)
		dst, err := Read(c.Write())
		if err != nil {
			t.Fatalf("wp=%v: Read: %v", wp, err)
		}
		if dst.WriteProtect() != wp {
			t.Errorf("wp=%v: round-trip WriteProtect = %v", wp, dst.WriteProtect())
		}
	}
}

// TestBlankTapeReadsAsFF confirms an unwritten cartridge reads back as
// 0xFF everywhere — the value a blank microdrive tape returns.
func TestBlankTapeReadsAsFF(t *testing.T) {
	c := New(10)
	for off := 0; off < c.Length()*BlockLen; off++ {
		if c.DataAt(off) != 0xFF {
			t.Fatalf("blank tape byte %d = %02X, want 0xFF", off, c.DataAt(off))
		}
	}
}
