package dma

import "testing"

// The FPGA's WR3 decode latches bit 6 into R3_dma_en_s and, when it is set,
// kicks the transfer FSM straight into START_DMA (dma.vhd:574-580) — the same
// state the $87 ENABLE command jumps to (dma.vhd:724). A WR3 base byte with D6
// set is therefore a second way to start a configured block, with no ENABLE
// anywhere in the stream.
func TestWR3EnableStartsATransfer(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 3; i++ {
		mem[0x4000+i] = byte(0xA0 + i)
	}
	d := New(mem)
	feed(d, []byte{
		0xC3,                         // RESET
		0x7D, 0x00, 0x40, 0x03, 0x00, // WR0: A->B, port A = $4000, length 3
		0x14,             // WR1: port A memory, increment
		0x10,             // WR2: port B memory, increment
		0x8D, 0x00, 0x60, // WR4: continuous, port B = $6000
		0xCF, // LOAD
		0xC0, // WR3 with D6 set — the enable; no $87 in this stream
	})
	for i := uint16(0); i < 3; i++ {
		if got, want := mem[0x6000+i], byte(0xA0+i); got != want {
			t.Errorf("dst[$%04X] = $%02X, want $%02X (WR3 D6 must start the block)", 0x6000+i, got, want)
		}
	}
}

// ...and the other half of the same decode: R3_dma_en_s is written at
// dma.vhd:576 and read nowhere else in the file, so it is dead state, not a
// gate. A WR3 byte with D6 clear arriving mid-block therefore has no effect on
// the transfer FSM at all — the block runs to completion. This is a guard on
// the shape of the behaviour above: the obvious wrong implementation is to
// treat R3_dma_en_s as an enable the transfer engine consults.
func TestWR3DisableDoesNotStopABlockInFlight(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 0x40; i++ {
		mem[0x4000+i] = byte(i + 1)
	}
	var now uint64
	d := New(mem)
	d.SetClock(func() uint64 { return now })
	feed(d, burstStream(0x4000, 0x6000, 0x40, 10))

	for now = 0; now < 100; now += 10 {
		d.Step(now)
	}
	if c := d.ByteCounter(); c == 0 || c >= 0x40 {
		t.Fatalf("fixture moved %d of 64 bytes; the block is not in flight", c)
	}

	d.WriteCommand(0x80) // WR3, D6 clear

	for ; now <= 0x40*10+200; now += 10 {
		d.Step(now)
	}
	for i := uint16(0); i < 0x40; i++ {
		if got, want := mem[0x6000+i], byte(i+1); got != want {
			t.Fatalf("dst[$%04X] = $%02X, want $%02X: a WR3 D6-clear byte stopped the block",
				0x6000+i, got, want)
		}
	}
}

// The Z80 DMA's WR4 announces an interrupt-control byte with D4, but the FPGA
// never consumes it: R4_BYTE_1 returns unconditionally to IDLE (dma.vhd:826-833)
// and the R4_BYTE_2 branch that would have read it is commented out
// (dma.vhd:835-844), along with R4_interrupt_control_s itself (dma.vhd:94-96).
// So a stream that sets D4 leaves the byte after the port B address to be
// decoded as a base byte — here a WR3 enable, which starts the block.
func TestWR4InterruptControlByteIsNotConsumed(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 3; i++ {
		mem[0x4000+i] = byte(0xB0 + i)
	}
	d := New(mem)
	feed(d, []byte{
		0xC3,                         // RESET
		0x7D, 0x00, 0x40, 0x03, 0x00, // WR0: A->B, port A = $4000, length 3
		0x14,       // WR1: port A memory, increment
		0x10,       // WR2: port B memory, increment
		0x99, 0x60, // WR4: port B HIGH follows (D3) and interrupt control (D4)
		0xCF, // LOAD — a command byte, not the interrupt-control byte
		0xC0, // WR3 enable
	})
	if got := d.Destination(); got != 0x6000 {
		t.Fatalf("port B = $%04X, want $6000", got)
	}
	for i := uint16(0); i < 3; i++ {
		if got, want := mem[0x6000+i], byte(0xB0+i); got != want {
			t.Errorf("dst[$%04X] = $%02X, want $%02X: the WR4 D4 byte swallowed a command",
				0x6000+i, got, want)
		}
	}
}

