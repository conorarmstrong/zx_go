package sdcard

import (
	"encoding/binary"
	"strings"
	"testing"
)

// syntheticBPB returns a 512-byte FAT32 boot sector with the supplied
// geometry. Only the fields ProbeFAT32 reads are populated.
func syntheticBPB(totalSectors, fatsz uint32, spc byte, label string) []byte {
	b := make([]byte, 512)
	copy(b[3:11], "MSWIN4.1")
	binary.LittleEndian.PutUint16(b[11:13], 512)
	b[13] = spc
	binary.LittleEndian.PutUint16(b[14:16], 32) // reserved
	b[16] = 2                                   // FATs
	binary.LittleEndian.PutUint32(b[32:36], totalSectors)
	binary.LittleEndian.PutUint32(b[36:40], fatsz)
	binary.LittleEndian.PutUint32(b[44:48], 2) // root cluster
	copy(b[71:82], padLabel(label))
	b[510], b[511] = 0x55, 0xAA
	return b
}

func padLabel(s string) []byte {
	b := make([]byte, 11)
	for i := range b {
		b[i] = ' '
	}
	copy(b, s)
	return b
}

func syntheticMBR(partLBA uint32) []byte {
	m := make([]byte, 512)
	m[446+4] = 0x0C // FAT32-LBA
	binary.LittleEndian.PutUint32(m[446+8:446+12], partLBA)
	m[510], m[511] = 0x55, 0xAA
	return m
}

func TestProbeFAT32AcceptsValidGeometry(t *testing.T) {
	// 1GB, 4K clusters (spc=8), enough clusters for FAT32.
	const total = 2_097_152
	const fatsz = 2044
	img := make([]byte, (2048+1)*512)
	copy(img[:512], syntheticMBR(2048))
	copy(img[2048*512:], syntheticBPB(total, fatsz, 8, "ZXNEXT"))

	info, err := ProbeFAT32(img)
	if err != nil {
		t.Fatalf("ProbeFAT32: %v", err)
	}
	if info.PartitionLBA != 2048 {
		t.Errorf("PartitionLBA = %d, want 2048", info.PartitionLBA)
	}
	if info.SectorsPerCluster != 8 {
		t.Errorf("SectorsPerCluster = %d, want 8", info.SectorsPerCluster)
	}
	if info.ClusterCount < 65525 {
		t.Errorf("ClusterCount = %d, want >=65525", info.ClusterCount)
	}
	if info.VolumeLabel != "ZXNEXT" {
		t.Errorf("VolumeLabel = %q, want ZXNEXT", info.VolumeLabel)
	}
}

// The official System/Next 26.11a "image for emulators" is 1GB with 32K
// clusters: 32,758 clusters, below the FAT32 minimum. The firmware
// prints "Error opening 'menu.ini/.def'!" and stops. The probe must
// identify it rather than passing it to the guest.
func TestProbeFAT32RejectsTheOfficial26_11aEmulatorImageGeometry(t *testing.T) {
	const total = 2_097_089
	const fatsz = 257
	img := make([]byte, 64*512)
	copy(img[:512], syntheticMBR(63))
	copy(img[63*512:], syntheticBPB(total, fatsz, 64, "NEXT1.4.1"))

	info, err := ProbeFAT32(img)
	if err == nil {
		t.Fatalf("ProbeFAT32 accepted the invalid 32K-cluster geometry (clusters=%d)", info.ClusterCount)
	}
	if !strings.Contains(err.Error(), "65525") {
		t.Errorf("error %q should name the 65,525-cluster minimum", err)
	}
	if !strings.Contains(err.Error(), "menu.ini") {
		t.Errorf("error %q should explain the firmware symptom", err)
	}
}

func TestProbeFAT32RejectsBadGeometry(t *testing.T) {
	cases := []struct {
		name string
		bpb  []byte
	}{
		{"bad bytes/sector", func() []byte {
			b := syntheticBPB(2_097_152, 2044, 8, "X")
			binary.LittleEndian.PutUint16(b[11:13], 1024)
			return b
		}()},
		{"bad sectors/cluster", syntheticBPB(2_097_152, 2044, 3, "X")},
		{"root cluster not 2", func() []byte {
			b := syntheticBPB(2_097_152, 2044, 8, "X")
			binary.LittleEndian.PutUint32(b[44:48], 3)
			return b
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img := make([]byte, 2048*512)
			copy(img[:512], syntheticMBR(2048))
			copy(img[2048*512:], tc.bpb)
			if _, err := ProbeFAT32(img); err == nil {
				t.Error("ProbeFAT32 accepted an invalid BPB")
			}
		})
	}
}

func TestProbeFAT32AcceptsRawSuperfloppy(t *testing.T) {
	img := make([]byte, 2048*512)
	copy(img, syntheticBPB(2_097_152, 2044, 8, "RAW"))
	info, err := ProbeFAT32(img)
	if err != nil {
		t.Fatalf("ProbeFAT32: %v", err)
	}
	if info.PartitionLBA != 0 {
		t.Errorf("PartitionLBA = %d, want 0 for a raw BPB", info.PartitionLBA)
	}
}
