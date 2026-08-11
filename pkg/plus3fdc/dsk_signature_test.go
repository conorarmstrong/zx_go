package plus3fdc

import (
	"os"
	"strings"
	"testing"
)

// The CPCEMU DSK header is 0x100 bytes, and only its first EIGHT are a
// signature: "MV - CPC". The rest of the description field is free text that
// varies by the tool that wrote the image — the widely-quoted
// "MV - CPCEMU Disk-File\r\nDisk-Info\r\n" is just what CPCEMU itself wrote.
//
// Requiring the full 21-character string rejects valid images. Found by
// screening a disk collection: Batman - The Movie (1989, Ocean) carries
// "MV - CPCEMU / 27 Sep 97 14:45" and was refused outright.

// stdDSKWithDescription builds a minimal single-track standard DSK whose
// description field is the given text.
func stdDSKWithDescription(desc string) []byte {
	const trackSize = 0x1300 // 4864, the usual +3 track size
	data := make([]byte, dskHeaderSize+trackSize)
	copy(data, desc)
	data[0x30] = 1 // cylinders
	data[0x31] = 1 // sides
	data[0x32] = byte(trackSize & 0xFF)
	data[0x33] = byte(trackSize >> 8)

	tr := data[dskHeaderSize:]
	copy(tr, trackInfoSignature)
	tr[0x10] = 0 // track number
	tr[0x11] = 0 // side
	tr[0x14] = 2 // sector size code (512)
	tr[0x15] = 1 // sector count
	tr[0x18] = 0 // C
	tr[0x19] = 0 // H
	tr[0x1A] = 1 // R
	tr[0x1B] = 2 // N
	return data
}

// TestParseDSKAcceptsAnyCPCDescription pins the signature to the eight bytes
// the format actually fixes.
func TestParseDSKAcceptsAnyCPCDescription(t *testing.T) {
	for _, desc := range []string{
		"MV - CPCEMU Disk-File\r\nDisk-Info\r\n",
		"MV - CPCEMU / 27 Sep 97 14:45",        // Batman - The Movie
		"MV - CPC format DSK\r\nDisk-Info\r\n", // other writers
	} {
		if _, err := ParseDSK(stdDSKWithDescription(desc)); err != nil {
			t.Errorf("ParseDSK(%q) = %v, want it accepted", desc, err)
		}
	}
}

// TestParseDSKStillRejectsNonDSK guards the other side: loosening the
// signature must not make it accept anything at all.
func TestParseDSKStillRejectsNonDSK(t *testing.T) {
	for _, desc := range []string{
		"NOT A DISK IMAGE AT ALL........",
		"MV - XYZ something else........",
	} {
		if _, err := ParseDSK(stdDSKWithDescription(desc)); err == nil {
			t.Errorf("ParseDSK(%q) was accepted", desc)
		}
	}
}

// TestLoadDiskSurfacesTheRealError pins the diagnostic. When an image's
// signature is recognised but its body will not parse, the caller must see
// why. Previously loadDiskByPath discarded that error and fell through to its
// extension fallback, reporting "unrecognised disk image format" — which sent
// the reader looking for a missing format rather than at the real fault.
func TestLoadDiskSurfacesTheRealError(t *testing.T) {
	dir := t.TempDir()
	// A well-signed standard DSK claiming tracks whose data is not there.
	bad := make([]byte, dskHeaderSize+8)
	copy(bad, dskSignatureStd)
	bad[0x30] = 4    // claims 4 cylinders
	bad[0x31] = 1    // one side
	bad[0x32] = 0x00 // 4864-byte tracks that the file is far too small to hold
	bad[0x33] = 0x13
	path := dir + "/broken.dsk"
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadDiskByPath(path)
	if err == nil {
		t.Fatal("a truncated DSK was accepted")
	}
	if strings.Contains(err.Error(), "unrecognised disk image format") {
		t.Errorf("error is %q; it must report why the DSK failed, not claim the format is unknown", err)
	}
}
