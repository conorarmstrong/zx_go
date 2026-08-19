package sam

import (
	"strings"
	"testing"
)

// SBT files.
//
// An SBT is not a disk image. It is a raw SAM CODE file that is meant to be
// copied onto a blank disk and booted, so what an emulator does with one is
// BUILD the disk around it: a standard 800K MGT carrying exactly one file, with
// a directory entry that auto-executes.
//
// The layout below is the format, not anyone's code. The 9-byte file header is
// the SAM Technical Manual's (type, size16, offset16, unused16, pages,
// startpage), and the rest is SAMDOS's on-disk structure:
//
//	Geometry              80 cylinders, 2 heads, 10 sectors, 512-byte sectors
//	Directory             the first 4 cylinders of head 0
//	Data sectors          510 bytes of payload, then a 2-byte chain pointer:
//	                        byte 510  next cylinder, bit 7 set if next head is 1
//	                        byte 511  next sector, 1-based
//	Chain order           sector ascending within a cylinder, cylinder
//	                      ascending within a head, then the next head
//	Directory entry       at cylinder 0, head 0, sector 1
//	  0        file type (19 = CODE)
//	  1..10    filename, space padded
//	  11..12   sector count, big-endian
//	  13       starting track, bit 7 = head
//	  14       starting sector
//	  15..     sector address bitmap, one bit per used sector
//	  236      starting page
//	  237..238 starting offset, little-endian
//	  239      length in 16K pages
//	  240..241 length mod 16384, little-endian
//	  242..244 auto-execute: 2, then the start address little-endian
//
// The capacity falls out of that and is the check that the layout is right:
// (76 + 80) tracks x 10 sectors x 510 payload bytes, less the 9-byte header,
// is 795591 bytes.

// sbtEntry reads the directory entry an SBT disk was built with.
func sbtEntry(t *testing.T, d *Disk) []byte {
	t.Helper()
	sec, ok := d.ReadSector(0, 0, 1)
	if !ok {
		t.Fatal("the directory sector is missing")
	}
	return sec
}

// A raw file becomes a bootable 800K MGT.
func TestAnSBTBecomesAnMGTDisk(t *testing.T) {
	d, err := LoadSBT(make([]byte, 1000))
	if err != nil {
		t.Fatalf("LoadSBT: %v", err)
	}
	cyls, heads, sectors, size := d.Geometry()
	if cyls != mgtCyls || heads != mgtHeads || sectors != mgt800KSectors || size != mgtSectorSize {
		t.Errorf("geometry = %dx%dx%d @%d, want a standard MGT", cyls, heads, sectors, size)
	}
	// SimCoupe treats these as read-only, and so should we: there is no file to
	// write the change back to, so a guest saving to this disk would appear to
	// succeed and lose the data on eject.
	if !d.WriteProtected() {
		t.Error("an SBT disk is writable: a guest's save would be silently discarded")
	}
}

// The directory entry names a CODE file that the DOS will auto-run.
func TestTheDirectoryEntryDescribesAnAutoExecutingCodeFile(t *testing.T) {
	const fileLen = 5000
	d, err := LoadSBT(make([]byte, fileLen))
	if err != nil {
		t.Fatalf("LoadSBT: %v", err)
	}
	e := sbtEntry(t, d)

	if e[0] != sbtFileTypeCode {
		t.Errorf("file type = %d, want %d (CODE)", e[0], sbtFileTypeCode)
	}
	name := string(e[1:11])
	if !strings.HasPrefix(strings.ToLower(name), "auto") {
		t.Errorf("filename = %q, want it to start with \"auto\" so the DOS boots it", name)
	}
	if len(name) != 10 {
		t.Errorf("filename field is %d bytes, want 10", len(name))
	}

	// The data starts on the first cylinder after the directory, sector 1.
	if e[13] != sbtDirectoryTracks {
		t.Errorf("starting track = %d, want %d", e[13], sbtDirectoryTracks)
	}
	if e[14] != 1 {
		t.Errorf("starting sector = %d, want 1", e[14])
	}

	// Auto-execute: type 2, then the address the file loads at.
	if e[242] != 2 {
		t.Errorf("auto-execute type = %d, want 2", e[242])
	}
	if got := int(e[243]) | int(e[244])<<8; got != sbtLoadAddress {
		t.Errorf("auto-execute address = %#04x, want %#04x", got, sbtLoadAddress)
	}
}

// The sector count is the file plus its header divided by the 510 bytes a
// sector actually carries, rounded up — not by 512. Using the full sector size
// under-counts and the DOS stops reading a sector early.
func TestTheSectorCountCoversTheHeaderAndRoundsUp(t *testing.T) {
	for _, fileLen := range []int{1, 500, 501, 510, 1000, 1020, 5000} {
		d, err := LoadSBT(make([]byte, fileLen))
		if err != nil {
			t.Fatalf("LoadSBT(%d): %v", fileLen, err)
		}
		e := sbtEntry(t, d)
		got := int(e[11])<<8 | int(e[12])
		total := fileLen + sbtHeaderSize
		want := (total + sbtSectorPayload - 1) / sbtSectorPayload
		if got != want {
			t.Errorf("file of %d bytes: sector count = %d, want %d", fileLen, got, want)
		}
	}
}

// One bit per used sector in the address bitmap, and none beyond.
func TestTheSectorBitmapMarksExactlyTheUsedSectors(t *testing.T) {
	const fileLen = 5000
	d, err := LoadSBT(make([]byte, fileLen))
	if err != nil {
		t.Fatalf("LoadSBT: %v", err)
	}
	e := sbtEntry(t, d)
	n := int(e[11])<<8 | int(e[12])

	set := 0
	for i := 15; i < 236; i++ {
		for b := 0; b < 8; b++ {
			if e[i]&(1<<uint(b)) != 0 {
				set++
			}
		}
	}
	if set != n {
		t.Errorf("%d bits set in the sector map, want %d (the sector count)", set, n)
	}
}

