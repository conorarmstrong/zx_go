package sdcard

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// path83To11 builds the 11-byte 8.3 form of an already-8.3 "NAME.EXT".
func path83To11(name string) []byte {
	base, ext, _, _ := basis(name)
	return padName83(base, ext)
}

func (b *fat32Builder) readChain(first uint32, size int) []byte {
	out := make([]byte, 0, size)
	for c := first; c >= 2 && c < 0x0FFFFFF8 && len(out) < size; c = b.getFAT(c) {
		off := b.clusterOffset(c)
		n := b.spc * 512
		if size-len(out) < n {
			n = size - len(out)
		}
		out = append(out, b.img[off:off+n]...)
	}
	return out
}

func (b *fat32Builder) readFileBytes(dirClus uint32, name83 string) ([]byte, bool) {
	want := string(path83To11(name83))
	for c := dirClus; c >= 2 && c < 0x0FFFFFF8; c = b.getFAT(c) {
		off := b.clusterOffset(c)
		cluster := b.img[off : off+b.spc*512]
		for i := 0; i+32 <= len(cluster); i += 32 {
			e := cluster[i : i+32]
			if e[0] == 0 {
				return nil, false
			}
			if e[0] == 0xE5 || e[11] == 0x0F || e[11]&attrDir != 0 {
				continue
			}
			if string(e[0:11]) == want {
				size := int(binary.LittleEndian.Uint32(e[28:32]))
				first := uint32(binary.LittleEndian.Uint16(e[20:22]))<<16 |
					uint32(binary.LittleEndian.Uint16(e[26:28]))
				return b.readChain(first, size), true
			}
		}
	}
	return nil, false
}

// readPath reads /dir/.../NAME.EXT from a FAT32 image for test verification.
func readPath(t *testing.T, img []byte, dirPath, file83 string) []byte {
	t.Helper()
	b, err := openFAT32(img)
	if err != nil {
		t.Fatalf("openFAT32: %v", err)
	}
	dirClus := uint32(2)
	for _, part := range splitNonEmpty(dirPath) {
		dirClus = b.findSubdir(dirClus, part)
		if dirClus == 0 {
			t.Fatalf("dir %q not found", part)
		}
	}
	data, ok := b.readFileBytes(dirClus, file83)
	if !ok {
		t.Fatalf("file %q not found in %q", file83, dirPath)
	}
	return data
}

func splitNonEmpty(p string) []string {
	var out []string
	for _, s := range bytes.Split([]byte(p), []byte("/")) {
		if len(s) > 0 {
			out = append(out, string(s))
		}
	}
	return out
}

func TestAddFileToFAT32_RoundTrips(t *testing.T) {
	// Build a starter image from a temp tree with one existing file.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "games"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := bytes.Repeat([]byte{0xA5}, 5000)
	if err := os.WriteFile(filepath.Join(dir, "games", "orig.bin"), orig, 0o644); err != nil {
		t.Fatal(err)
	}
	img, err := BuildFAT32(dir, FAT32Opts{SizeMB: 64})
	if err != nil {
		t.Fatalf("BuildFAT32: %v", err)
	}

	// Insert a multi-cluster file into a new /imported directory.
	want := make([]byte, 40000)
	for i := range want {
		want[i] = byte(i*7 + 3)
	}
	sdPath, err := AddFileToFAT32(img, "imported", "sonic.nex", want)
	if err != nil {
		t.Fatalf("AddFileToFAT32: %v", err)
	}
	if sdPath != "/IMPORTED/SONIC.NEX" {
		t.Errorf("sdPath = %q, want /IMPORTED/SONIC.NEX", sdPath)
	}

	// The inserted file reads back byte-for-byte.
	got := readPath(t, img, "imported", "SONIC.NEX")
	if !bytes.Equal(got, want) {
		t.Errorf("inserted file mismatch: got %d bytes, want %d (equal=%v)", len(got), len(want), bytes.Equal(got, want))
	}

	// The pre-existing file is untouched.
	gotOrig := readPath(t, img, "games", "ORIG.BIN")
	if !bytes.Equal(gotOrig, orig) {
		t.Errorf("pre-existing file corrupted by the insert")
	}
}

func TestAddFileToFAT32_LongNameGetsUniqueAlias(t *testing.T) {
	dir := t.TempDir()
	img, err := BuildFAT32(dir, FAT32Opts{SizeMB: 64})
	if err != nil {
		t.Fatal(err)
	}
	p1, err := AddFileToFAT32(img, "imported", "RevivalSurvival.nex", []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	p2, err := AddFileToFAT32(img, "imported", "RevivalSurvival.nex", []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	if p1 == p2 {
		t.Errorf("re-import produced the same path %q (would corrupt the directory)", p1)
	}
	// Both must read back to their own contents (no clobber).
	if got := readPath(t, img, "imported", filepath.Base(p1)); string(got) != "one" {
		t.Errorf("first import = %q, want one", got)
	}
	if got := readPath(t, img, "imported", filepath.Base(p2)); string(got) != "two" {
		t.Errorf("second import = %q, want two", got)
	}
}
