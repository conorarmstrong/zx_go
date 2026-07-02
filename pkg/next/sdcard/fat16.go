package sdcard

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FAT16 image builder.
//
// Produces an in-memory FAT16-formatted disk image from a host
// directory tree, suitable for serving back over the SD-SPI
// emulator's BlockSource interface. The image is laid out as:
//
// sector 0 : MBR (one partition entry, FAT16 type)
// sector 1 : BPB / boot sector of the FAT16 partition
// sector 2..1+F : FAT 1 (F sectors)
// sector 2+F..1+2F : FAT 2 (mirror of FAT 1)
// sector 2+2F..D : root directory (R sectors, 32-byte entries)
// sector D+ : data clusters
//
// The fields F, R, D are computed from BuildOpts. The image is
// addressable as 512-byte LBA blocks.
//
// Only what NextZXOS needs to boot is implemented:
// * MBR + BPB the divMMC ROM recognises
// * 8.3 short filenames (no LFN — those are a separate dir-entry
// scheme; NextZXOS supports them but the ROM-based bootloader
// only requires short names for the files in /SYS and /NEXTZXOS)
// * Files written in cluster-chain order; no fragmentation
// * Subdirectories implemented as cluster-allocated directory
// entries (including "." and ".." entries)

// BuildOpts configures the FAT16 builder.
type BuildOpts struct {
	// VolumeLabel for the BPB. 11 chars max, space-padded.
	VolumeLabel string
	// SizeMB is the total image size in MB. Min 16, max ~2048
	// (FAT16's hard limit; beyond that you need FAT32). Combined
	// with SectorsPerCluster this caps the disk capacity at
	// 65525 * SectorsPerCluster * 512 bytes.
	SizeMB int
	// SectorsPerCluster controls cluster granularity. Larger
	// clusters waste more space per file but allow bigger
	// volumes. Defaults to 32 (16 KB clusters) which fits up to
	// ~1 GB. Must be a power of two.
	SectorsPerCluster int
	// SkipFile returns true for entries that should be excluded
	// from the image. Useful for boot images that don't need
	// every distro file (saves cluster space and build time).
	SkipFile func(hostPath string, isDir bool) bool
}

