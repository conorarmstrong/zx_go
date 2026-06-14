package sdcard

import "testing"

// the development log: a real SD card holds DataOut high for at least one
// byte (Ncr) between a command's last bit and the response. Our card
// answered IMMEDIATELY on non-CMD17/18 commands, so fixed-count SPI
// readers in TBBLUE.FW saw the response one byte early — the first
// divergent instruction vs the reference emulator ($78E5 fork: HW reads $FF,
// ours read a real byte). CMD17/18 already pad (handleReadBlock);
// every R1-family response must pad the same single $FF.
func TestCard_R1ResponsesHaveNcrPad(t *testing.T) {
	img := make([]byte, 1024*1024)
	src, _ := NewImageSource(img, false)
	c := NewCard(src)
	c.WriteCS(0xFE) // select slot 0

	// CMD0 frame (GO_IDLE_STATE).
	for _, b := range []byte{0x40, 0, 0, 0, 0, 0x95} {
		c.WriteData(b)
	}
	if got := c.ReadData(); got != 0xFF {
		t.Fatalf("first post-command byte = $%02X, want $FF (Ncr pad)", got)
	}
	if got := c.ReadData(); got != 0xFF {
		t.Fatalf("second post-command byte = $%02X, want $FF (Ncr=2, reference-matched — the development log)", got)
	}
	if got := c.ReadData(); got != 0x01 {
		t.Fatalf("second post-command byte = $%02X, want $01 (R1 idle)", got)
	}
}

// CMD58 (READ_OCR) returns R3 = R1 + 4 OCR bytes; the Ncr pad must
// precede the whole response.
func TestCard_CMD58_NcrPadBeforeR3(t *testing.T) {
	img := make([]byte, 1024*1024)
	src, _ := NewImageSource(img, false)
	c := NewCard(src)
	c.WriteCS(0xFE)
	for _, b := range []byte{0x40 | 58, 0, 0, 0, 0, 0xFF} {
		c.WriteData(b)
	}
	want := []byte{0xFF, 0xFF, 0x00, 0x80, 0x00, 0x00, 0x00} // Ncr=2, R1, OCR[4]
	for i, w := range want {
		if got := c.ReadData(); got != w {
			t.Fatalf("byte %d = $%02X, want $%02X", i, got, w)
		}
	}
}
