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
// cycles). Cycle code 2 = 2 cycles, so 4 bytes at
// 2+2 = 16 T-states.
func TestTransferDurationCycleLength(t *testing.T) {
	d := New(memMap{})
	feed(d, withTiming(0x4000, 0x6000, 4, 0x02, 0x02, 0))
	if got := d.Duration(); got != 4*(2+2) {
		t.Errorf("Duration = %d, want %d", got, 4*(2+2))
	}
}

// A 4-cycle / 3-cycle pairing: 4 bytes at (4+3) = 28.
func TestTransferDurationMixedCycles(t *testing.T) {
	d := New(memMap{})
	feed(d, withTiming(0x4000, 0x6000, 4, 0x00 /*=4*/, 0x01 /*=3*/, 0))
	if got := d.Duration(); got != 4*(4+3) {
		t.Errorf("Duration = %d, want %d", got, 4*(4+3))
	}
}

// A non-zero prescaler forces each byte to take at least that many cycles
// (the fixed-time-transfer feature used for sampled audio): 8 bytes × 100,
// = 800.
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

// Burst mode gives the CPU time back only where the FPGA actually deasserts
// cpu_busreq_n_s: inside WAITING_CYCLES (dma.vhd:441-449), which is reachable
// only with a non-zero prescaler (dma.vhd:424). That is the interleaved case,
// and it is the one that must not charge the sink: the CPU is running in the
// gaps, not stopped. (Burst with no prescaler never enters that state and does
// charge; see TestBurstWithoutPrescalerChargesTheCPULikeContinuous.)
func TestCycleSinkInterleavedBurstDoesNotCharge(t *testing.T) {
	var charged uint64
	var now uint64
	d := New(memMap{})
	d.SetCycleSink(func(n uint64) { charged += n })
	d.SetClock(func() uint64 { return now })
	feed(d, burstStream(0x4000, 0x6000, 8, 50))
	for ; now <= 8*50+50; now += 10 {
		d.Step(now)
	}
	if d.ByteCounter() != 8 {
		t.Fatalf("interleaved burst moved %d of 8 bytes; the fixture is not transferring", d.ByteCounter())
	}
	if charged != 0 {
		t.Errorf("interleaved burst charged = %d, want 0 (the CPU runs in the gaps)", charged)
	}
	if d.Duration() == 0 {
		t.Error("Duration should still be computed in burst mode")
	}
}

// Burst mode gives the CPU time only inside WAITING_CYCLES, and WAITING_CYCLES
// is reachable only when the prescaler is non-zero (dma.vhd:424 enters it under
// "R2_portB_preescaler_s > 0", and dma.vhd:441-449 is the only place that
// raises cpu_busreq_n again mid-block). A burst block with no prescaler
// therefore holds the bus from its single request edge all the way to
// FINISH_DMA, exactly like a continuous one, and the CPU is frozen for the
// whole of it (zxnext.vhd:1824-1835 freezes the CPU while the DMA holds the
// bus). So it is charged.
func TestBurstWithoutPrescalerChargesTheCPULikeContinuous(t *testing.T) {
	var charged uint64
	d := New(memMap{})
	d.SetCycleSink(func(n uint64) { charged += n })
	// Burst (WR4 $CD) with no WR2 timing byte, so no prescaler byte either.
	feed(d, []byte{0xC3, 0x7D, 0x00, 0x40, 0x08, 0x00, 0x14, 0x10,
		0xCD, 0x00, 0x60, 0xCF, 0x87})
	if d.Duration() == 0 {
		t.Fatal("the fixture computed no duration, so it moved nothing")
	}
	if charged != d.Duration() {
		t.Errorf("burst without a prescaler charged %d, want %d: the bus is held for the whole block",
			charged, d.Duration())
	}
}

// Both port timings default to "01", and "01" is three cycles, not two. The
// reset pin sets R1_portA_timming_byte_s and R2_portB_timming_byte_s to "01"
// (dma.vhd:233-234), the $C3 command sets them to "01" again (dma.vhd:641-642),
// and the transfer FSM decodes "01" as TRANSFERING_READ_3 / WRITE_3, three
// cycles (dma.vhd:314, :321). A stream that sends no WR1 or WR2 timing byte
// therefore runs at 3+3 per byte.
func TestDefaultPortTimingIsThreeCycles(t *testing.T) {
	const want = uint64(8 * (3 + 3))

	// Power-up: nothing has been written to the controller at all.
	d := New(memMap{})
	feed(d, []byte{
		0x7D, 0x00, 0x40, 0x08, 0x00, // WR0: A->B, port A = $4000, length 8
		0x14, 0x10, // WR1, WR2: both memory, incrementing, no timing byte
		0x8D, 0x00, 0x60, // WR4: continuous, port B = $6000
		0xCF, 0x87, // LOAD, ENABLE
	})
	if got := d.Duration(); got != want {
		t.Errorf("power-up Duration = %d, want %d", got, want)
	}

	// And after a $C3, which restores the same two defaults.
	d2 := New(memMap{})
	feed(d2, withTiming(0x4000, 0x6000, 8, 0x02, 0x02, 0)) // program 2 cycles
	feed(d2, []byte{
		0xC3,                         // RESET
		0x7D, 0x00, 0x40, 0x08, 0x00, // WR0: A->B, port A = $4000, length 8
		0x14, 0x10,
		0x8D, 0x00, 0x60,
		0xCF, 0x87,
	})
	if got := d2.Duration(); got != want {
		t.Errorf("Duration after $C3 = %d, want %d", got, want)
	}
}

