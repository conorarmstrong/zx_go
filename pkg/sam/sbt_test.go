package sam

import (
	"bytes"
	"strings"
	"testing"
)

// The ROM's BOOT reads one sector to $8000 and compares the four bytes at
// $8100..$8103 (samcoupe.rom:0x5967). The first data sector is a 9-byte file
// header followed by the file's own bytes, so those four bytes are content
// offsets 247..250: content shorter than 251 bytes cannot carry the signature
// at all.
func TestLoadSBTRefusesTooShort(t *testing.T) {
	_, err := LoadSBT(make([]byte, 250), "tiny")
	if err == nil {
		t.Fatal("LoadSBT accepted 250 bytes; the boot signature needs 251")
	}
	if !strings.Contains(err.Error(), "251") {
		t.Errorf("error should name the 251-byte minimum, got %q", err)
	}
}

// Without the signature the ROM does RST $08 / DEFB $35 (samcoupe.rom:0x5976),
// which is error 53, "No DOS" (message table base 0x7651, index 53 at 0x7807).
// Refusing at load time is the only way the user learns which file is at fault.
func TestLoadSBTRefusesWrongSignature(t *testing.T) {
	content := make([]byte, 512)
	copy(content[sbtSigOffset:], "XOOT")
	_, err := LoadSBT(content, "notboot.sbt")
	if err == nil {
		t.Fatal("LoadSBT accepted content with no boot signature")
	}
	if !strings.Contains(err.Error(), "53") || !strings.Contains(err.Error(), "No DOS") {
		t.Errorf("error should say the ROM would answer 53 No DOS, got %q", err)
	}
}

// The ROM compares with XOR (HL) / AND $5F (samcoupe.rom:0x5971) against the
// literal 42 4F 4F D4 at $FB94 (samcoupe.rom:0x7B94), so bits 5 and 7 of each
// byte are ignored: case does not matter and the terminator bit on the last
// character is invisible. samdos2 relies on that, carrying "BOO"+$D4.
func TestLoadSBTSignatureMasksBits5And7(t *testing.T) {
	accept := []string{"BOOT", "boot", "BOO\xD4", "boo\xf4"}
	for _, sig := range accept {
		content := make([]byte, 512)
		copy(content[sbtSigOffset:], sig)
		if _, err := LoadSBT(content, "x.sbt"); err != nil {
			t.Errorf("signature %q should be accepted (AND $5F), got %v", sig, err)
		}
	}
	content := make([]byte, 512)
	copy(content[sbtSigOffset:], "BOOU")
	if _, err := LoadSBT(content, "x.sbt"); err == nil {
		t.Error(`"BOOU" differs from "BOOT" in bit 0, which AND $5F keeps: must be refused`)
	}
}

// sbtContent builds SBT content of n bytes carrying the boot signature, filled
// with a position-dependent pattern so a misplaced byte is visible.
func sbtContent(n int) []byte {
	c := make([]byte, n)
	for i := range c {
		c[i] = byte(i*7 + 1)
	}
	copy(c[sbtSigOffset:], "BOOT")
	return c
}

// mustLoadSBT builds the disk or fails the test.
func mustLoadSBT(t *testing.T, content []byte, name string) *Disk {
	t.Helper()
	d, err := LoadSBT(content, name)
	if err != nil {
		t.Fatalf("LoadSBT(%s): %v", name, err)
	}
	return d
}

// mustSector reads a sector or fails the test.
func mustSector(t *testing.T, d *Disk, cyl, head, sector int) []byte {
	t.Helper()
	s, ok := d.ReadSector(cyl, head, sector)
	if !ok {
		t.Fatalf("ReadSector(%d,%d,%d) out of range", cyl, head, sector)
	}
	return s
}

// An 800K MGT disk keeps cylinders 0..3 of side 0 for the directory, leaving
// 1560 data sectors (which is exactly what the 195-byte allocation map at
// directory offset 15 addresses). The first carries 501 content bytes after
// the 9-byte header, the rest 510 each.
func TestLoadSBTRefusesOversizeContent(t *testing.T) {
	if _, err := LoadSBT(sbtContent(sbtCapacity), "big.sbt"); err != nil {
		t.Fatalf("content of exactly %d bytes should fit: %v", sbtCapacity, err)
	}
	_, err := LoadSBT(sbtContent(sbtCapacity+1), "toobig.sbt")
	if err == nil {
		t.Fatalf("content of %d bytes should not fit an 800K disk", sbtCapacity+1)
	}
	if !strings.Contains(err.Error(), "795591") {
		t.Errorf("error should name the %d-byte capacity, got %q", sbtCapacity, err)
	}
}

