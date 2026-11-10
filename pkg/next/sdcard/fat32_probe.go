package sdcard

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// FAT32Info is the geometry of a FAT32-LBA card image, parsed from
// its MBR and BPB. It exists so a caller can tell a bootable NextZXOS
// card from an image the firmware will reject before it even starts
// loading files.
type FAT32Info struct {
	// PartitionLBA is the first sector of the partition. 0 means the
	// image is a superfloppy (a BPB at LBA 0, no MBR).
	PartitionLBA uint32
	// BytesPerSector is the BPB's logical sector size (must be 512).
	BytesPerSector int
	// SectorsPerCluster is the BPB's allocation unit.
	SectorsPerCluster int
	// ReservedSectors is the count before the first FAT.
	ReservedSectors int
	// NumFATs is the number of FAT copies.
	NumFATs int
	// FATSizeSectors is one FAT's length.
	FATSizeSectors uint32
	// TotalSectors is the partition's total sector count.
	TotalSectors uint32
	// RootCluster is the first cluster of the root directory.
	RootCluster uint32
	// ClusterCount is the number of data clusters.
	ClusterCount uint32
	// VolumeLabel is the BPB's 11-byte label, trimmed.
	VolumeLabel string
}

// ProbeFAT32 parses img's MBR/BPB and validates that it is a FAT32
// volume. It supports both the normal MBR-partitioned layout (the
// official cards and BuildFAT32) and a raw superfloppy BPB at LBA 0.
//
// A successful parse does NOT guarantee the filesystem is otherwise
// sound; it does catch the common, fatal geometry mistakes that make
// the Next firmware print "Error opening 'menu.ini/.def'!" — notably
// a FAT32 BPB whose cluster count is below the 65,525-cluster minimum
// (the official System/Next 26.11a emulator image is one such card: 32K
// clusters on a 1GB volume gives only ~32,758 clusters).
func ProbeFAT32(img []byte) (FAT32Info, error) {
	var info FAT32Info
	if len(img) < 512 {
		return info, fmt.Errorf("image too small for an MBR/BPB (%d bytes)", len(img))
	}
	partLBA := uint32(0)
	bpbOff := 0
	mbr := img[:512]
	if isBootSector(mbr) {
		// Superfloppy: BPB at LBA 0.
		info.PartitionLBA = 0
	} else {
		if !isMBR(mbr) {
			return info, fmt.Errorf("no MBR signature or FAT boot sector at LBA 0")
		}
		found := false
		for i := 0; i < 4; i++ {
			e := mbr[446+i*16 : 446+(i+1)*16]
			typ := e[4]
			if typ != 0x0B && typ != 0x0C {
				continue
			}
			partLBA = binary.LittleEndian.Uint32(e[8:12])
			found = true
			break
		}
		if !found {
			return info, fmt.Errorf("MBR has no FAT32-LBA partition (type 0x0B/0x0C)")
		}
		info.PartitionLBA = partLBA
		bpbOff = int(partLBA) * 512
	}
	if bpbOff+512 > len(img) {
		return info, fmt.Errorf("partition starts at LBA %d, beyond the image", partLBA)
	}
	bpb := img[bpbOff : bpbOff+512]
	if !isBootSector(bpb) {
		return info, fmt.Errorf("no boot signature at partition start LBA %d", partLBA)
	}

	info.BytesPerSector = int(binary.LittleEndian.Uint16(bpb[11:13]))
	info.SectorsPerCluster = int(bpb[13])
	info.ReservedSectors = int(binary.LittleEndian.Uint16(bpb[14:16]))
	info.NumFATs = int(bpb[16])
	total16 := uint32(binary.LittleEndian.Uint16(bpb[19:21]))
	total32 := binary.LittleEndian.Uint32(bpb[32:36])
	fatsz16 := uint32(binary.LittleEndian.Uint16(bpb[22:24]))
	fatsz32 := binary.LittleEndian.Uint32(bpb[36:40])
	info.RootCluster = binary.LittleEndian.Uint32(bpb[44:48])
	info.VolumeLabel = strings.TrimSpace(string(bpb[71:82]))
	if total16 != 0 {
		info.TotalSectors = total16
	} else {
		info.TotalSectors = total32
	}
	if fatsz16 != 0 {
		info.FATSizeSectors = fatsz16
	} else {
		info.FATSizeSectors = fatsz32
	}

	if err := info.validate(); err != nil {
		return info, err
	}
	return info, nil
}

func (i *FAT32Info) validate() error {
	if i.BytesPerSector != 512 {
		return fmt.Errorf("FAT bytes/sector = %d, need 512", i.BytesPerSector)
	}
	if i.SectorsPerCluster == 0 || i.SectorsPerCluster&(i.SectorsPerCluster-1) != 0 {
		return fmt.Errorf("FAT sectors/cluster = %d, need a non-zero power of two", i.SectorsPerCluster)
	}
	if i.ReservedSectors == 0 || i.NumFATs == 0 || i.FATSizeSectors == 0 {
		return fmt.Errorf("invalid BPB: reserved=%d fats=%d fat-size=%d",
			i.ReservedSectors, i.NumFATs, i.FATSizeSectors)
	}
	if i.RootCluster != 2 {
		return fmt.Errorf("FAT root cluster = %d, need 2", i.RootCluster)
	}
	fatSectors := uint32(i.NumFATs) * i.FATSizeSectors
	if i.TotalSectors <= uint32(i.ReservedSectors)+fatSectors {
		return fmt.Errorf("FAT total sectors %d too small for reserved+FATs (%d)",
			i.TotalSectors, uint32(i.ReservedSectors)+fatSectors)
	}
	i.ClusterCount = (i.TotalSectors - uint32(i.ReservedSectors) - fatSectors) / uint32(i.SectorsPerCluster)
	// The FAT32 specification requires at least 65,525 clusters. The
	// Next firmware uses the cluster count to choose its FAT driver;
	// below this it parses the FAT32 BPB as FAT16 and rejects files.
	if i.ClusterCount < 65525 {
		return fmt.Errorf("invalid FAT32 geometry: %d clusters (need >=65525), "+
			"%d sectors/cluster on a %d-sector partition — "+
			"the Next firmware will fail with \"Error opening 'menu.ini/.def'!\"",
			i.ClusterCount, i.SectorsPerCluster, i.TotalSectors)
	}
	return nil
}

func isBootSector(b []byte) bool {
	return len(b) >= 512 && b[510] == 0x55 && b[511] == 0xAA &&
		(binary.LittleEndian.Uint16(b[11:13]) == 512)
}

func isMBR(b []byte) bool {
	return len(b) >= 512 && b[510] == 0x55 && b[511] == 0xAA
}
