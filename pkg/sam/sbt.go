package sam

import (
	"fmt"
	"strings"
)

// sbtSigOffset is where the boot signature sits inside an SBT's own bytes.
//
// The ROM's BOOT reads one 512-byte sector to $8000 and compares $8100..$8103
// (samcoupe.rom:0x5967: LD DE,$80FF / LD HL,$FB94 / LD B,$04, then INC DE
// before each compare, so the first byte tested is $8100). The sector opens
// with the file's 9-byte header, so $8100 is content offset 0x100-9 = 247.
const sbtSigOffset = 247

// sbtMinLen is the shortest content that can carry the four signature bytes.
const sbtMinLen = sbtSigOffset + 4

// The data area of an 800K MGT disk and how much of a file each sector holds.
//
// Cylinders 0..3 of side 0 are the directory, so 1560 of the disk's 1600
// sectors carry data, which is exactly what the 195-byte allocation map at
// directory offset 15 addresses (195 x 8 = 1560 bits).
//
// The first sector gives 9 bytes to the file header and 2 to the chain link,
// leaving 501; every later sector is loaded 2 bytes back over the previous
// one's link (samdos2 bootstrap: LD HL,$81FE, then DEC HL / LD E,(HL) /
// DEC HL / LD D,(HL)), so it contributes 510.
const (
	sbtDirTracks     = 4 // cylinders 0..3 of side 0
	sbtDataSectors   = (mgtCyls*mgtHeads - sbtDirTracks) * mgt800KSectors
	sbtFileHeaderLen = 9
	sbtLinkLen       = 2
	sbtFirstPayload  = mgtSectorSize - sbtFileHeaderLen - sbtLinkLen // 501
	sbtNextPayload   = mgtSectorSize - sbtLinkLen                    // 510
	sbtCapacity      = sbtFirstPayload + (sbtDataSectors-1)*sbtNextPayload
)

// Where BOOT looks: samcoupe.rom:0x591E loads DE with $0401 (D = track 4,
// E = sector 1) and the seek loop at 0x5923-0x5937 steps until the WD1772's
// track register reads 4. It drives the drive through ports $E0-$E3, and the
// side is port bit 2 (io.go:61), so this is side 0.
const (
	sbtFirstTrack  = 4
	sbtFirstSector = 1
)

// LoadSBT builds a bootable 800K MGT disk around a SAM CODE file (.sbt).
//
// An SBT is not a disk image: it is the raw content of one SAM CODE file, with
// no header of its own, meant to be written to a disk and started with BOOT.
//
// No DOS is needed on the disk and none is supplied. BOOT does not read the
// directory: it seeks drive 1 to track 4, reads sector 1 of side 0 to $8000,
// compares the four bytes at $8100 against its own copy of the word BOOT, and
// jumps to $8009 (samcoupe.rom:0x591E, 0x5967, 0x597B). Error 53, "No DOS", is
// what it says when that comparison fails, which is why an SBT whose signature
// is missing is refused here by name rather than left to fail cryptically on
// the machine.
//
// The disk still gets a directory entry for the file. Without one a DOS booted
// from the disk sees the whole data area as free: SAMDOS 2, asked to save
// anything, allocated cylinder 4 sector 1 and wrote straight over the boot
// file.
func LoadSBT(content []byte, name string) (*Disk, error) {
	if len(content) < sbtMinLen {
		return nil, fmt.Errorf("sam: %s is %d bytes; an SBT needs at least %d "+
			"to carry the boot signature the ROM checks", name, len(content), sbtMinLen)
	}
	if len(content) > sbtCapacity {
		return nil, fmt.Errorf("sam: %s is %d bytes; an 800K SAM disk holds %d "+
			"in its %d data sectors", name, len(content), sbtCapacity, sbtDataSectors)
	}
	if !bootSignature(content[sbtSigOffset : sbtSigOffset+4]) {
		return nil, fmt.Errorf("sam: %s is not a bootable SBT: bytes %d..%d are %q, "+
			"not the BOOT signature, so the ROM would answer 53 No DOS",
			name, sbtSigOffset, sbtSigOffset+3, content[sbtSigOffset:sbtSigOffset+4])
	}
	d := blankMGT()
	header := sbtFileHeader(len(content))
	rest := content
	for i, total := 0, sbtSectorCount(len(content)); i < total; i++ {
		sec := make([]byte, mgtSectorSize)
		payload, at := sbtNextPayload, 0
		if i == 0 {
			payload, at = sbtFirstPayload, sbtFileHeaderLen
			copy(sec, header)
		}
		n := min(len(rest), payload)
		copy(sec[at:], rest[:n])
		rest = rest[n:]
		if i+1 < total {
			nc, nh, ns := dataSectorAddr(i + 1)
			sec[mgtSectorSize-2] = trackByte(nc, nh)
			sec[mgtSectorSize-1] = byte(ns)
		}
		cyl, head, sector := dataSectorAddr(i)
		// WriteSector refuses an out-of-range address or a write-protected
		// disk. Neither can happen to a disk this function just built, so a
		// refusal means the geometry or the blank's defaults changed underneath
		// it. Saying so beats handing back a half-written disk that boots into
		// the ROM's cryptic error 53 with nothing to explain it.
		if !d.WriteSector(cyl, head, sector, sec) {
			return nil, fmt.Errorf("sam: writing sector %d of %d to cyl %d head %d sector %d",
				i+1, total, cyl, head, sector)
		}
	}
	if !d.WriteSector(0, 0, 1, sbtDirectorySector(header, name, len(content))) {
		return nil, fmt.Errorf("sam: writing the directory sector")
	}
	return d, nil
}

