package sdcard

import (
	"bytes"
	"testing"
)

// writeOneBlock drives a complete CMD24 transaction: the command, the
// 0xFE data token, 512 payload bytes and two CRC bytes.
func writeOneBlock(c *Card, arg uint32, payload []byte) {
	sendCommand(c, 24, arg)
	c.WriteData(0xFE)
	for _, b := range payload {
		c.WriteData(b)
	}
	c.WriteData(0xFF)
	c.WriteData(0xFF)
	// Drain the data-accepted token and the not-busy byte, so a
	// following command reads its own R1 rather than this response.
	for i := 0; i < 16; i++ {
		if b := c.ReadData(); b == 0xFF {
			break
		}
	}
}

// An SDSC card (CCS=0, which is what we advertise) takes a BYTE
// address in the command argument. CMD17 already divided it back out
// to an LBA; CMD24 passed the raw argument to WriteBlock as if it were
// one, so a write addressed at sector 2 landed at offset 2 rather than
// at 1024 and corrupted the image.
func TestWriteBlockUsesByteAddressingOnSDSC(t *testing.T) {
	img := make([]byte, 8*512)
	src, err := NewImageSource(img, false)
	if err != nil {
		t.Fatal(err)
	}
	c := NewCard(src) // SDSC: byte-addressed
	initCard(t, c)

	payload := bytes.Repeat([]byte{0xA5}, 512)
	writeOneBlock(c, 2*512, payload) // host asks for sector 2

	if !bytes.Equal(img[2*512:3*512], payload) {
		t.Errorf("sector 2 was not written; first bytes there = % x", img[2*512:2*512+8])
	}
	if !bytes.Equal(img[0:512], make([]byte, 512)) {
		t.Errorf("sector 0 was clobbered: % x", img[0:8])
	}
}

// The same argument read back through CMD17 must return what CMD24
// wrote. This is the round trip NextZXOS actually performs.
func TestWriteThenReadRoundTripsOnSDSC(t *testing.T) {
	img := make([]byte, 8*512)
	src, err := NewImageSource(img, false)
	if err != nil {
		t.Fatal(err)
	}
	c := NewCard(src)
	initCard(t, c)

	payload := bytes.Repeat([]byte{0x5A}, 512)
	writeOneBlock(c, 3*512, payload)

	if r1 := sendCommand(c, 17, 3*512); r1 != 0x00 {
		t.Fatalf("CMD17 R1 = $%02X, want $00", r1)
	}
	var token byte = 0xFF
	for i := 0; i < 16 && token == 0xFF; i++ {
		token = c.ReadData()
	}
	if token != 0xFE {
		t.Fatalf("CMD17 data token = $%02X, want $FE", token)
	}
	got := make([]byte, 512)
	for i := range got {
		got[i] = c.ReadData()
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("read back % x..., want % x...", got[:8], payload[:8])
	}
}

// CMD25 streams consecutive blocks. Stepping the raw byte address by
// one put each block 512 sectors past the last instead of one.
func TestWriteMultiBlockAdvancesBySectorOnSDSC(t *testing.T) {
	img := make([]byte, 8*512)
	src, err := NewImageSource(img, false)
	if err != nil {
		t.Fatal(err)
	}
	c := NewCard(src)
	initCard(t, c)

	first := bytes.Repeat([]byte{0x11}, 512)
	second := bytes.Repeat([]byte{0x22}, 512)
	sendCommand(c, 25, 1*512)
	for _, blk := range [][]byte{first, second} {
		c.WriteData(0xFC) // multi-block data token
		for _, b := range blk {
			c.WriteData(b)
		}
		c.WriteData(0xFF)
		c.WriteData(0xFF)
	}
	c.WriteData(0xFD) // stop-tran token

	if !bytes.Equal(img[1*512:2*512], first) {
		t.Errorf("sector 1: % x, want 11...", img[1*512:1*512+8])
	}
	if !bytes.Equal(img[2*512:3*512], second) {
		t.Errorf("sector 2: % x, want 22...", img[2*512:2*512+8])
	}
}

// CMD32/33/38 latch a byte-address range on an SDSC card too.
func TestEraseRangeUsesByteAddressingOnSDSC(t *testing.T) {
	img := bytes.Repeat([]byte{0xEE}, 8*512)
	src, err := NewImageSource(img, false)
	if err != nil {
		t.Fatal(err)
	}
	c := NewCard(src)
	initCard(t, c)

	sendCommand(c, 32, 2*512)
	sendCommand(c, 33, 2*512)
	sendCommand(c, 38, 0)

	if !bytes.Equal(img[2*512:3*512], make([]byte, 512)) {
		t.Errorf("sector 2 not erased: % x", img[2*512:2*512+8])
	}
	if !bytes.Equal(img[3*512:4*512], bytes.Repeat([]byte{0xEE}, 512)) {
		t.Errorf("sector 3 was erased too: % x", img[3*512:3*512+8])
	}
}
