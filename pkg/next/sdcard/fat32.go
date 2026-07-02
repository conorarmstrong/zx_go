package sdcard

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"
)

// FAT32 image builder.
//
// Produces an in-memory FAT32-LBA disk image from a host directory
// tree. This is the bootable-card format: NextZXOS boots from a
// FAT32 card (a FAT16 image does not boot), so this builder is what
// backs "out-of-box SD" for users without a pre-made card image.
//
// Layout (sector = 512 bytes):
//
//	LBA 0                MBR, one partition entry, type 0x0C (FAT32 LBA)
//	LBA P                BPB (boot sector of the partition, P = 2048)
//	LBA P+1              FSInfo
//	LBA P+6              backup BPB
//	LBA P+7              backup FSInfo
//	LBA P+rsvd           FAT 1
//	LBA P+rsvd+fatsz     FAT 2 (mirror)
//	LBA P+rsvd+2*fatsz   data clusters; root directory = cluster 2
//
// Long filenames are written as standard VFAT LFN entry chains with
// generated 8.3 aliases (basis name + ~N numeric tail) — NextZXOS
// reads its config files by long name (e.g. enMenus-example.cfg).

// FAT32Opts configures BuildFAT32.
type FAT32Opts struct {
	// VolumeLabel for the BPB + the root volume-label entry.
	// 11 chars max, space-padded. Default "ZXNEXT".
	VolumeLabel string
	// SizeMB is the total image size. Minimum 64 (FAT32 needs
	// ≥ 65525 clusters; smaller would need sub-512-byte clusters).
	SizeMB int
	// SectorsPerCluster: power of two, 0 = auto (largest value
	// that still yields a legal FAT32 cluster count).
	SectorsPerCluster int
	// SkipFile mirrors BuildOpts.SkipFile.
	SkipFile func(hostPath string, isDir bool) bool
}

const (
	fat32EOC      = 0x0FFFFFFF
	fat32PartLBA  = 2048 // 1 MB alignment, matches the FAT16 builder
	fat32Reserved = 32   // reserved sectors
)

// BuildFAT32 walks dir and produces a FAT32-LBA image containing
// every file and subdirectory under it (macOS metadata skipped).
func BuildFAT32(dir string, opts FAT32Opts) ([]byte, error) {
	if opts.SizeMB <= 0 {
		opts.SizeMB = 256
	}
	if opts.SizeMB < 64 {
		return nil, fmt.Errorf("sdcard: SizeMB=%d too small for FAT32 (min 64)", opts.SizeMB)
	}
	if opts.VolumeLabel == "" {
		opts.VolumeLabel = "ZXNEXT"
	}

	const sectorSize = 512
	totalSectors := opts.SizeMB * 1024 * 1024 / sectorSize
	partSectors := totalSectors - fat32PartLBA

	spc := opts.SectorsPerCluster
	if spc <= 0 {
		// Auto: largest power-of-two SPC that keeps the cluster
		// count ≥ 65525 (the FAT32 legal minimum).
		spc = 64
		for spc > 1 {
			if clustersFor(partSectors, spc) >= 65525 {
				break
			}
			spc >>= 1
		}
	}
	clusters := clustersFor(partSectors, spc)
	if clusters < 65525 {
		return nil, fmt.Errorf("sdcard: SizeMB=%d spc=%d yields %d clusters (< FAT32 minimum 65525)",
			opts.SizeMB, spc, clusters)
	}
	// Sectors per FAT for the final cluster count (entries 0,1 + data).
	fatsz := (int(clusters)+2)*4/sectorSize + 1

	img := make([]byte, totalSectors*sectorSize)
	b := &fat32Builder{
		img:          img,
		spc:          spc,
		fatsz:        fatsz,
		totalSectors: totalSectors,
		clusters:     clusters,
		fatOffset:    (fat32PartLBA + fat32Reserved) * sectorSize,
		dataOffset:   (fat32PartLBA + fat32Reserved + 2*fatsz) * sectorSize,
		nextFree:     2,
		label:        opts.VolumeLabel,
		skipFile:     opts.SkipFile,
	}
	b.writeMBR()
	b.writeBPB(0)
	b.writeFSInfo(1)
	b.writeBPB(6) // backup boot sector
	b.writeFSInfo(7)
	b.initFAT()

	root := b.allocCluster() // cluster 2 = root directory
	if root != 2 {
		return nil, fmt.Errorf("sdcard: internal: root cluster = %d, want 2", root)
	}
	// Volume-label dirent first, matching how real formatters lay
	// out the root.
	b.appendDirent(root, rawDirent([]byte(padFAT(b.label, 11)), attrVolume, 0, 0))
	if err := b.addTree(dir, root); err != nil {
		return nil, err
	}
	b.mirrorFAT()
	return img, nil
}