// The Z80 DMA calls timing code 11 "do not use", but the FPGA gives it a
// definite meaning: the timing decode is a case statement whose "when others"
// arm goes to TRANSFERING_READ_2 / TRANSFERING_WRITE_2, the four-cycle path
// (dma.vhd:316, :323). So 11 behaves as 00 does, not as 10 does.
func TestTimingCodeElevenIsFourCycles(t *testing.T) {
	d := New(memMap{})
	feed(d, withTiming(0x4000, 0x6000, 8, 0x03, 0x03, 0))
	if got, want := d.Duration(), uint64(8*(4+4)); got != want {
		t.Errorf("Duration at timing code 11/11 = %d, want %d", got, want)
	}

	// ...and the neighbouring code really is the two-cycle one, so the test
	// above is pinning the 11 arm rather than the whole decode.
	d2 := New(memMap{})
	feed(d2, withTiming(0x4000, 0x6000, 8, 0x02, 0x02, 0))
	if got, want := d2.Duration(), uint64(8*(2+2)); got != want {
		t.Errorf("Duration at timing code 10/10 = %d, want %d", got, want)
	}
}

// $C7 and $CB each write "01" back into one port's timing register
// (dma.vhd:647-651), so they undo a programmed timing byte and put that port
// back on three cycles. They are per port: $C7 is port A's, $CB is port B's.
func TestResetPortTimingCommands(t *testing.T) {
	// Both ports programmed to two cycles, then both reset.
	block := func(after ...byte) []byte {
		stream := []byte{
			0xC3,                         // RESET
			0x7D, 0x00, 0x40, 0x08, 0x00, // WR0: A->B, port A = $4000, length 8
			0x54, 0x02, // WR1: port A memory, increment, + timing byte: 2 cycles
			0x50, 0x02, // WR2: port B memory, increment, + timing byte: 2 cycles
		}
		stream = append(stream, after...)
		return append(stream, 0x8D, 0x00, 0x60, 0xCF, 0x87)
	}

	d := New(memMap{})
	feed(d, block(0xC7, 0xCB))
	if got, want := d.Duration(), uint64(8*(3+3)); got != want {
		t.Errorf("Duration after $C7 $CB = %d, want %d", got, want)
	}

	// $C7 alone leaves port B where the guest put it, which is what makes these
	// two separate commands rather than one.
	dA := New(memMap{})
	feed(dA, block(0xC7))
	if got, want := dA.Duration(), uint64(8*(3+2)); got != want {
		t.Errorf("Duration after $C7 alone = %d, want %d", got, want)
	}

	dB := New(memMap{})
	feed(dB, block(0xCB))
	if got, want := dB.Duration(), uint64(8*(2+3)); got != want {
		t.Errorf("Duration after $CB alone = %d, want %d", got, want)
	}
}

// The per-block bus acquisition is derived from the FPGA but deliberately not
// charged, and this pins that so the deferral cannot quietly become an
// oversight. busAcquisitionCycles records what the hardware spends; Duration
// deliberately leaves it out.
//
// The reason is not that the charge is wrong. An earlier note here said the
// emulator's one-jump charge left no sample point inside the stall, and that
// adding a cost to it was a phase lottery. The first half of that is true and
// the second half misread it: zxnext.vhd:1827 forces cpu_m1_n inactive while
// the DMA holds the bus, so the real Z80 takes no M1 cycle inside a stall
// either, and one jump is the right shape for it.
//
// What the measurement actually shows is that TX-1696 is marginal. Perturbing
// the per-block cost across -5..+10 T-states breaks it at -4, -2, +3, +4, +6,
// +8, +9, +10 and leaves it working at -5, -3, -1, 0, +1, +2, +5, +7: seven of
// sixteen, scattered, with no monotonic relationship to the amount added. Zero
// is a working phase by luck. Charging three cycles lands on a failing one.
//
// So this stays deferred to avoid knowingly regressing a title while the
// marginality is unexplained, and the marginality is the real defect. See
// ROADMAP item 2.
func TestBusAcquisitionIsDerivedButNotCharged(t *testing.T) {
	if busAcquisitionCycles != 3 {
		t.Errorf("busAcquisitionCycles = %d, want 3: START_DMA, WAITING_ACK and "+
			"FINISH_DMA are one clock each (dma.vhd:267-305, :469-495)", busAcquisitionCycles)
	}
	d := New(memMap{})
	feed(d, withTiming(0x4000, 0x6000, 4, 0x02, 0x02, 0))
	if got, want := d.Duration(), uint64(4*(2+2)); got != want {
		t.Errorf("Duration = %d, want %d: the acquisition must not be in the charge "+
			"while a block is charged to the CPU as one lump", got, want)
	}
}
