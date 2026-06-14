package sdcard

import "testing"

// iter 367: SD erase command sequence CMD32 (ERASE_WR_BLK_START) +
// CMD33 (ERASE_WR_BLK_END) + CMD38 (ERASE). The latched inclusive
// LBA range is zeroed on CMD38.

func TestCard_Erase_ZeroesRange(t *testing.T) {
	img := make([]byte, 8192) // 16 blocks
	for i := range img {
		img[i] = 0xBB // non-zero so erase is observable
	}
	src, _ := NewImageSource(img, false)
	c := NewCard(src)

	// Erase blocks 1..3 inclusive.
	if r := sendCommand(c, 32, 1); r != 0x00 {
		t.Fatalf("CMD32 R1=%02X, want 0x00", r)
	}
	if r := sendCommand(c, 33, 3); r != 0x00 {
		t.Fatalf("CMD33 R1=%02X, want 0x00", r)
	}
	if r := sendCommand(c, 38, 0); r != 0x00 {
		t.Fatalf("CMD38 R1=%02X, want 0x00", r)
	}

	// Blocks 1..3 must be zero; block 0 and block 4 untouched (0xBB).
	for b := 1; b <= 3; b++ {
		for i := 0; i < 512; i++ {
			if img[b*512+i] != 0x00 {
				t.Fatalf("block %d byte %d = %02X, want 0 (erased)", b, i, img[b*512+i])
			}
		}
	}
	if img[0*512] != 0xBB {
		t.Errorf("block 0 = %02X, want 0xBB (untouched)", img[0*512])
	}
	if img[4*512] != 0xBB {
		t.Errorf("block 4 = %02X, want 0xBB (untouched)", img[4*512])
	}
}

// CMD38 with no prior CMD32 must not erase anything (returns error).
func TestCard_Erase_NoRangeIsRejected(t *testing.T) {
	img := make([]byte, 4096)
	for i := range img {
		img[i] = 0x77
	}
	src, _ := NewImageSource(img, false)
	c := NewCard(src)

	if r := sendCommand(c, 38, 0); r != 0x04 {
		t.Errorf("bare CMD38 R1=%02X, want 0x04 (illegal — no range set)", r)
	}
	if img[0] != 0x77 {
		t.Errorf("bare CMD38 wiped block 0 (=%02X); must be a no-op", img[0])
	}
}

// CMD32/33 given out of order (end < start) still erases the range.
func TestCard_Erase_SwappedBoundsStillErases(t *testing.T) {
	img := make([]byte, 8192)
	for i := range img {
		img[i] = 0x5A
	}
	src, _ := NewImageSource(img, false)
	c := NewCard(src)

	sendCommand(c, 32, 5) // start > end
	sendCommand(c, 33, 4)
	sendCommand(c, 38, 0)

	for b := 4; b <= 5; b++ {
		if img[b*512] != 0x00 {
			t.Errorf("block %d not erased (=%02X)", b, img[b*512])
		}
	}
}
