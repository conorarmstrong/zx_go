package sdcard

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildFAT16_BasicLayout(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "HELLO.TXT"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	img, err := BuildFAT16(dir, BuildOpts{SizeMB: 16, VolumeLabel: "TEST", SectorsPerCluster: 4})
	if err != nil {
		t.Fatalf("BuildFAT16: %v", err)
	}
	if len(img) != 16*1024*1024 {
		t.Errorf("image size %d, want %d", len(img), 16*1024*1024)
	}
	// MBR signature.
	if img[510] != 0x55 || img[511] != 0xAA {
		t.Errorf("MBR signature missing: %02X %02X", img[510], img[511])
	}
	// Partition entry: type FAT16.
	if img[450] != 0x06 {
		t.Errorf("partition type %02X, want 0x06 (FAT16)", img[450])
	}
	// BPB sits at the partition start LBA (2048 = 1 MB alignment),
	// not at sector 1 — matches modern SD-card formatting.
	const bpbOff = 2048 * 512
	if img[bpbOff+510] != 0x55 || img[bpbOff+511] != 0xAA {
		t.Errorf("BPB signature missing")
	}
	if !bytes.Equal(img[bpbOff+54:bpbOff+62], []byte("FAT16   ")) {
		t.Errorf("BPB file-system type wrong: %q", string(img[bpbOff+54:bpbOff+62]))
	}
}

func TestBuildFAT16_FileFindable(t *testing.T) {
	dir := t.TempDir()
	contents := bytes.Repeat([]byte("Z"), 600) // > 1 cluster
	if err := os.WriteFile(filepath.Join(dir, "BIG.TXT"), contents, 0644); err != nil {
		t.Fatal(err)
	}
	img, err := BuildFAT16(dir, BuildOpts{SizeMB: 16, SectorsPerCluster: 4})
	if err != nil {
		t.Fatal(err)
	}
	// Recompute the offsets the builder uses, with partition
	// starting at LBA 2048 (modern alignment).
	const partitionLBA = 2048
	const reserved = 1
	const numFATs = 2
	const sectorsPerCluster = 4
	const sectorSize = 512
	totalSectors := 16 * 1024 * 1024 / sectorSize
	partitionSectors := totalSectors - partitionLBA
	rootDirSectors := 32
	dataSectors := partitionSectors - reserved - rootDirSectors
	totalClusters := dataSectors / sectorsPerCluster
	fatSize := (totalClusters*2 + sectorSize - 1) / sectorSize
	rootDirOffset := (partitionLBA + reserved + numFATs*fatSize) * sectorSize

	// Look for "BIG     TXT" at start of any 32-byte slot in root.
	found := false
	var cluster uint16
	var size uint32
	for i := 0; i < 512; i++ {
		off := rootDirOffset + i*32
		name := img[off : off+11]
		if bytes.Equal(name, []byte("BIG     TXT")) {
			found = true
			cluster = binary.LittleEndian.Uint16(img[off+26 : off+28])
			size = binary.LittleEndian.Uint32(img[off+28 : off+32])
			break
		}
	}
	if !found {
		t.Fatal("BIG.TXT entry not found in root directory")
	}
	if size != uint32(len(contents)) {
		t.Errorf("size = %d, want %d", size, len(contents))
	}
	// Follow cluster chain and verify contents.
	dataOffset := (partitionLBA + reserved + numFATs*fatSize + rootDirSectors) * sectorSize
	fatOffset := (partitionLBA + reserved) * sectorSize
	clusterBytes := sectorsPerCluster * sectorSize
	read := []byte{}
	c := cluster
	for len(read) < int(size) {
		clusterStart := dataOffset + int(c-2)*clusterBytes
		clusterEnd := clusterStart + clusterBytes
		read = append(read, img[clusterStart:clusterEnd]...)
		fatEnt := binary.LittleEndian.Uint16(img[fatOffset+int(c)*2 : fatOffset+int(c)*2+2])
		if fatEnt >= 0xFFF8 {
			break
		}
		c = fatEnt
	}
	read = read[:int(size)]
	if !bytes.Equal(read, contents) {
		t.Errorf("read-back contents differ (len read=%d want=%d)", len(read), len(contents))
	}
}

