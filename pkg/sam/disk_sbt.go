package sam

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SBT files: a raw SAM file presented as a bootable disk.
//
// An SBT is NOT a disk image, which is why it needs its own path through here.
// It is a single SAM CODE file, of the kind you would copy onto a blank disk
// and boot, so loading one means BUILDING the disk around it: a standard 800K
// MGT carrying exactly that one file, with a directory entry the DOS will
// auto-execute.
//
// The layout is SAMDOS's, and the 9-byte file header is the SAM Technical
// Manual's (type, size16, offset16, unused16, pages, startpage):
//
//	Geometry        80 cylinders, 2 heads, 10 sectors, 512-byte sectors
//	Directory       the first 4 cylinders of head 0
//	Data sectors    510 bytes of payload, then a 2-byte chain pointer:
//	                  byte 510  next cylinder, bit 7 set if the next head is 1
//	                  byte 511  next sector, 1-based; 0 ends the chain
//	Chain order     sector ascending within a cylinder, cylinder ascending
//	                within a head, then on to the next head
//
// The capacity falls out of the chain and is the check that the reading of it
// is right: 76 cylinders on head 0 plus 80 on head 1, ten sectors each, 510
// payload bytes apiece, less the 9-byte header — 795591 bytes.
//
// Only the format is taken from elsewhere; a format is a fact about bytes. The
// construction below is this package's own, and is deliberately different in
// kind: the whole image is built up front into the flat sector store the SAM's
// WD1772 already addresses, rather than sectors being synthesised on each read.

const (
	// sbtFileTypeCode is SAMDOS's file type for CODE.
	sbtFileTypeCode = 19

	// sbtHeaderSize is the SAM file header prepended to the raw bytes.
	sbtHeaderSize = 9

	// sbtLoadAddress is where a CODE file of this shape loads and runs: the
	// start of the SAM's second 16K page.
	sbtLoadAddress = 0x8000

	// sbtDirectoryTracks is how many cylinders of head 0 SAMDOS reserves for
	// the directory.
	sbtDirectoryTracks = 4

	// sbtSectorPayload is how much of a 512-byte sector holds file data. The
	// last two bytes are the chain pointer, and counting the full 512 is the
	// mistake that makes a sector count too small and truncates the file.
	sbtSectorPayload = mgtSectorSize - 2

	// sbtDirEntryOffset is where the directory entry's fields sit that describe
	// the file to the ROM's auto-run path.
	sbtDirStartPage   = 236
	sbtDirStartOffset = 237
	sbtDirPageCount   = 239
	sbtDirLengthMod   = 240
	sbtDirAutoExec    = 242

	// sbtFilename is the name the entry carries, exactly ten characters. It
	// begins with "auto" because that is what makes a DOS boot it.
	sbtFilename = "autoExec  "
)

// sbtMaxFileSize is the largest raw file the chain can carry.
//
// The chain visits every cylinder of head 0 after the directory (76) and every
// cylinder of head 1 (80), ten sectors each at 510 payload bytes, and the
// 9-byte header goes in front of the file rather than beside it: 795591 bytes.
//
// That figure is the check that the chain order above was read correctly. It is
// derived here rather than written down, so a change to the geometry or to the
// directory size cannot leave it stale.
const sbtChainTracks = (mgtCyls - sbtDirectoryTracks) + mgtCyls
const sbtMaxFileSize = sbtChainTracks*mgt800KSectors*sbtSectorPayload - sbtHeaderSize

// LoadDiskFile parses a SAM disk image, using the file name to resolve the one
// format that has nothing in its bytes to identify it.
//
// SBT has no signature at all — it is a raw file, and any byte sequence is a
// valid one — so it can only be recognised by its extension. That is why
// LoadDisk cannot be given the job: with no name to go on it would have to
// treat every unrecognised file as an SBT, and a corrupt disk image would
// quietly become a bootable disk containing itself.
func LoadDiskFile(name string, data []byte) (*Disk, error) {
	if strings.EqualFold(filepath.Ext(name), ".sbt") {
		return LoadSBT(data)
	}
	return LoadDisk(data)
}

// LoadSBT builds a bootable 800K MGT disk carrying data as its only file.
//
// The disk is write-protected. There is no image behind it to write back to, so
// a guest saving to it would appear to succeed and lose the data on eject.
func LoadSBT(data []byte) (*Disk, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("sam: empty SBT file, nothing to boot")
	}
	if len(data) > sbtMaxFileSize {
		return nil, fmt.Errorf("sam: SBT file is too large (%d bytes; a SAM disk holds %d)",
			len(data), sbtMaxFileSize)
	}

	// The file as SAMDOS stores it: the SAM file header, then the bytes.
	stored := make([]byte, sbtHeaderSize+len(data))
	writeSAMFileHeader(stored[:sbtHeaderSize], len(data))
	copy(stored[sbtHeaderSize:], data)

	sectorCount := (len(stored) + sbtSectorPayload - 1) / sbtSectorPayload

	d := blankMGT()
	d.writeProtect = true
	writeSBTDirEntry(d, len(data), sectorCount)
	if err := writeSBTChain(d, stored, sectorCount); err != nil {
		return nil, err
	}
	return d, nil
}

