package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// A WR4 byte with D4 set and D2/D3 clear parks the zxnDMA's register-write
// sequencer in the FPGA's unimplemented R4_BYTE_2 state, where it swallows
// every later command byte including $C3 RESET. Only the reset pin recovers it,
// and rebootLocked never drove that pin, so a guest could kill the DMA for the
// rest of the process: Machine -> Reboot left it just as dead.
//
// Rebooting a machine has to hand back working hardware.
func TestRebootRecoversAWedgedDMA(t *testing.T) {
	e, err := newEmulator(roms.ModelNext)
	if err != nil {
		t.Fatalf("newEmulator(Next): %v", err)
	}
	if e.nextDMA == nil {
		t.Fatal("a Next emulator has no zxnDMA")
	}

	// Somewhere to move bytes from and to, clear of the ROM.
	var src, dst uint16 = 0xC000, 0xC800
	for i := uint16(0); i < 3; i++ {
		e.mem.Write(src+i, byte(0xA0+i))
		e.mem.Write(dst+i, 0)
	}

	e.nextDMA.WriteCommand(0x91) // WR4, D4 alone: wedged

	e.coreMu.Lock()
	e.rebootLocked()
	e.coreMu.Unlock()

	for _, b := range []byte{
		0x7D, byte(src), byte(src >> 8), 0x03, 0x00, // WR0: A->B, port A, length 3
		0x14, 0x10, // WR1, WR2: both memory, incrementing
		0x8D, byte(dst), byte(dst >> 8), // WR4: continuous, port B
		0xCF, 0x87, // LOAD, ENABLE
	} {
		e.nextDMA.WriteCommand(b)
	}

	for i := uint16(0); i < 3; i++ {
		if got, want := e.mem.Read(dst+i), byte(0xA0+i); got != want {
			t.Errorf("dst[$%04X] = $%02X, want $%02X: the reboot left the DMA wedged",
				dst+i, got, want)
		}
	}
}
