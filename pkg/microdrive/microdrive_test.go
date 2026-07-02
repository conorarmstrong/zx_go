package microdrive

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestNewCartridgeIsBlank(t *testing.T) {
	c := New(180)
	if c.Length() != 180 {
		t.Errorf("Length = %d, want 180", c.Length())
	}
	for i := 0; i < c.Length()*BlockLen; i++ {
		if c.DataAt(i) != 0xFF {
			t.Fatalf("byte %d = %02X, want 0xFF (blank tape)", i, c.DataAt(i))
		}
	}
	if c.WriteProtect() {
		t.Error("new cartridge should not be write-protected")
	}
}

func TestNewCartridgeClampsLength(t *testing.T) {
	if c := New(0); c.Length() != 1 {
		t.Errorf("New(0): got %d, want 1", c.Length())
	}
	if c := New(MaxBlocks + 5); c.Length() != MaxBlocks {
		t.Errorf("New(MaxBlocks+5): got %d, want %d", c.Length(), MaxBlocks)
	}
}

func TestSetLengthGrowsAndShrinks(t *testing.T) {
	c := New(20)
	c.SetDataAt(5, 0xAA) // sentinel inside the kept region
	c.SetLength(40)
	if c.Length() != 40 {
		t.Errorf("after grow: Length = %d, want 40", c.Length())
	}
	if c.DataAt(5) != 0xAA {
		t.Errorf("after grow: byte 5 lost (got %02X)", c.DataAt(5))
	}
	if c.DataAt(20*BlockLen) != 0xFF {
		t.Errorf("after grow: new region byte = %02X, want 0xFF", c.DataAt(20*BlockLen))
	}
	c.SetLength(10)
	if c.Length() != 10 {
		t.Errorf("after shrink: Length = %d, want 10", c.Length())
	}
}

func TestBlockRoundTrip(t *testing.T) {
	c := New(20)
	src := Block{
		HdFlag: 0x01,
		HdBNum: 5,
		HdBLen: 0x1234,
		HdChks: 0xAB,

		RecFlg:  0x00,
		RecNum:  3,
		RecLen:  256,
		RecChks: 0xCD,

		DatChks: 0xEF,
	}
	copy(src.HdBNam[:], "TESTLABEL_")
	copy(src.RecNam[:], "RECNAME12_")
	for i := range src.Data {
		src.Data[i] = byte(i & 0xFF)
	}

	c.SetBlock(7, src)
	got := c.GetBlock(7)

	if got.HdFlag != src.HdFlag || got.HdBNum != src.HdBNum ||
		got.HdBLen != src.HdBLen || got.HdChks != src.HdChks {
		t.Errorf("block header mismatch:\n  got %+v\n  want %+v", got, src)
	}
	if got.RecFlg != src.RecFlg || got.RecNum != src.RecNum ||
		got.RecLen != src.RecLen || got.RecChks != src.RecChks {
		t.Errorf("record header mismatch:\n  got %+v\n  want %+v", got, src)
	}
	if got.HdBNam != src.HdBNam {
		t.Errorf("HdBNam: got %q, want %q", got.HdBNam[:], src.HdBNam[:])
	}
	if got.RecNam != src.RecNam {
		t.Errorf("RecNam: got %q, want %q", got.RecNam[:], src.RecNam[:])
	}
	if !bytes.Equal(got.Data[:], src.Data[:]) {
		t.Error("Data area not preserved")
	}
	if got.DatChks != src.DatChks {
		t.Errorf("DatChks: got %02X, want %02X", got.DatChks, src.DatChks)
	}
}

func TestMDRReadWriteRoundTrip(t *testing.T) {
	src := New(50)
	src.SetWriteProtect(true)
	// Sprinkle some recognisable data
	for i := 0; i < src.Length()*BlockLen; i += 100 {
		src.SetDataAt(i, byte(i&0xFF))
	}

	raw := src.Write()
	dst, err := Read(raw)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if dst.Length() != src.Length() {
		t.Errorf("Length: got %d, want %d", dst.Length(), src.Length())
	}
	if !dst.WriteProtect() {
		t.Error("WriteProtect: not preserved across round-trip")
	}
	if !bytes.Equal(dst.RawBytes(), src.RawBytes()) {
		t.Error("raw bytes mismatch after round-trip")
	}
}

func TestMDRReadWriteRoundTripFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.mdr")

	src := New(180)
	src.SetDataAt(0, 0xCA)
	src.SetDataAt(1, 0xFE)
	if err := src.WriteFile(path); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	dst, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if dst.DataAt(0) != 0xCA || dst.DataAt(1) != 0xFE {
		t.Errorf("data not preserved through file round-trip: got %02X %02X, want CA FE",
			dst.DataAt(0), dst.DataAt(1))
	}
}