// BOOT seeks to track 4 and reads sector 1 (samcoupe.rom:0x591E, LD DE,$0401,
// with the seek loop at 0x5923-0x5937) on side 0, since it addresses the drive
// through ports $E0-$E3 and the side is port bit 2 (io.go:61). So the file has
// to start there, and the directory tracks before it stay untouched.
func TestSBTFirstDataSectorAtTrack4Sector1(t *testing.T) {
	content := sbtContent(512)
	d := mustLoadSBT(t, content, "x.sbt")

	sec := mustSector(t, d, 4, 0, 1)
	if len(sec) != mgtSectorSize {
		t.Fatalf("sector is %d bytes, want %d", len(sec), mgtSectorSize)
	}
	if got := sec[sbtFileHeaderLen : sbtFileHeaderLen+sbtFirstPayload]; !bytes.Equal(got, content[:sbtFirstPayload]) {
		t.Errorf("first sector payload is not content[0:%d]", sbtFirstPayload)
	}
	last := mustSector(t, d, 3, 0, 10)
	for i, b := range last {
		if b != 0 {
			t.Fatalf("directory track 3 sector 10 byte %d = %#02x, should be untouched", i, b)
		}
	}
}

// The ROM tests $8100..$8103 after loading the sector to $8000
// (samcoupe.rom:0x5967: LD DE,$80FF, then INC DE before each compare). This
// pins the byte positions inside the sector rather than inside the content, so
// a wrong header length shows up here even if the payload copy looks right.
func TestSBTSignatureLandsAtSectorOffset0x100(t *testing.T) {
	d := mustLoadSBT(t, sbtContent(512), "x.sbt")
	sec := mustSector(t, d, 4, 0, 1)
	for i, want := range bootLiteral {
		if got := sec[0x100+i] ^ want; got&0x5F != 0 {
			t.Errorf("sector byte %#x = %#02x, does not match the ROM literal %#02x under AND $5F",
				0x100+i, sec[0x100+i], want)
		}
	}
}

// The bootstrap loads the second sector at $81FE (samdos2.sbt:0, LD HL,$81FE /
// LD DE,$0402), two bytes back over the previous sector's chain link, and
// carries on stepping back two bytes a sector. So every sector after the first
// contributes 510 bytes and has no header of its own.
func TestSBTLaterSectorsCarry510Bytes(t *testing.T) {
	content := sbtContent(sbtFirstPayload + sbtNextPayload + 7)
	d := mustLoadSBT(t, content, "x.sbt")

	second := mustSector(t, d, 4, 0, 2)
	want := content[sbtFirstPayload : sbtFirstPayload+sbtNextPayload]
	if !bytes.Equal(second[:sbtNextPayload], want) {
		t.Errorf("second sector payload is not content[%d:%d]", sbtFirstPayload, sbtFirstPayload+sbtNextPayload)
	}
	third := mustSector(t, d, 4, 0, 3)
	if !bytes.Equal(third[:7], content[sbtFirstPayload+sbtNextPayload:]) {
		t.Errorf("third sector should hold the trailing 7 bytes, got %#v", third[:7])
	}
}

// The bootstrap follows the chain from the end of the block it just read:
// samdos2.sbt:0x66 is DEC HL / LD E,(HL) / DEC HL / LD D,(HL), so the sector
// number is the last byte and the track number the one before it.
func TestSBTSectorLinksPointAtTheNextSector(t *testing.T) {
	d := mustLoadSBT(t, sbtContent(sbtFirstPayload+sbtNextPayload+7), "x.sbt")
	for _, tc := range []struct {
		sector       int
		wantT, wantS byte
	}{{1, 4, 2}, {2, 4, 3}} {
		sec := mustSector(t, d, 4, 0, tc.sector)
		if sec[510] != tc.wantT || sec[511] != tc.wantS {
			t.Errorf("sector %d link = {%d,%d}, want {%d,%d}",
				tc.sector, sec[510], sec[511], tc.wantT, tc.wantS)
		}
	}
}