// The file's bytes come back in order when the chain is followed, which is the
// only thing the guest actually does. This is the whole format working.
func TestFollowingTheChainReturnsTheFileInOrder(t *testing.T) {
	// A pattern with a long period, so a sector read from the wrong place, or
	// out of order, or overlapping its neighbour, shows up in the bytes.
	file := make([]byte, 12345)
	for i := range file {
		file[i] = byte(i*7 + i/251)
	}

	d, err := LoadSBT(file)
	if err != nil {
		t.Fatalf("LoadSBT: %v", err)
	}
	e := sbtEntry(t, d)

	var got []byte
	cyl, head, sector := int(e[13]&0x7F), int(e[13]>>7), int(e[14])
	for i := 0; i < 4000; i++ {
		sec, ok := d.ReadSector(cyl, head, sector)
		if !ok {
			t.Fatalf("chain reached a sector that does not exist: cyl %d head %d sector %d",
				cyl, head, sector)
		}
		got = append(got, sec[:sbtSectorPayload]...)
		next := sec[sbtSectorPayload]
		nextSector := int(sec[sbtSectorPayload+1])
		if nextSector == 0 {
			break // end of chain
		}
		cyl, head, sector = int(next&0x7F), int(next>>7), nextSector
	}

	// The chain carries the 9-byte header first, then the file.
	want := len(file) + sbtHeaderSize
	if len(got) < want {
		t.Fatalf("the chain yielded %d bytes, want at least %d", len(got), want)
	}
	if got[0] != sbtFileTypeCode {
		t.Errorf("the stream does not start with the file header (type = %d)", got[0])
	}
	body := got[sbtHeaderSize : sbtHeaderSize+len(file)]
	for i := range file {
		if body[i] != file[i] {
			t.Fatalf("file byte %d = %#02x, want %#02x", i, body[i], file[i])
		}
	}
}

// The header describes the file the way the SAM Technical Manual lays it out.
func TestTheFileHeaderDescribesTheFile(t *testing.T) {
	const fileLen = 20000 // more than one 16K page
	d, err := LoadSBT(make([]byte, fileLen))
	if err != nil {
		t.Fatalf("LoadSBT: %v", err)
	}
	first, ok := d.ReadSector(sbtDirectoryTracks, 0, 1)
	if !ok {
		t.Fatal("the first data sector is missing")
	}

	if first[0] != sbtFileTypeCode {
		t.Errorf("header type = %d, want %d", first[0], sbtFileTypeCode)
	}
	if got := int(first[1]) | int(first[2])<<8; got != fileLen%16384 {
		t.Errorf("header length mod 16384 = %d, want %d", got, fileLen%16384)
	}
	if got := int(first[3]) | int(first[4])<<8; got != sbtLoadAddress {
		t.Errorf("header start address = %#04x, want %#04x", got, sbtLoadAddress)
	}
	if got := int(first[7]); got != fileLen/16384 {
		t.Errorf("header page count = %d, want %d", got, fileLen/16384)
	}
	if first[8] != 1 {
		t.Errorf("header first page = %d, want 1", first[8])
	}
}

// A file too big for the disk is refused rather than silently truncated. The
// capacity is what the chain can actually reach.
func TestATooLargeFileIsRefused(t *testing.T) {
	if _, err := LoadSBT(make([]byte, sbtMaxFileSize)); err != nil {
		t.Errorf("a file of exactly the maximum size was refused: %v", err)
	}
	_, err := LoadSBT(make([]byte, sbtMaxFileSize+1))
	if err == nil {
		t.Fatal("a file one byte over the maximum was accepted")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %q, want it to say the file is too large", err)
	}
}

// An empty file has nothing to boot and is refused rather than producing a
// disk whose directory entry describes zero sectors.
func TestAnEmptyFileIsRefused(t *testing.T) {
	if _, err := LoadSBT(nil); err == nil {
		t.Error("an empty SBT was accepted")
	}
}

// SBT has no signature — SimCoupe distinguishes it by file extension, and so
// must we. LoadDiskFile is the path-aware entry point; LoadDisk on the same
// bytes must NOT guess, or every unrecognised file would become a disk.
func TestSBTIsChosenByExtensionNotByGuessing(t *testing.T) {
	file := make([]byte, 3000)
	for i := range file {
		file[i] = byte(i)
	}

	d, err := LoadDiskFile("game.sbt", file)
	if err != nil {
		t.Fatalf("LoadDiskFile(.sbt): %v", err)
	}
	if !d.WriteProtected() {
		t.Error("the .sbt path did not build an SBT disk")
	}

	if _, err := LoadDisk(file); err == nil {
		t.Error("LoadDisk accepted a bare file with no extension to go on: every " +
			"unrecognised file would become a disk")
	}
}

// The other formats still route by content when a path is supplied, so a .dsk
// holding a headerless MGT is still an MGT.
func TestLoadDiskFileStillRoutesTheOtherFormatsByContent(t *testing.T) {
	if _, err := LoadDiskFile("old.dsk", make([]byte, mgt800KSize)); err != nil {
		t.Errorf("an MGT named .dsk: %v", err)
	}
	if _, err := LoadDiskFile("game.dsk", uniformEDSK(t, 4, 2, 10, 512)); err != nil {
		t.Errorf("an EDSK named .dsk: %v", err)
	}
}