// sbtDirectorySector builds the disk's first directory sector: the file in the
// first of its two 256-byte slots, the second left empty.
func sbtDirectorySector(header []byte, name string, length int) []byte {
	sec := make([]byte, mgtSectorSize)
	entry := sec[:sbtEntryLen]
	entry[0] = sbtCodeType
	copy(entry[1:11], samFileName(name))
	// The sector count is stored MSB first, unlike everything else the SAM
	// writes: SAMDOS 2 wrote 00 28 here for a forty-sector file.
	sectors := sbtSectorCount(length)
	entry[11] = byte(sectors >> 8)
	entry[12] = byte(sectors)
	cyl, head, sector := dataSectorAddr(0)
	entry[13] = trackByte(cyl, head)
	entry[14] = byte(sector)
	// The allocation map: one bit a data sector, in the order dataSectorAddr
	// walks them, LSB first inside each byte.
	for i := 0; i < sectors; i++ {
		entry[sbtMapAt+i/8] |= 1 << (i % 8)
	}
	copy(entry[sbtEntryHeaderAt:], header)
	// The tail of the entry is a template, not a set of understood fields.
	// SAMDOS 2 was asked to save files of different names, lengths and start
	// addresses; what follows is what it wrote every time, with the six bytes
	// that did vary taken from the header. No meaning is claimed for any of it.
	for i := 220; i <= 230; i++ {
		entry[i] = ' '
	}
	for _, at := range []int{231, 232, 233, 234, 235, 242, 243, 244} {
		entry[at] = 0xFF
	}
	entry[236] = header[8] // page
	entry[237] = header[3] // start offset, low
	entry[238] = header[4] // start offset, high
	entry[239] = header[7] // length in whole 16K pages
	entry[240] = header[1] // length remainder, low
	entry[241] = header[2] // length remainder, high
	return sec
}

// sbtEntryHeaderAt is where the entry repeats the file's 9-byte header.
const sbtEntryHeaderAt = 211

// sbtMapAt is where the 195-byte sector allocation map sits in an entry.
const (
	sbtMapAt  = 15
	sbtMapLen = sbtDataSectors / 8 // 195
)

// sbtEntryLen is a directory entry's size; two fit in a sector, and the four
// directory tracks hold eighty in all.
const sbtEntryLen = 256

// samNameLen is the directory's name field: ten characters, space padded.
const samNameLen = 10

// samFileName turns a host file name into the ten characters a SAM directory
// entry carries, dropping the directory part and the .sbt extension.
func samFileName(path string) []byte {
	base := path
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndex(base, "."); i > 0 {
		base = base[:i]
	}
	out := []byte(strings.Repeat(" ", samNameLen))
	copy(out, base)
	return out
}