// A zero track-and-sector pair is what stops the load: samdos2.sbt:0x6A is
// LD A,D / OR E / JR NZ, so the loop runs again only while the link is
// non-zero. A last sector that points anywhere would read on for ever.
func TestSBTFinalSectorLinkIsZero(t *testing.T) {
	for _, n := range []int{sbtMinLen, sbtFirstPayload + 1, sbtFirstPayload + 2*sbtNextPayload} {
		content := sbtContent(n)
		d := mustLoadSBT(t, content, "x.sbt")
		last := sbtSectorCount(n) - 1
		cyl, head, sector := dataSectorAddr(last)
		sec := mustSector(t, d, cyl, head, sector)
		if sec[510] != 0 || sec[511] != 0 {
			t.Errorf("%d-byte file: last sector (%d,%d,%d) link = {%d,%d}, want {0,0}",
				n, cyl, head, sector, sec[510], sec[511])
		}
	}
}

// A track holds ten sectors, so the eleventh continues on the next cylinder,
// still on side 0. SAMDOS 2's own twenty-sector chain spans cylinders 4 and 5
// of side 0 and its bootstrap only ever addresses ports $E0-$E3 (side 0).
func TestSBTChainCrossesToTheNextCylinder(t *testing.T) {
	content := sbtContent(sbtFirstPayload + 10*sbtNextPayload)
	if got := sbtSectorCount(len(content)); got != 11 {
		t.Fatalf("test needs an 11-sector file, got %d", got)
	}
	d := mustLoadSBT(t, content, "x.sbt")

	if sec := mustSector(t, d, 4, 0, 10); sec[510] != 5 || sec[511] != 1 {
		t.Errorf("track 4 sector 10 link = {%d,%d}, want {5,1}", sec[510], sec[511])
	}
	eleventh := mustSector(t, d, 5, 0, 1)
	want := content[sbtFirstPayload+9*sbtNextPayload:]
	if !bytes.Equal(eleventh[:len(want)], want) {
		t.Error("the eleventh sector should be at cylinder 5 head 0 sector 1")
	}
}

// Side 0 gives 76 data cylinders (4..79), so data sector 760 is the first on
// side 1, back at cylinder 0. SAMDOS 2 names a side-1 track with bit 7 set: on
// a disk whose side-0 data sectors were all marked used it wrote a start track
// of $80 and a start sector of 1.
func TestSBTChainCrossesToSide1(t *testing.T) {
	const side0Sectors = (mgtCyls - sbtFirstTrack) * mgt800KSectors // 760
	content := sbtContent(sbtFirstPayload + side0Sectors*sbtNextPayload)
	if got := sbtSectorCount(len(content)); got != side0Sectors+1 {
		t.Fatalf("test needs a %d-sector file, got %d", side0Sectors+1, got)
	}
	d := mustLoadSBT(t, content, "x.sbt")

	if sec := mustSector(t, d, 79, 0, 10); sec[510] != 0x80 || sec[511] != 1 {
		t.Errorf("last side-0 sector link = {%#02x,%d}, want {0x80,1}", sec[510], sec[511])
	}
	first := mustSector(t, d, 0, 1, 1)
	want := content[sbtFirstPayload+(side0Sectors-1)*sbtNextPayload:]
	if !bytes.Equal(first[:len(want)], want) {
		t.Error("data sector 760 should be at cylinder 0 head 1 sector 1")
	}
}

// BOOT loads the sector to $8000 (samcoupe.rom:0x593F) and jumps to $8009
// (samcoupe.rom:0x597B), so nine bytes have to precede the file's own code.
// Those nine are the SAM CODE header, and its field layout was read back from
// SAMDOS 2 itself: SAVE "bigfile" CODE 40000,20000 wrote
// 13 20 0e 40 9c ff ff 01 61: type, length mod 16384, $8000 + start offset in
// its page, $FFFF, length div 16384, and a page byte that measured $5F, $60,
// $61 for pages 0, 1 and 2.
func TestSBTFirstSectorHeader(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want []byte
	}{
		{512, []byte{0x13, 0x00, 0x02, 0x09, 0x80, 0xFF, 0xFF, 0x00, 0x5F}},
		{20000, []byte{0x13, 0x20, 0x0E, 0x09, 0x80, 0xFF, 0xFF, 0x01, 0x5F}},
	} {
		d := mustLoadSBT(t, sbtContent(tc.n), "x.sbt")
		got := mustSector(t, d, 4, 0, 1)[:sbtFileHeaderLen]
		if !bytes.Equal(got, tc.want) {
			t.Errorf("%d-byte file header = % 02x, want % 02x", tc.n, got, tc.want)
		}
	}
}