func clustersFor(partSectors, spc int) uint32 {
	// Iterate once: data = part - reserved - 2*fat(sized for count).
	clusters := (partSectors - fat32Reserved) / spc
	for i := 0; i < 8; i++ {
		fatsz := (clusters+2)*4/512 + 1
		clusters = (partSectors - fat32Reserved - 2*fatsz) / spc
	}
	return uint32(clusters)
}

type fat32Builder struct {
	img          []byte
	spc          int
	fatsz        int
	totalSectors int
	clusters     uint32

	fatOffset  int
	dataOffset int
	nextFree   uint32

	label    string
	skipFile func(string, bool) bool

	// scanAlloc selects free-cluster allocation by scanning the FAT
	// (set when appending to an existing image, whose free space may be
	// fragmented) rather than the linear bump used while building a
	// fresh image.
	scanAlloc bool
}

func (b *fat32Builder) writeMBR() {
	pte := b.img[446:462]
	pte[0] = 0x00
	pte[1], pte[2], pte[3] = 0x00, 0x21, 0x20 // CHS first (ignored)
	pte[4] = 0x0C                             // FAT32 LBA
	pte[5], pte[6], pte[7] = 0xFE, 0xFF, 0xFF // CHS last (ignored)
	binary.LittleEndian.PutUint32(pte[8:12], fat32PartLBA)
	binary.LittleEndian.PutUint32(pte[12:16], uint32(b.totalSectors-fat32PartLBA))
	b.img[510], b.img[511] = 0x55, 0xAA
}

func (b *fat32Builder) writeBPB(sector int) {
	off := (fat32PartLBA + sector) * 512
	s := b.img[off : off+512]
	partSectors := b.totalSectors - fat32PartLBA
	s[0], s[1], s[2] = 0xEB, 0x58, 0x90
	copy(s[3:11], []byte("MSWIN4.1"))
	binary.LittleEndian.PutUint16(s[11:13], 512)
	s[13] = byte(b.spc)
	binary.LittleEndian.PutUint16(s[14:16], fat32Reserved)
	s[16] = 2 // FATs
	// root entries (s[17:19]) and total16 (s[19:21]) stay 0 on FAT32
	s[21] = 0xF8
	// fatsz16 (s[22:24]) stays 0 on FAT32
	binary.LittleEndian.PutUint16(s[24:26], 63)  // sectors/track
	binary.LittleEndian.PutUint16(s[26:28], 255) // heads
	binary.LittleEndian.PutUint32(s[28:32], fat32PartLBA)
	binary.LittleEndian.PutUint32(s[32:36], uint32(partSectors))
	binary.LittleEndian.PutUint32(s[36:40], uint32(b.fatsz))
	// ext flags (s[40:42]) = 0: FAT mirrored. fs version (s[42:44]) = 0.
	binary.LittleEndian.PutUint32(s[44:48], 2) // root cluster
	binary.LittleEndian.PutUint16(s[48:50], 1) // FSInfo sector
	binary.LittleEndian.PutUint16(s[50:52], 6) // backup boot sector
	s[64] = 0x80
	s[66] = 0x29
	binary.LittleEndian.PutUint32(s[67:71], 0x32585A4E) // volume ID "NZX2"
	copy(s[71:82], padFAT(b.label, 11))
	copy(s[82:90], []byte("FAT32   "))
	s[510], s[511] = 0x55, 0xAA
}

