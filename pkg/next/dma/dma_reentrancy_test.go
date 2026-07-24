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