// samPageSize is the SAM's paging granularity: a file header splits a length
// into whole 16K pages and a remainder, and gives its start address as an
// offset inside one page.
const samPageSize = 16384

// sbtCodeType is the SAM file type for CODE (docs/sam-coupe.md; SAMDOS 2 wrote
// $13 for every SAVE ... CODE observed).
const sbtCodeType = 19

// sbtBootPage is the page byte a SAM CODE header carries for a file whose
// start address lies in page 0.
//
// SAMDOS 2 was asked to save the same 600 bytes from three addresses and wrote
// $5F for 12000 (page 0), $60 for 20000 (page 1) and $61 for 40000 (page 2),
// so the byte is the page number with a fixed bias. What that bias means is
// not established here, only measured. Note that $5F is not a page number this
// machine could page in - internal RAM is 32 pages (memory.go:58) and ramPage
// masks to five bits - which is further reason to treat the byte as an opaque
// stored field rather than a page register value. Nothing in this package ever
// loads it into one.
//
// A BOOT file has no page of its own: the ROM reads the sector to $8000 in
// whatever page section C holds and jumps to $8009 (samcoupe.rom:0x593F,
// 0x597B) without ever looking at the header, so page 0 is the honest entry.
const sbtBootPage = 0x5F

// sbtEntryAddr is where BOOT starts the file: $8000 + the 9-byte header
// (samcoupe.rom:0x597B, JP $8009).
const sbtEntryAddr = 0x8000 + sbtFileHeaderLen

// sbtFileHeader builds the 9-byte SAM CODE header that precedes the file's own
// bytes in its first sector. The field layout is SAMDOS 2's own: asked to
// SAVE "bigfile" CODE 40000,20000 it wrote 13 20 0e 40 9c ff ff 01 61, which
// is type, length mod 16K, $8000 + offset in page, $FFFF, length div 16K,
// page. The $FFFF pair was the same for every file saved and is reproduced
// without a meaning being claimed for it.
func sbtFileHeader(length int) []byte {
	return []byte{
		sbtCodeType,
		byte(length % samPageSize), byte((length % samPageSize) >> 8),
		byte(sbtEntryAddr & 0xFF), byte(sbtEntryAddr >> 8),
		0xFF, 0xFF,
		byte(length / samPageSize),
		sbtBootPage,
	}
}

// sbtSectorCount is how many sectors a file of n bytes occupies. A file always
// takes at least one sector, even an empty one.
func sbtSectorCount(n int) int {
	if n <= sbtFirstPayload {
		return 1
	}
	return 1 + (n-sbtFirstPayload+sbtNextPayload-1)/sbtNextPayload
}

// dataSectorAddr maps a data sector index to its disk address. The data area
// runs from cylinder 4 of side 0, ten sectors to a track, one cylinder to the
// next. Side 0 runs out at cylinder 79, and the data area carries on at
// cylinder 0 of side 1.
func dataSectorAddr(i int) (cyl, head, sector int) {
	track, sector := i/mgt800KSectors, sbtFirstSector+i%mgt800KSectors
	side0 := mgtCyls - sbtFirstTrack
	if track < side0 {
		return sbtFirstTrack + track, 0, sector
	}
	return track - side0, 1, sector
}

// trackByte is how a track is named in a chain link and in a directory entry:
// the cylinder number with bit 7 set for side 1.
func trackByte(cyl, head int) byte {
	if head != 0 {
		return byte(cyl) | 0x80
	}
	return byte(cyl)
}

// bootLiteral is the ROM's own copy of the word, at samcoupe.rom:0x7B94
// ($FB94): 42 4F 4F D4, "BOOT" with bit 7 set on the last character as the
// ROM's keyword tables mark a terminator.
var bootLiteral = [4]byte{0x42, 0x4F, 0x4F, 0xD4}

// bootSignature reports whether four bytes match the ROM's BOOT literal the
// way the ROM matches it: XOR then AND $5F (samcoupe.rom:0x5971), which drops
// bit 5 (letter case) and bit 7 (the terminator marker) from the comparison.
func bootSignature(b []byte) bool {
	for i, want := range bootLiteral {
		if (b[i]^want)&0x5F != 0 {
			return false
		}
	}
	return true
}