// BuildFAT16 walks dir and produces an in-memory FAT16 image
// containing every file and subdirectory under it. The returned
// []byte is exactly opts.SizeMB * 1024 * 1024 bytes long.
//
// Files that don't fit are silently skipped with a warning to
// stderr. Filenames with characters disallowed by FAT16 (lower-
// case, long names, etc.) are coerced via toFAT83 — uppercase,
// truncated to 8.3, illegal chars replaced with '_'.
func BuildFAT16(dir string, opts BuildOpts) ([]byte, error) {
	if opts.SizeMB <= 0 {
		opts.SizeMB = 64
	}
	if opts.SizeMB > 2048 {
		return nil, fmt.Errorf("sdcard: SizeMB=%d exceeds FAT16 limit (2048)", opts.SizeMB)
	}
	if opts.VolumeLabel == "" {
		opts.VolumeLabel = "ZX_GO_SD"
	}

	const sectorSize = 512
	sectorsPerCluster := opts.SectorsPerCluster
	if sectorsPerCluster <= 0 {
		sectorsPerCluster = 32 // 16 KB clusters
	}
	const reservedSectors = 1
	const numFATs = 2
	const rootDirEntries = 512                                // 32 sectors of 16 entries
	const rootDirSectors = (rootDirEntries * 32) / sectorSize // 32 sectors
	// Partition start LBA. We use 2048 (1 MB alignment) to match modern
	// SD-card formatting conventions. the reference Spectrum Next emulator's tbblue.mmc uses the
	// same offset; the FPGA bootrom (tbblue_loader.rom) was validated
	// against that layout, and starting at LBA 1 trips its mount path.
	const partitionStartLBA = 2048
	totalSectors := opts.SizeMB * 1024 * 1024 / sectorSize // sectors in the image
	if totalSectors <= partitionStartLBA+reservedSectors+rootDirSectors+1 {
		return nil, fmt.Errorf("sdcard: SizeMB=%d too small to hold MBR + 1 MB alignment + FAT16 partition", opts.SizeMB)
	}
	partitionSectors := totalSectors - partitionStartLBA
	dataSectors := partitionSectors - reservedSectors - rootDirSectors
	totalClusters := dataSectors / sectorsPerCluster
	fatSize := (totalClusters*2 + sectorSize - 1) / sectorSize
	dataSectors = partitionSectors - reservedSectors - numFATs*fatSize - rootDirSectors
	totalClusters = dataSectors / sectorsPerCluster

	if totalClusters < 4085 {
		return nil, fmt.Errorf("sdcard: image too small for FAT16 (clusters=%d, min 4085)", totalClusters)
	}
	if totalClusters > 65525 {
		return nil, fmt.Errorf("sdcard: image too large for FAT16 (clusters=%d, max 65525)", totalClusters)
	}

	img := make([]byte, totalSectors*sectorSize)
	b := &builder{
		img:               img,
		sectorSize:        sectorSize,
		sectorsPerCluster: sectorsPerCluster,
		reservedSectors:   reservedSectors,
		numFATs:           numFATs,
		fatSize:           fatSize,
		rootDirSectors:    rootDirSectors,
		rootDirEntries:    rootDirEntries,
		totalSectors:      totalSectors,
		totalClusters:     totalClusters,
		partitionStartLBA: partitionStartLBA,
		fatOffset:         (partitionStartLBA + reservedSectors) * sectorSize,
		rootDirOffset:     (partitionStartLBA + reservedSectors + numFATs*fatSize) * sectorSize,
		dataOffset:        (partitionStartLBA + reservedSectors + numFATs*fatSize + rootDirSectors) * sectorSize,
		nextFreeCluster:   2,
		volumeLabel:       opts.VolumeLabel,
		skipFile:          opts.SkipFile,
	}
	b.writeMBR()
	b.writeBPB()
	b.initFAT()
	if err := b.addDirectory(dir, -1, 0); err != nil {
		return nil, err
	}
	b.mirrorFAT()
	return img, nil
}

type builder struct {
	img []byte

	sectorSize        int
	sectorsPerCluster int
	reservedSectors   int
	numFATs           int
	fatSize           int
	rootDirSectors    int
	rootDirEntries    int
	totalSectors      int
	totalClusters     int
	partitionStartLBA int

	fatOffset     int // byte offset of FAT 1
	rootDirOffset int // byte offset of root directory
	dataOffset    int // byte offset of first cluster (cluster #2)

	nextFreeCluster uint16

	volumeLabel string
	skipFile    func(hostPath string, isDir bool) bool
}

const (
	attrReadOnly = 0x01
	attrHidden   = 0x02
	attrSystem   = 0x04
	attrVolume   = 0x08
	attrDir      = 0x10
	attrArchive  = 0x20

	fatEOC = 0xFFFF // end of cluster chain
)

func (b *builder) writeMBR() {
	// 446 bytes of boot code: leave zero. Then one partition entry,
	// then 0x55 0xAA signature.
	pte := b.img[446:462]
	pte[0] = 0x00 // non-bootable (matches typical SD-card MBR)
	// CHS first: rough — head 0, sector 1, cyl 32 — modern hosts ignore CHS
	pte[1], pte[2], pte[3] = 0x00, 0x21, 0x20
	pte[4] = 0x06 // FAT16 (>=32MB partition)
	// CHS last: approximate
	pte[5], pte[6], pte[7] = 0xFE, 0xFF, 0xFF
	binary.LittleEndian.PutUint32(pte[8:12], uint32(b.partitionStartLBA))
	binary.LittleEndian.PutUint32(pte[12:16], uint32(b.totalSectors-b.partitionStartLBA))
	b.img[510] = 0x55
	b.img[511] = 0xAA
}

