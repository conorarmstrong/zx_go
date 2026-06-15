package dma

import "testing"

// withTiming builds an A->B mem transfer that also sends the WR1/WR2 timing
// bytes (and, if presc != 0, the WR2 prescaler byte), so the duration model can
// be exercised. aCyc/bCyc are the raw timing-byte D1:D0 codes (0=4,1=3,2=2).
func withTiming(src, dst, length uint16, aCycCode, bCycCode byte, presc byte) []byte {
	wr1 := byte(0x14 | 0x40)   // port A increment, mem, D6 timing-byte-follows
	wr2 := byte(0x10 | 0x40)   // port B increment, mem, D6 timing-byte-follows
	bTiming := bCycCode & 0x03 // WR2 timing byte: cycle code
	if presc != 0 {
		bTiming |= 0x20 // D5: prescaler byte follows
	}
	stream := []byte{
		0xC3,
		0x7D, byte(src), byte(src >> 8), byte(length), byte(length >> 8),
		wr1, aCycCode & 0x03,
		wr2, bTiming,
	}
	if presc != 0 {
		stream = append(stream, presc)
	}
	stream = append(stream,
		0x8D, byte(dst), byte(dst>>8), // WR4 continuous, port B addr
		0xCF, 0x87, // LOAD, ENABLE
	)
	return stream
}

// Without a prescaler, a transfer's duration is length × (read cycles + write
// cycles). Cycle code 2 = 2 cycles, so 4 bytes at 2+2 = 16 T-states.
func TestTransferDurationCycleLength(t *testing.T) {
	d := New(memMap{})
	feed(d, withTiming(0x4000, 0x6000, 4, 0x02, 0x02, 0))
	if got := d.Duration(); got != 4*(2+2) {
		t.Errorf("Duration = %d, want %d", got, 4*(2+2))
	}
}

// A 4-cycle / 3-cycle pairing: 4 bytes at (4+3) = 28 T-states.
func TestTransferDurationMixedCycles(t *testing.T) {
	d := New(memMap{})
	feed(d, withTiming(0x4000, 0x6000, 4, 0x00 /*=4*/, 0x01 /*=3*/, 0))
	if got := d.Duration(); got != 4*(4+3) {
		t.Errorf("Duration = %d, want %d", got, 4*(4+3))
	}
}

// A non-zero prescaler forces each byte to take at least that many cycles
// (the fixed-time-transfer feature used for sampled audio): 8 bytes × 100 = 800.
func TestPrescalerDominatesDuration(t *testing.T) {
	d := New(memMap{})
	feed(d, withTiming(0x4000, 0x6000, 8, 0x02, 0x02, 100))
	if got := d.Duration(); got != 8*100 {
		t.Errorf("Duration with prescaler = %d, want %d", got, 8*100)
	}
}

// The transfer mode is decoded from WR4 D6:D5.
func TestModeDecoded(t *testing.T) {
	d := New(memMap{})
	// Burst: WR4 D6:D5 = 10 → base byte 0b110_0_1101 with addr follows.
	feed(d, []byte{0xC3, 0x7D, 0x00, 0x40, 0x01, 0x00, 0x14, 0x10,
		0xCD, 0x00, 0x60, 0xCF, 0x87}) // WR4 0xCD = burst + port B addr
	if d.Mode() != modeBurst {
		t.Errorf("mode = %d, want burst", d.Mode())
	}
	d2 := New(memMap{})
	feed(d2, []byte{0xC3, 0x7D, 0x00, 0x40, 0x01, 0x00, 0x14, 0x10,
		0xAD, 0x00, 0x60, 0xCF, 0x87}) // WR4 0xAD = continuous
	if d2.Mode() != modeContinuous {
		t.Errorf("mode = %d, want continuous", d2.Mode())
	}
}

// In continuous mode the transfer duration is charged to the CPU clock via the
// cycle sink; in burst mode it is not (the CPU keeps running).
func TestCycleSinkContinuousCharges(t *testing.T) {
	var charged uint64
	d := New(memMap{})
	d.SetCycleSink(func(n uint64) { charged += n })
	feed(d, withTiming(0x4000, 0x6000, 8, 0x02, 0x02, 0)) // WR4 in withTiming = continuous
	if charged != 8*(2+2) {
		t.Errorf("continuous charged = %d, want %d", charged, 8*(2+2))
	}
}

func TestCycleSinkBurstDoesNotCharge(t *testing.T) {
	var charged uint64
	d := New(memMap{})
	d.SetCycleSink(func(n uint64) { charged += n })
	// Burst-mode stream (WR4 0xCD = burst).
	feed(d, []byte{0xC3, 0x7D, 0x00, 0x40, 0x08, 0x00, 0x14, 0x10,
		0xCD, 0x00, 0x60, 0xCF, 0x87})
	if charged != 0 {
		t.Errorf("burst charged = %d, want 0 (CPU keeps running)", charged)
	}
	if d.Duration() == 0 {
		t.Error("Duration should still be computed in burst mode")
	}
}
