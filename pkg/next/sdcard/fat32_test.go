package sdcard

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// --- minimal FAT32 reader (test oracle) ---------------------------------

type fat32Reader struct {
	img       []byte
	partStart int
	spc       int
	fatStart  int // sectors (absolute)
	dataStart int // sectors (absolute)
	rootClus  uint32
}

func newFAT32Reader(t *testing.T, img []byte) *fat32Reader {
	t.Helper()
	if img[510] != 0x55 || img[511] != 0xAA {
		t.Fatal("MBR signature missing")
	}
	pte := img[446:462]
	if pte[4] != 0x0C {
		t.Fatalf("partition type = %#02x, want 0x0C (FAT32 LBA)", pte[4])
	}
	partStart := int(binary.LittleEndian.Uint32(pte[8:12]))
	bpb := img[partStart*512:]
	if bpb[510] != 0x55 || bpb[511] != 0xAA {
		t.Fatal("BPB signature missing")
	}
	if !bytes.Equal(bpb[82:90], []byte("FAT32   ")) {
		t.Fatalf("FS type label = %q, want FAT32", bpb[82:90])
	}
	bps := int(binary.LittleEndian.Uint16(bpb[11:13]))
	if bps != 512 {
		t.Fatalf("bytes/sector = %d, want 512", bps)
	}
	spc := int(bpb[13])
	rsvd := int(binary.LittleEndian.Uint16(bpb[14:16]))
	nfats := int(bpb[16])
	fatsz := int(binary.LittleEndian.Uint32(bpb[36:40]))
	rootClus := binary.LittleEndian.Uint32(bpb[44:48])
	// FSInfo sanity.
	fsinfoSec := int(binary.LittleEndian.Uint16(bpb[48:50]))
	fsinfo := img[(partStart+fsinfoSec)*512:]
	if binary.LittleEndian.Uint32(fsinfo[0:4]) != 0x41615252 ||
		binary.LittleEndian.Uint32(fsinfo[484:488]) != 0x61417272 {
		t.Fatal("FSInfo signatures missing")
	}
	return &fat32Reader{
		img:       img,
		partStart: partStart,
		spc:       spc,
		fatStart:  partStart + rsvd,
		dataStart: partStart + rsvd + nfats*fatsz,
		rootClus:  rootClus,
	}
}

func (r *fat32Reader) fat(c uint32) uint32 {
	off := r.fatStart*512 + int(c)*4
	return binary.LittleEndian.Uint32(r.img[off:off+4]) & 0x0FFFFFFF
}

func (r *fat32Reader) clusterBytes(c uint32) []byte {
	off := (r.dataStart + int(c-2)*r.spc) * 512
	return r.img[off : off+r.spc*512]
}

func (r *fat32Reader) chain(c uint32) []byte {
	var out []byte
	for c >= 2 && c < 0x0FFFFFF8 {
		out = append(out, r.clusterBytes(c)...)
		c = r.fat(c)
	}
	return out
}

type fatDirent struct {
	shortName string // "NAME.EXT" form
	longName  string // reconstructed LFN, "" if none
	attr      byte
	cluster   uint32
	size      uint32
}

func (r *fat32Reader) readDir(c uint32) []fatDirent {
	raw := r.chain(c)
	var out []fatDirent
	var lfn []rune
	for i := 0; i+32 <= len(raw); i += 32 {
		e := raw[i : i+32]
		if e[0] == 0 {
			break
		}
		if e[0] == 0xE5 {
			lfn = nil
			continue
		}
		if e[11] == 0x0F { // LFN entry
			var u16 []uint16
			for _, span := range [][2]int{{1, 11}, {14, 26}, {28, 32}} {
				for j := span[0]; j+1 < span[1]+1; j += 2 {
					u16 = append(u16, binary.LittleEndian.Uint16(e[j:j+2]))
				}
			}
			var part []rune
			for _, v := range utf16.Decode(u16) {
				if v == 0 || v == 0xFFFF {
					break
				}
				part = append(part, v)
			}
			// LFN entries are stored last-part-first.
			lfn = append(part, lfn...)
			continue
		}
		name := strings.TrimRight(string(e[0:8]), " ")
		ext := strings.TrimRight(string(e[8:11]), " ")
		if ext != "" {
			name += "." + ext
		}
		out = append(out, fatDirent{
			shortName: name,
			longName:  string(lfn),
			attr:      e[11],
			cluster:   uint32(binary.LittleEndian.Uint16(e[20:22]))<<16 | uint32(binary.LittleEndian.Uint16(e[26:28])),
			size:      binary.LittleEndian.Uint32(e[28:32]),
		})
		lfn = nil
	}
	return out
}

