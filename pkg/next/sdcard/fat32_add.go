package sdcard

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// AddFileToFAT32 inserts data as a file named fileName under the directory
// dirPath (e.g. "imported", or "games/imported") into an existing in-memory
// FAT32 image, creating intermediate directories as needed. It returns the
// absolute SD-card path of the written file using its 8.3 short name (e.g.
// "/imported/SONIC.NEX"), which a loader can reference unambiguously.
//
// The image is modified in place; callers persist it (e.g. ImageSource.
// WriteBackTo). The file is written with a unique 8.3 alias within the target
// directory, so re-importing the same name produces a fresh entry rather than
// corrupting the directory.
func AddFileToFAT32(img []byte, dirPath, fileName string, data []byte) (string, error) {
	b, err := openFAT32(img)
	if err != nil {
		return "", err
	}

	dirClus := uint32(2) // root directory
	var sdDir strings.Builder
	for _, part := range strings.Split(strings.Trim(dirPath, "/"), "/") {
		if part == "" {
			continue
		}
		sub := b.findSubdir(dirClus, part)
		if sub == 0 {
			if sub = b.createSubdir(dirClus, part); sub == 0 {
				return "", fmt.Errorf("sdcard: could not create directory %q (disk full?)", part)
			}
		}
		dirClus = sub
		sdDir.WriteString("/")
		sdDir.WriteString(strings.ToUpper(part))
	}

	used := b.collectUsed(dirClus)
	short11, _ := shortAlias(fileName, used)

	first, n := b.writeData(data)
	if first == 0 && len(data) > 0 {
		return "", fmt.Errorf("sdcard: not enough free space to write %s", fileName)
	}
	if !b.appendDirent(dirClus, rawDirent(short11, attrArchive, first, uint32(n))) {
		return "", fmt.Errorf("sdcard: directory full adding %s", fileName)
	}
	b.mirrorFAT()

	return sdDir.String() + "/" + name83ToPath(short11), nil
}

// openFAT32 reconstructs a builder over an existing FAT32 image so files can be
// appended: it reads the BPB at the partition start, derives the FAT and data
// offsets, and locates the first free cluster by scanning the FAT.
func openFAT32(img []byte) (*fat32Builder, error) {
	if len(img) < 512 || img[510] != 0x55 || img[511] != 0xAA {
		return nil, fmt.Errorf("sdcard: no MBR signature")
	}
	// The first partition's start LBA — don't assume the 1 MB-aligned
	// value BuildFAT32 uses; real cards vary.
	partLBA := int(binary.LittleEndian.Uint32(img[454:458]))
	if partLBA == 0 {
		partLBA = fat32PartLBA
	}
	if len(img) < (partLBA+1)*512 {
		return nil, fmt.Errorf("sdcard: image too small for its FAT32 partition")
	}
	bpb := img[partLBA*512:]
	if bpb[510] != 0x55 || bpb[511] != 0xAA {
		return nil, fmt.Errorf("sdcard: no boot signature at the FAT32 partition start (LBA %d)", partLBA)
	}
	if bytesPerSector := binary.LittleEndian.Uint16(bpb[11:13]); bytesPerSector != 512 {
		return nil, fmt.Errorf("sdcard: unsupported bytes/sector %d", bytesPerSector)
	}
	spc := int(bpb[13])
	reserved := int(binary.LittleEndian.Uint16(bpb[14:16]))
	numFATs := int(bpb[16])
	fatsz := int(binary.LittleEndian.Uint32(bpb[36:40]))
	partSectors := int(binary.LittleEndian.Uint32(bpb[32:36]))
	rootClus := binary.LittleEndian.Uint32(bpb[44:48])
	if spc == 0 || fatsz == 0 || numFATs < 1 || rootClus != 2 {
		return nil, fmt.Errorf("sdcard: unsupported FAT32 BPB (spc=%d fatsz=%d fats=%d root=%d)", spc, fatsz, numFATs, rootClus)
	}
	clusters := uint32((partSectors - reserved - numFATs*fatsz) / spc)
	b := &fat32Builder{
		img:          img,
		spc:          spc,
		fatsz:        fatsz,
		totalSectors: len(img) / 512,
		clusters:     clusters,
		fatOffset:    (partLBA + reserved) * 512,
		dataOffset:   (partLBA + reserved + numFATs*fatsz) * 512,
		nextFree:     2,
		scanAlloc:    true,
	}
	for c := uint32(2); c < clusters+2; c++ {
		b.nextFree = c
		if b.getFAT(c) == 0 {
			return b, nil
		}
	}
	b.nextFree = clusters + 2 // full
	return b, nil
}