// writeSAMFileHeader fills the 9-byte header SAMDOS puts in front of a file.
func writeSAMFileHeader(h []byte, fileLen int) {
	h[0] = sbtFileTypeCode
	h[1] = byte(fileLen)               // length mod 16384, low
	h[2] = byte(fileLen>>8) & 0x3F     // length mod 16384, high
	h[3] = byte(sbtLoadAddress & 0xFF) // start address, low
	h[4] = byte(sbtLoadAddress >> 8)   // start address, high
	h[5] = 0xFF                        // unused
	h[6] = 0xFF                        // unused
	h[7] = byte(fileLen>>14) & 0x1F    // length in 16K pages
	h[8] = 1                           // first page
}

// writeSBTDirEntry writes the single directory entry at cylinder 0, head 0,
// sector 1.
func writeSBTDirEntry(d *Disk, fileLen, sectorCount int) {
	e := make([]byte, mgtSectorSize)

	e[0] = sbtFileTypeCode
	copy(e[1:11], sbtFilename)

	// Sector count is big-endian here, unlike every length in the file header.
	e[11] = byte(sectorCount >> 8)
	e[12] = byte(sectorCount)

	// Where the file starts. Bit 7 of the track byte is the head, the same
	// encoding the chain pointers use.
	e[13] = sbtDirectoryTracks
	e[14] = 1

	// The sector address bitmap: one bit per sector the file occupies.
	for i := 0; i < sectorCount/8; i++ {
		e[15+i] = 0xFF
	}
	if rem := sectorCount % 8; rem != 0 {
		e[15+sectorCount/8] = byte(1<<uint(rem)) - 1
	}

	// What the ROM needs to page the file in and run it.
	e[sbtDirStartPage] = 1
	e[sbtDirStartOffset] = byte(sbtLoadAddress & 0xFF)
	e[sbtDirStartOffset+1] = byte(sbtLoadAddress >> 8)
	e[sbtDirPageCount] = byte(fileLen>>14) & 0x1F
	e[sbtDirLengthMod] = byte(fileLen)
	e[sbtDirLengthMod+1] = byte(fileLen>>8) & 0x3F

	// Auto-execute with normal paging, then the address to jump to.
	e[sbtDirAutoExec] = 2
	e[sbtDirAutoExec+1] = byte(sbtLoadAddress & 0xFF)
	e[sbtDirAutoExec+2] = byte(sbtLoadAddress >> 8)

	d.WriteSectorForce(0, 0, 1, e)
}

// writeSBTChain lays the stored file out across the disk, sector by sector,
// writing each sector's pointer to the next.
//
// The walk and the pointers are generated together, from one cursor, so they
// cannot disagree. Deriving the placement and the chain separately is how a
// file ends up written to sectors the chain never visits — readable disk,
// truncated file, and nothing to show for it until the guest runs out of data.
func writeSBTChain(d *Disk, stored []byte, sectorCount int) error {
	cyl, head, sector := sbtDirectoryTracks, 0, 1

	for i := 0; i < sectorCount; i++ {
		buf := make([]byte, mgtSectorSize)
		lo := i * sbtSectorPayload
		hi := lo + sbtSectorPayload
		if hi > len(stored) {
			hi = len(stored)
		}
		copy(buf, stored[lo:hi])

		// Advance the cursor, then record where it went. The last sector keeps
		// a zero pointer, which is what ends the chain.
		nextCyl, nextHead, nextSector := sbtNext(cyl, head, sector)
		if i < sectorCount-1 {
			if nextSector == 0 {
				return fmt.Errorf("sam: SBT needs %d sectors, more than the chain can reach",
					sectorCount)
			}
			buf[sbtSectorPayload] = byte(nextCyl)
			if nextHead == 1 {
				buf[sbtSectorPayload] |= 0x80
			}
			buf[sbtSectorPayload+1] = byte(nextSector)
		}

		if !d.WriteSectorForce(cyl, head, sector, buf) {
			return fmt.Errorf("sam: SBT ran off the disk at cylinder %d head %d sector %d",
				cyl, head, sector)
		}
		cyl, head, sector = nextCyl, nextHead, nextSector
	}
	return nil
}

// sbtNext steps the chain cursor: sector ascending within a cylinder, cylinder
// ascending within a head, then on to the next head. A zero sector means the
// walk has come back round to the directory and there is nowhere left to go.
func sbtNext(cyl, head, sector int) (int, int, int) {
	sector++
	if sector <= mgt800KSectors {
		return cyl, head, sector
	}
	sector = 1
	cyl++
	if cyl < mgtCyls {
		return cyl, head, sector
	}
	cyl = 0
	head++
	if head < mgtHeads {
		return cyl, head, sector
	}
	return 0, 0, 0 // back at the directory: the disk is full
}

// WriteSectorForce writes a sector regardless of the write-protect flag.
//
// Building an SBT disk sets write-protect before the sectors go in, because the
// flag belongs to the finished disk rather than to its construction. A guest
// still cannot write to it; this is the builder, not the controller.
func (d *Disk) WriteSectorForce(cyl, head, sector int, buf []byte) bool {
	off, ok := d.offset(cyl, head, sector)
	if !ok {
		return false
	}
	copy(d.data[off:off+d.sectorSize], buf)
	return true
}