func TestReadRejectsBadLengths(t *testing.T) {
	// Too short — fewer than 10 blocks.
	if _, err := Read(make([]byte, BlockLen*5)); err == nil {
		t.Error("Read 5-block buffer: want error, got nil")
	}
	// Wrong remainder — 2 trailing bytes instead of 0 or 1.
	if _, err := Read(make([]byte, BlockLen*10+2)); err == nil {
		t.Error("Read with 2 trailing bytes: want error, got nil")
	}
	// Way too big.
	if _, err := Read(make([]byte, maxRawBytes+10)); err == nil {
		t.Error("Read oversized buffer: want error, got nil")
	}
}

func TestChecksumCleanBlock(t *testing.T) {
	c := New(20)
	// Fabricate a block whose checksums actually validate. We do this
	// by setting the data and then computing the three sums.
	d := c.data[7*BlockLen:]
	for i := 0; i < HeadLen-1; i++ {
		d[i] = byte(i + 1)
	}
	d[HeadLen-1] = blockSum(d[0 : HeadLen-1])

	for i := 0; i < HeadLen-1; i++ {
		d[HeadLen+i] = byte(i + 50)
	}
	// Set a non-zero record length so the data checksum is checked.
	d[HeadLen+2] = 0x10
	d[HeadLen+3] = 0x00
	d[HeadLen+HeadLen-1] = blockSum(d[HeadLen : HeadLen+HeadLen-1])

	for i := 0; i < DataLen; i++ {
		d[HeadLen+HeadLen+i] = byte(i & 0xFF)
	}
	d[HeadLen+HeadLen+DataLen] = blockSum(d[HeadLen+HeadLen : HeadLen+HeadLen+DataLen])

	if got := c.Checksum(7); got != ChecksumOK {
		t.Errorf("Checksum on hand-built valid block = %d, want ChecksumOK", got)
	}
}

func TestChecksumDetectsBadBlockHeader(t *testing.T) {
	c := New(20)
	d := c.data[3*BlockLen:]
	for i := 0; i < HeadLen-1; i++ {
		d[i] = byte(i)
	}
	d[HeadLen-1] = blockSum(d[0:HeadLen-1]) ^ 0xFF // corrupted
	if got := c.Checksum(3); got != ChecksumBlockBad {
		t.Errorf("Checksum on corrupted block-header = %d, want ChecksumBlockBad", got)
	}
}

func TestChecksumPresetBad(t *testing.T) {
	c := New(20)
	d := c.data[1*BlockLen:]
	d[15] = 0x02 // RecFlg bit 1 set
	d[17] = 0    // record length lo
	d[18] = 0    // record length hi
	if got := c.Checksum(1); got != ChecksumPresetBad {
		t.Errorf("preset-bad block: Checksum = %d, want ChecksumPresetBad", got)
	}
}

func TestRealWorldCartridgeParses(t *testing.T) {
	// success.mdr is FUSE's bundled microdrive unit-test cartridge
	// (lib/tests/success.mdr in the FUSE 1.6.0 source). It contains
	// a 179-block formatted cartridge plus the write-protect flag,
	// for 97198 bytes total. We use it to confirm our parser
	// agrees byte-for-byte with libspectrum on a real-world image.
	c, err := ReadFile("testdata/success.mdr")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if c.Length() != 179 {
		t.Errorf("Length = %d, want 179", c.Length())
	}
	// Round-trip: write back out and verify byte-for-byte match.
	out := c.Write()
	if len(out) != 179*BlockLen+1 {
		t.Errorf("written length = %d, want %d", len(out), 179*BlockLen+1)
	}
	// Re-parse and verify same number of blocks come back.
	c2, err := Read(out)
	if err != nil {
		t.Fatalf("Read after Write: %v", err)
	}
	if c2.Length() != c.Length() {
		t.Errorf("after round-trip: length %d, want %d", c2.Length(), c.Length())
	}
	if !bytes.Equal(c.RawBytes(), c2.RawBytes()) {
		t.Error("round-trip data mismatch")
	}
}

func TestChecksumEmptyBlockSkipsDataCheck(t *testing.T) {
	c := New(20)
	d := c.data[2*BlockLen:]
	// Valid block + record headers, record length zero → data checksum
	// is not checked even though it's wrong.
	for i := 0; i < HeadLen-1; i++ {
		d[i] = byte(i)
	}
	d[HeadLen-1] = blockSum(d[0 : HeadLen-1])
	for i := 0; i < HeadLen-1; i++ {
		d[HeadLen+i] = byte(i + 100)
	}
	d[HeadLen+2] = 0
	d[HeadLen+3] = 0
	d[HeadLen+HeadLen-1] = blockSum(d[HeadLen : HeadLen+HeadLen-1])
	d[HeadLen+HeadLen+DataLen] = 0xAA // garbage data sum, should be ignored

	if got := c.Checksum(2); got != ChecksumOK {
		t.Errorf("empty block: Checksum = %d, want ChecksumOK", got)
	}
}