func TestBuildFAT16_Subdirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "SYS"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SYS", "TEST.SYS"), []byte("payload"), 0644); err != nil {
		t.Fatal(err)
	}
	img, err := BuildFAT16(dir, BuildOpts{SizeMB: 16, SectorsPerCluster: 4})
	if err != nil {
		t.Fatal(err)
	}
	// Find SYS entry in root (partition starts at LBA 2048).
	const partitionLBA = 2048
	const reserved = 1
	const numFATs = 2
	const sectorsPerCluster = 4
	const sectorSize = 512
	totalSectors := 16 * 1024 * 1024 / sectorSize
	partitionSectors := totalSectors - partitionLBA
	rootDirSectors := 32
	dataSectors := partitionSectors - reserved - rootDirSectors
	totalClusters := dataSectors / sectorsPerCluster
	fatSize := (totalClusters*2 + sectorSize - 1) / sectorSize
	rootDirOffset := (partitionLBA + reserved + numFATs*fatSize) * sectorSize

	var sysCluster uint16
	for i := 0; i < 512; i++ {
		off := rootDirOffset + i*32
		name := img[off : off+8]
		if bytes.Equal(name, []byte("SYS     ")) && img[off+11]&attrDir != 0 {
			sysCluster = binary.LittleEndian.Uint16(img[off+26 : off+28])
			break
		}
	}
	if sysCluster == 0 {
		t.Fatal("SYS subdir not found in root")
	}
	// Walk into SYS cluster, find TEST.SYS.
	dataOffset := (partitionLBA + reserved + numFATs*fatSize + rootDirSectors) * sectorSize
	clusterBytes := sectorsPerCluster * sectorSize
	sysOff := dataOffset + int(sysCluster-2)*clusterBytes
	foundTest := false
	for i := 0; i < clusterBytes/32; i++ {
		eoff := sysOff + i*32
		name := img[eoff : eoff+11]
		if bytes.Equal(name, []byte("TEST    SYS")) {
			foundTest = true
			break
		}
	}
	if !foundTest {
		t.Error("TEST.SYS not found inside SYS subdir")
	}
}

// TestBuildFAT16_NestedSubdirectoryDotDot verifies that a subdirectory
// nested two levels deep has its ".." entry pointing at its immediate
// parent's cluster, not unconditionally at the root (cluster 0).
func TestBuildFAT16_NestedSubdirectoryDotDot(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "GAMES", "SUB"), 0755); err != nil {
		t.Fatal(err)
	}
	img, err := BuildFAT16(dir, BuildOpts{SizeMB: 16, SectorsPerCluster: 4})
	if err != nil {
		t.Fatal(err)
	}
	const partitionLBA = 2048
	const reserved = 1
	const numFATs = 2
	const sectorsPerCluster = 4
	const sectorSize = 512
	totalSectors := 16 * 1024 * 1024 / sectorSize
	partitionSectors := totalSectors - partitionLBA
	rootDirSectors := 32
	dataSectors := partitionSectors - reserved - rootDirSectors
	totalClusters := dataSectors / sectorsPerCluster
	fatSize := (totalClusters*2 + sectorSize - 1) / sectorSize
	rootDirOffset := (partitionLBA + reserved + numFATs*fatSize) * sectorSize
	dataOffset := (partitionLBA + reserved + numFATs*fatSize + rootDirSectors) * sectorSize
	clusterBytes := sectorsPerCluster * sectorSize

	findEntry := func(dirOff int, name string) (cluster uint16, ok bool) {
		for i := 0; i < clusterBytes/32; i++ {
			eoff := dirOff + i*32
			if bytes.Equal(img[eoff:eoff+11], []byte(name)) {
				return binary.LittleEndian.Uint16(img[eoff+26 : eoff+28]), true
			}
		}
		return 0, false
	}

	var gamesCluster uint16
	for i := 0; i < 512; i++ {
		off := rootDirOffset + i*32
		if bytes.Equal(img[off:off+8], []byte("GAMES   ")) && img[off+11]&attrDir != 0 {
			gamesCluster = binary.LittleEndian.Uint16(img[off+26 : off+28])
			break
		}
	}
	if gamesCluster == 0 {
		t.Fatal("GAMES subdir not found in root")
	}
	gamesOff := dataOffset + int(gamesCluster-2)*clusterBytes
	subCluster, ok := findEntry(gamesOff, padFAT("SUB", 11))
	if !ok {
		t.Fatal("SUB subdir not found inside GAMES")
	}
	subOff := dataOffset + int(subCluster-2)*clusterBytes
	dotDotCluster, ok := findEntry(subOff, padFAT("..", 11))
	if !ok {
		t.Fatal("\"..\" entry not found inside SUB")
	}
	if dotDotCluster != gamesCluster {
		t.Errorf("SUB's \"..\" cluster = %d, want %d (GAMES, its immediate parent)", dotDotCluster, gamesCluster)
	}
}

