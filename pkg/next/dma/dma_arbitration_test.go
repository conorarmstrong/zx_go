package dma

import "testing"

// A block holds the CPU off the bus for its bytes, at each port's programmed
// cycle length, and the duration it reports and the amount it charges are the
// same number. Three further states bracket the transfer loop with the DMA
// still owning the bus -- START_DMA, WAITING_ACK and FINISH_DMA -- and their
// three clocks are deliberately not in this figure; see
// TestBusAcquisitionIsDerivedButNotCharged.
func TestABlockChargesTheCPUForItsBytes(t *testing.T) {
	cases := []struct {
		name         string
		bytes        uint16
		aCode, bCode byte
		want         uint64
	}{
		{"3 bytes at 2+2 cycles", 3, 0x02, 0x02, 3 * (2 + 2)},
		{"7 bytes at 4+4 cycles", 7, 0x00, 0x00, 7 * (4 + 4)},
		{"1 byte at 4+4 cycles", 1, 0x00, 0x00, 1 * (4 + 4)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var charged uint64
			d := New(memMap{})
			d.SetCycleSink(func(n uint64) { charged += n })
			feed(d, withTiming(0x4000, 0x6000, c.bytes, c.aCode, c.bCode, 0))
			if got := d.Duration(); got != c.want {
				t.Errorf("Duration = %d, want %d", got, c.want)
			}
			if charged != c.want {
				t.Errorf("charged = %d, want %d", charged, c.want)
			}
		})
	}
}

// $83 is Disable DMA, and dma.vhd:727-728 makes it a single assignment:
// dma_seq_s <= IDLE. That is not a request to stop at the end of the block, it
// is the transfer FSM leaving mid-flight — the bytes still owed are abandoned,
// the bus request drops (dma.vhd:260-262) and no FINISH_DMA runs, so
// end-of-block never latches and auto-restart never reloads.
func TestDisableDMAAbortsABlockInFlight(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 0x40; i++ {
		mem[0x4000+i] = byte(i + 1)
	}
	var now uint64
	d := New(mem)
	d.SetClock(func() uint64 { return now })
	feed(d, burstStream(0x4000, 0x6000, 0x40, 10)) // burst + prescaler: interleaved

	for now = 0; now < 50; now += 10 {
		d.Step(now)
	}
	stopped := d.ByteCounter()
	if stopped == 0 || stopped >= 0x40 {
		t.Fatalf("fixture moved %d of 64 bytes; the block is not in flight", stopped)
	}

	d.WriteCommand(0x83) // Disable DMA

	for ; now <= 0x40*10+200; now += 10 {
		d.Step(now)
	}
	if got := d.ByteCounter(); got != stopped {
		t.Errorf("byte counter = %d, want %d: $83 did not abort the block", got, stopped)
	}
	for i := stopped; i < 0x40; i++ {
		if got := mem[0x6000+i]; got != 0 {
			t.Errorf("dst[$%04X] = $%02X, want $00: bytes moved after $83", 0x6000+i, got)
		}
	}
}