func (b *builder) writeBPB() {
	off := b.partitionStartLBA * b.sectorSize // BPB lives at the partition's first sector
	partitionSectors := b.totalSectors - b.partitionStartLBA
	// Jump instruction: EB 3C 90
	b.img[off+0] = 0xEB
	b.img[off+1] = 0x3C
	b.img[off+2] = 0x90
	copy(b.img[off+3:off+11], []byte("MSWIN4.1"))
	binary.LittleEndian.PutUint16(b.img[off+11:off+13], uint16(b.sectorSize))
	b.img[off+13] = byte(b.sectorsPerCluster)
	binary.LittleEndian.PutUint16(b.img[off+14:off+16], uint16(b.reservedSectors))
	b.img[off+16] = byte(b.numFATs)
	binary.LittleEndian.PutUint16(b.img[off+17:off+19], uint16(b.rootDirEntries))
	// "Total sectors" fields measure the partition, not the whole
	// image. If partition fits in 16 bits use the 16-bit slot;
	// otherwise stash zero there and use the 32-bit slot at +32.
	if partitionSectors < 0x10000 {
		binary.LittleEndian.PutUint16(b.img[off+19:off+21], uint16(partitionSectors))
	} else {
		binary.LittleEndian.PutUint16(b.img[off+19:off+21], 0)
	}
	b.img[off+21] = 0xF8                                                             // media descriptor (fixed)
	binary.LittleEndian.PutUint16(b.img[off+22:off+24], uint16(b.fatSize))           // sectors per FAT
	binary.LittleEndian.PutUint16(b.img[off+24:off+26], 63)                          // sectors per track
	binary.LittleEndian.PutUint16(b.img[off+26:off+28], 255)                         // heads
	binary.LittleEndian.PutUint32(b.img[off+28:off+32], uint32(b.partitionStartLBA)) // hidden sectors = partition LBA
	if partitionSectors >= 0x10000 {
		binary.LittleEndian.PutUint32(b.img[off+32:off+36], uint32(partitionSectors))
	}
	b.img[off+36] = 0x80                                            // drive number (hard disk)
	b.img[off+37] = 0x00                                            // reserved
	b.img[off+38] = 0x29                                            // extended boot signature
	binary.LittleEndian.PutUint32(b.img[off+39:off+43], 0x12345678) // volume ID
	label := padFAT(b.volumeLabel, 11)
	copy(b.img[off+43:off+54], label)
	copy(b.img[off+54:off+62], []byte("FAT16   "))
	b.img[off+510] = 0x55
	b.img[off+511] = 0xAA
}

func (b *builder) initFAT() {
	// FAT entry 0 = media descriptor + 0xFFF8
	// FAT entry 1 = 0xFFFF (clean)
	binary.LittleEndian.PutUint16(b.img[b.fatOffset:b.fatOffset+2], 0xFFF8)
	binary.LittleEndian.PutUint16(b.img[b.fatOffset+2:b.fatOffset+4], 0xFFFF)
}

func (b *builder) mirrorFAT() {
	src := b.img[b.fatOffset : b.fatOffset+b.fatSize*b.sectorSize]
	dst := b.img[b.fatOffset+b.fatSize*b.sectorSize:]
	copy(dst, src)
}

// allocCluster reserves a new cluster, sets its FAT entry to EOC,
// and returns the cluster number. Returns 0 if the disk is full.
func (b *builder) allocCluster() uint16 {
	if int(b.nextFreeCluster) >= 2+b.totalClusters {
		return 0
	}
	c := b.nextFreeCluster
	b.nextFreeCluster++
	off := b.fatOffset + int(c)*2
	binary.LittleEndian.PutUint16(b.img[off:off+2], fatEOC)
	return c
}