func (b *fat32Builder) writeFSInfo(sector int) {
	off := (fat32PartLBA + sector) * 512
	s := b.img[off : off+512]
	binary.LittleEndian.PutUint32(s[0:4], 0x41615252)
	binary.LittleEndian.PutUint32(s[484:488], 0x61417272)
	binary.LittleEndian.PutUint32(s[488:492], 0xFFFFFFFF) // free count unknown
	binary.LittleEndian.PutUint32(s[492:496], 0xFFFFFFFF) // next free unknown
	binary.LittleEndian.PutUint32(s[508:512], 0xAA550000)
}

func (b *fat32Builder) initFAT() {
	b.setFAT(0, 0x0FFFFFF8)
	b.setFAT(1, 0x0FFFFFFF)
}

func (b *fat32Builder) setFAT(c, v uint32) {
	off := b.fatOffset + int(c)*4
	cur := binary.LittleEndian.Uint32(b.img[off : off+4])
	binary.LittleEndian.PutUint32(b.img[off:off+4], cur&0xF0000000|v&0x0FFFFFFF)
}

func (b *fat32Builder) getFAT(c uint32) uint32 {
	off := b.fatOffset + int(c)*4
	return binary.LittleEndian.Uint32(b.img[off:off+4]) & 0x0FFFFFFF
}

func (b *fat32Builder) mirrorFAT() {
	src := b.img[b.fatOffset : b.fatOffset+b.fatsz*512]
	copy(b.img[b.fatOffset+b.fatsz*512:], src)
}

func (b *fat32Builder) allocCluster() uint32 {
	if b.scanAlloc {
		// Appending to an existing image: skip clusters already in use.
		for c := b.nextFree; c < b.clusters+2; c++ {
			if b.getFAT(c) == 0 {
				b.nextFree = c + 1
				b.setFAT(c, fat32EOC)
				return c
			}
		}
		return 0
	}
	if b.nextFree >= b.clusters+2 {
		return 0
	}
	c := b.nextFree
	b.nextFree++
	b.setFAT(c, fat32EOC)
	return c
}

func (b *fat32Builder) linkCluster(prev, next uint32) {
	b.setFAT(prev, next)
	b.setFAT(next, fat32EOC)
}

func (b *fat32Builder) clusterOffset(c uint32) int {
	return b.dataOffset + int(c-2)*b.spc*512
}

// appendDirent writes one 32-byte entry into the first free slot of
// the directory chain rooted at dirClus, extending the chain when
// full. Returns false when the disk is full.
func (b *fat32Builder) appendDirent(dirClus uint32, ent []byte) bool {
	c := dirClus
	for {
		off := b.clusterOffset(c)
		clusterBytes := b.img[off : off+b.spc*512]
		for i := 0; i+32 <= len(clusterBytes); i += 32 {
			if clusterBytes[i] == 0 {
				copy(clusterBytes[i:i+32], ent)
				return true
			}
		}
		next := b.getFAT(c)
		if next >= 0x0FFFFFF8 {
			extra := b.allocCluster()
			if extra == 0 {
				return false
			}
			b.linkCluster(c, extra)
			c = extra
		} else {
			c = next
		}
	}
}

// rawDirent builds one 32-byte 8.3 directory entry. name11 must be
// the 11-byte padded form.
func rawDirent(name11 []byte, attr byte, cluster uint32, size uint32) []byte {
	e := make([]byte, 32)
	copy(e[0:11], name11)
	e[11] = attr
	binary.LittleEndian.PutUint16(e[20:22], uint16(cluster>>16))
	binary.LittleEndian.PutUint16(e[26:28], uint16(cluster&0xFFFF))
	binary.LittleEndian.PutUint32(e[28:32], size)
	return e
}

