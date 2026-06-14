package dma

import "testing"

type memMap map[uint16]byte

func (m memMap) Read(a uint16) byte     { return m[a] }
func (m memMap) Write(a uint16, v byte) { m[a] = v }

// transferCmd builds a complete zxnDMA / Z80-DMA command stream for a
// memory->memory transfer: RESET, WR0 (direction + src + length), WR1
// (port A mode), WR2 (port B mode), WR4 (dst), WR6 LOAD, WR6 ENABLE.
func transferCmd(src, dst, length uint16, aMode, bMode byte, aToB bool) []byte {
	wr0 := byte(0x79) // WR0: D6..D3=1111 (all four follow bytes), D1D0=01 transfer
	if aToB {
		wr0 |= 0x04 // D2 = port A -> port B
	}
	wr1 := map[byte]byte{addrDecrement: 0x04, addrIncrement: 0x14, addrFixed: 0x24}[aMode]
	wr2 := map[byte]byte{addrDecrement: 0x00, addrIncrement: 0x10, addrFixed: 0x20}[bMode]
	return []byte{
		0xC3, // RESET
		wr0, byte(src), byte(src >> 8), byte(length), byte(length >> 8),
		wr1,
		wr2,
		0x8D, byte(dst), byte(dst >> 8), // WR4: port B addr low+high, byte mode
		0xCF, // LOAD
		0x87, // ENABLE
	}
}

func feed(d *DMA, bytes []byte) {
	for _, b := range bytes {
		d.WriteCommand(b)
	}
}

// TestCapturedNextGuideCommand locks in the exact byte stream the
// NextGuide ".guide" viewer writes to port 0x6B for one of its text
// shifts. The old fixed-7-step stub mis-read C7 CB as src=$CBC7 and ran
// a 3709-byte garbage transfer; the real decode is src=$3C0E, dst=$3C50,
// length=$000F, A->B, both ports decrementing.
func TestCapturedNextGuideCommand(t *testing.T) {
	mem := memMap{}
	// Source range walked by a 15-byte DECREMENTING read from $3C0E.
	for i := uint16(0); i < 0x0F; i++ {
		mem[0x3C0E-i] = byte(0x80 + i)
	}
	d := New(mem)
	feed(d, []byte{
		0xC3, 0xC7, 0xCB,
		0x7D, 0x0E, 0x3C, 0x0F, 0x00, // WR0: A->B, src=$3C0E, len=$000F
		0x44, 0x02, // WR1 (port A decrement) + timing byte
		0x40, 0x02, // WR2 (port B decrement) + timing byte
		0xAD, 0x50, 0x3C, // WR4: dst=$3C50 (continuous mode)
		0x82,       // WR5
		0xCF, 0x87, // LOAD, ENABLE
	})

	if d.Source() != 0x3C0E {
		t.Errorf("Source = $%04X, want $3C0E", d.Source())
	}
	if d.Destination() != 0x3C50 {
		t.Errorf("Destination = $%04X, want $3C50", d.Destination())
	}
	if d.Length() != 0x000F {
		t.Errorf("Length = $%04X, want $000F", d.Length())
	}
	// 15 bytes copied, both decrementing: dst $3C50-i = src $3C0E-i.
	for i := uint16(0); i < 0x0F; i++ {
		if got, want := mem[0x3C50-i], byte(0x80+i); got != want {
			t.Errorf("dst[$%04X] = $%02X, want $%02X", 0x3C50-i, got, want)
		}
	}
}

// TestBlockCopyIncrement is the canonical incrementing mem->mem copy.
func TestBlockCopyIncrement(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 16; i++ {
		mem[0x4000+i] = byte(0x10 + i)
	}
	d := New(mem)
	feed(d, transferCmd(0x4000, 0x6000, 16, addrIncrement, addrIncrement, true))
	for i := uint16(0); i < 16; i++ {
		if got, want := mem[0x6000+i], byte(0x10+i); got != want {
			t.Errorf("dst[$%04X] = $%02X, want $%02X", 0x6000+i, got, want)
		}
	}
}

// TestFixedSourceRepeatsByte: a fixed source pumps one byte N times.
func TestFixedSourceRepeatsByte(t *testing.T) {
	mem := memMap{0x4000: 0x42}
	d := New(mem)
	feed(d, transferCmd(0x4000, 0x6000, 8, addrFixed, addrIncrement, true))
	for i := uint16(0); i < 8; i++ {
		if mem[0x6000+i] != 0x42 {
			t.Errorf("dst[$%04X] = $%02X, want $42 (fixed src)", 0x6000+i, mem[0x6000+i])
		}
	}
}