// A disk carrying no directory entry is not a disk a DOS can live with: booted
// from it, SAMDOS 2 saw the whole data area as free and its first SAVE
// allocated cylinder 4 sector 1, writing straight over the boot file. So the
// file gets an entry, in the first slot, as CODE. SAMDOS 2's own SAVE "bigfile"
// CODE wrote 13 62 69 67 66 69 6c 65 20 20 20 at directory offsets 0..10: type
// $13, then ten characters padded with spaces.
func TestSBTDirectoryEntryNamesTheFileAsCode(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"gamefile.sbt", "gamefile  "},
		{"/somewhere/Manic.SBT", "Manic     "},
		{"averylongtitle.sbt", "averylongt"},
	} {
		d := mustLoadSBT(t, sbtContent(512), tc.name)
		dir := mustSector(t, d, 0, 0, 1)
		if dir[0] != 19 {
			t.Errorf("%s: directory type = %d, want 19 (CODE)", tc.name, dir[0])
		}
		if got := string(dir[1:11]); got != tc.want {
			t.Errorf("%s: directory name = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// SAMDOS 2 stores the sector count big-endian: it wrote 00 03 at directory
// offsets 11..12 for a 1234-byte file and 00 28 (40) for a 20000-byte one,
// which is exactly the 501-then-510 split this builder uses.
func TestSBTDirectorySectorCountIsBigEndian(t *testing.T) {
	for _, tc := range []struct {
		n      int
		hi, lo byte
	}{{1234, 0, 3}, {600, 0, 2}, {20000, 0, 40}} {
		d := mustLoadSBT(t, sbtContent(tc.n), "x.sbt")
		dir := mustSector(t, d, 0, 0, 1)
		if dir[11] != tc.hi || dir[12] != tc.lo {
			t.Errorf("%d-byte file: sector count = {%d,%d}, want {%d,%d}",
				tc.n, dir[11], dir[12], tc.hi, tc.lo)
		}
	}
}

// Where the file starts, in the same track/sector form as a chain link. It has
// to agree with the ROM's own hard-coded target (samcoupe.rom:0x591E), and
// SAMDOS 2 wrote 04 01 at directory offsets 13..14 for a file it placed there.
func TestSBTDirectoryRecordsTheStartSector(t *testing.T) {
	d := mustLoadSBT(t, sbtContent(512), "x.sbt")
	dir := mustSector(t, d, 0, 0, 1)
	if dir[13] != sbtFirstTrack || dir[14] != sbtFirstSector {
		t.Errorf("start = {%d,%d}, want {%d,%d}", dir[13], dir[14], sbtFirstTrack, sbtFirstSector)
	}
}

// The 195 bytes at directory offset 15 are a bitmap of the 1560 data sectors,
// counted the way the chain runs: side 0 cylinders 4..79, then side 1
// cylinders 0..79, LSB first inside each byte. SAMDOS 2 wrote 07 for a
// three-sector file and ff ff ff ff ff 00 for a forty-sector one.
func TestSBTDirectoryAllocationMap(t *testing.T) {
	three := mustSector(t, mustLoadSBT(t, sbtContent(1234), "x.sbt"), 0, 0, 1)
	if three[sbtMapAt] != 0x07 {
		t.Errorf("three-sector map byte = %#02x, want 0x07", three[sbtMapAt])
	}
	for i := 1; i < sbtMapLen; i++ {
		if three[sbtMapAt+i] != 0 {
			t.Fatalf("three-sector map byte %d = %#02x, should be clear", i, three[sbtMapAt+i])
		}
	}

	forty := mustSector(t, mustLoadSBT(t, sbtContent(20000), "x.sbt"), 0, 0, 1)
	for i := 0; i < 5; i++ {
		if forty[sbtMapAt+i] != 0xFF {
			t.Errorf("forty-sector map byte %d = %#02x, want 0xff", i, forty[sbtMapAt+i])
		}
	}
	if forty[sbtMapAt+5] != 0 {
		t.Errorf("forty-sector map byte 5 = %#02x, want 0", forty[sbtMapAt+5])
	}

	// The first sector of side 1 is data sector 760, so bit 0 of map byte 95.
	const side0Sectors = (mgtCyls - sbtFirstTrack) * mgt800KSectors
	over := mustSector(t, mustLoadSBT(t, sbtContent(sbtFirstPayload+side0Sectors*sbtNextPayload), "x.sbt"), 0, 0, 1)
	if over[sbtMapAt+95] != 0x01 {
		t.Errorf("map byte 95 = %#02x, want 0x01 (the first side-1 sector)", over[sbtMapAt+95])
	}
}

// SAMDOS 2 repeats the file's 9-byte header inside the directory entry, at
// offsets 211..219: for SAVE "bigfile" CODE 40000,20000 both the entry there
// and the first data sector's opening bytes read 13 20 0e 40 9c ff ff 01 61.
func TestSBTDirectoryRepeatsTheFileHeader(t *testing.T) {
	d := mustLoadSBT(t, sbtContent(20000), "x.sbt")
	dir := mustSector(t, d, 0, 0, 1)
	data := mustSector(t, d, 4, 0, 1)
	if !bytes.Equal(dir[211:220], data[:sbtFileHeaderLen]) {
		t.Errorf("directory header copy = % 02x, first sector header = % 02x",
			dir[211:220], data[:sbtFileHeaderLen])
	}
}

// The rest of the entry is a template read back from SAMDOS 2, not a set of
// fields whose meaning is established here. Saving two files of different
// names, lengths and addresses showed offsets 220..230 always eleven spaces,
// 231..235 and 242..244 always $FF, and 236..241 a second copy of the header's
// page, start offset, page count and length in that order.
func TestSBTDirectoryTailMatchesTheObservedTemplate(t *testing.T) {
	d := mustLoadSBT(t, sbtContent(20000), "x.sbt")
	dir := mustSector(t, d, 0, 0, 1)
	h := mustSector(t, d, 4, 0, 1)[:sbtFileHeaderLen]

	if got := string(dir[220:231]); got != "           " {
		t.Errorf("offsets 220..230 = %q, want eleven spaces", got)
	}
	for _, at := range []int{231, 232, 233, 234, 235, 242, 243, 244} {
		if dir[at] != 0xFF {
			t.Errorf("offset %d = %#02x, want 0xff", at, dir[at])
		}
	}
	want := []byte{h[8], h[3], h[4], h[7], h[1], h[2]}
	if !bytes.Equal(dir[236:242], want) {
		t.Errorf("offsets 236..241 = % 02x, want % 02x", dir[236:242], want)
	}
	for at := 245; at < 256; at++ {
		if dir[at] != 0 {
			t.Errorf("offset %d = %#02x, want 0", at, dir[at])
		}
	}
}

// The disk carries exactly one file, so the other seventy-nine slots stay as
// blankMGT left them. An entry whose type byte is zero is a free slot, which is
// how the empty second half of sector 1 read on the disk SAMDOS 2 booted from.
func TestSBTLeavesTheOtherDirectorySlotsEmpty(t *testing.T) {
	d := mustLoadSBT(t, sbtContent(20000), "x.sbt")
	if second := mustSector(t, d, 0, 0, 1)[sbtEntryLen:]; !bytes.Equal(second, make([]byte, sbtEntryLen)) {
		t.Error("the second slot of directory sector 1 should be empty")
	}
	for _, s := range []int{2, 10} {
		if sec := mustSector(t, d, 0, 0, s); !bytes.Equal(sec, make([]byte, mgtSectorSize)) {
			t.Errorf("directory sector %d should be empty", s)
		}
	}
	if sec := mustSector(t, d, 3, 0, 10); !bytes.Equal(sec, make([]byte, mgtSectorSize)) {
		t.Error("the last directory sector should be empty")
	}
}

// End to end on the real ROM: BOOT reads cylinder 4 head 0 sector 1 to $8000,
// checks the four bytes at $8100 and jumps to $8009 (samcoupe.rom:0x591E,
// 0x5967, 0x597B). The file here sets the border and parks in a one-instruction
// loop, so both the jump and the sector placement are visible from outside.
func TestSBTBootsOnTheRealROM(t *testing.T) {
	content := sbtContent(512)
	copy(content, []byte{
		0x3E, 0x05, // LD A,5
		0xD3, 0xFE, // OUT ($FE),A  -- border
		0x18, 0xFE, // JR $  -- park here at $800D
	})

	m, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	m.InsertDisk(0, mustLoadSBT(t, content, "boot.sbt"))
	runFrames(m, 600)
	tapKey(m, 7, 0) // SPACE -> BASIC
	runFrames(m, 300)
	for _, k := range []struct{ row, bit int }{{7, 4}, {5, 1}, {5, 1}, {2, 4}} { // B O O T
		tapKey(m, k.row, k.bit)
	}
	tapKey(m, 6, 0) // ENTER
	runFrames(m, 200)

	if m.CPU.PC != 0x800D {
		t.Errorf("PC = %#04x, want 0x800d: BOOT did not reach the file's own code", m.CPU.PC)
	}
	if got := m.BorderColour(); got != 5 {
		t.Errorf("border = %d, want 5: the booted code did not run", got)
	}
}
