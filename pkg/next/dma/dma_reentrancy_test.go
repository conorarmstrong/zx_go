package dma

import "testing"

// selfPortBus routes an IO-endpoint write straight back into the DMA's own
// command port, which is exactly what the emulator wires up: the DMA's IOBus
// is the ULA's port dispatch (cmd/zx_go/next.go), and the ULA forwards
// $6B / $0B to WriteCommand (pkg/ula/ula.go).
type selfPortBus struct {
	d      *DMA
	writes int
}

func (s *selfPortBus) ReadPort(uint16) byte { return 0xFF }

func (s *selfPortBus) WritePort(port uint16, val byte) {
	s.writes++
	if port&0xFF == 0x6B {
		s.d.WriteCommand(val)
	}
}

// TestDMASelfPortDoesNotRecurse guards against unbounded re-entry: a transfer
// whose destination is the DMA's own command port streams ENABLE ($87) bytes
// back into WriteCommand. Without a guard each one starts a nested Trigger and
// the emulator dies with `fatal error: stack overflow`, which no recover() can
// catch. Real hardware has no such hazard: the FPGA is a state machine already
// sitting in its transfer state, so an ENABLE arriving mid-block is not a
// second transfer.
func TestDMASelfPortDoesNotRecurse(t *testing.T) {
	mem := memMap{}
	for a := uint16(0x4000); a < 0x4100; a++ {
		mem[a] = 0x87 // ENABLE
	}
	d := New(mem)
	bus := &selfPortBus{d: d}
	d.SetIOBus(bus)

	for _, b := range []byte{
		0x7D, 0x00, 0x40, 0x40, 0x00, // WR0: A->B, port A = $4000, length $0040
		wr1Byte(addrIncrement, false), // port A walks the ENABLE bytes
		wr2Byte(addrFixed, true),      // port B is a fixed IO endpoint
		0x8D, 0x6B, 0x00,              // WR4: port B address = $006B
		0xCF, // LOAD
		0x87, // ENABLE
	} {
		d.WriteCommand(b)
	}

	if bus.writes != 0x40 {
		t.Errorf("port writes = %d, want 64 (one per byte of the block)", bus.writes)
	}
}

// TestDMAEnableDuringTransferIsIgnored is the same rule stated directly: an
// ENABLE that arrives while a block is in flight must not restart it.
func TestDMAEnableDuringTransferIsIgnored(t *testing.T) {
	mem := memMap{}
	d := New(mem)
	reads := 0
	d.SetIOBus(&hookBus{onWrite: func(uint16, byte) {
		reads++
		d.WriteCommand(0x87) // nested ENABLE
	}})

	for _, b := range []byte{
		0x7D, 0x00, 0x40, 0x04, 0x00, // WR0: A->B, port A = $4000, length 4
		wr1Byte(addrIncrement, false),
		wr2Byte(addrFixed, true),
		0x8D, 0x00, 0x20, // WR4: port B = IO port $2000
		0xCF, 0x87,
	} {
		d.WriteCommand(b)
	}
	if reads != 4 {
		t.Errorf("bytes transferred = %d, want 4", reads)
	}
}

type hookBus struct{ onWrite func(uint16, byte) }

func (h *hookBus) ReadPort(uint16) byte            { return 0xFF }
func (h *hookBus) WritePort(port uint16, val byte) { h.onWrite(port, val) }

// A transfer whose destination is the DMA's own command port can stream a
// command byte that stops the transfer it is part of. $83 DISABLE DMA is one
// assignment, dma_seq_s <= IDLE (dma.vhd:727-728), so the transfer FSM leaves
// the transfer states there and then. FINISH_DMA is downstream of them
// (dma.vhd:469-495) and never runs: end-of-block does not latch, auto-restart
// does not reload, and the bus is given back rather than held to the end of the
// block the guest asked for.
//
// Nothing in the model's block loop noticed. runBlock walks the shared
// d.remaining field, which $83 sets to zero, so the loop ended -- and then ran
// the whole epilogue anyway on the way out.
func TestReentrantDisableAbandonsTheBlockWithoutFinishing(t *testing.T) {
	mem := memMap{}
	for a := uint16(0x4000); a < 0x4010; a++ {
		mem[a] = 0xA0 // WR3 with no follow bytes and no second enable: inert
	}
	mem[0x4001] = 0x83 // ...except the second byte, which is DISABLE DMA

	d := New(mem)
	bus := &selfPortBus{d: d}
	d.SetIOBus(bus)
	charged := []uint64{}
	d.SetCycleSink(func(n uint64) { charged = append(charged, n) })

	feed(d, []byte{
		0x7D, 0x00, 0x40, 0x08, 0x00, // WR0: A->B, port A = $4000, length 8
		wr1Byte(addrIncrement, false),
		wr2Byte(addrFixed, true),
		0x8D, 0x6B, 0x00, // WR4: port B = the DMA's own command port
		0xA2,       // WR5: auto-restart, so a wrong FINISH_DMA is visible
		0xCF, 0x87, // LOAD, ENABLE
	})

	if bus.writes != 2 {
		t.Fatalf("port writes = %d, want 2: the block must stop at the DISABLE byte", bus.writes)
	}
	if got := d.ReadCommand(); got != 0x3A {
		t.Errorf("status after a re-entrant $83 = $%02X, want $3A. Bit 5 clear means "+
			"end-of-block latched and bit 0 set means at-least-one survived, but $83 "+
			"leaves for IDLE before FINISH_DMA and IDLE clears both (dma.vhd:265)", got)
	}
	if d.CurrentA() != 0x4002 {
		t.Errorf("port A pointer = $%04X, want $4002: auto-restart reloaded the start "+
			"address, so FINISH_DMA ran for a block that never reached it", d.CurrentA())
	}
	if len(charged) != 0 {
		t.Errorf("CPU charged %v T-states for an abandoned block, want nothing: the bus "+
			"goes back at the DISABLE (dma.vhd:260-262), it is not held for the "+
			"bytes the block never moved", charged)
	}
}