// dma_delay_i is an input pin, and it is the DMA's only real arbitration
// partner on the Next: bus_busreq_n_i is tied to '1' and cpu_bao_n is left open
// (zxnext.vhd:1787, :1791, with the comment at :1822 that there is no DMA
// controller on the expansion bus), so the BUSREQ-in / BUSAK-out daisy chain
// never does anything. What does is im2_dma_delay (zxnext.vhd:1785, :2001-2010):
// an IM2 peripheral with its NR$CC/$CD/$CE dma-int-enable bit set, an NMI, or a
// RETI pop holds the DMA off the bus so the interrupt can be serviced.
//
// While it is high START_DMA keeps cpu_busreq_n_s deasserted (dma.vhd:269-273):
// the DMA does not merely fail to get the bus, it does not ask for it. Nothing
// moves until the pin drops, and then the whole block runs.
func TestBusDelayBlocksTheBusRequest(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 3; i++ {
		mem[0x4000+i] = byte(0xA0 + i)
	}
	d := New(mem)
	moved, sawRequest := 0, false
	d.SetIOBus(&hookBus{onWrite: func(uint16, byte) {
		moved++
		if d.BusRequested() {
			sawRequest = true
		}
	}})

	d.SetBusDelay(true)
	feed(d, []byte{
		0xC3,                         // RESET
		0x7D, 0x00, 0x40, 0x03, 0x00, // WR0: A->B, port A = $4000, length 3
		0x14,                     // WR1: port A memory, increment
		wr2Byte(addrFixed, true), // WR2: port B IO, fixed
		0x8D, 0xDF, 0x00,         // WR4: continuous, port B = IO $00DF
		0xCF, 0x87, // LOAD, ENABLE
	})
	if moved != 0 {
		t.Errorf("moved %d bytes with dma_delay_i asserted, want 0", moved)
	}
	if d.BusRequested() {
		t.Error("the DMA asserted BUSREQ while dma_delay_i was high")
	}

	d.SetBusDelay(false)
	if moved != 3 {
		t.Errorf("after the delay dropped, moved %d bytes, want 3", moved)
	}
	if !sawRequest {
		t.Error("the DMA never held the bus during the block it did run")
	}
	if d.BusRequested() {
		t.Error("the DMA is still holding the bus after the block finished")
	}
}

// The delay does not only gate the start. TRANSFERING_WRITE_4 tests it again
// after every byte and goes back to START_DMA rather than on to the next read
// when it is high (dma.vhd:427-428, :456-461), so an interrupt raised part way
// through a block parks the transfer and hands the bus back with the pointers
// where they stand. Dropping the pin resumes that same block from there — it
// does not restart it, and it does not abandon it.
func TestBusDelayPausesAndResumesMidBlock(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 8; i++ {
		mem[0x4000+i] = byte(0xA0 + i)
	}
	d := New(mem)
	var out []byte
	d.SetIOBus(&hookBus{onWrite: func(_ uint16, v byte) {
		out = append(out, v)
		if len(out) == 3 { // an IM2 device asserts its dma-int-enable mid-block
			d.SetBusDelay(true)
		}
	}})
	feed(d, []byte{
		0xC3,                         // RESET
		0x7D, 0x00, 0x40, 0x08, 0x00, // WR0: A->B, port A = $4000, length 8
		0x14,                     // WR1: port A memory, increment
		wr2Byte(addrFixed, true), // WR2: port B IO, fixed
		0x8D, 0xDF, 0x00,         // WR4: continuous, port B = IO $00DF
		0xCF, 0x87, // LOAD, ENABLE
	})
	if len(out) != 3 {
		t.Fatalf("the block moved %d bytes before parking, want 3", len(out))
	}
	if d.BusRequested() {
		t.Error("the parked block is still holding the bus")
	}

	d.SetBusDelay(false)

	want := []byte{0xA0, 0xA1, 0xA2, 0xA3, 0xA4, 0xA5, 0xA6, 0xA7}
	if len(out) != len(want) {
		t.Fatalf("after the delay dropped the block moved %d bytes in total, want %d", len(out), len(want))
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("byte %d = $%02X, want $%02X: the resumed block did not carry on "+
				"from the pointers it parked with", i, out[i], want[i])
		}
	}
}

