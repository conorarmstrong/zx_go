package sdcard

import "testing"

// iter 366: CMD25 WRITE_MULTIPLE_BLOCK. The host sends CMD25 with a
// start LBA, then streams blocks each framed by a 0xFC token + 512
// data + 2 CRC; each accepted block writes to the next sequential
// LBA and the card replies 0x05 (accepted). A 0xFD stop-tran token
// ends the transaction.

// drainAccepted polls up to 16 ReadData bytes for the data-response
// token (0x05) the card emits after each accepted block.
func drainAccepted(c *Card) byte {
	var b byte = 0xFF
	for i := 0; i < 16 && b == 0xFF; i++ {
		b = c.ReadData()
	}
	return b
}

func writeOneMultiBlock(c *Card, fill byte) {
	c.WriteData(0xFC) // multi-write start token
	for i := 0; i < 512; i++ {
		c.WriteData(byte(i) ^ fill)
	}
	c.WriteData(0xFF) // CRC hi
	c.WriteData(0xFF) // CRC lo
}

func TestCard_CMD25_WriteMultipleBlock(t *testing.T) {
	img := make([]byte, 8192)
	src, _ := NewImageSource(img, false)
	c := NewCard(src)

	// CMD25 starting at LBA 1.
	if r1 := sendCommand(c, 25, 1); r1 != 0x00 {
		t.Fatalf("CMD25 R1=%02X, want 0x00", r1)
	}

	// Block 0 → LBA 1, block 1 → LBA 2.
	writeOneMultiBlock(c, 0xA5)
	if tok := drainAccepted(c); tok != 0x05 {
		t.Fatalf("block 0 token=%02X, want 0x05", tok)
	}
	writeOneMultiBlock(c, 0x3C)
	if tok := drainAccepted(c); tok != 0x05 {
		t.Fatalf("block 1 token=%02X, want 0x05", tok)
	}

	// Stop-tran token ends the stream; card replies (not-busy).
	c.WriteData(0xFD)
	c.ReadData() // consume the not-busy byte

	// Verify both blocks landed at sequential LBAs.
	for i := 0; i < 512; i++ {
		if got, want := img[1*512+i], byte(i)^0xA5; got != want {
			t.Fatalf("LBA1 byte %d = %02X, want %02X", i, got, want)
		}
		if got, want := img[2*512+i], byte(i)^0x3C; got != want {
			t.Fatalf("LBA2 byte %d = %02X, want %02X", i, got, want)
		}
	}
	// LBA 3 must be untouched (still zero).
	for i := 0; i < 512; i++ {
		if img[3*512+i] != 0 {
			t.Fatalf("LBA3 byte %d = %02X, want 0 (untouched)", i, img[3*512+i])
		}
	}
}

// After the stop-tran token, the card must be back in command mode:
// a fresh CMD (e.g. CMD13 SEND_STATUS) should dispatch normally.
func TestCard_CMD25_StopTranReturnsToCommandMode(t *testing.T) {
	img := make([]byte, 8192)
	src, _ := NewImageSource(img, false)
	c := NewCard(src)

	sendCommand(c, 25, 0)
	writeOneMultiBlock(c, 0x11)
	drainAccepted(c)
	c.WriteData(0xFD) // stop tran
	c.ReadData()

	// CMD13 (SEND_STATUS) returns a 2-byte R2; first byte 0x00.
	c.WriteCS(0xFE)
	c.WriteData(0x40 | 13)
	c.WriteData(0)
	c.WriteData(0)
	c.WriteData(0)
	c.WriteData(0)
	c.WriteData(0xFF)
	var r byte = 0xFF
	for i := 0; i < 16 && r == 0xFF; i++ {
		r = c.ReadData()
	}
	if r != 0x00 {
		t.Errorf("after CMD25 stop-tran, CMD13 R2[0]=%02X, want 0x00 (command mode restored)", r)
	}
}