// addTree adds the host directory's contents into dirClus,
// recursing into subdirectories.
func (b *fat32Builder) addTree(hostPath string, dirClus uint32) error {
	entries, err := os.ReadDir(hostPath)
	if err != nil {
		return fmt.Errorf("sdcard: ReadDir %s: %w", hostPath, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	used := map[string]bool{} // 8.3 aliases already taken in this dir

	for _, ent := range entries {
		name := ent.Name()
		if name == ".DS_Store" || strings.HasPrefix(name, "._") ||
			name == ".fseventsd" || name == ".Spotlight-V100" || name == ".Trashes" {
			continue
		}
		full := filepath.Join(hostPath, name)
		if b.skipFile != nil && b.skipFile(full, ent.IsDir()) {
			continue
		}

		short11, needLFN := shortAlias(name, used)
		if ent.IsDir() {
			sub := b.allocCluster()
			if sub == 0 {
				fmt.Fprintf(os.Stderr, "sdcard: disk full at directory %s\n", name)
				continue
			}
			// "." and ".." first, per spec.
			dot := rawDirent([]byte(padFAT(".", 11)), attrDir, sub, 0)
			dotdot := rawDirent([]byte(padFAT("..", 11)), attrDir, parentForDotDot(dirClus), 0)
			copy(b.img[b.clusterOffset(sub):], append(dot, dotdot...))
			if needLFN {
				b.writeLFN(dirClus, name, short11)
			}
			if !b.appendDirent(dirClus, rawDirent(short11, attrDir, sub, 0)) {
				fmt.Fprintf(os.Stderr, "sdcard: directory full adding %s\n", name)
				continue
			}
			if err := b.addTree(full, sub); err != nil {
				return err
			}
		} else {
			info, err := ent.Info()
			if err != nil {
				continue
			}
			first, n := b.writeFile(full, info.Size())
			if first == 0 && info.Size() > 0 {
				fmt.Fprintf(os.Stderr, "sdcard: disk full at file %s\n", name)
				continue
			}
			if needLFN {
				b.writeLFN(dirClus, name, short11)
			}
			if !b.appendDirent(dirClus, rawDirent(short11, attrArchive, first, uint32(n))) {
				fmt.Fprintf(os.Stderr, "sdcard: directory full adding %s\n", name)
			}
		}
	}
	return nil
}

// parentForDotDot returns the cluster a new subdirectory's ".."
// entry must store: its immediate parent's first cluster, which is
// dirClus (the directory the subdir is being created in) — except
// when that parent is the root directory, where the FAT convention
// is cluster 0. (A subdir's ".." depends ONLY on its immediate
// parent; the grandparent is irrelevant — an earlier version
// wrongly zeroed ".." whenever the grandparent was root, breaking
// "cd .." out of any depth-≥2 directory such as /machines/next.)
func parentForDotDot(dirClus uint32) uint32 {
	if dirClus == 2 { // parent is the root directory
		return 0
	}
	return dirClus
}

// writeFile streams a host file into a fresh cluster chain.
// Returns the first cluster and the byte count.
func (b *fat32Builder) writeFile(path string, size int64) (uint32, int64) {
	if size == 0 {
		return 0, 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer func() { _ = f.Close() }()

	var first, prev uint32
	remaining := size
	for remaining > 0 {
		c := b.allocCluster()
		if c == 0 {
			return 0, 0
		}
		if first == 0 {
			first = c
		} else {
			b.linkCluster(prev, c)
		}
		prev = c
		off := b.clusterOffset(c)
		chunk := int64(b.spc * 512)
		if remaining < chunk {
			chunk = remaining
		}
		// io.ReadFull, NOT f.Read: a single Read may return fewer
		// bytes than requested with no error, which would leave gaps
		// in (and shift the rest of) a multi-cluster file while the
		// loop advances `remaining` by the full chunk — silent
		// corruption of any file larger than one short read.
		if _, err := io.ReadFull(f, b.img[off:off+int(chunk)]); err != nil {
			return 0, 0
		}
		remaining -= chunk
	}
	return first, size
}

// --- 8.3 alias + LFN machinery ------------------------------------------

// shortAlias produces the 11-byte 8.3 alias for name, unique within
// `used`, and reports whether an LFN chain is required (the name
// doesn't round-trip as plain 8.3).
//
// A name that fits 8.3 except for letter case (e.g. "menu.ini",
// "config.ini") gets its plain upper-cased alias WITHOUT a numeric
// tail — the TBBLUE firmware's bootloader resolves these files by
// 8.3 name only, so "MENU~1.INI" would break the boot ("Error
// opening 'menu.ini'"). An LFN chain still records the original
// case for LFN-aware readers (NextZXOS itself).
func shortAlias(name string, used map[string]bool) ([]byte, bool) {
	base, ext, fits83, caseOnly := basis(name)
	if fits83 {
		alias := padName83(base, ext)
		key := string(alias)
		if !used[key] {
			used[key] = true
			// LFN needed only to preserve non-uppercase spelling.
			return alias, caseOnly
		}
	}
	// Numeric-tail generation: BASE~N.EXT
	for n := 1; n < 1000000; n++ {
		tail := fmt.Sprintf("~%d", n)
		keep := 8 - len(tail)
		if keep > len(base) {
			keep = len(base)
		}
		alias := padName83(base[:keep]+tail, ext)
		key := string(alias)
		if !used[key] {
			used[key] = true
			return alias, true
		}
	}
	// Practically unreachable.
	alias := padName83("XXXXXXXX", ext)
	used[string(alias)] = true
	return alias, true
}

// basis converts a long name into its 8.3 basis (upper-cased,
// illegal chars replaced). fits83 reports that the cleaned name
// fits 8.3 with no character loss (so the plain alias is usable
// without a numeric tail); caseOnly additionally reports that the
// ONLY difference from the original is letter case (an LFN entry
// preserves the spelling but the alias needs no tail).
func basis(name string) (base, ext string, fits83, caseOnly bool) {
	dot := strings.LastIndex(name, ".")
	var rawBase, rawExt string
	if dot > 0 {
		rawBase, rawExt = name[:dot], name[dot+1:]
	} else {
		rawBase, rawExt = name, ""
	}
	clean := func(s string) (string, bool) {
		ok := true
		var out strings.Builder
		for _, r := range strings.ToUpper(s) {
			switch {
			case r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
				strings.ContainsRune("$%'-_@~`!(){}^#&", r):
				out.WriteRune(r)
			default:
				out.WriteRune('_')
				ok = false
			}
		}
		return out.String(), ok
	}
	cb, okB := clean(rawBase)
	ce, okE := clean(rawExt)
	fits83 = okB && okE &&
		len(cb) <= 8 && len(ce) <= 3 &&
		strings.Count(name, ".") <= 1
	caseOnly = fits83 && name != strings.ToUpper(name)
	if len(cb) > 8 {
		cb = cb[:8]
	}
	if len(ce) > 3 {
		ce = ce[:3]
	}
	return cb, ce, fits83, caseOnly
}

func padName83(base, ext string) []byte {
	out := make([]byte, 11)
	copy(out, "           ")
	copy(out[0:8], base)
	copy(out[8:11], ext)
	return out
}

// lfnChecksum is the standard VFAT checksum of the 11-byte alias.
func lfnChecksum(name11 []byte) byte {
	var sum byte
	for _, c := range name11 {
		sum = ((sum & 1) << 7) + (sum >> 1) + c
	}
	return sum
}

// writeLFN emits the LFN entry chain for `long` ahead of where the
// 8.3 entry will be appended. Entries are stored last-part-first
// with the terminal entry flagged 0x40.
func (b *fat32Builder) writeLFN(dirClus uint32, long string, alias11 []byte) {
	u := utf16.Encode([]rune(long))
	// Pad to a multiple of 13 with terminator + 0xFFFF fill.
	n := (len(u) + 12) / 13
	padded := make([]uint16, n*13)
	for i := range padded {
		padded[i] = 0xFFFF
	}
	copy(padded, u)
	if len(u) < len(padded) {
		padded[len(u)] = 0x0000
	}
	sum := lfnChecksum(alias11)
	for seq := n; seq >= 1; seq-- {
		part := padded[(seq-1)*13 : seq*13]
		e := make([]byte, 32)
		ord := byte(seq)
		if seq == n {
			ord |= 0x40
		}
		e[0] = ord
		spans := [][2]int{{1, 5}, {14, 6}, {28, 2}} // offset, count(u16)
		idx := 0
		for _, sp := range spans {
			for k := 0; k < sp[1]; k++ {
				binary.LittleEndian.PutUint16(e[sp[0]+k*2:sp[0]+k*2+2], part[idx])
				idx++
			}
		}
		e[11] = 0x0F
		e[13] = sum
		b.appendDirent(dirClus, e)
	}
}