// The same pin, against the interleaved burst that Step() pumps. Here the pause
// is visible directly: no byte falls due while the delay is high, and the
// pacing survives it — the FPGA clears DMA_timer_s when the next read finally
// starts (dma.vhd:309), so a long pause does not leave a backlog that dumps the
// rest of the block in one go.
func TestBusDelayPausesAnInterleavedBurst(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 0x40; i++ {
		mem[0x4000+i] = byte(i + 1)
	}
	var now uint64
	d := New(mem)
	d.SetClock(func() uint64 { return now })
	feed(d, burstStream(0x4000, 0x6000, 0x40, 10))

	for now = 0; now < 50; now += 10 {
		d.Step(now)
	}
	paused := d.ByteCounter()
	if paused == 0 || paused >= 0x40 {
		t.Fatalf("fixture moved %d of 64 bytes; the block is not in flight", paused)
	}

	d.SetBusDelay(true)
	for ; now <= 1000; now += 10 {
		d.Step(now)
	}
	now -= 10 // the clock reading the last parked Step saw
	if got := d.ByteCounter(); got != paused {
		t.Errorf("byte counter = %d while dma_delay was high, want it parked at %d", got, paused)
	}

	// Resume at that same clock reading. Exactly one byte is owed: the FPGA
	// clears DMA_timer_s as the next read starts (dma.vhd:309), so the byte the
	// pause interrupted goes now and the one after it is a full prescaler period
	// away. A backlog would empty most of the remaining 59 here instead.
	d.SetBusDelay(false)
	d.Step(now)
	if got := d.ByteCounter(); got != paused+1 {
		t.Errorf("the first step after the delay dropped moved %d bytes, want 1: "+
			"the fixed-time pacing did not survive the pause", got-paused)
	}

	for ; now <= 1000+0x40*10+200; now += 10 {
		d.Step(now)
	}
	for i := uint16(0); i < 0x40; i++ {
		if got, want := mem[0x6000+i], byte(i+1); got != want {
			t.Fatalf("dst[$%04X] = $%02X, want $%02X: the resumed burst did not complete",
				0x6000+i, got, want)
		}
	}
}

// Status bit 0 is status_atleastone, and it is not the "transfers always
// complete instantly so it is always zero" bit our model treated it as. The
// FPGA raises it in TRANSFERING_WRITE_4 (dma.vhd:412), which is after the first
// byte's write, and an interleaved burst sits in that loop for the whole block
// while the CPU runs and polls. So a status read taken mid-block reads $3B, and
// one taken between the ENABLE and the first byte still reads $3A.
func TestStatusAtLeastOneIsSetDuringABlock(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 0x40; i++ {
		mem[0x4000+i] = byte(i + 1)
	}
	var now uint64
	d := New(mem)
	d.SetClock(func() uint64 { return now })
	feed(d, burstStream(0x4000, 0x6000, 0x40, 10))

	d.WriteCommand(0xBF) // Read Status Byte
	if got := d.ReadCommand(); got != 0x3A {
		t.Errorf("status straight after ENABLE = $%02X, want $3A: no byte has been written yet", got)
	}

	for now = 0; now < 50; now += 10 {
		d.Step(now)
	}
	if c := d.ByteCounter(); c == 0 || c >= 0x40 {
		t.Fatalf("fixture moved %d of 64 bytes; the block is not in flight", c)
	}
	d.WriteCommand(0xBF)
	if got := d.ReadCommand(); got != 0x3B {
		t.Errorf("status mid-block = $%02X, want $3B (status_atleastone set)", got)
	}
}

// And it comes back down. FINISH_DMA clears status_endofblock_n (dma.vhd:471)
// and the return to IDLE clears status_atleastone (dma.vhd:265), so once the
// block is over and the controller is at rest the status byte reads $1A again —
// which is what the FPGA golden capture returns after every completed block.
func TestStatusAtLeastOneClearsAtEndOfBlock(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 0x40; i++ {
		mem[0x4000+i] = byte(i + 1)
	}
	var now uint64
	d := New(mem)
	d.SetClock(func() uint64 { return now })
	feed(d, burstStream(0x4000, 0x6000, 0x40, 10))

	for now = 0; now <= 0x40*10+200; now += 10 {
		d.Step(now)
	}
	if got := d.ByteCounter(); got != 0x40 {
		t.Fatalf("the block moved %d of 64 bytes; it has not finished", got)
	}
	d.WriteCommand(0xBF) // Read Status Byte
	if got := d.ReadCommand(); got != 0x1A {
		t.Errorf("status after the block ended = $%02X, want $1A (end of block, back at IDLE)", got)
	}
}