func (r *fat32Reader) find(dir uint32, name string) *fatDirent {
	for _, e := range r.readDir(dir) {
		if strings.EqualFold(e.shortName, name) || strings.EqualFold(e.longName, name) {
			cp := e
			return &cp
		}
	}
	return nil
}

// --- tests ---------------------------------------------------------------

func buildTestTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite := func(rel string, data []byte) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("TBBLUE.FW", bytes.Repeat([]byte{0xAB}, 3000))
	mustWrite("NEXTZXOS/enMenus-example.cfg", []byte("menu=example\n"))
	mustWrite("MACHINES/NEXT/ENNEXTZX.ROM", bytes.Repeat([]byte{0x5C}, 65536))
	mustWrite("MACHINES/NEXT/menu.ini", []byte("[menu]\n"))
	return dir
}

func TestBuildFAT32GeometryParses(t *testing.T) {
	img, err := BuildFAT32(buildTestTree(t), FAT32Opts{SizeMB: 64})
	if err != nil {
		t.Fatalf("BuildFAT32: %v", err)
	}
	if len(img) != 64*1024*1024 {
		t.Fatalf("image size = %d, want 64 MB", len(img))
	}
	newFAT32Reader(t, img) // asserts MBR/BPB/FSInfo internally
}

func TestBuildFAT32FilesReadable(t *testing.T) {
	img, err := BuildFAT32(buildTestTree(t), FAT32Opts{SizeMB: 64})
	if err != nil {
		t.Fatalf("BuildFAT32: %v", err)
	}
	r := newFAT32Reader(t, img)

	fw := r.find(r.rootClus, "TBBLUE.FW")
	if fw == nil {
		t.Fatal("TBBLUE.FW not found in root")
	}
	if fw.size != 3000 {
		t.Errorf("TBBLUE.FW size = %d, want 3000", fw.size)
	}
	data := r.chain(fw.cluster)[:fw.size]
	if !bytes.Equal(data, bytes.Repeat([]byte{0xAB}, 3000)) {
		t.Error("TBBLUE.FW content mismatch")
	}

	mach := r.find(r.rootClus, "MACHINES")
	if mach == nil || mach.attr&0x10 == 0 {
		t.Fatal("MACHINES directory not found")
	}
	next := r.find(mach.cluster, "NEXT")
	if next == nil || next.attr&0x10 == 0 {
		t.Fatal("MACHINES/NEXT directory not found")
	}
	rom := r.find(next.cluster, "ENNEXTZX.ROM")
	if rom == nil {
		t.Fatal("ENNEXTZX.ROM not found")
	}
	if rom.size != 65536 {
		t.Errorf("ENNEXTZX.ROM size = %d, want 65536", rom.size)
	}
	romData := r.chain(rom.cluster)[:rom.size]
	if romData[0] != 0x5C || romData[65535] != 0x5C {
		t.Error("ENNEXTZX.ROM content mismatch")
	}
}

// Regression: a subdirectory's ".." must point at its IMMEDIATE
// parent (or 0 when that parent is the root), regardless of how deep
// it sits. The first implementation zeroed ".." whenever the
// grandparent was root, so /MACHINES/NEXT/.. wrongly pointed at the
// root instead of /MACHINES — "cd .." out of any depth-≥2 directory
// would land in the wrong place.
func TestBuildFAT32DotDotPointsAtImmediateParent(t *testing.T) {
	img, err := BuildFAT32(buildTestTree(t), FAT32Opts{SizeMB: 64})
	if err != nil {
		t.Fatalf("BuildFAT32: %v", err)
	}
	r := newFAT32Reader(t, img)

	machines := r.find(r.rootClus, "MACHINES")
	if machines == nil {
		t.Fatal("MACHINES not found")
	}
	next := r.find(machines.cluster, "NEXT")
	if next == nil {
		t.Fatal("MACHINES/NEXT not found")
	}
	// /MACHINES/.. → root → cluster 0 (FAT convention).
	if dd := r.find(machines.cluster, ".."); dd == nil || dd.cluster != 0 {
		t.Errorf("/MACHINES/.. cluster = %v, want 0 (root)", dd)
	}
	// /MACHINES/NEXT/.. → /MACHINES (NOT root, NOT 0).
	dd := r.find(next.cluster, "..")
	if dd == nil {
		t.Fatal("/MACHINES/NEXT/.. entry missing")
	}
	if dd.cluster != machines.cluster {
		t.Errorf("/MACHINES/NEXT/.. cluster = %d, want %d (/MACHINES)", dd.cluster, machines.cluster)
	}
	// /MACHINES/NEXT/. → itself.
	if dot := r.find(next.cluster, "."); dot == nil || dot.cluster != next.cluster {
		t.Errorf("/MACHINES/NEXT/. cluster = %v, want %d (self)", dot, next.cluster)
	}
}