// findSubdir scans the directory chain at dirClus for a subdirectory whose 8.3
// short name matches name (case-insensitive). Returns its first cluster, or 0
// if not found.
func (b *fat32Builder) findSubdir(dirClus uint32, name string) uint32 {
	want := string(dirName83(name))
	for c := dirClus; c >= 2 && c < 0x0FFFFFF8; c = b.getFAT(c) {
		off := b.clusterOffset(c)
		cluster := b.img[off : off+b.spc*512]
		for i := 0; i+32 <= len(cluster); i += 32 {
			e := cluster[i : i+32]
			switch {
			case e[0] == 0x00:
				return 0 // end of directory
			case e[0] == 0xE5, e[11] == 0x0F, e[11]&attrDir == 0:
				continue
			case string(e[0:11]) == want:
				return uint32(binary.LittleEndian.Uint16(e[20:22]))<<16 |
					uint32(binary.LittleEndian.Uint16(e[26:28]))
			}
		}
	}
	return 0
}

// createSubdir allocates and initialises a new subdirectory `name` inside
// dirClus and links it in. Returns the new directory's first cluster, or 0 on
// disk-full.
func (b *fat32Builder) createSubdir(dirClus uint32, name string) uint32 {
	sub := b.allocCluster()
	if sub == 0 {
		return 0
	}
	b.zeroCluster(sub)
	dot := rawDirent([]byte(padFAT(".", 11)), attrDir, sub, 0)
	dotdot := rawDirent([]byte(padFAT("..", 11)), attrDir, parentForDotDot(dirClus), 0)
	copy(b.img[b.clusterOffset(sub):], append(dot, dotdot...))
	short11 := dirName83(name)
	if !b.appendDirent(dirClus, rawDirent(short11, attrDir, sub, 0)) {
		return 0
	}
	return sub
}

// writeData streams data into a fresh cluster chain (clusters are zeroed first,
// so trailing bytes of the final cluster are clean). Returns the first cluster
// and the byte count.
func (b *fat32Builder) writeData(data []byte) (uint32, int) {
	if len(data) == 0 {
		return 0, 0
	}
	var first, prev uint32
	rem := data
	for len(rem) > 0 {
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
		b.zeroCluster(c)
		chunk := b.spc * 512
		if len(rem) < chunk {
			chunk = len(rem)
		}
		copy(b.img[b.clusterOffset(c):], rem[:chunk])
		rem = rem[chunk:]
	}
	return first, len(data)
}

// collectUsed gathers the 8.3 aliases already present in dirClus, so a new
// file's alias can be made unique within the directory.
func (b *fat32Builder) collectUsed(dirClus uint32) map[string]bool {
	used := map[string]bool{}
	for c := dirClus; c >= 2 && c < 0x0FFFFFF8; c = b.getFAT(c) {
		off := b.clusterOffset(c)
		cluster := b.img[off : off+b.spc*512]
		for i := 0; i+32 <= len(cluster); i += 32 {
			e := cluster[i : i+32]
			if e[0] == 0x00 {
				return used
			}
			if e[0] == 0xE5 || e[11] == 0x0F {
				continue
			}
			used[string(e[0:11])] = true
		}
	}
	return used
}

func (b *fat32Builder) zeroCluster(c uint32) {
	off := b.clusterOffset(c)
	for i := 0; i < b.spc*512; i++ {
		b.img[off+i] = 0
	}
}

// dirName83 returns the 11-byte 8.3 form of a directory name (no extension).
func dirName83(name string) []byte {
	base, ext, _, _ := basis(name)
	return padName83(base, ext)
}

// name83ToPath turns an 11-byte 8.3 dirent name into a "NAME.EXT" path segment.
func name83ToPath(name11 []byte) string {
	base := strings.TrimRight(string(name11[0:8]), " ")
	ext := strings.TrimRight(string(name11[8:11]), " ")
	if ext == "" {
		return base
	}
	return base + "." + ext
}