// $C3 RESET is the same hazard through a different door: it rebuilds the
// controller from scratch, so a block running underneath it comes back to a
// device that has no block. Running the epilogue then writes end-of-block and a
// byte count into a controller that has just been reset, which is a state no
// sequence of commands could otherwise produce.
func TestReentrantResetLeavesTheControllerReset(t *testing.T) {
	mem := memMap{}
	for a := uint16(0x4000); a < 0x4010; a++ {
		mem[a] = 0xA0
	}
	mem[0x4001] = 0xC3 // RESET

	d := New(mem)
	d.SetIOBus(&selfPortBus{d: d})

	feed(d, []byte{
		0x7D, 0x00, 0x40, 0x08, 0x00,
		wr1Byte(addrIncrement, false),
		wr2Byte(addrFixed, true),
		0x8D, 0x6B, 0x00,
		0xCF, 0x87,
	})

	if got := d.ReadCommand(); got != 0x3A {
		t.Errorf("status after a re-entrant $C3 = $%02X, want the reset value $3A", got)
	}
	if d.ByteCounter() != 0 {
		t.Errorf("byte counter after a re-entrant $C3 = %d, want 0: the reset zeroes it "+
			"and no block survives the reset to count past it", d.ByteCounter())
	}
}

// The interleaved burst path has the same door. Step() pumps a byte at a time
// from the CPU's hook, and a byte written to an IO endpoint aimed at $6B is a
// command like any other -- so a $83 can stop the block from inside Step()
// exactly as it can from inside runBlock(), and the same epilogue must not run.
func TestReentrantDisableStopsAnInterleavedBurst(t *testing.T) {
	mem := memMap{}
	for a := uint16(0x4000); a < 0x4010; a++ {
		mem[a] = 0xA0
	}
	mem[0x4001] = 0x83 // DISABLE DMA, the second byte the burst pumps

	var now uint64
	d := New(mem)
	bus := &selfPortBus{d: d}
	d.SetIOBus(bus)
	d.SetClock(func() uint64 { return now })

	feed(d, []byte{
		0x7D, 0x00, 0x40, 0x08, 0x00, // WR0: A->B, port A = $4000, length 8
		0x14,             // WR1: port A memory, increment
		0x68,             // WR2: port B IO, fixed, timing byte follows
		0x22,             // WR2 timing byte: prescaler follows, cycle code 2
		20,               // prescaler: a byte every 20 T-states
		0xCD, 0x6B, 0x00, // WR4: burst, port B = the DMA's own command port
		0xA2,       // WR5: auto-restart
		0xCF, 0x87, // LOAD, ENABLE
	})
	if bus.writes != 0 {
		t.Fatalf("burst wrote %d bytes at ENABLE, want 0: it must defer to Step", bus.writes)
	}

	for now = 0; now < 400; now += 10 {
		d.Step(now)
	}

	if bus.writes != 2 {
		t.Fatalf("port writes = %d, want 2: the burst must stop at the DISABLE byte", bus.writes)
	}
	if got := d.ReadCommand(); got != 0x3A {
		t.Errorf("status after a re-entrant $83 = $%02X, want $3A: $83 leaves for IDLE "+
			"before FINISH_DMA, and IDLE clears both status bits (dma.vhd:265)", got)
	}
	if d.CurrentA() != 0x4002 {
		t.Errorf("port A pointer = $%04X, want $4002: auto-restart reloaded the start "+
			"address, so FINISH_DMA ran for a block that never reached it", d.CurrentA())
	}
}