// TestFixedDestinationFanIn: a fixed destination keeps only the last byte.
func TestFixedDestinationFanIn(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 4; i++ {
		mem[0x4000+i] = byte(0xA0 + i)
	}
	d := New(mem)
	feed(d, transferCmd(0x4000, 0x6000, 4, addrIncrement, addrFixed, true))
	if got := mem[0x6000]; got != 0xA3 {
		t.Errorf("dst-fixed final = $%02X, want $A3", got)
	}
}

// TestDirectionBtoA verifies a port-B -> port-A transfer: the source is
// port B's address, the destination is port A's.
func TestDirectionBtoA(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 4; i++ {
		mem[0x6000+i] = byte(0x50 + i) // port B holds the source data
	}
	d := New(mem)
	// portA=$4000, portB=$6000, B->A => copy $6000.. into $4000..
	feed(d, transferCmd(0x4000, 0x6000, 4, addrIncrement, addrIncrement, false))
	for i := uint16(0); i < 4; i++ {
		if got, want := mem[0x4000+i], byte(0x50+i); got != want {
			t.Errorf("B->A dst[$%04X] = $%02X, want $%02X", 0x4000+i, got, want)
		}
	}
}

// TestLengthZeroMeans64K verifies the "length 0 == 65536" rule.
func TestLengthZeroMeans64K(t *testing.T) {
	count := 0
	mem := &countingMem{count: &count}
	d := New(mem)
	feed(d, transferCmd(0x0000, 0x0000, 0, addrFixed, addrFixed, true))
	if count != 65536 {
		t.Errorf("write count = %d, want 65536", count)
	}
}

type countingMem struct{ count *int }

func (c *countingMem) Read(_ uint16) byte     { return 0 }
func (c *countingMem) Write(_ uint16, _ byte) { *c.count++ }

// TestResetClearsConfig verifies a $C3 RESET (at a base-byte boundary)
// clears the latched configuration. Note RESET is a WR6 command, so it
// is only honoured when a base byte — not a follow byte — is expected.
func TestResetClearsConfig(t *testing.T) {
	mem := memMap{}
	d := New(mem)
	feed(d, []byte{0x7D, 0x34, 0x12, 0x78, 0x56}) // WR0: src=$1234, len=$5678
	if d.Source() != 0x1234 || d.Length() != 0x5678 {
		t.Fatalf("setup: Source=$%04X Length=$%04X", d.Source(), d.Length())
	}
	d.WriteCommand(0xC3) // RESET (all follow bytes already consumed)
	if d.Source() != 0 || d.Length() != 0 {
		t.Errorf("after RESET: Source=$%04X Length=$%04X, want 0/0", d.Source(), d.Length())
	}
}

// TestForwardOverlapPropagation documents the observable overlap
// behaviour: incrementing src=$4000 dst=$4001 propagates the first byte.
func TestForwardOverlapPropagation(t *testing.T) {
	mem := memMap{0x4000: 0xAA, 0x4001: 0xBB, 0x4002: 0xCC, 0x4003: 0xDD}
	d := New(mem)
	feed(d, transferCmd(0x4000, 0x4001, 3, addrIncrement, addrIncrement, true))
	for _, addr := range []uint16{0x4001, 0x4002, 0x4003} {
		if mem[addr] != 0xAA {
			t.Errorf("$%04X = $%02X, want $AA (forward-overlap propagation)", addr, mem[addr])
		}
	}
}

// TestBackwardOverlapShift mirrors the NextGuide text shift: a +1 shift
// done with DECREMENTING ports is overlap-safe and moves the block up by
// one without smearing.
func TestBackwardOverlapShift(t *testing.T) {
	mem := memMap{0x4000: 0xAA, 0x4001: 0xBB, 0x4002: 0xCC, 0x4003: 0xDD}
	d := New(mem)
	// src=$4002 down to $4000, dst=$4003 down to $4001 (3 bytes).
	feed(d, transferCmd(0x4002, 0x4003, 3, addrDecrement, addrDecrement, true))
	// $4003<-$4002=CC, $4002<-$4001=BB, $4001<-$4000=AA, $4000 untouched.
	want := map[uint16]byte{0x4000: 0xAA, 0x4001: 0xAA, 0x4002: 0xBB, 0x4003: 0xCC}
	for addr, w := range want {
		if mem[addr] != w {
			t.Errorf("$%04X = $%02X, want $%02X", addr, mem[addr], w)
		}
	}
}