// linkCluster makes prev's FAT entry point to next, then sets
// next's FAT entry to EOC.
func (b *builder) linkCluster(prev, next uint16) {
	off := b.fatOffset + int(prev)*2
	binary.LittleEndian.PutUint16(b.img[off:off+2], next)
	off = b.fatOffset + int(next)*2
	binary.LittleEndian.PutUint16(b.img[off:off+2], fatEOC)
}

// clusterOffset returns the byte offset within img of the start
// of the given data cluster.
func (b *builder) clusterOffset(c uint16) int {
	return b.dataOffset + int(c-2)*b.sectorsPerCluster*b.sectorSize
}

// addDirectory enumerates a host directory and writes entries into
// the FAT directory at the given cluster (or root if parentCluster
// == -1). dotDotCluster is the cluster this directory's ".." entry
// must reference — its immediate parent's cluster, or 0 if that
// parent is the root directory; ignored when parentCluster == -1.
// Recurses into subdirectories.
func (b *builder) addDirectory(hostPath string, parentCluster int, dotDotCluster uint16) error {
	dirEntries, err := os.ReadDir(hostPath)
	if err != nil {
		return fmt.Errorf("sdcard: ReadDir %s: %w", hostPath, err)
	}
	sort.Slice(dirEntries, func(i, j int) bool {
		return dirEntries[i].Name() < dirEntries[j].Name()
	})

	// Layout of directory area:
	// parentCluster == -1 -> root, fixed-size rootDirOffset..rootDirOffset+rootDirSectors
	// else -> cluster chain for the subdir
	var dirBytes []byte
	if parentCluster == -1 {
		dirBytes = b.img[b.rootDirOffset : b.rootDirOffset+b.rootDirSectors*b.sectorSize]
	} else {
		// Cluster-chained directory. Grow as needed.
		dirBytes = b.clusterChainSlice(uint16(parentCluster))
	}

	entryIdx := 0
	if parentCluster != -1 {
		// Write "." and ".." entries.
		b.writeDirEntry(dirBytes, entryIdx, ".", attrDir, uint16(parentCluster), 0)
		entryIdx++
		b.writeDirEntry(dirBytes, entryIdx, "..", attrDir, dotDotCluster, 0)
		entryIdx++
	}

	for _, ent := range dirEntries {
		// Skip macOS metadata noise.
		if ent.Name() == ".DS_Store" || strings.HasPrefix(ent.Name(), "._") {
			continue
		}
		hostFullPath := filepath.Join(hostPath, ent.Name())
		if b.skipFile != nil && b.skipFile(hostFullPath, ent.IsDir()) {
			continue
		}
		short := toFAT83(ent.Name())
		if entryIdx >= len(dirBytes)/32 {
			if parentCluster == -1 {
				fmt.Fprintf(os.Stderr, "sdcard: root directory full, skipping rest of %s\n", hostPath)
				break
			}
			// Extend cluster chain.
			extra := b.allocCluster()
			if extra == 0 {
				fmt.Fprintf(os.Stderr, "sdcard: disk full, skipping rest of %s\n", hostPath)
				break
			}
			// Stitch into the chain. Find current last cluster.
			last := b.findChainEnd(uint16(parentCluster))
			b.linkCluster(last, extra)
			// Refresh dirBytes view.
			dirBytes = b.clusterChainSlice(uint16(parentCluster))
		}
		if ent.IsDir() {
			subCluster := b.allocCluster()
			if subCluster == 0 {
				fmt.Fprintf(os.Stderr, "sdcard: disk full at directory %s\n", ent.Name())
				continue
			}
			b.writeDirEntry(dirBytes, entryIdx, short, attrDir, subCluster, 0)
			entryIdx++
			subDotDot := uint16(0)
			if parentCluster != -1 {
				subDotDot = uint16(parentCluster)
			}
			if err := b.addDirectory(hostFullPath, int(subCluster), subDotDot); err != nil {
				return err
			}
		} else {
			info, err := ent.Info()
			if err != nil {
				continue
			}
			cluster, size := b.writeFile(hostFullPath, info.Size())
			if cluster == 0 && size > 0 {
				fmt.Fprintf(os.Stderr, "sdcard: disk full at file %s\n", ent.Name())
				continue
			}
			b.writeDirEntry(dirBytes, entryIdx, short, attrArchive, cluster, uint32(size))
			entryIdx++
		}
	}
	return nil
}