// The same half-removed decode has a sharper edge. WR4's follow-byte chain is
// an elsif ladder (dma.vhd:603-611): D2 selects R4_BYTE_0, else D3 selects
// R4_BYTE_1, else D4 selects R4_BYTE_2 — and R4_BYTE_2's case branch is one of
// the commented-out ones (dma.vhd:835-844). The write sequencer therefore lands
// in a state the case statement does not name, and dma.vhd:891's
// "when others => null" swallows every byte written to the controller from then
// on. It is a genuine hardware lock-up, not a decoding nicety, and a guest that
// sets D4 without D2 or D3 cannot program the DMA again.
func TestWR4InterruptControlOnlyWedgesTheWriteSequencer(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 3; i++ {
		mem[0x4000+i] = byte(0xA0 + i)
	}
	d := New(mem)
	d.WriteCommand(0x91) // WR4: D4 set, D2 and D3 clear

	feed(d, []byte{
		0xC3,                         // RESET
		0x7D, 0x00, 0x40, 0x03, 0x00, // WR0: A->B, port A = $4000, length 3
		0x14,             // WR1: port A memory, increment
		0x10,             // WR2: port B memory, increment
		0x8D, 0x00, 0x60, // WR4: continuous, port B = $6000
		0xCF, 0x87, // LOAD, ENABLE
	})
	for i := uint16(0); i < 3; i++ {
		if got := mem[0x6000+i]; got != 0 {
			t.Errorf("dst[$%04X] = $%02X, want $00: the wedged controller moved bytes",
				0x6000+i, got)
		}
	}
}

// And the wedge is not something software can clear. $C3 RESET is decoded
// inside the write sequencer's own IDLE branch (dma.vhd:511, :637-645), which a
// wedged sequencer never reaches, and the only other assignment of IDLE to it
// is under the reset pin (dma.vhd:229). So a driver that tries the documented
// recovery — RESET, then reprogram — gets nowhere.
func TestResetCommandDoesNotClearTheWedge(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 3; i++ {
		mem[0x4000+i] = byte(0xA0 + i)
	}
	d := New(mem)
	d.WriteCommand(0x91) // WR4 D4 only: wedged
	d.WriteCommand(0xC3) // RESET — swallowed like everything else

	feed(d, []byte{
		0x7D, 0x00, 0x40, 0x03, 0x00, // WR0: A->B, port A = $4000, length 3
		0x14, 0x10, // WR1, WR2: both memory, incrementing
		0x8D, 0x00, 0x60, // WR4: continuous, port B = $6000
		0xCF, 0x87, // LOAD, ENABLE
	})
	for i := uint16(0); i < 3; i++ {
		if got := mem[0x6000+i]; got != 0 {
			t.Errorf("dst[$%04X] = $%02X, want $00: a $C3 RESET cleared the wedge",
				0x6000+i, got)
		}
	}
}

// Only the reset pin recovers it. dma.vhd:211-245 is the reset_i branch, and it
// is the one place outside the sequencer's own IDLE branch that assigns
// reg_wr_seq_s := IDLE (dma.vhd:229). It also restores the documented power-up
// values: the transfer FSM to IDLE, both port timings to "01", the prescaler to
// zero, the transfer mode to continuous, auto-restart off, the read mask to all
// seven registers with the sequence back at status, and both status bits clear.
// The latched addresses, block length, direction and port modes are NOT in that
// branch, so they survive.
func TestHardwareResetClearsTheWedge(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 3; i++ {
		mem[0x4000+i] = byte(0xA0 + i)
	}
	d := New(mem)
	// Move every field the reset branch names away from its reset value first,
	// so restoring them is visible: burst mode, a prescaler, a one-register read
	// mask, auto-restart, and a finished block to latch end-of-block.
	feed(d, burstStream(0x4000, 0x6000, 3, 40))
	feed(d, []byte{0xA2, 0xBB, 0x08}) // WR5 auto-restart, read mask = port A lo
	if d.Mode() != modeBurst {
		t.Fatalf("fixture mode = %d, want burst", d.Mode())
	}

	d.WriteCommand(0x91) // WR4 D4 only: wedged
	d.Reset()            // the reset_i pin

	if d.Mode() != modeContinuous {
		t.Errorf("mode after reset = %d, want continuous (R4_mode_s <= \"01\")", d.Mode())
	}
	if got := d.ReadCommand(); got != 0x3A {
		t.Errorf("first read after reset = $%02X, want status $3A: the read mask and "+
			"sequence cursor, end-of-block and at-least-one must all be back at their defaults", got)
	}

	// And the controller programs and transfers again. The destination has to be
	// somewhere the fixture has not already written: the burst above landed
	// $A0 $A1 $A2 at $6000, so re-running the same transfer there would assert
	// nothing at all -- a still-wedged controller that swallowed every byte
	// below would leave exactly the bytes this loop wants to find.
	feed(d, []byte{
		0x7D, 0x00, 0x40, 0x03, 0x00, // WR0: A->B, port A = $4000, length 3
		0x14, 0x10, // WR1, WR2: both memory, incrementing
		0x8D, 0x00, 0x61, // WR4: continuous, port B = $6100
		0xCF, 0x87, // LOAD, ENABLE
	})
	for i := uint16(0); i < 3; i++ {
		if got, want := mem[0x6100+i], byte(0xA0+i); got != want {
			t.Errorf("dst[$%04X] = $%02X, want $%02X: the reset pin did not clear the wedge",
				0x6100+i, got, want)
		}
	}
}