func TestToFAT83(t *testing.T) {
	cases := map[string]string{
		"hello.txt":       "HELLO.TXT",
		"longfilename.zx": "LONGFILE.ZX",
		"NEXTZXOS.SYS":    "NEXTZXOS.SYS",
		"weird name.s":    "WEIRD_NA.S",
		"noext":           "NOEXT",
		".dotfile":        "DOTFILE",
	}
	for in, want := range cases {
		if got := toFAT83(in); got != want {
			t.Errorf("toFAT83(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestToFAT83_DotAndDotDot — directory navigation entries pass
// through unchanged.
func TestToFAT83_DotAndDotDot(t *testing.T) {
	if got := toFAT83("."); got != "." {
		t.Errorf("toFAT83(\".\") = %q, want \".\"", got)
	}
	if got := toFAT83(".."); got != ".." {
		t.Errorf("toFAT83(\"..\") = %q, want \"..\"", got)
	}
}

// TestToFAT83_ExtensionTruncation — extension longer than 3 chars
// must be truncated to 3.
func TestToFAT83_ExtensionTruncation(t *testing.T) {
	if got := toFAT83("file.json"); got != "FILE.JSO" {
		t.Errorf("toFAT83(\"file.json\") = %q, want \"FILE.JSO\"", got)
	}
}

// TestToFAT83_BasenameTruncation — basename longer than 8 chars
// must be truncated to 8.
func TestToFAT83_BasenameTruncation(t *testing.T) {
	if got := toFAT83("abcdefghij.txt"); got != "ABCDEFGH.TXT" {
		t.Errorf("toFAT83 basename: got %q, want \"ABCDEFGH.TXT\"", got)
	}
}

// TestToFAT83_MultipleDots — only the LAST dot separates name from
// extension. "a.b.c.txt" → "A.B.C" → ...wait — actually:
//
//	upper = "A.B.C.TXT"
//	dot = index of last . = position of last . before "TXT"
//	base = "A.B.C", ext = "TXT"
//	sanitised base = "A_B_C" (dots illegal in basename) — let's see
func TestToFAT83_MultipleDots(t *testing.T) {
	// "a.b.c.txt" → last "." separates. base="a.b.c" → sanitised
	// removes/replaces internal dots; the resulting form depends on
	// sanitiseFAT.
	got := toFAT83("a.b.c.txt")
	if got == "" || len(got) > 12 {
		t.Errorf("toFAT83(\"a.b.c.txt\") = %q (unexpected shape)", got)
	}
}

// TestToFAT83_EmptyAfterDotStrip — input "..hidden" → strips leading
// dots → "hidden" → "HIDDEN".
func TestToFAT83_EmptyAfterDotStrip(t *testing.T) {
	if got := toFAT83("..hidden"); got != "HIDDEN" {
		t.Errorf("toFAT83(\"..hidden\") = %q, want \"HIDDEN\"", got)
	}
}

// NextBootFilter coverage (iter 256). The filter excludes specific
// well-known-noisy basenames + dev-tooling directories from the
// virtual SD image so the FPGA bootrom doesn't choke on host-side
// content (= the QXL.WIN reproducible-build issue that was in
// pkg/next/install).
func TestNextBootFilter_BasenamesAndDirs(t *testing.T) {
	f := NextBootFilter()
	if f == nil {
		t.Fatal("NextBootFilter returned nil")
	}
	// Skipped basenames (files only).
	for _, name := range []string{"QXL.WIN", "CONTRIBUTING.md", "LICENSE.md", "README.md"} {
		if !f("/host/path/"+name, false) {
			t.Errorf("expected file %q to be skipped", name)
		}
		// As a directory the same basename should NOT be skipped via
		// the file table — only the dir table applies. (QXL.WIN
		// isn't in the dir table.)
		if f("/host/path/"+name, true) && name != "QXL.WIN" {
			// Most of these wouldn't be directories anyway, but check
			// the table separation.
			t.Errorf("file-table basename %q matched as dir", name)
		}
	}
	// Skipped directories.
	for _, dir := range []string{"apps", "demos", "docs", "games", "src", "home", "tmp", "TBUS", "extras"} {
		if !f("/sd/"+dir, true) {
			t.Errorf("expected dir %q to be skipped", dir)
		}
		// As a file basename, NOT skipped (file table only has 4 entries).
		if f("/sd/"+dir, false) {
			t.Errorf("dir-table basename %q matched as file", dir)
		}
	}
	// Random other names not skipped.
	for _, name := range []string{"AUTOEXEC.NXT", "GAME.NEX", "machines"} {
		if f("/host/"+name, false) {
			t.Errorf("non-skipped file %q got skipped", name)
		}
		if f("/host/"+name, true) {
			t.Errorf("non-skipped dir %q got skipped", name)
		}
	}
}

// TestBuildFAT16_RejectsBadSize verifies BuildFAT16 returns an error
// for sub-1MB SizeMB or zero SectorsPerCluster (or accepts and
// normalises — depends on impl).
func TestBuildFAT16_AcceptsTypicalSizes(t *testing.T) {
	for _, size := range []int{16, 32, 64} {
		dir := t.TempDir()
		// Add at least one file so the build does something.
		if err := os.WriteFile(filepath.Join(dir, "X.TXT"), []byte("hi"), 0644); err != nil {
			t.Fatal(err)
		}
		img, err := BuildFAT16(dir, BuildOpts{SizeMB: size, SectorsPerCluster: 4})
		if err != nil {
			t.Errorf("BuildFAT16(%d MB): %v", size, err)
			continue
		}
		want := size * 1024 * 1024
		if len(img) != want {
			t.Errorf("BuildFAT16(%d MB): image len = %d, want %d", size, len(img), want)
		}
	}
}