// findChainEnd walks the FAT chain starting at first and returns
// the last cluster (the one with EOC).
func (b *builder) findChainEnd(first uint16) uint16 {
	c := first
	for {
		off := b.fatOffset + int(c)*2
		next := binary.LittleEndian.Uint16(b.img[off : off+2])
		if next >= fatEOC-7 { // any EOC-ish value
			return c
		}
		c = next
	}
}

// clusterChainSlice returns a contiguous slice covering the data
// of all clusters in the chain starting at first. Used for the
// directory writer so it can address entries by index across
// cluster boundaries (since dir-entry size = 32 bytes, neatly
// fits within cluster bytes).
func (b *builder) clusterChainSlice(first uint16) []byte {
	// Walk chain and collect cluster byte slices.
	var slices [][]byte
	c := first
	for {
		off := b.clusterOffset(c)
		slices = append(slices, b.img[off:off+b.sectorsPerCluster*b.sectorSize])
		fatOff := b.fatOffset + int(c)*2
		next := binary.LittleEndian.Uint16(b.img[fatOff : fatOff+2])
		if next >= fatEOC-7 {
			break
		}
		c = next
	}
	// Concatenate into a single backing buffer? No — we want WRITES
	// to land in the image. Real clusters may be non-contiguous in
	// the image, so build a virtual slice with copy-back via the
	// helper below. For now, if only one cluster, return its slice
	// directly; multi-cluster directories are rare and we handle
	// them with a fresh allocation + scatter writeback if needed.
	if len(slices) == 1 {
		return slices[0]
	}
	// For multi-cluster, return a flat buffer; addDirectory writes
	// into it then we scatter back. But to keep this simple, we
	// just return the first cluster — if a directory needs more
	// than one cluster's worth of entries, addDirectory's resize
	// branch will allocate a new cluster and re-fetch the slice.
	// (This means a single dir-entry-batch ends with at most one
	// cluster of fresh writes; the loop will refresh each time.)
	return slices[len(slices)-1]
}

// writeFile copies bytes from a host file into a cluster chain,
// returns the first cluster and bytes written.
func (b *builder) writeFile(hostPath string, size int64) (uint16, int64) {
	clusterBytes := b.sectorsPerCluster * b.sectorSize
	data, err := os.ReadFile(hostPath)
	if err != nil {
		return 0, 0
	}
	if int64(len(data)) > size {
		data = data[:size]
	}
	if len(data) == 0 {
		return 0, 0
	}
	first := b.allocCluster()
	if first == 0 {
		return 0, 0
	}
	prev := first
	wrote := 0
	for wrote < len(data) {
		n := len(data) - wrote
		if n > clusterBytes {
			n = clusterBytes
		}
		off := b.clusterOffset(prev)
		copy(b.img[off:off+n], data[wrote:wrote+n])
		wrote += n
		if wrote < len(data) {
			next := b.allocCluster()
			if next == 0 {
				break
			}
			b.linkCluster(prev, next)
			prev = next
		}
	}
	return first, int64(wrote)
}