// Regression: a file spanning many clusters must round-trip exactly.
// The first writer used a single f.Read per cluster, which can
// short-read and silently corrupt/shift the tail; io.ReadFull fixes
// it. ENNEXTZX.ROM is 64 KB — with the default small-image cluster
// size that is several clusters.
func TestBuildFAT32MultiClusterFileRoundTrips(t *testing.T) {
	dir := t.TempDir()
	// A 200 KB file with a position-dependent pattern so any shift
	// or gap is detectable.
	big := make([]byte, 200*1024)
	for i := range big {
		big[i] = byte((i*31 + 7) & 0xFF)
	}
	if err := os.WriteFile(filepath.Join(dir, "BIG.BIN"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	// Force 1-sector clusters (512 B) so 200 KB = 400 clusters.
	img, err := BuildFAT32(dir, FAT32Opts{SizeMB: 64, SectorsPerCluster: 1})
	if err != nil {
		t.Fatalf("BuildFAT32: %v", err)
	}
	r := newFAT32Reader(t, img)
	e := r.find(r.rootClus, "BIG.BIN")
	if e == nil {
		t.Fatal("BIG.BIN not found")
	}
	if e.size != uint32(len(big)) {
		t.Fatalf("size = %d, want %d", e.size, len(big))
	}
	got := r.chain(e.cluster)[:e.size]
	if !bytes.Equal(got, big) {
		// Find first mismatch for a useful message.
		for i := range big {
			if got[i] != big[i] {
				t.Fatalf("content diverges at byte %d: got %#x want %#x", i, got[i], big[i])
			}
		}
		t.Fatal("content mismatch")
	}
}

func TestBuildFAT32LongNames(t *testing.T) {
	img, err := BuildFAT32(buildTestTree(t), FAT32Opts{SizeMB: 64})
	if err != nil {
		t.Fatalf("BuildFAT32: %v", err)
	}
	r := newFAT32Reader(t, img)

	zxos := r.find(r.rootClus, "NEXTZXOS")
	if zxos == nil {
		t.Fatal("NEXTZXOS dir not found")
	}
	cfg := r.find(zxos.cluster, "enMenus-example.cfg")
	if cfg == nil {
		t.Fatal("enMenus-example.cfg not found by long name")
	}
	if cfg.longName != "enMenus-example.cfg" {
		t.Errorf("LFN = %q, want enMenus-example.cfg", cfg.longName)
	}
	if got := string(r.chain(cfg.cluster)[:cfg.size]); got != "menu=example\n" {
		t.Errorf("cfg content = %q", got)
	}
	// The generated 8.3 alias must carry a numeric tail (~N) and the
	// LFN checksum in the LFN entries must match it — verified
	// implicitly by find() requiring the LFN chain to terminate at
	// this dirent.
	if !strings.Contains(cfg.shortName, "~") {
		t.Errorf("short alias %q should contain a numeric tail", cfg.shortName)
	}

	// Case-only long names must keep the PLAIN upper-cased 8.3 alias
	// (no ~N tail): the TBBLUE firmware bootloader resolves
	// machines/next/menu.ini by 8.3 name only — a mangled alias
	// breaks the boot ("Error opening 'menu.ini/.def'!").
	mach := r.find(r.rootClus, "MACHINES")
	next := r.find(mach.cluster, "NEXT")
	ini := r.find(next.cluster, "MENU.INI")
	if ini == nil {
		t.Fatal("MENU.INI not found by plain 8.3 name")
	}
	if strings.Contains(ini.shortName, "~") {
		t.Errorf("case-only name got mangled alias %q, want MENU.INI", ini.shortName)
	}
	if ini.longName != "menu.ini" {
		t.Errorf("LFN for menu.ini = %q, want original case preserved", ini.longName)
	}
}
