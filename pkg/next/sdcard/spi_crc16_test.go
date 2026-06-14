package sdcard

import (
	"bytes"
	"testing"
)

// SD data blocks carry a real CRC-16/CCITT over the payload (poly
// 0x1021, init 0x0000 — the "XMODEM" variant), high byte first. Real
// cards and the reference SD model send this; we previously sent
// dummy $FF/$00 CRC bytes. The Guide crash's first divergence vs the
// reference (read-tape, the development log) is exactly the 2nd CRC byte of a
// data block esxDOS reads ($FF ours vs $45 reference) — so the CRC must be
// real and correct.
func TestCRC16CCITT_KnownVector(t *testing.T) {
	// Canonical CRC-16/XMODEM check value for "123456789" is 0x31C3.
	if got := crc16CCITT([]byte("123456789")); got != 0x31C3 {
		t.Fatalf("crc16CCITT(%q) = %#04x, want 0x31C3", "123456789", got)
	}
	// Empty input → 0x0000 (init value, no bytes processed).
	if got := crc16CCITT(nil); got != 0x0000 {
		t.Fatalf("crc16CCITT(nil) = %#04x, want 0x0000", got)
	}
}

// A CMD17 data block's trailing 2 bytes must be the real CRC16 of the
// 512-byte payload (high byte first), not a dummy constant.
func TestReadBlock_RealCRC(t *testing.T) {
	img := make([]byte, 512*4)
	for i := 0; i < 512; i++ {
		img[i] = byte(i*7 + 3) // deterministic non-trivial pattern in block 0
	}
	src, _ := NewImageSource(img, false)
	c := NewCard(src)

	// CMD17 READ_SINGLE_BLOCK, block 0 (SDSC: arg = lba*512 = 0).
	if r1 := sendCommand(c, 17, 0); r1 != 0x00 {
		t.Fatalf("CMD17 R1=%02X, want 0x00", r1)
	}
	// Poll for the 0xFE data token (past the Nac idle byte).
	var tok byte = 0xFF
	for i := 0; i < 8 && tok == 0xFF; i++ {
		tok = c.ReadData()
	}
	if tok != 0xFE {
		t.Fatalf("data token=%02X, want 0xFE", tok)
	}
	// 512 data bytes, then 2 CRC bytes (high byte first).
	data := make([]byte, 512)
	for i := range data {
		data[i] = c.ReadData()
	}
	crcHi, crcLo := c.ReadData(), c.ReadData()
	got := uint16(crcHi)<<8 | uint16(crcLo)
	want := crc16CCITT(data)

	if !bytes.Equal(data, img[:512]) {
		t.Fatalf("data payload != block 0")
	}
	if got != want {
		t.Fatalf("data-block CRC = %#04x, want %#04x (real CRC16 of payload)", got, want)
	}
	// Guard against the old dummy CRC sneaking back: a real payload's
	// CRC must not be the placeholder $FFFF or $0000.
	if got == 0xFFFF || got == 0x0000 {
		t.Fatalf("data-block CRC = %#04x looks like a dummy placeholder", got)
	}
}