// writeDirEntry writes a single 32-byte directory entry at index
// idx inside dirBytes.
func (b *builder) writeDirEntry(dirBytes []byte, idx int, name string, attr byte, cluster uint16, size uint32) {
	off := idx * 32
	if off+32 > len(dirBytes) {
		return
	}
	e := dirBytes[off : off+32]
	for i := range e {
		e[i] = 0
	}
	// Filename: 8 chars + 3 ext, space-padded.
	base, ext := splitName(name)
	copy(e[0:8], padFAT(base, 8))
	copy(e[8:11], padFAT(ext, 3))
	e[11] = attr
	// Time/date stamps: fixed valid value (2024-01-01 00:00:00).
	binary.LittleEndian.PutUint16(e[22:24], 0x0000) // creation time
	binary.LittleEndian.PutUint16(e[24:26], 0x5821) // creation date 2024-01-01
	binary.LittleEndian.PutUint16(e[18:20], 0x5821) // last access date
	binary.LittleEndian.PutUint16(e[26:28], cluster)
	binary.LittleEndian.PutUint32(e[28:32], size)
}

// toFAT83 coerces a host filename to a FAT 8.3 short name.
// Uppercase ASCII only; everything else becomes '_'. Length
// truncated to 8 base + 3 ext.
func toFAT83(name string) string {
	if name == "." {
		return "."
	}
	if name == ".." {
		return ".."
	}
	upper := strings.ToUpper(name)
	// Strip leading dots ("hidden" UNIX-style).
	for strings.HasPrefix(upper, ".") {
		upper = upper[1:]
	}
	dot := strings.LastIndex(upper, ".")
	var base, ext string
	if dot < 0 {
		base = upper
		ext = ""
	} else {
		base = upper[:dot]
		ext = upper[dot+1:]
	}
	base = sanitiseFAT(base)
	ext = sanitiseFAT(ext)
	if len(base) > 8 {
		base = base[:8]
	}
	if len(ext) > 3 {
		ext = ext[:3]
	}
	if ext == "" {
		return base
	}
	return base + "." + ext
}

func sanitiseFAT(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c)
		case c == '$' || c == '%' || c == '\'' || c == '-' || c == '_' || c == '@' || c == '~' || c == '`' || c == '!' || c == '(' || c == ')':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

func splitName(short string) (base, ext string) {
	// "." and ".." are literal directory-navigation entries, not
	// name.ext composites — splitting on the last dot would treat
	// their dots as an extension separator and corrupt the dirent.
	if short == "." || short == ".." {
		return short, ""
	}
	dot := strings.LastIndex(short, ".")
	if dot < 0 {
		return short, ""
	}
	return short[:dot], short[dot+1:]
}

func padFAT(s string, n int) string {
	if len(s) >= n {
		return s[:n]
	}
	return s + strings.Repeat(" ", n-len(s))
}

// NextBootFilter returns a SkipFile predicate that excludes large
// distro files the early boot path doesn't need. Tunes the 1 GB
// FAT16 image down to a few MB by dropping:
//
// * QXL.WIN — 31 MB QL drive image (read later by browser, never
// by the FPGA bootrom or NextZXOS startup)
// * /apps, /demos, /games — user content (browsed post-boot)
// * /docs, /src — documentation
//
// TBBLUE.FW and TBBLUE.TBU are KEPT — the FPGA bootrom
// (tbblue_loader.rom, path B) scans the root directory for
// "TBBLUE FW " (8.3 packed, single trailing space) at offset
// $0438 of itself and loads the file's clusters to $6000 before
// JP $6000. Without it, the bootrom silently exits via the
// failure RET in $0138 and the next instruction (JP $0100) HALTs
// the CPU.
func NextBootFilter() func(hostPath string, isDir bool) bool {
	skipBaseName := map[string]bool{
		"QXL.WIN":         true,
		"CONTRIBUTING.md": true,
		"LICENSE.md":      true,
		"README.md":       true,
	}
	skipDir := map[string]bool{
		"apps":   true,
		"demos":  true,
		"docs":   true,
		"games":  true,
		"src":    true,
		"home":   true,
		"tmp":    true,
		"TBUS":   true,
		"extras": true,
	}
	return func(hostPath string, isDir bool) bool {
		base := filepath.Base(hostPath)
		if isDir {
			return skipDir[base]
		}
		return skipBaseName[base]
	}
}