// $87 is one assignment: dma_seq_s <= START_DMA (dma.vhd:724-725). There is no
// armed flag anywhere in the device — nothing asks whether a LOAD has happened
// or whether the last block finished — and the transfer FSM always writes a
// byte before testing the counter against the block length (dma.vhd:426, :433).
// So an ENABLE arriving after a block has ended, with no LOAD, no CONTINUE and
// no auto-restart, moves exactly one more byte from where the pointers stand.
func TestEnableAfterEndOfBlockMovesOneMoreByte(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 8; i++ {
		mem[0x4000+i] = byte(0xA0 + i)
	}
	d := New(mem)
	feed(d, transferCmd(0x4000, 0x6000, 4, addrIncrement, addrIncrement, true))
	if d.ByteCounter() != 4 {
		t.Fatalf("the first block moved %d bytes, want 4", d.ByteCounter())
	}

	d.WriteCommand(0x87)

	if d.ByteCounter() != 5 {
		t.Errorf("byte counter = %d after the second ENABLE, want 5", d.ByteCounter())
	}
	if got := mem[0x6004]; got != 0xA4 {
		t.Errorf("dst[$6004] = $%02X, want $A4: the second ENABLE moved nothing", got)
	}
	if got := mem[0x6005]; got != 0 {
		t.Errorf("dst[$6005] = $%02X, want $00: the second ENABLE moved more than one byte", got)
	}
}

// The same bit on the other transfer path. A continuous block runs to
// completion inside one call here, so the only place the rest of the machine
// can observe it mid-flight is an IO endpoint's port callback, which is a real
// observation point: the DMA's IO bus is the ULA's port dispatch, and $6B and
// $0B route straight back into this controller. status_atleastone is set in
// TRANSFERING_WRITE_4 (dma.vhd:412), which the FSM enters after the write cycle
// itself, so the read taken during the FIRST byte's write still sees it clear
// and every read after that sees it set. The block is not at its end yet, so
// bit 5 stays set throughout: $3A then $3B.
func TestStatusAtLeastOneIsSetInsideASynchronousBlock(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 4; i++ {
		mem[0x4000+i] = byte(0xA0 + i)
	}
	d := New(mem)
	var seen []byte
	d.SetIOBus(&hookBus{onWrite: func(uint16, byte) {
		d.WriteCommand(0xBF) // Read Status Byte
		seen = append(seen, d.ReadCommand())
	}})
	feed(d, []byte{
		0xC3,                         // RESET
		0x7D, 0x00, 0x40, 0x04, 0x00, // WR0: A->B, port A = $4000, length 4
		0x14,             // WR1: port A memory, increment
		0x28,             // WR2: port B IO, fixed
		0x8D, 0xDF, 0x00, // WR4: continuous, port B = IO port $00DF
		0xCF, 0x87, // LOAD, ENABLE
	})
	if len(seen) != 4 {
		t.Fatalf("the fixture read status %d times, want 4: it never entered the block", len(seen))
	}
	// The first read is the one hardware cannot arbitrate. status_atleastone
	// goes up in TRANSFERING_WRITE_4 (dma.vhd:412), the same state that drops
	// dma_write_cycle, so the flag and the completion of the write land on one
	// clock edge; and no guest can read the register in between, because the
	// CPU is frozen while the DMA holds the bus (zxnext.vhd:1824-1835). Only
	// this model has an observer inside the write. It is pinned here as the
	// model's ordering rather than as a hardware claim, so that moving the
	// assignment does not go unnoticed.
	if seen[0] != 0x3A {
		t.Errorf("status during the first byte's write = $%02X, want $3A: "+
			"status_atleastone is set after that write completes", seen[0])
	}
	for i, got := range seen[1:] {
		if got != 0x3B {
			t.Errorf("status during byte %d's write = $%02X, want $3B (status_atleastone set)", i+1, got)
		}
	}
	// And the return to IDLE clears it again (dma.vhd:265).
	d.WriteCommand(0xBF)
	if got := d.ReadCommand(); got != 0x1A {
		t.Errorf("status after the block = $%02X, want $1A", got)
	}
}
